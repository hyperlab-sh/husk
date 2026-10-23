package main

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hyperlab-sh/husk/internal/host"
	"reflect"
	"testing"
)

func pluginFlags(t *testing.T, args ...string) *flag.FlagSet {
	t.Helper()
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	var paths, dirs, addPaths, addDirs stringFlag
	fs.Var(&paths, "plugin", "")
	fs.Var(&dirs, "plugin-dir", "")
	fs.Var(&addPaths, "add-plugin", "")
	fs.Var(&addDirs, "add-plugin-dir", "")
	var bundle bundleFlag
	fs.Var(&bundle, "bundle", "")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return fs
}

func pluginFlagDocs(t *testing.T, args ...string) map[string]map[string]any {
	t.Helper()
	return pluginInstanceDocs(pluginFlags(t, args...))
}

func TestAddPluginPreservesConfiguredPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	configDir := filepath.Join(root, "config", "husk")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, id := range []string{"a", "b", "c"} {
		dir := filepath.Join(root, id)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		manifest := `{"manifest_version":2,"protocol":2,"id":"` + id + `","name":"Example","version":"1","command":["./plugin"]}`
		if err := os.WriteFile(filepath.Join(dir, "husk-plugin.json"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, dir)
	}
	a, b, c := paths[0], paths[1], paths[2]
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeC, err := filepath.Rel(cwd, c)
	if err != nil {
		t.Fatal(err)
	}
	// Use the process directory itself for relative/absolute dedup, without
	// changing cwd or relying on a path outside the test's private files.
	for _, tc := range []struct {
		name                      string
		basePaths, baseDirs       any
		args, wantPaths, wantDirs []string
		invalid                   bool
	}{
		{"append paths", []string{a, b}, []string{}, []string{"--add-plugin", c}, []string{a, b, c}, []string{}, false},
		{"duplicate path", []string{a, b}, []string{}, []string{"--add-plugin", a}, []string{a, b}, []string{}, false},
		{"clean spelling", []string{a, b}, []string{}, []string{"--add-plugin", a + "/."}, []string{a, b}, []string{}, false},
		{"relative absolute paths", []string{relativeC}, []string{}, []string{"--add-plugin", c}, []string{relativeC}, []string{}, false},
		{"lower duplicates", []string{a, a, b}, []string{}, []string{"--add-plugin", c}, []string{a, b, c}, []string{}, false},
		{"addition order", []string{a}, []string{}, []string{"--add-plugin", c, "--add-plugin", b, "--add-plugin", c}, []string{a, c, b}, []string{}, false},
		{"append dirs", []string{}, []string{a, b}, []string{"--add-plugin-dir", c}, []string{}, []string{a, b, c}, false},
		{"directory addition order", []string{}, []string{a}, []string{"--add-plugin-dir", c, "--add-plugin-dir", b, "--add-plugin-dir", c}, []string{}, []string{a, c, b}, false},
		{"relative absolute dirs", []string{}, []string{"."}, []string{"--add-plugin-dir", cwd}, []string{}, []string{"."}, false},
		{"cross array", []string{a, b}, []string{a}, []string{"--plugin-dir", b, "--add-plugin", c}, []string{a, b, c}, []string{b}, false},
		{"invalid paths", 42, []string{}, []string{"--add-plugin", c}, []string{c}, []string{}, true},
		{"invalid dirs", []string{}, 42, []string{"--add-plugin-dir", c}, []string{}, []string{c}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := map[string]any{"husk": map[string]any{"plugins": map[string]any{"paths": tc.basePaths, "dirs": tc.baseDirs}}, "theme": map[string]any{"allow_low_contrast": "invalid"}}
			data, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			fs := pluginFlags(t, tc.args...)
			additions, err := pluginAdditions(fs)
			if err != nil {
				t.Fatal(err)
			}
			prog := newProgramRef()
			h, err := newHost(prog, goldenModel(), "add-flags", shortRuntimeDir(t, "add-flags"), pluginInstanceDocs(fs), nil, host.WithPluginAdditions(additions))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { prog.closeGate(); _ = h.Close() }()
			for key, want := range map[string][]string{"paths": tc.wantPaths, "dirs": tc.wantDirs} {
				value, source, err := h.Frontend().Setting("husk.plugins." + key)
				encoded, _ := json.Marshal(value)
				var got []string
				_ = json.Unmarshal(encoded, &got)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s = %v (%s, %v), want %v", key, got, source, err, want)
				}
				if _, added := additions[key]; added && source != "instance" {
					t.Fatalf("%s source = %s", key, source)
				}
			}
			if tc.invalid {
				found := false
				for _, report := range h.Frontend().StartupReports() {
					for _, p := range report.Problems {
						if p.Code == "invalid_value" && p.Layer == "user" && strings.HasPrefix(p.Key, "husk.plugins.") {
							found = true
						}
					}
				}
				if !found {
					t.Fatal("invalid lower array diagnostic was lost")
				}
			}
		})
	}
}

