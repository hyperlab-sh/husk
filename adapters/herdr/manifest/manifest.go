// Package manifest performs pure conversion of TOML plugin declarations.
// It depends on no host package and never executes manifest commands.
package manifest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Problem is a diagnostic in the executable-converter stderr format.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field"`
}

// Entry preserves an invocation's original argv and metadata.
type Entry struct {
	ID          string   `toml:"id" json:"id,omitempty"`
	Title       string   `toml:"title" json:"title,omitempty"`
	Description string   `toml:"description" json:"description,omitempty"`
	Platforms   []string `toml:"platforms" json:"platforms,omitempty"`
	Command     []string `toml:"command" json:"command,omitempty"`
	Contexts    []string `toml:"contexts" json:"contexts,omitempty"`
	DefaultKey  string   `toml:"default_key" json:"-"`
	Shortcut    string   `toml:"shortcut" json:"-"`
	Placement   string   `toml:"placement" json:"placement,omitempty"`
	Width       any      `toml:"width" json:"width,omitempty"`
	Height      any      `toml:"height" json:"height,omitempty"`
	On          string   `toml:"on" json:"on,omitempty"`
	Pattern     string   `toml:"pattern" json:"pattern,omitempty"`
	Action      string   `toml:"action" json:"action,omitempty"`
}

// Manifest is the source document; commands remain uninterpreted argv.
type Manifest struct {
	ID           string   `toml:"id"`
	Name         string   `toml:"name"`
	Version      string   `toml:"version"`
	Description  string   `toml:"description"`
	MinVersion   string   `toml:"min_herdr_version"`
	Platforms    []string `toml:"platforms"`
	Build        []Entry  `toml:"build"`
	Startup      []Entry  `toml:"startup"`
	Actions      []Entry  `toml:"actions"`
	Events       []Entry  `toml:"events"`
	Panes        []Entry  `toml:"panes"`
	LinkHandlers []Entry  `toml:"link_handlers"`
}

// Action is a public host action declaration.
type Action struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Public      bool   `json:"public"`
}

// Vendor is informational metadata, not the runtime's source of truth.
type Vendor struct {
	ID           string   `json:"id"`
	MinVersion   string   `json:"min_version"`
	Actions      []Entry  `json:"actions"`
	Panes        []Entry  `json:"panes"`
	Startup      []Entry  `json:"startup"`
	Events       []string `json:"events"`
	LinkHandlers []Entry  `json:"link_handlers"`
}

// Document is the v2 converter output, independent of host implementation types.
type Document struct {
	ManifestVersion int      `json:"manifest_version"`
	Protocol        int      `json:"protocol"`
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Description     string   `json:"description"`
	Command         []string `json:"command"`
	Actions         []Action `json:"actions"`
	Events          struct {
		Subscribe []string `json:"subscribe"`
	} `json:"events"`
	Keybindings []any  `json:"keybindings"`
	Vendor      Vendor `json:"x-herdr"`
}

// Options contains explicit conversion inputs. RegistryIDs contains every enabled
// registry record's original id only when Dir is itself a registry directory.
type Options struct {
	Dir         string
	Executable  string
	Platform    string
	RegistryIDs []string
}

// Parse decodes a TOML manifest and checks required top-level fields.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.ID == "" || strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Version) == "" || strings.TrimSpace(m.MinVersion) == "" {
		return m, fmt.Errorf("id, name, version and min_herdr_version are required")
	}
	if !validPlatforms(m.Platforms) {
		return m, fmt.Errorf("invalid platforms")
	}
	return m, nil
}

// MapID applies the host's ASCII identifier rule after replacing colons.
func MapID(id string) (string, error) {
	mapped := strings.ReplaceAll(id, ":", "-")
	if len(mapped) == 0 || len(mapped) > 64 {
		return "", fmt.Errorf("id cannot map to a 1–64 character identifier")
	}
	for i, c := range []byte(mapped) {
		alpha := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if !alpha && (i == 0 || c != '.' && c != '_' && c != '-') {
			return "", fmt.Errorf("id contains an unsupported character")
		}
	}
	if mapped == "husk" {
		return "", fmt.Errorf("id is reserved")
	}
	return mapped, nil
}

// Supports reports whether a platform list permits an OS. Source macos maps to
// Go's darwin name; a missing list permits all supported operating systems.
func Supports(platforms []string, platform string) bool {
	if platform == "darwin" {
		platform = "macos"
	}
	return platforms == nil || slices.Contains(platforms, platform)
}

func validPlatforms(platforms []string) bool {
	for _, p := range platforms {
		if p != "linux" && p != "macos" && p != "windows" {
			return false
		}
	}
	return true
}

func entryID(id string) (string, error) {
	if strings.Contains(id, ".") {
		return "", fmt.Errorf("entry id must be dot-free")
	}
	return MapID(id)
}

func validSize(v any) bool {
	if v == nil {
		return true
	}
	switch v := v.(type) {
	case int64:
		return v > 0
	case string:
		if !strings.HasSuffix(v, "%") {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSuffix(v, "%"))
		return err == nil && n > 0 && n <= 100
	default:
		return false
	}
}

