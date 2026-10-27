package main

import (
	"os"
	"path/filepath"

	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

// captureEnvironment keeps filesystem locations explicit and injectable. The
// source registry uses config_dir(), not config_path(): HERDR_CONFIG_PATH
// overrides only the upstream config file and never moves plugins.json.
func captureEnvironment(executable, platform string, getenv func(string) string, userHome func() (string, error), lookups ...func(string) (string, bool)) environment {
	env := environment{executable: executable, platform: platform, getenv: getenv}
	if len(lookups) > 0 {
		env.lookup = lookups[0]
	} else {
		env.lookup = func(key string) (string, bool) {
			v := getenv(key)
			return v, v != ""
		}
	}
	home, err := userHome()
	if err != nil || !filepath.IsAbs(home) {
		home = ""
	}
	env.home = home
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

func (env environment) value(key string) string {
	if env.getenv != nil {
		return env.getenv(key)
	}
	return os.Getenv(key)
}

func (env environment) lookupValue(key string) (string, bool) {
	if env.lookup != nil {
		return env.lookup(key)
	}
	return os.LookupEnv(key)
}

func (env environment) homeDir() string {
	if env.getenv != nil {
		return env.home
	}
	home, _ := os.UserHomeDir()
	return home
}
