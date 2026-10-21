// Package manifest loads husk-plugin.json plugin manifests.
//
// A manifest describes one out-of-process plugin: how to launch it, which
// transport it speaks, and which slots it claims. Manifest files live in a
// plugin directory next to the plugin's assets; the command's relative
// entries are resolved against that directory.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the manifest file name expected inside a plugin directory.
const FileName = "husk-plugin.json"

// Transport values.
const (
	TransportSocket = "socket"
	TransportStdio  = "stdio"
)

// Plugin is a parsed husk-plugin.json manifest.
type Plugin struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Language    string   `json:"language"`
	Description string   `json:"description"`
	Command     []string `json:"command"`
	Transport   string   `json:"transport"`
	Slots       []string `json:"slots"`

	// Dir is the directory the manifest (or executable) was found in.
	Dir string `json:"-"`
}

// ResolvedCommand returns the command ready to execute:
//
//   - argv[0] is left as-is when it is a bare name (looked up on PATH, so
//     interpreter commands like ["python3", "./plugin.py"] work); any other
//     relative argv[0] is resolved against the manifest directory; absolute
//     paths are kept.
//   - later arguments are only rewritten when they look like explicit
//     manifest-relative paths: they must contain a path separator and either
//     start with "./" or "../", or exist relative to the manifest directory.
//     Arbitrary option arguments are never touched.
//
// Execute the result with the working directory returned by WorkDir so that
// manifest-relative arguments resolve correctly regardless of Husk's cwd.
func (p *Plugin) ResolvedCommand() []string {
	if len(p.Command) == 0 {
		return nil
	}
	out := make([]string, len(p.Command))
	out[0] = p.resolveExecutable(p.Command[0])
	for i := 1; i < len(p.Command); i++ {
		if p.isManifestRelativePath(p.Command[i]) {
			out[i] = p.resolve(p.Command[i])
		} else {
			out[i] = p.Command[i]
		}
	}
	return out
}

// WorkDir is the working directory a plugin should be launched in: the
// directory containing its manifest (or, for plain executables, the
// executable's directory).
func (p *Plugin) WorkDir() string {
	if p.Dir == "" {
		return "."
	}
	return p.Dir
}

func (p *Plugin) resolveExecutable(s string) string {
	if filepath.IsAbs(s) {
		return s
	}
	if !strings.ContainsRune(s, filepath.Separator) {
		return s // bare name: leave to PATH lookup
	}
	return p.resolve(s)
}

func (p *Plugin) isManifestRelativePath(s string) bool {
	if filepath.IsAbs(s) || !strings.ContainsRune(s, filepath.Separator) {
		return false
	}
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return true
	}
	_, err := os.Stat(filepath.Join(p.Dir, s))
	return err == nil
}

func (p *Plugin) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(p.Dir, path)
}

func (p *Plugin) validate() error {
	if p.ID == "" {
		return errors.New("manifest: missing required field \"id\"")
	}
	if p.Name == "" {
		return errors.New("manifest: missing required field \"name\"")
	}
	if p.Version == "" {
		return errors.New("manifest: missing required field \"version\"")
	}
	if len(p.Command) == 0 || p.Command[0] == "" {
		return errors.New("manifest: missing required field \"command\"")
	}
	switch p.Transport {
	case "", TransportSocket:
		p.Transport = TransportSocket
	case TransportStdio:
	default:
		return fmt.Errorf("manifest: invalid transport %q (want %q or %q)", p.Transport, TransportSocket, TransportStdio)
	}
	return nil
}

func parseFile(path string) (*Plugin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Plugin
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("manifest: parse %s: %w", path, err)
	}
	p.Dir = filepath.Dir(path)
	if abs, err := filepath.Abs(p.Dir); err == nil {
		p.Dir = abs
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("manifest: %s: %w", path, err)
	}
	return &p, nil
}

// Load resolves a --plugin argument into a Plugin. The path may be:
//   - a plugin directory containing husk-plugin.json
//   - a husk-plugin.json file itself
//   - any other existing file: treated as a socket-transport executable
//   - a non-existent path: an error unless it looks like a command with
//     arguments; then it is treated as a socket-transport command
//
// Relative executable paths are resolved against the path's directory, so
// `husk --plugin ./plugins/foo/plugin.sh` works from anywhere.
func Load(path string) (*Plugin, error) {
	manifestPath := path
	if stat, err := os.Stat(path); err == nil {
		if stat.IsDir() {
			manifestPath = filepath.Join(path, FileName)
		}
		if _, err := os.Stat(manifestPath); err == nil && filepath.Base(manifestPath) == FileName {
			return parseFile(manifestPath)
		}
		// Plain executable (or script): implicit socket-transport plugin. Resolve
		// to an absolute path once so later resolution cannot double it up.
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		return &Plugin{
			ID:        filepath.Base(abs),
			Name:      filepath.Base(abs),
			Version:   "0.0.0",
			Command:   []string{abs},
			Transport: TransportSocket,
			Dir:       filepath.Dir(abs),
		}, nil
	}
	if parts := splitCommand(path); len(parts) > 1 {
		return &Plugin{
			ID:        parts[0],
			Name:      parts[0],
			Version:   "0.0.0",
			Command:   parts,
			Transport: TransportSocket,
			Dir:       ".",
		}, nil
	}
	return nil, fmt.Errorf("manifest: plugin path %s not found", path)
}

// LoadDir loads every immediate subdirectory of dir that contains a
// husk-plugin.json. Subdirectories without a manifest are skipped. Returns
// an error only if dir itself cannot be read.
func LoadDir(dir string) ([]*Plugin, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var plugins []*Plugin
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), FileName)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		p, err := parseFile(path)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, p)
	}
	return plugins, nil
}

func splitCommand(path string) []string {
	var parts []string
	cur := ""
	for _, r := range path {
		if r == ' ' {
			if cur != "" {
				parts = append(parts, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	return parts
}
