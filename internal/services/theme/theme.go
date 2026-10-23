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
	overrides := map[string]string{}
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
	overrides["name"] = in.Settings.String("theme.name")
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
	distributionOptOut := source == "distribution"
	for _, layer := range in.ConfigLayers {
		if layer.Layer == "distribution" {
			if _, exists := c.Dotted(layer.Doc, "theme.allow_low_contrast"); exists {
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
		c.Issue(&r.Problems, severity, "low_contrast", in.Settings.Source("theme.palette."+v.Foreground), "theme.palette."+v.Foreground, fmt.Sprintf("%s on %s is %.2f:1, need %.1f:1", v.Foreground, v.Background, v.Ratio, v.Minimum))
	}
	if err = c.Invalid(r.Problems); err != nil {
		return nil, r, err
	}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	next := &snapshot{old.gen + 1, p, overrides}
	return c.Prepare(ctx, r, func() bool { return s.current.CompareAndSwap(old, next) }, s.Generation), r, nil
}
