package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
)

// TestMain lets a test re-execute this binary as the husk command: with
// HUSK_TEST_RUN_MAIN set it runs main() on the given arguments and exits.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "detect", "convert", "discover", "converter-action-list":
			os.Exit(execConverterTestRole(os.Args[1:]))
		}
	}
	if os.Getenv("HUSK_TEST_RUN_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStartInstanceID(t *testing.T) {
	cases := []struct {
		name    string
		given   bool
		want    string
		wantErr bool
	}{
		{"", false, "4242", false},
		{"notes", true, "notes", false},
		{"a-1", true, "a-1", false},
		{"", true, "", true}, // given but empty is invalid, never the pid default
		{"4242", true, "", true},
		{"Foo_1", true, "", true},
		{"-x", true, "", true},
		{strings.Repeat("a", 33), true, "", true},
	}
	for _, tc := range cases {
		got, err := startInstanceID(tc.name, tc.given, 4242)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("startInstanceID(%q, %v) = %q, %v; want %q, error %v", tc.name, tc.given, got, err, tc.want, tc.wantErr)
		}
	}
}

// An invalid --instance exits 2 before the filesystem is touched: no runtime
// directory, no socket, no entry.
func TestInvalidInstanceFlagExits2AndCreatesNothing(t *testing.T) {
	for _, name := range []string{"Foo_1", "4242", "", "-x", strings.Repeat("a", 33)} {
		t.Run(name, func(t *testing.T) {
			xdg, err := os.MkdirTemp("/tmp", "hk")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(xdg)
			cmd := exec.Command(os.Args[0], "--instance", name)
			cmd.Env = append(os.Environ(), "HUSK_TEST_RUN_MAIN=1", "XDG_RUNTIME_DIR="+xdg, "HOME="+xdg)
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 2 {
				t.Fatalf("err = %v, output = %s; want exit 2", err, out)
			}
			if !strings.Contains(string(out), "husk: invalid instance name "+`"`+name+`"`) ||
				!strings.Contains(string(out), "use [a-z0-9][a-z0-9-]*, at most 32 bytes, not digits only") {
				t.Fatalf("output = %s, want the spec's message", out)
			}
			entries, err := os.ReadDir(xdg)
			if err != nil || len(entries) != 0 {
				t.Fatalf("created %v (err %v) before validating", entries, err)
			}
		})
	}
}

// A positional after the start-mode flags is a misplaced subcommand: exit 2,
// nothing created, no instance started.
func TestLeftoverPositionalExits2AndCreatesNothing(t *testing.T) {
	xdg, err := os.MkdirTemp("/tmp", "hk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(xdg)
	cmd := exec.Command(os.Args[0], "--instance", "notes", "ping")
	cmd.Env = append(os.Environ(), "HUSK_TEST_RUN_MAIN=1", "XDG_RUNTIME_DIR="+xdg, "HOME="+xdg)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("err = %v, output = %s; want exit 2", err, out)
	}
	if !strings.Contains(string(out), `husk: unexpected argument "ping"; subcommands come first`) {
		t.Fatalf("output = %s", out)
	}
	if entries, err := os.ReadDir(xdg); err != nil || len(entries) != 0 {
		t.Fatalf("created %v (err %v)", entries, err)
	}
}

func TestPickVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ldflags string
		bi      *debug.BuildInfo
		ok      bool
		want    string
	}{
		{"ldflags wins", "1.2.3", &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}}, true, "1.2.3"},
		{"module version", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.4.0"}}, true, "v0.4.0"},
		{"devel is no version", "", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true, ""},
		{"empty module version", "", &debug.BuildInfo{}, true, ""},
		{"no build info", "", nil, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickVersion(tc.ldflags, tc.bi, tc.ok); got != tc.want {
				t.Errorf("pickVersion = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildVersionPrefersLdflags(t *testing.T) {
	defer func(v string) { version = v }(version)
	version = "1.2.3"
	if got := buildVersion(); got != "1.2.3" {
		t.Errorf("buildVersion() = %q, want the ldflags version", got)
	}
}

// An unusable runtime directory (not private to the user) is the environment
// exit, 7, the same code the CLI uses for the same check.
func TestStartUnusableRuntimeDirExits7(t *testing.T) {
	xdg, err := os.MkdirTemp("/tmp", "hk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(xdg)
	rt := filepath.Join(xdg, "husk")
	if err := os.Mkdir(rt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(rt, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "--instance", "notes")
	cmd.Env = append(os.Environ(), "HUSK_TEST_RUN_MAIN=1", "XDG_RUNTIME_DIR="+xdg, "HOME="+xdg)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != output.ExitEnvironment {
		t.Fatalf("err = %v, output = %s; want exit 7", err, out)
	}
	if !strings.Contains(string(out), "husk: runtime directory:") {
		t.Fatalf("output = %s", out)
	}
}
