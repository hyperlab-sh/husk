// Command husk-herdr implements the executable manifest-converter roles.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/registry"
)

type environment struct {
	executable   string
	platform     string
	configPath   string
	registryPath string
}

func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr, captureEnvironment(exe, runtime.GOOS, os.Getenv, os.UserHomeDir)))
}

func execute(args []string, out, diagnostics io.Writer, env environment) int {
	usage := func() int {
		fmt.Fprintln(diagnostics, "usage: husk-herdr detect <dir> | discover | convert <dir>")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "detect", "convert":
		if len(args) != 2 || args[1] == "" || args[1][0] == '-' {
			return usage()
		}
	case "discover":
		if len(args) != 1 {
			return usage()
		}
	default:
		return usage()
	}
	if args[0] == "detect" {
		info, err := os.Stat(filepath.Join(args[1], "herdr-plugin.toml"))
		if err != nil || !info.Mode().IsRegular() {
			return 1
		}
		return 0
	}
	emit := func(problems []manifest.Problem) bool {
		return manifest.WriteDiagnostics(diagnostics, problems, nil) == nil
	}
	if args[0] == "discover" {
		// Discovery must remain available so invalid adapter settings can be
		// reported as per-plugin conversion refusals in the host catalog.
		records, problems := registry.Read(env.registryPath)
		if !emit(problems) {
			return 1
		}
		dirs, problems := registry.Discover(records, env.platform)
		if !emit(problems) {
			return 1
		}
		for _, dir := range dirs {
			if _, err := fmt.Fprintln(out, dir); err != nil {
				return 1
			}
		}
		return 0
	}
	if env.configPath != "" {
		if err := checkConfig(env.configPath); err != nil {
			emit([]manifest.Problem{{Code: "adapter_config_invalid", Message: err.Error(), Field: env.configPath}})
			return 1
		}
	}
	// Missing or invalid registries affect discovery only. A directly loaded
	// directory remains convertible without registry-local collision scope.
	records, _ := registry.Read(env.registryPath)
	dir, err := filepath.Abs(args[1])
	if err != nil {
		emit([]manifest.Problem{{Code: "manifest_invalid", Message: err.Error(), Field: "manifest"}})
		return 1
	}
	data, err := os.ReadFile(filepath.Join(dir, "herdr-plugin.toml"))
	if err != nil {
		emit([]manifest.Problem{{Code: "manifest_invalid", Message: err.Error(), Field: "manifest"}})
		return 1
	}
	doc, problems, notes := manifest.Convert(data, manifest.Options{Dir: dir, Executable: env.executable, Platform: env.platform, RegistryIDs: registry.IDsFor(records, dir)})
	// Fatal conversion problems come first; the host keeps only the first
	// stderr line on refusal. The manifest converter drops earlier warnings.
	if err := manifest.WriteDiagnostics(diagnostics, problems, notes); err != nil {
		return 1
	}
	if doc == nil {
		return 1
	}
	if err := json.NewEncoder(out).Encode(doc); err != nil {
		return 1
	}
	return 0
}
