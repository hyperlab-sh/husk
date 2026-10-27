package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The test executable implements the converter by argv mode, never by an
// inherited HUSK variable (those must all be stripped before execution).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "detect", "convert", "discover":
			os.Exit(converterChild(os.Args[1:]))
		}
	}
	os.Exit(m.Run())
}

func converterChild(args []string) int {
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "HUSK_") && entry != "HUSK_PROTOCOL_VERSION="+protocol.Version {
			fmt.Fprintln(os.Stderr, "environment leaked")
			return 9
		}
	}
	if os.Getenv("HUSK_PROTOCOL_VERSION") != protocol.Version {
		return 9
	}
	if args[0] == "discover" {
		fmt.Print(os.Getenv("CONVERTER_DIRS"))
		if os.Getenv("CONVERTER_DISCOVER_FAIL") != "" {
			fmt.Fprintln(os.Stderr, "discovery failed")
			return 7
		}
		return 0
	}
	mode := filepath.Base(args[1])
	if args[0] == "detect" {
		if mode == "decline" {
			return 1
		}
		if mode == "detect-fail" {
			fmt.Fprintln(os.Stderr, "detect first\ndetect second")
			return 3
		}
		return 0
	}
	switch mode {
	case "real-deadline":
		ready, err := os.OpenFile(filepath.Join(args[1], "ready"), os.O_WRONLY, 0)
		if err != nil {
			return 8
		}
		_, err = ready.Write([]byte{1})
		_ = ready.Close()
		if err != nil {
			return 8
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			return 8
		}
		defer reader.Close()
		defer writer.Close()
		var b [1]byte
		_, _ = reader.Read(b[:]) // blocks until the host cancels this real process
		return 8
	case "stdout-large":
		fmt.Print(strings.Repeat("x", maxManifestBytes+1))
		return 0
	case "stderr-large":
		fmt.Fprint(os.Stderr, strings.Repeat("x", maxManifestBytes+1))
	case "mixed-stderr":
		fmt.Fprintln(os.Stderr, "plain diagnostic\nnot {json\n{\"message\":\"no code\"}")
	case "bad":
		fmt.Print("not json")
		return 0
	case "exit":
		fmt.Fprintln(os.Stderr, "first diagnostic\nsecond diagnostic")
		return 4
	case "relative":
		fmt.Print(`{"manifest_version":2,"protocol":2,"id":"converted","name":"Converted","version":"1","command":["relative"]}`)
		return 0
	}
	if mode == "hujson" {
		fmt.Fprintln(os.Stdout, "// v2 configuration accepts comments")
	}
	exe, _ := os.Executable()
	doc := map[string]any{"manifest_version": 2, "protocol": protocol.Major, "id": "converted", "name": "Converted", "version": "1", "command": []string{exe}, "actions": []any{map[string]any{"id": "converted.open", "title": "Open"}}, "x-fixture": map[string]any{"nested": map[string]any{"value": true}}}
	_ = json.NewEncoder(os.Stdout).Encode(doc)
	fmt.Fprintln(os.Stderr, `{"code":"fixture_warning","message":"a warning","field":"actions"}`)
	return 0
}

func execFixture(t *testing.T, opts ExecOptions) *ExecConverter {
	t.Helper()
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Clock == nil {
		opts.Clock = testclock.New()
	}
	c, err := NewExecConverter(exe, opts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExecConverterConversion(t *testing.T) {
	t.Setenv("HUSK_SOCKET_PATH", "not-for-converters")
	t.Setenv("HUSK_PLUGIN_TOKEN", "not-for-converters")
	t.Setenv("HUSK_OTHER", "not-for-converters")
	c := execFixture(t, ExecOptions{})
	if c.Format() != "exec:"+filepath.Base(c.path) {
		t.Fatal(c.Format())
	}
	root := t.TempDir()
	for _, mode := range []string{"ok", "hujson", "mixed-stderr", "stdout-large", "stderr-large", "decline", "bad", "exit", "detect-fail", "relative"} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(root, mode)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			src, claimed := c.Detect(dir)
			if mode == "decline" {
				if claimed {
					t.Fatal("decline claimed")
				}
				return
			}
			if !claimed {
				t.Fatal("not claimed")
			}
			spec, warnings, err := c.Convert(src)
			if mode != "ok" && mode != "hujson" && mode != "mixed-stderr" {
				if err == nil || !strings.Contains(err.Error(), "converter_failed") {
					t.Fatalf("failure = %v", err)
				}
				if mode == "exit" && (!strings.Contains(err.Error(), "exit status 4") || !strings.Contains(err.Error(), "first diagnostic") || strings.Contains(err.Error(), "second diagnostic")) {
					t.Fatal(err)
				}
				if strings.HasSuffix(mode, "-large") && (!strings.Contains(err.Error(), "output exceeds 1 MiB") || !strings.Contains(err.Error(), "exit status 0")) {
					t.Fatal(err)
				}
				if mode == "detect-fail" && !strings.Contains(err.Error(), "exit status 3") {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if spec.ID != "converted" || len(spec.Actions) != 1 || len(warnings) != 1 || warnings[0].Code != "converter:fixture_warning" || warnings[0].Field != "actions" {
				t.Fatalf("spec=%+v warnings=%+v", spec, warnings)
			}
			clone := spec.Clone()
			spec.Vendor["x-fixture"][0] = ' '
			spec.Problems[0].Code = "changed"
			catalog, err := clone.CatalogJSON()
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]json.RawMessage
			_ = json.Unmarshal(catalog, &doc)
			if !strings.Contains(string(doc["x-fixture"]), `"value":true`) || !strings.Contains(string(doc["problems"]), `"code":"converter:fixture_warning"`) {
				t.Fatal(string(catalog))
			}
		})
	}
}

