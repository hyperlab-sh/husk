package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/registry"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/pelletier/go-toml/v2"
)

func doctor(args []string, out, diagnostics io.Writer, env environment) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	instance := fs.String("instance", "", "Husk instance name")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return 2
	}
	o, err := runtimeOptions("", env)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	fmt.Fprintf(out, "binary: %s\nsocket: %s\n", o.Binary, o.Socket)
	session, err := upstream.New(upstream.Options{Socket: o.Socket}).Attach(context.Background(), "0.9.0")
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	fmt.Fprintf(out, "version: %s\nregistry: %s\n", session.Version, env.registryPath)
	enabled := map[string]bool{}
	var plugins []struct {
		ID      string `json:"plugin_id"`
		Enabled bool   `json:"enabled"`
	}
	_ = json.Unmarshal(session.Plugins, &plugins)
	for _, p := range plugins {
		enabled[p.ID] = p.Enabled
	}
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
		fmt.Fprintf(out, "plugin %s: registry_enabled=%t server_enabled=%t hooks=%s\n", rec.ID, rec.Enabled, enabled[rec.ID], o.Settings.HookOwner(rec.ID, enabled[rec.ID]))
		id, _ := manifest.MapID(rec.ID)
		if len(mapped[id]) > 1 {
			fmt.Fprintf(out, "duplicate id %s: %s\n", id, strings.Join(mapped[id], ", "))
		}
	}
	if o.Settings.Server == "start" && o.Settings.Session != "" {
		fmt.Fprintln(out, "warning: a separate session runs enabled startup/event hooks again; daemon hooks can be duplicated")
	}
	root, _ := upstream.ConfigDir(env.platform, os.Getenv, os.Getenv("HOME"))
	config := os.Getenv("HERDR_CONFIG_PATH")
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
				if strings.Contains(string(raw), "plugin") {
					fmt.Fprintf(out, "binding (copy into a Husk keymap): %s\n", raw)
				}
			}
		}
	}
	// User-only diagnostics use the installed Husk CLI, never the shim token.
	binary := os.Getenv("HUSK_BIN_PATH")
	if binary == "" {
		binary = "husk"
	}
	cliArgs := []string{"plugin", "list", "--format", "json"}
	if *instance != "" {
		cliArgs = append(cliArgs, "--instance", *instance)
	}
	cmd := exec.Command(binary, cliArgs...)
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(k, "HUSK_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	raw, e := cmd.Output()
	if e != nil {
		fmt.Fprintf(out, "instance: unavailable (%v)\n", e)
	} else {
		var list struct {
			Plugins []struct {
				ID string `json:"id"`
			} `json:"plugins"`
		}
		if json.Unmarshal(raw, &list) == nil {
			present := map[string]bool{}
			for _, p := range list.Plugins {
				present[p.ID] = true
			}
			for _, rec := range records {
				id, _ := manifest.MapID(rec.ID)
				if rec.Enabled && manifest.Supports(rec.Platforms, env.platform) && !present[id] {
					fmt.Fprintf(out, "plugin %s absent from instance; run husk-herdr sync\n", rec.ID)
				}
			}
		}
	}
	fmt.Fprintln(out, "manual check: compare the attached session, hook owner and action bindings before invoking plugin commands")
	return 0
}
