package host

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "detect", "convert", "discover":
			os.Exit(hostConverterChild())
		case "converter-idle":
			scanner := bufio.NewScanner(os.Stdin)
			for scanner.Scan() {
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func hostConverterChild() int {
	role := filepath.Base(os.Args[0])
	command := os.Args[1]
	if trace := os.Getenv("CONVERTER_TRACE"); trace != "" {
		f, err := os.OpenFile(trace, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return 8
		}
		fmt.Fprintln(f, role+":"+strings.Join(os.Args[1:], " "))
		_ = f.Close()
	}
	if command == "discover" {
		fmt.Print(os.Getenv("CONVERTER_DISCOVER"))
		if os.Getenv("CONVERTER_FAIL_DISCOVER") == role {
			fmt.Fprintln(os.Stderr, "failed discovery")
			return 5
		}
		return 0
	}
	dir := os.Args[2]
	format, err := os.ReadFile(filepath.Join(dir, "format"))
	if err != nil || string(format) != role {
		return 1
	}
	if command == "detect" {
		return 0
	}
	if filepath.Base(dir) == "bad" {
		fmt.Fprintln(os.Stderr, "bad conversion")
		return 6
	}
	exe, _ := os.Executable()
	id := filepath.Base(dir)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"manifest_version": 2, "protocol": protocol.Major, "id": id, "name": id, "version": "1", "command": []string{exe, "converter-idle"}, "actions": []any{map[string]any{"id": id + ".open", "title": "Open"}}, "x-fixture": map[string]any{"kept": true}})
	return 0
}

func TestHostExecConvertersStartupAndAdd(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	root := t.TempDir()
	exe, _ := os.Executable()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.Symlink(exe, path); err != nil {
			t.Fatal(err)
		}
	}
	makeDir := func(id, format string) string {
		t.Helper()
		dir := filepath.Join(root, id)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "format"), []byte(format), 0600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	alpha := makeDir("alpha", "first")
	beta := makeDir("beta", "second")
	later := makeDir("later", "second")
	bad := makeDir("bad", "first")
	trace := filepath.Join(root, "trace")
	t.Setenv("CONVERTER_TRACE", trace)
	t.Setenv("CONVERTER_DISCOVER", alpha+"\n"+beta+"\n"+beta+"\n")
	var logs []string
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) {
		settings := pluginSettings(c)
		settings["paths"] = []string{alpha, bad}
		settings["converters"] = []string{second, filepath.Join(root, "missing")}
	}, withHook(hook), WithConverters([]string{first}), WithLogger(func(_, msg string) { logs = append(logs, msg) }))
	if err != nil {
		t.Fatal(err)
	}
	// Startup diagnostics are synchronous; inspect before the loop starts logging.
	joined := strings.Join(logs, "\n")
	if strings.Count(joined, "converter_unavailable") != 1 || !strings.Contains(joined, "converter_failed") {
		t.Fatal(joined)
	}
	if h.root.Planned() != 2 {
		t.Fatalf("planned=%d", h.root.Planned())
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Count(text, "first:detect "+alpha+"\n") != 1 || strings.Count(text, "second:convert "+beta+"\n") != 1 || strings.Count(text, ":discover\n") != 2 || strings.Contains(text, "second:detect "+alpha) {
		t.Fatal(text)
	}
	runHost(t, ready, h)
	code, out, stderr := cliRun(h, "action", "list", "--format", "json")
	if code != 0 || !strings.Contains(out, "alpha.open") || !strings.Contains(out, "beta.open") {
		t.Fatalf("actions: %d %s %s", code, out, stderr)
	}
	code, out, stderr = cliRun(h, "plugin", "add", later, "--format", "json")
	if code != 0 || !strings.Contains(out, `"plugin":"later"`) {
		t.Fatalf("add: %d %s %s", code, out, stderr)
	}
	code, out, stderr = cliRun(h, "action", "list", "--format", "json")
	if code != 0 || !strings.Contains(out, "later.open") {
		t.Fatalf("added actions: %d %s %s", code, out, stderr)
	}
}

func TestHostExecConverterDiscoveryFailure(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	exe, _ := os.Executable()
	t.Setenv("CONVERTER_FAIL_DISCOVER", filepath.Base(exe))
	var logs []string
	h, err := boot(t, nil, WithConverters([]string{exe}), WithLogger(func(_, msg string) { logs = append(logs, msg) }))
	if err != nil {
		t.Fatal(err)
	}
	if h.root.Planned() != 0 || strings.Count(strings.Join(logs, "\n"), "converter_failed") != 1 {
		t.Fatalf("planned=%d logs=%v", h.root.Planned(), logs)
	}
}
