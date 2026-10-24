// Package registry reads the user's installed-plugin catalog without modifying it.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
)

// Record is the subset of an installed registry record used by conversion.
type Record struct {
	ID           string   `json:"plugin_id"`
	ManifestPath string   `json:"manifest_path"`
	Root         string   `json:"plugin_root"`
	Enabled      bool     `json:"enabled"`
	Platforms    []string `json:"platforms"`
	Warnings     []string `json:"warnings"`
}

// Read loads the JSON array stored by the application. A missing, unreadable or
// malformed registry is an empty catalog plus one diagnostic, not a fatal error.
func Read(path string) ([]Record, []manifest.Problem) {
	data, err := os.ReadFile(path)
	var records []Record
	if err == nil {
		err = json.Unmarshal(data, &records)
		if err == nil && records == nil {
			err = fmt.Errorf("registry must be a JSON array")
		}
	}
	if err != nil {
		return nil, []manifest.Problem{{Code: "registry_warning", Message: fmt.Sprintf("%s: %v", path, err), Field: "registry"}}
	}
	return records, nil
}

// IDsFor returns all enabled original ids when dir is a registered directory.
// Disabled entries do not reserve IDs; platform filters do not narrow collisions.
func IDsFor(records []Record, dir string) []string {
	registered := false
	for _, r := range records {
		if filepath.Clean(r.Root) == filepath.Clean(dir) {
			registered = true
		}
	}
	if !registered {
		return nil
	}
	var ids []string
	for _, r := range records {
		if r.Enabled {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// Discover filters in registry order, checking each remaining manifest from disk.
// Every invalid record is isolated so it cannot hide valid later records.
func Discover(records []Record, platform string) ([]string, []manifest.Problem) {
	dirs := []string{}
	var problems []manifest.Problem
	seenDisabled := map[string]bool{}
	seenDirs := map[string]bool{}
	for _, r := range records {
		if !r.Enabled {
			if !seenDisabled[r.ID] {
				problems = append(problems, manifest.Problem{Code: "disabled_in_registry", Message: fmt.Sprintf("plugin %q is disabled", r.ID), Field: r.Root})
				seenDisabled[r.ID] = true
			}
			continue
		}
		if !manifest.Supports(r.Platforms, platform) {
			continue
		}
		path := r.ManifestPath
		if path == "" {
			path = filepath.Join(r.Root, "herdr-plugin.toml")
		}
		data, err := os.ReadFile(path)
		if err == nil {
			_, err = manifest.Parse(data)
		}
		if !filepath.IsAbs(r.Root) || strings.ContainsAny(r.Root, "\r\n") {
			err = fmt.Errorf("plugin_root must be an absolute single-line directory")
		}
		if err != nil {
			message := fmt.Sprintf("%s: %v", path, err)
			if len(r.Warnings) > 0 {
				message += "; " + strings.Join(r.Warnings, "; ")
			}
			problems = append(problems, manifest.Problem{Code: "registry_warning", Message: message, Field: r.Root})
			continue
		}
		for _, warning := range r.Warnings {
			problems = append(problems, manifest.Problem{Code: "registry_warning", Message: warning, Field: r.Root})
		}
		if !seenDirs[r.Root] {
			dirs = append(dirs, r.Root)
			seenDirs[r.Root] = true
		}
	}
	return dirs, problems
}
