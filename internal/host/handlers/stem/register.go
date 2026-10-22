package stem

import (
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
)

// Ports carries the Stem-owned port implementations the composition root
// wires into a handlers.Mux with Register. Every field must be non-nil.
type Ports struct {
	Session contracts.SessionPort
	Calls   contracts.CallsPort
	Event   contracts.EventPort
	Hook    contracts.HookPort
	Surface contracts.SurfacePort
	State   contracts.StatePort
}

// Register wires the six Stem adapters into m by method prefix: session,
// action, event, hook, state, and the Surface port's three prefixes —
// surface, slot and toast. The other inbound prefixes — plugin, config,
// settings, secrets, profile, theme and key — belong to other units and stay
// unregistered here; the Mux answers them with unknown_method until those
// units land.
func Register(m *handlers.Mux, p Ports) {
	m.Register("session", Session{Port: p.Session})
	m.Register("action", Calls{Port: p.Calls})
	m.Register("event", Event{Port: p.Event})
	m.Register("hook", Hook{Port: p.Hook})
	m.Register("state", State{Port: p.State})
	surface := Surface{Port: p.Surface}
	m.Register("surface", surface)
	m.Register("slot", surface)
	m.Register("toast", surface)
}
