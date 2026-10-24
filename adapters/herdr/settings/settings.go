// Package settings loads the adapter's configuration, shared by conversion and runtime.
package settings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Config contains the adapter-only options. Empty values mean the defaults.
type Config struct {
	Binary  string            `toml:"herdr_bin"`
	Session string            `toml:"session"`
	Server  string            `toml:"server"`
	Hooks   string            `toml:"hooks"`
	Plugins map[string]Plugin `toml:"plugins"`
}

// Plugin overrides one plugin's hook policy.
type Plugin struct {
	Hooks string `toml:"hooks"`
}

// Load validates a file; a missing file uses defaults. Diagnostics name a line.
func Load(path string) (Config, error) {
	var c Config
	if path == "" {
		return c, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		var decoded *toml.DecodeError
		if errors.As(err, &decoded) {
			line, _ := decoded.Position()
			return c, fmt.Errorf("%s: line %d: %w", path, line, err)
		}
		return c, err
	}
	invalid := func(key string) error {
		line := 1
		for i, s := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(s), key) {
				line = i + 1
				break
			}
		}
		return fmt.Errorf("%s: line %d: invalid %s", path, line, key)
	}
	if c.Binary != "" && !filepath.IsAbs(c.Binary) {
		return c, invalid("herdr_bin")
	}
	if c.Server != "" && c.Server != "attach" && c.Server != "start" {
		return c, invalid("server")
	}
	valid := func(s string) bool { return s == "" || s == "auto" || s == "always" || s == "never" }
	if !valid(c.Hooks) {
		return c, invalid("hooks")
	}
	for _, p := range c.Plugins {
		if !valid(p.Hooks) {
			return c, invalid("hooks")
		}
	}
	return c, nil
}

// HookPolicy resolves the per-plugin override before the top-level default.
func (c Config) HookPolicy(id string) string {
	if p := c.Plugins[id].Hooks; p != "" {
		return p
	}
	if c.Hooks != "" {
		return c.Hooks
	}
	return "auto"
}

// HookOwner returns shim, server, or never for a validated policy.
func (c Config) HookOwner(id string, enabled bool) string {
	switch c.HookPolicy(id) {
	case "never":
		return "never"
	case "auto":
		if enabled {
			return "server"
		}
	}
	return "shim"
}
