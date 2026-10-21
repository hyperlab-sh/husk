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

// ResolvedCommand returns the command with relative entries resolved against
// the manifest directory. Entry 0 is always resolved; later entries are only
// resolved when they look like file paths (contain a path separator and exist
// relative to Dir).
func (p *Plugin) ResolvedCommand() []string {
	if len(p.Command) == 0 {
		return nil
	}
	out := make([]string, len(p.Command))
	out[0] = p.resolve(p.Command[0])
	copy(out[1:], p.Command[1:])
	return out
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
		// Plain executable (or script): implicit socket-transport plugin.
		return &Plugin{
			ID:        filepath.Base(path),
			Name:      filepath.Base(path),
			Version:   "0.0.0",
			Command:   []string{path},
			Transport: TransportSocket,
			Dir:       filepath.Dir(path),
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
