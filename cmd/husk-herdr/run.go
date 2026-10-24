package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/settings"
	"github.com/hyperlab-sh/husk/adapters/herdr/shim"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

func runtimeOptions(dir string, env environment) (shim.Options, error) {
	c, err := settings.Load(env.configPath)
	if err != nil {
		return shim.Options{}, err
	}
	binary := c.Binary
	if binary == "" {
		binary = "herdr"
	}
	binary, err = exec.LookPath(binary)
	if err != nil {
		return shim.Options{}, fmt.Errorf("herdr binary not found")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return shim.Options{}, err
	}
	home := env.homeDir()
	inheritedSocket := ""
	if env.value("HERDR_ENV") == "1" {
		inheritedSocket = env.value("HERDR_SOCKET_PATH")
	}
	socket, err := upstream.ResolveSocket(upstream.SocketOptions{Session: c.Session, SocketPath: inheritedSocket, Home: home, Platform: env.platform, Getenv: env.value})
	if err != nil {
		return shim.Options{}, err
	}
	root, ok := upstream.ConfigDir(env.platform, env.value, home)
	if !ok {
		return shim.Options{}, fmt.Errorf("absolute configuration directory is required")
	}
	o := shim.Options{Dir: dir, Binary: binary, Socket: socket, Settings: c, Env: os.Environ(), RegistryPath: env.registryPath, Platform: env.platform, Host: &shim.SocketHost{Path: os.Getenv("HUSK_SOCKET_PATH"), Token: os.Getenv("HUSK_PLUGIN_TOKEN")}, LogPath: stderrPath()}
	if dir != "" {
		data, e := os.ReadFile(filepath.Join(dir, "herdr-plugin.toml"))
		if e != nil {
			return o, e
		}
		m, e := manifest.Parse(data)
		if e != nil {
			return o, e
		}
		component := shim.PluginPathComponent(m.ID)
		o.ConfigDir = filepath.Join(root, "plugins", "config", component)
		o.StateDir = filepath.Join(shim.StateDir(env.platform, env.lookupValue, os.TempDir()), "plugins", component)
	}
	return o, nil
}
func runPlugin(dir string, out, log io.Writer, env environment) int {
	dir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintln(log, err)
		return 1
	}
	o, err := runtimeOptions(dir, env)
	if err == nil {
		if os.Getenv("HUSK_SOCKET_PATH") == "" || os.Getenv("HUSK_PLUGIN_TOKEN") == "" {
			err = fmt.Errorf("HUSK_SOCKET_PATH and HUSK_PLUGIN_TOKEN must be set by the host")
		} else {
			err = shim.Run(context.Background(), o, os.Stdin, out, log)
		}
	}
	return reportRun(err, log)
}

// reportRun leaves the terminal diagnostic as the final stderr line. Pod failure
// reasons and failure toasts are the host's exit-reporting policy, not ours.
func reportRun(err error, log io.Writer) int {
	if err != nil {
		fmt.Fprintln(log, err)
		return 1
	}
	return 0
}
