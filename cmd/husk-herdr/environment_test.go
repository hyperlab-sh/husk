package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

func TestCapturedRegistryLocations(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		values         map[string]string
		homeError      bool
		want           string
	}{
		{"xdg", "linux", map[string]string{"XDG_CONFIG_HOME": "xdg", "HERDR_CONFIG_PATH": "alternate/config.toml"}, false, "xdg/herdr/plugins.json"},
		{"config_override_does_not_move_registry", "linux", map[string]string{"HERDR_CONFIG_PATH": "alternate/config.toml"}, false, "home/.config/herdr/plugins.json"},
		{"windows_appdata", "windows", map[string]string{"APPDATA": "roaming", "HERDR_CONFIG_PATH": "alternate/config.toml"}, false, "roaming/herdr/plugins.json"},
		{"windows_profile", "windows", map[string]string{"USERPROFILE": "profile"}, false, "profile/AppData/Roaming/herdr/plugins.json"},
		{"windows_home_environment", "windows", map[string]string{"HOME": "homeenv"}, true, "homeenv/.config/herdr/plugins.json"},
		{"no_home", "linux", nil, true, ""},
		{"no_home_with_xdg", "linux", map[string]string{"XDG_CONFIG_HOME": "xdg"}, true, "xdg/herdr/plugins.json"},
		{"relative_xdg", "linux", map[string]string{"XDG_CONFIG_HOME": "relative"}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, dir := testEnv(t)
			root := t.TempDir()
			lookup := func(key string) string {
				value := tc.values[key]
				if value == "" || tc.name == "relative_xdg" {
					return value
				}
				return filepath.Join(root, filepath.FromSlash(value))
			}
			home := func() (string, error) {
				if tc.homeError {
					return "", errors.New("no home")
				}
				return filepath.Join(root, "home"), nil
			}
			env := captureEnvironment("/opt/husk-herdr", tc.platform, lookup, home)
			want := ""
			if tc.want != "" {
				want = filepath.Join(root, filepath.FromSlash(tc.want))
			}
			if env.registryPath != want {
				t.Fatalf("registry=%q want %q", env.registryPath, want)
			}
			socketHome, homeErr := home()
			if homeErr != nil {
				socketHome = ""
			}
			socketPath, socketErr := upstream.ResolveSocket(upstream.SocketOptions{
				Platform: tc.platform, Getenv: lookup, Home: socketHome,
			})
			if want == "" {
				if socketErr == nil {
					t.Fatalf("socket resolved without an absolute config directory: %q", socketPath)
				}
			} else if socketErr != nil || filepath.Dir(socketPath) != filepath.Dir(env.registryPath) {
				t.Fatalf("registry and socket disagree: registry=%q socket=%q err=%v", env.registryPath, socketPath, socketErr)
			}
			if env.configPath != "" && !filepath.IsAbs(env.configPath) {
				t.Fatalf("relative adapter config: %q", env.configPath)
			}
			if want != "" {
				if err := os.MkdirAll(filepath.Dir(want), 0700); err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal([]map[string]any{{"plugin_id": "sample:tool", "plugin_root": dir, "enabled": true}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(want, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, diagnostics bytes.Buffer
			if code := execute([]string{"discover"}, &out, &diagnostics, env); code != 0 {
				t.Fatalf("discover=%d err=%s", code, &diagnostics)
			}
			if want != "" {
				if out.String() != dir+"\n" || diagnostics.Len() != 0 {
					t.Fatalf("wrong registry discovered: out=%s err=%s", &out, &diagnostics)
				}
			} else if out.Len() != 0 || !bytes.Contains(diagnostics.Bytes(), []byte(`"code":"registry_warning"`)) {
				t.Fatalf("unresolved home did not behave as missing registry: out=%s err=%s", &out, &diagnostics)
			}
		})
	}
}
