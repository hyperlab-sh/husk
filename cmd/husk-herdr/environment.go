package main

import (
	"path/filepath"

	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

// captureEnvironment keeps filesystem locations explicit and injectable. The
// source registry uses config_dir(), not config_path(): HERDR_CONFIG_PATH
// overrides only the upstream config file and never moves plugins.json.
func captureEnvironment(executable, platform string, getenv func(string) string, userHome func() (string, error)) environment {
	env := environment{executable: executable, platform: platform}
	home, err := userHome()
	if err != nil || !filepath.IsAbs(home) {
		home = ""
	}
	configHome := getenv("XDG_CONFIG_HOME")
	if configHome == "" && home != "" {
		configHome = filepath.Join(home, ".config")
	}
	if filepath.IsAbs(configHome) {
		env.configPath = filepath.Join(configHome, "husk-herdr", "config.toml")
	}
	if registryDir, ok := upstream.ConfigDir(platform, getenv, home); ok {
		env.registryPath = filepath.Join(registryDir, "plugins.json")
	}
	// With no resolvable absolute location discovery sees a missing registry,
	// never a relative path in the converter's current working directory.
	return env
}
