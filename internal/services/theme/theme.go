// Package theme derives one contrast-checked immutable palette from Settings.
package theme

import (
	"context"
	"fmt"
	"strings"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
	"sync/atomic"
)

type snapshot struct {
	gen       c.Generation
	palette   palette.Palette
	overrides map[string]string
	themes    map[string]definition
}

// Service publishes a palette only after format and contrast checks succeed.
type Service struct{ current atomic.Pointer[snapshot] }

// New serves the built-in palette even if first configuration fails.
func New() *Service {
	s := &Service{}
	s.current.Store(&snapshot{palette: palette.Default(), overrides: map[string]string{}})
	return s
}

// Generation is the palette generation, independent of Settings generation.
func (s *Service) Generation() c.Generation { return s.current.Load().gen }

// Palette is a value copy; readers never lock.
func (s *Service) Palette() palette.Palette { return s.current.Load().palette }

// Wire returns the same roles for protocol.theme.get/theme.changed.
func (s *Service) Wire() protocol.Palette { return protocol.Palette(s.Palette()) }

// Overrides returns a detached role map.
func (s *Service) Overrides() map[string]string { return c.Clone(s.current.Load().overrides) }

// Get implements theme.get for every authenticated caller.
func (s *Service) Get(caller c.Caller) (protocol.Palette, error) {
	if err := c.CheckCaller(caller); err != nil {
		return protocol.Palette{}, err
	}
	return s.Wire(), nil
}

// Check performs a user-only dry build for theme.check.
func (s *Service) Check(ctx context.Context, caller c.Caller, in c.ReloadInput) (c.Report, error) {
	if err := c.RequireUser(caller); err != nil {
		return c.Report{}, err
	}
	_, r, err := s.Rebuild(ctx, in)
	return r, err
}

// Rebuild produces an unpublished candidate and every violated contrast pair.
func (s *Service) Rebuild(ctx context.Context, in c.ReloadInput) (c.Candidate, c.Report, error) {
	old := s.current.Load()
	r := c.Report{Service: "theme", Gen: old.gen}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	if in.Settings == nil {
		return nil, r, c.Failure("unavailable", "settings_missing")
	}
	themes := resolveThemes(in.Themes, &r.Problems)
	name := in.Settings.String("theme.name")
	selected, exists := themes[name]
	if name == "" || name == palette.Default().Name {
		name = palette.Default().Name
	} else if !exists {
		layer := in.Settings.Source("theme.name")
		r.Problems = append(r.Problems, c.Problem{Severity: "warning", Code: "unknown_theme", Layer: layer, Key: "theme.name", Message: fmt.Sprintf("theme %s does not exist; using %s", name, palette.Default().Name), Source: &protocol.Source{Layer: layer, File: configFile(in, layer)}})
		name = palette.Default().Name
	}
	overrides := c.Clone(selected.roles)
	if overrides == nil {
		overrides = map[string]string{}
	}
	for role := range palette.Roles(palette.Default()) {
		if role == "name" {
			continue
		}
		key := "theme.palette." + role
		if in.Settings.Source(key) != "default" {
			hex := in.Settings.String(key)
			if _, err := palette.RelativeLuminance(hex); err != nil {
				c.Issue(&r.Problems, "error", "invalid_color", in.Settings.Source(key), key, "role "+role+": want #RRGGBB")
			}
			overrides[role] = strings.ToLower(hex)
		}
	}
	overrides["name"] = name
	if err := c.Invalid(r.Problems); err != nil {
		return nil, r, err
	}
	p, err := palette.Overlay(palette.Default(), overrides)
	if err != nil {
		c.Issue(&r.Problems, "error", "invalid_color", "user", "theme.palette", err.Error())
		return nil, r, c.Invalid(r.Problems)
	}
	allow := in.Settings.Bool("theme.allow_low_contrast")
	source := in.Settings.Source("theme.allow_low_contrast")
	// Only a distribution value of true is an opt-out, whether or not a
	// stronger layer overrides it; false is the default and no problem.
	distributionOptOut := source == "distribution" && allow
	for _, layer := range in.ConfigLayers {
		if layer.Layer == "distribution" {
			if v, exists := c.Dotted(layer.Doc, "theme.allow_low_contrast"); exists && v == true {
				distributionOptOut = true
			}
		}
	}
	if distributionOptOut {
		c.Issue(&r.Problems, "error", "distribution_opt_out", "distribution", "theme.allow_low_contrast", "distribution cannot opt out of contrast")
		allow = false
	}
	for _, v := range palette.CheckContrast(p) {
		severity := "error"
		if allow && (source == "user" || source == "profile" || source == "instance") {
			severity = "warning"
		}
		key, layer, file := attribute(in, selected, name, v.Foreground, v.Background)
		problem := c.Problem{Severity: severity, Code: "low_contrast", Layer: layer, Key: key, Message: fmt.Sprintf("%s on %s is %.2f:1, need %.1f:1", v.Foreground, v.Background, v.Ratio, v.Minimum)}
		problem.Source = &protocol.Source{Layer: layer, File: file}
		r.Problems = append(r.Problems, problem)
	}
	if err = c.Invalid(r.Problems); err != nil {
		return nil, r, err
	}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	next := &snapshot{gen: old.gen + 1, palette: p, overrides: overrides, themes: themes}
	return c.Prepare(ctx, r, old.palette != p, func() bool { return s.current.CompareAndSwap(old, next) }, s.Generation), r, nil
}

// attribute names the role a failing pair is charged to: the foreground when
// its layer overrode it, else the background's override; the built-in palette
// passes, so one of them is overridden. An unset slot_background follows the
// canvas it is derived from.
func attribute(in c.ReloadInput, selected definition, name, fg, bg string) (key, layer, file string) {
	origin := func(role string) (string, string, string, bool) {
		key := "theme.palette." + role
		if layer := in.Settings.Source(key); layer != "default" {
			return key, layer, configFile(in, layer), true
		}
		if _, ok := selected.roles[role]; ok {
			return "themes." + name + "." + role, selected.layer, selected.file, true
		}
		if role == "slot_background" {
			key = "theme.palette.canvas"
			if layer := in.Settings.Source(key); layer != "default" {
				return key, layer, configFile(in, layer), true
			}
			if _, ok := selected.roles["canvas"]; ok {
				return "themes." + name + ".canvas", selected.layer, selected.file, true
			}
		}
		return "", "", "", false
	}
	if k, l, f, ok := origin(fg); ok {
		return k, l, f
	}
	if k, l, f, ok := origin(bg); ok {
		return k, l, f
	}
	return "theme.palette." + fg, "default", ""
}

// configFile is the config-relative file of a layer's config document, from
// the layer documents the reload was given: for a profile the suffix from the
// last "/profiles/", for an instance from the last "/instances/", for the user
// layer "config.json". Empty when the layer has no file (distribution, an
// instance folded from flags) or was not supplied.
func configFile(in c.ReloadInput, layer string) string {
	for _, l := range in.ConfigLayers {
		if string(l.Layer) != layer || l.File == "" {
			continue
		}
		path := strings.ReplaceAll(l.File, "\\", "/")
		if !strings.HasPrefix(path, "/") && !strings.Contains(path, ":") {
			return path
		}
		marker := map[string]string{"profile": "/profiles/", "instance": "/instances/"}[layer]
		switch {
		case marker != "":
			if at := strings.LastIndex(path, marker); at >= 0 {
				return path[at+1:]
			}
		case layer == "user" && (path == "config.json" || strings.HasSuffix(path, "/config.json")):
			return "config.json"
		}
	}
	return ""
}