func TestAddPluginUsageAndHelp(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		code     int
		contains []string
	}{
		{"paths conflict", []string{"--plugin", "a", "--add-plugin", "b"}, 2, []string{"--plugin", "--add-plugin", "cannot be used together"}},
		{"dirs conflict", []string{"--add-plugin-dir", "a", "--plugin-dir", "b"}, 2, []string{"--plugin-dir", "--add-plugin-dir", "cannot be used together"}},
		{"help", []string{"--help"}, 0, []string{"-add-plugin value", "-add-plugin-dir value"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cmd := exec.Command(os.Args[0], tc.args...)
			cmd.Env = append(os.Environ(), "HUSK_TEST_RUN_MAIN=1", "HOME="+root, "XDG_RUNTIME_DIR="+root)
			out, err := cmd.CombinedOutput()
			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.code {
				t.Fatalf("exit %d, want %d: %s", code, tc.code, out)
			}
			for _, text := range tc.contains {
				if !strings.Contains(string(out), text) {
					t.Fatalf("output lacks %q: %s", text, out)
				}
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatalf("usage touched filesystem: %v, %v", entries, err)
			}
		})
	}
}

func TestBundleFlagInstanceDocument(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "application")
	relative, err := filepath.Abs("application")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		args   []string
		want   string
		plugin bool
	}{
		{"relative", []string{"--bundle", "application"}, relative, false},
		{"absolute", []string{"--bundle", abs}, abs, false},
		{"last wins", []string{"--bundle", abs, "--bundle", "application"}, relative, false},
		{"empty disables", []string{"--bundle", abs, "--bundle", ""}, "", false},
		{"combined", []string{"--bundle", abs, "--plugin", "example"}, abs, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := pluginFlagDocs(t, tc.args...)
			husk := map[string]any{"bundle": tc.want}
			if tc.plugin {
				husk["plugins"] = map[string]any{"paths": []string{"example"}}
			}
			want := map[string]map[string]any{"config": {"husk": husk}}
			if !reflect.DeepEqual(docs, want) {
				t.Fatalf("got %#v want %#v", docs, want)
			}
		})
	}
}

func TestPluginFlagInstanceDocument(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"absent", nil, nil},
		{"paths", []string{"--plugin", "a", "--plugin", "b"}, map[string]any{"paths": []string{"a", "b"}}},
		{"dirs", []string{"--plugin-dir", "d"}, map[string]any{"dirs": []string{"d"}}},
		{"literal", []string{"--plugin", "~/raw/./plugin", "--plugin-dir", ""}, map[string]any{"paths": []string{"~/raw/./plugin"}, "dirs": []string{""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pluginFlagDocs(t, tc.args...)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("absent flags produced document: %#v", got)
				}
				return
			}
			want := map[string]map[string]any{"config": {"husk": map[string]any{"plugins": tc.want}}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v want %#v", got, want)
			}
		})
	}
}