func TestExecConverterDiscovery(t *testing.T) {
	c := execFixture(t, ExecOptions{})
	dir := t.TempDir()
	for _, tc := range []struct {
		name, output string
		fail         bool
		count        int
	}{
		{"empty", "", false, 0}, {"directories", dir + "\n" + dir + "\n", false, 2}, {"limit", strings.Repeat(dir+"\n", 256), false, 256}, {"overflow", strings.Repeat(dir+"\n", 257), true, 0}, {"relative", "relative\n", true, 0},
		{"blank-lines", "\n" + dir + "\n\n", false, 1}, {"crlf", "\r\n" + dir + "\r\n\r\n", false, 1}, {"limit-with-blanks", strings.Repeat("\n"+dir+"\n", 256), false, 256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONVERTER_DIRS", tc.output)
			dirs, err := c.Discover()
			if (err != nil) != tc.fail {
				t.Fatalf("dirs=%v err=%v", dirs, err)
			}
			if !tc.fail && len(dirs) != tc.count {
				t.Fatalf("dirs=%v", dirs)
			}
			for _, got := range dirs {
				if got != dir {
					t.Errorf("directory=%q want=%q", got, dir)
				}
			}
		})
	}
	t.Setenv("CONVERTER_DISCOVER_FAIL", "1")
	if _, err := c.Discover(); err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("discover failure=%v", err)
	}
}

func TestExecConverterDeadline(t *testing.T) {
	clock := testclock.New()
	started := make(chan struct{})
	c := execFixture(t, ExecOptions{Clock: clock, Run: func(ctx context.Context, _ string, _, _ []string, _, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	done := make(chan error, 1)
	go func() { _, err := c.Discover(); done <- err }()
	<-started
	clock.Advance(4 * time.Second)
	if !clock.Active(5 * time.Second) {
		// Drain an early completion so a mutant cannot leave a worker behind.
		_ = testclock.Receive(t, done)
		t.Fatal("converter deadline fired before five seconds")
	}
	clock.Advance(time.Second)
	err := testclock.Receive(t, done)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded after 5s") {
		t.Fatalf("deadline=%v", err)
	}
	if clock.Active(5 * time.Second) {
		t.Fatal("deadline timer retained")
	}
}

func TestExecConverterRegistration(t *testing.T) {
	c := execFixture(t, ExecOptions{Run: func(context.Context, string, []string, []string, io.Writer, io.Writer) error {
		t.Fatal("external converter preempted built-in")
		return nil
	}})
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), "./relative-converter"} {
		if _, err := NewExecConverter(path, ExecOptions{}); err == nil || !strings.Contains(err.Error(), "converter_unavailable") {
			t.Fatalf("unavailable=%v", err)
		}
	}
	t.Setenv("PATH", filepath.Dir(c.path))
	if _, err := NewExecConverter(filepath.Base(c.path), ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe, _ := os.Executable()
	raw, _ := json.Marshal(map[string]any{"manifest_version": 2, "protocol": 2, "id": "builtin", "name": "Builtin", "version": "1", "command": []string{exe}})
	if err := os.WriteFile(filepath.Join(dir, FileName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	reg.Register(c)
	spec, _, err := reg.LoadPath(dir)
	if err != nil || spec.ID != "builtin" {
		t.Fatalf("spec=%v err=%v", spec, err)
	}
}
