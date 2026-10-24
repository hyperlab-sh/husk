package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

func checkConfig(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings struct {
		Binary  string `toml:"herdr_bin"`
		Session string `toml:"session"`
		Server  string `toml:"server"`
		Hooks   string `toml:"hooks"`
		Plugins map[string]struct {
			Hooks string `toml:"hooks"`
		} `toml:"plugins"`
	}
	if err := toml.Unmarshal(data, &settings); err != nil {
		var decoded *toml.DecodeError
		if errors.As(err, &decoded) {
			line, _ := decoded.Position()
			return fmt.Errorf("%s: line %d: %w", path, line, err)
		}
		return err
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
	if settings.Binary != "" && !filepath.IsAbs(settings.Binary) {
		return invalid("herdr_bin")
	}
	if settings.Server != "" && settings.Server != "attach" && settings.Server != "start" {
		return invalid("server")
	}
	validHooks := func(s string) bool { return s == "" || s == "auto" || s == "always" || s == "never" }
	if !validHooks(settings.Hooks) {
		return invalid("hooks")
	}
	for _, plugin := range settings.Plugins {
		if !validHooks(plugin.Hooks) {
			return invalid("hooks")
		}
	}
	return nil
}
