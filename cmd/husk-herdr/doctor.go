package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/registry"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/pelletier/go-toml/v2"
)

func doctor(args []string, out, diagnostics io.Writer, env environment) int {
	target, ok := instanceArgs("doctor", args, diagnostics)
	if !ok {
		return 2
	}
	o, err := runtimeOptions("", env)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	fmt.Fprintf(out, "binary: %s\nsocket: %s\n", o.Binary, o.Socket)
	version, err := upstream.New(upstream.Options{Socket: o.Socket, Dial: env.upstreamDial}).Check(context.Background(), "0.9.0")
	result := 0
	if err != nil {
		status := "absent"
		var named *upstream.Error
		if errors.As(err, &named) && named.Code != "unavailable" {
			status, result = "failed", 1
		}
		fmt.Fprintf(out, "server: %s (%v)\n", status, err)
	} else {
		fmt.Fprintf(out, "version: %s\n", version)
	}
	fmt.Fprintf(out, "registry: %s\n", env.registryPath)
	records, problems := registry.Read(env.registryPath)
	for _, p := range problems {
		fmt.Fprintln(out, p.Message)
	}
	mapped := map[string][]string{}
	for _, rec := range records {
		id, e := manifest.MapID(rec.ID)
		if e == nil && rec.Enabled {
			mapped[id] = append(mapped[id], rec.ID)
		}
	}
	for _, rec := range records {
		if !manifest.Supports(rec.Platforms, env.platform) {
			continue
		}
		fmt.Fprintf(out, "plugin %s: registry_enabled=%t\n", rec.ID, rec.Enabled)
		fmt.Fprintf(out, "plugin %s root: %s\n", rec.ID, rec.Root)
		id, _ := manifest.MapID(rec.ID)
		if len(mapped[id]) > 1 {
			fmt.Fprintf(out, "duplicate id %s: %s\n", id, strings.Join(mapped[id], ", "))
		}
	}
	root, _ := upstream.ConfigDir(env.platform, env.value, env.homeDir())
	config := env.value("HERDR_CONFIG_PATH")
	if config == "" {
		config = filepath.Join(root, "config.toml")
	}
	if data, e := os.ReadFile(config); e == nil {
		var c struct {
			Keys struct {
				Command []map[string]any `toml:"command"`
			} `toml:"keys"`
		}
		if toml.Unmarshal(data, &c) == nil {
			for _, binding := range c.Keys.Command {
				raw, _ := json.Marshal(binding)
				var parsed struct{ Type, Command string }
				if json.Unmarshal(raw, &parsed) != nil || parsed.Type != "plugin_action" {
					continue
				}
				for _, rec := range records {
					id, e := manifest.MapID(rec.ID)
					if e == nil && rec.Enabled && manifest.Supports(rec.Platforms, env.platform) && (strings.HasPrefix(parsed.Command, id+".") || strings.HasPrefix(parsed.Command, rec.ID+".")) {
						fmt.Fprintf(out, "binding (copy into a Husk keymap): %s\n", raw)
						break
					}
				}
			}
		}
	}
	// User-only diagnostics use the installed Husk CLI, never the shim token.
	present, problem, code := env.instancePlugins(target)
	if code != 0 {
		fmt.Fprintf(out, "instance: unavailable (%s)\n", problem)
	} else {
		dirs, problems := registry.Discover(records, env.platform)
		syncable := map[string]bool{}
		for _, dir := range dirs {
			syncable[dir] = true
		}
		for _, p := range problems {
			if p.Code == "registry_warning" {
				fmt.Fprintf(out, "%s: %s\n", p.Code, oneLine(p.Message))
			}
		}
		for _, rec := range records {
			if !rec.Enabled || !manifest.Supports(rec.Platforms, env.platform) || !syncable[rec.Root] {
				continue
			}
			id, problem := convertedID(rec.Root, env)
			if problem != "" {
				fmt.Fprintf(out, "plugin %s: %s\n", rec.ID, problem)
				continue
			}
			if _, found := present[id]; !found {
				fmt.Fprintf(out, "plugin %s absent from instance; run husk-herdr sync\n", rec.ID)
			}
		}
	}
	fmt.Fprintln(out, "manual check: compare the selected session and action bindings before invoking plugin commands")
	return result
}