// Convert returns no document on fatal errors. Nonfatal drops are problems;
// install-time builds are a separate note and are never executed.
func Convert(data []byte, opts Options) (*Document, []Problem, []string) {
	var problems []Problem
	fail := func(code, field string, err error) (*Document, []Problem, []string) {
		return nil, append(problems, Problem{code, err.Error(), field}), nil
	}
	m, err := Parse(data)
	if err != nil {
		return fail("manifest_invalid", "manifest", err)
	}
	id, err := MapID(m.ID)
	if err != nil {
		return fail("id_unmappable", "id", err)
	}
	for _, original := range opts.RegistryIDs {
		other, err := MapID(original)
		if err == nil && other == id && original != m.ID {
			return fail("id_unmappable", "id", fmt.Errorf("registry ids %q and %q both map to %q", m.ID, original, id))
		}
	}
	if !Supports(m.Platforms, opts.Platform) {
		return fail("platform_unsupported", "platforms", fmt.Errorf("plugin excludes %s", opts.Platform))
	}
	if !filepath.IsAbs(opts.Dir) || !filepath.IsAbs(opts.Executable) {
		return fail("manifest_invalid", "command", fmt.Errorf("converter executable and plugin directory must be absolute"))
	}
	d := &Document{ManifestVersion: 2, Protocol: 2, ID: id, Name: m.Name, Version: m.Version, Description: m.Description, Command: []string{opts.Executable, "run", opts.Dir}, Actions: []Action{}, Keybindings: []any{}, Vendor: Vendor{ID: m.ID, MinVersion: m.MinVersion, Actions: []Entry{}, Panes: []Entry{}, Startup: []Entry{}, Events: []string{}, LinkHandlers: []Entry{}}}
	d.Events.Subscribe = []string{}
	var notes []string
	if len(m.Build) > 0 {
		notes = append(notes, "build_not_run: install-time commands are not executed")
	}
	actionIDs := map[string]bool{}
	sourceActions := map[string]bool{}
	for _, group := range []struct {
		name    string
		entries []Entry
	}{{"actions", m.Actions}, {"panes", m.Panes}, {"startup", m.Startup}, {"events", m.Events}, {"link_handlers", m.LinkHandlers}} {
		for i, e := range group.entries {
			field := fmt.Sprintf("%s[%d]", group.name, i)
			if !validPlatforms(e.Platforms) {
				return fail("manifest_invalid", field, fmt.Errorf("invalid platforms"))
			}
			if !Supports(e.Platforms, opts.Platform) {
				continue
			}
			if group.name != "link_handlers" && (len(e.Command) == 0 || e.Command[0] == "") {
				return fail("manifest_invalid", field, fmt.Errorf("command must be nonempty argv"))
			}
			switch group.name {
			case "actions", "panes":
				local, err := entryID(e.ID)
				if err != nil {
					return fail("id_unmappable", field+".id", err)
				}
				if e.Title == "" {
					return fail("manifest_invalid", field+".title", fmt.Errorf("title is required"))
				}
				title := e.Title
				if group.name == "panes" {
					local = "pane-" + local
					title = "Open " + title
					if !validSize(e.Width) || !validSize(e.Height) {
						return fail("manifest_invalid", field, fmt.Errorf("size must be positive cells or a percentage from 1 to 100"))
					}
					switch e.Placement {
					case "", "popup", "overlay", "zoomed", "tab", "split":
					default:
						return fail("manifest_invalid", field+".placement", fmt.Errorf("unknown placement"))
					}
				}
				if actionIDs[local] {
					return fail("duplicate_action", field+".id", fmt.Errorf("duplicate mapped action %q", local))
				}
				actionIDs[local] = true
				d.Actions = append(d.Actions, Action{id + "." + local, title, e.Description, true})
				if group.name == "actions" {
					sourceActions[e.ID] = true
					d.Vendor.Actions = append(d.Vendor.Actions, e)
				} else {
					d.Vendor.Panes = append(d.Vendor.Panes, e)
				}
				for _, suggestion := range []struct{ name, value string }{{"default_key", e.DefaultKey}, {"shortcut", e.Shortcut}} {
					if suggestion.value != "" {
						problems = append(problems, Problem{"suggestion_ignored", "key suggestions are not imported", field + "." + suggestion.name})
					}
				}
			case "startup":
				d.Vendor.Startup = append(d.Vendor.Startup, e)
			case "events":
				if e.On == "" {
					return fail("manifest_invalid", field+".on", fmt.Errorf("event name is required"))
				}
				if !slices.Contains(d.Vendor.Events, e.On) {
					d.Vendor.Events = append(d.Vendor.Events, e.On)
				}
			case "link_handlers":
				if _, err := regexp.Compile(e.Pattern); err != nil {
					problems = append(problems, Problem{"pattern_unsupported", err.Error(), field + ".pattern"})
					continue
				}
				if !sourceActions[e.Action] {
					problems = append(problems, Problem{"unknown_action", "handler must name a declared action", field + ".action"})
					continue
				}
				d.Vendor.LinkHandlers = append(d.Vendor.LinkHandlers, e)
			}
		}
	}
	return d, problems, notes
}
