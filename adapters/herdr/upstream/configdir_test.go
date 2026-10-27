package upstream_test

import (
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

func TestConfigDir(t *testing.T) {
	for _, tc := range []struct {
		name, platform, home string
		values               map[string]string
		want                 string
		ok                   bool
	}{
		{"home", "linux", "/home/test", nil, "/home/test/.config/herdr", true},
		{"macos", "darwin", "/home/test", nil, "/home/test/.config/herdr", true},
		{"xdg", "linux", "/home/test", map[string]string{"XDG_CONFIG_HOME": "/config"}, "/config/herdr", true},
		{"xdg_without_home", "linux", "", map[string]string{"XDG_CONFIG_HOME": "/config"}, "/config/herdr", true},
		{"config_file_ignored", "linux", "/home/test", map[string]string{"HERDR_CONFIG_PATH": "/alternate/config.toml"}, "/home/test/.config/herdr", true},
		{"windows_appdata", "windows", "/home/test", map[string]string{"APPDATA": "/roaming"}, "/roaming/herdr", true},
		{"windows_xdg_first", "windows", "/home/test", map[string]string{"XDG_CONFIG_HOME": "/config", "APPDATA": "/roaming"}, "/config/herdr", true},
		{"windows_profile", "windows", "/home/test", map[string]string{"USERPROFILE": "/profile"}, "/profile/AppData/Roaming/herdr", true},
		{"windows_home", "windows", "/home/test", nil, "/home/test/.config/herdr", true},
		{"windows_home_environment", "windows", "", map[string]string{"HOME": "/home/env"}, "/home/env/.config/herdr", true},
		{"home_environment", "linux", "", map[string]string{"HOME": "/home/env"}, "/home/env/.config/herdr", true},
		{"relative_home_environment", "windows", "", map[string]string{"HOME": "relative"}, "", false},
		{"no_home", "linux", "", nil, "", false},
		{"relative_home", "linux", "relative", nil, "", false},
		{"relative_xdg", "linux", "/home/test", map[string]string{"XDG_CONFIG_HOME": "relative"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := upstream.ConfigDir(tc.platform, func(key string) string { return tc.values[key] }, tc.home)
			want := tc.want
			if want != "" {
				want = filepath.FromSlash(want)
			}
			if got != want || ok != tc.ok {
				t.Fatalf("directory=%q, ok=%v; want %q, %v", got, ok, want, tc.ok)
			}
		})
	}
}

func TestConfigDirDefaults(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	got, ok := upstream.ConfigDir("", nil, "")
	if got != filepath.Join(root, "herdr") || !ok {
		t.Fatalf("default environment not used: directory=%q ok=%v", got, ok)
	}
}
