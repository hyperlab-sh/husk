package upstream

import (
	"os"
	"path/filepath"
	"runtime"
)

// ConfigDir resolves the global source configuration directory shared by the
// plugin registry and session sockets. HERDR_CONFIG_PATH overrides only the
// configuration file and does not participate in directory resolution.
// Empty platform and nil getenv use runtime.GOOS and os.Getenv respectively.
// If home is unresolved, HOME supplies the fallback home directory.
// An unresolved or relative directory returns an empty path and false.
func ConfigDir(platform string, getenv func(string) string, home string) (string, bool) {
	if platform == "" {
		platform = runtime.GOOS
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	if !filepath.IsAbs(home) {
		home = getenv("HOME")
		if !filepath.IsAbs(home) {
			home = ""
		}
	}
	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" {
		if !filepath.IsAbs(xdg) {
			return "", false
		}
		return filepath.Join(xdg, "herdr"), true
	}
	if platform == "windows" {
		if appData := getenv("APPDATA"); filepath.IsAbs(appData) {
			return filepath.Join(appData, "herdr"), true
		}
		if profile := getenv("USERPROFILE"); filepath.IsAbs(profile) {
			return filepath.Join(profile, "AppData", "Roaming", "herdr"), true
		}
	}
	if home != "" {
		return filepath.Join(home, ".config", "herdr"), true
	}
	return "", false
}
