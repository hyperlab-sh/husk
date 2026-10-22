package services

import (
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
)

// Ports carries the port implementations for the six user-facing method
// families. Register panics on a missing port or the Mux's own composition
// rules (duplicate or malformed prefix): serving a family without its port
// is a composition bug, not an input error.
type Ports struct {
	Settings contracts.SettingsPort
	Secrets  contracts.SecretsPort
	Profile  contracts.ProfilePort
	Theme    contracts.ThemePort
	Keys     contracts.KeybindingPort
	Config   contracts.ConfigPort
	// Limiter is the shared settings.get/secrets.get budget applied per
	// plugin id. Pass nil to disable the cap; the composition root normally
	// passes NewWindowLimiter(clock, contracts.RateLimitPerSecond).
	Limiter contracts.Limiter
}

// Register wires m to serve the settings, secrets, profile, theme, keys and
// config prefixes with the given ports. The pods Server owns the Mux; this is
// the single composition point for the Services families.
func Register(m *handlers.Mux, p Ports) {
	if m == nil {
		panic("services: Register requires a mux")
	}
	if p.Settings == nil || p.Secrets == nil || p.Profile == nil || p.Theme == nil || p.Keys == nil || p.Config == nil {
		panic("services: Register requires every Services port")
	}
	m.Register("settings", Settings{Port: p.Settings, Limiter: p.Limiter})
	m.Register("secrets", Secrets{Port: p.Secrets, Limiter: p.Limiter})
	m.Register("profile", Profile{Port: p.Profile})
	m.Register("theme", Theme{Port: p.Theme})
	m.Register("keys", Keys{Port: p.Keys})
	m.Register("config", Config{Port: p.Config})
}
