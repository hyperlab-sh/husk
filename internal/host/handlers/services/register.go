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
	// Limiter is the one budget of every plugin-callable Services method
	// (settings.get, secrets.get, secrets.names, theme.get, keys.list) applied
	// per plugin id (services.md, Limits — the cap is spec, not an option),
	// normally NewWindowLimiter(clock, contracts.RateLimitPerSecond).
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
	if p.Limiter == nil {
		panic("services: Register requires a rate limiter (services.md Limits)")
	}
	m.Register("settings", Settings{Port: p.Settings, Limiter: p.Limiter})
	m.Register("secrets", Secrets{Port: p.Secrets, Limiter: p.Limiter})
	m.Register("profile", Profile{Port: p.Profile})
	m.Register("theme", Theme{Port: p.Theme, Limiter: p.Limiter})
	m.Register("keys", Keys{Port: p.Keys, Limiter: p.Limiter})
	m.Register("config", Config{Port: p.Config})
}
