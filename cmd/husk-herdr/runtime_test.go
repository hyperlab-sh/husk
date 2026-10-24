package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeOptionsUsesPaneMarkerAndCapturedLocations(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		for _, marker := range []string{"", "0", "1"} {
			t.Run(platform+"/marker="+marker, func(t *testing.T) {
				_, plugin := testEnv(t)
				home := t.TempDir()
				exe, _ := os.Executable()
				values := map[string]string{"HOME": home, "HERDR_ENV": marker, "HERDR_SOCKET_PATH": filepath.Join(home, "inherited.sock"), "APPDATA": filepath.Join(home, "roaming"), "LOCALAPPDATA": filepath.Join(home, "local")}
				env := captureEnvironment(exe, platform, func(k string) string { return values[k] }, func() (string, error) { return home, nil }, func(k string) (string, bool) { v, ok := values[k]; return v, ok })
				if err := os.MkdirAll(filepath.Dir(env.configPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(env.configPath, []byte(fmt.Sprintf("herdr_bin=%q\n", exe)), 0600); err != nil {
					t.Fatal(err)
				}
				o, err := runtimeOptions(plugin, env)
				if err != nil {
					t.Fatal(err)
				}
				config := filepath.Join(home, ".config", "herdr")
				state := filepath.Join(home, ".local", "state", "herdr")
				if platform == "windows" {
					config = filepath.Join(home, "roaming", "herdr")
					state = filepath.Join(home, "local", "herdr")
				}
				wantSocket := filepath.Join(config, "herdr.sock")
				if marker == "1" {
					wantSocket = values["HERDR_SOCKET_PATH"]
				}
				if o.Socket != wantSocket || o.ConfigDir != filepath.Join(config, "plugins", "config", "sample%3Atool") || o.StateDir != filepath.Join(state, "plugins", "sample%3Atool") {
					t.Fatalf("captured locations not used: socket=%q config=%q state=%q", o.Socket, o.ConfigDir, o.StateDir)
				}
				values["XDG_STATE_HOME"] = "" // Presence is distinct from absence upstream.
				o, err = runtimeOptions(plugin, env)
				if err != nil || o.StateDir != filepath.Join("herdr", "plugins", "sample%3Atool") {
					t.Fatalf("set-empty state home lost: state=%q err=%v", o.StateDir, err)
				}
			})
		}
	}
}

func TestDoctorWithoutServerStillChecksRegistryRootsBindingsAndInstance(t *testing.T) {
	home := t.TempDir()
	exe, _ := os.Executable()
	// Only the home lookup knows this directory: reading process HOME instead
	// would silently miss the registry-adjacent config and all useful bindings.
	env := captureEnvironment(exe, "linux", func(string) string { return "" }, func() (string, error) { return home, nil })
	if err := os.MkdirAll(filepath.Dir(env.configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.configPath, []byte(fmt.Sprintf("herdr_bin=%q\n", exe)), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(env.registryPath)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.registryPath, []byte(`[{"plugin_id":"sample:tool","enabled":true,"plugin_root":"/plugins/sample"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	config := `[[keys.command]]
key = 'prefix+a'
type = 'plugin_action'
command = 'sample-tool.go'
[[keys.command]]
key = 'prefix+b'
type = 'plugin_action'
command = 'sample:tool.other'
[[keys.command]]
key = 'prefix+c'
type = 'shell'
command = 'echo plugin'
[[keys.command]]
key = 'prefix+d'
type = 'plugin_action'
command = 'unknown.go'
[[keys.command]]
key = 'prefix+e'
type = 'plugin_action'
command = 'sample-tools.go'
`
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUSK_BIN_PATH", exe)
	t.Setenv("HUSK_INSTANCE", "inherited-instance")
	t.Setenv("HUSK_PLUGIN_TOKEN", "must-be-dropped")
	var out, diagnostics bytes.Buffer
	if code := execute([]string{"doctor"}, &out, &diagnostics, env); code != 0 {
		t.Fatalf("absent server made independent diagnostics fail: %d %s %s", code, &out, &diagnostics)
	}
	text := out.String()
	for _, want := range []string{"server: absent (", "registry: " + env.registryPath, "registry_enabled=true", "root: /plugins/sample", "sample-tool.go", "sample:tool.other", "absent from instance; run husk-herdr sync", "manual check:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	for _, unwanted := range []string{"echo plugin", "unknown.go", "sample-tools.go", "instance: unavailable"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("unrelated binding or failed instance check %q in %s", unwanted, text)
		}
	}
	if strings.Count(text, "binding (copy into a Husk keymap)") != 2 || diagnostics.Len() != 0 {
		t.Fatalf("bindings or diagnostics differ: %s %s", &out, &diagnostics)
	}
}
