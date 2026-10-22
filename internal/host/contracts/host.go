package contracts

import (
	"context"
	"time"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Clock supplies time. protocol.Clock satisfies it.
type Clock interface {
	Now() time.Time
}

// Limiter is a per-key admission rule. The settings and secrets adapters key
// it by plugin id and allow 20 calls per second per plugin (services.md); a
// refusal is busy with data.reason "rate_limited".
type Limiter interface {
	// Allow reports whether one more call under key may proceed now, and counts it.
	Allow(key string) bool
}

// RateLimitPerSecond is the per-plugin cap for settings.get and secrets.get.
const RateLimitPerSecond = 20

// Config is everything the composition root needs. host-root implements Host
// over it.
type Config struct {
	// InstanceID is the pid or the --instance name.
	InstanceID string
	// RuntimeDir is the checked runtime directory.
	RuntimeDir string
	// Entry is the registration entry the Server writes once listening.
	Entry instance.Entry
	// Plugins are the --plugin arguments, in order.
	Plugins []string
	// PluginDirs are the --plugin-dir arguments, in order.
	PluginDirs []string
	// Post posts a notice to the loop from any goroutine; false after shutdown.
	Post func(any) bool
	// Clock drives every host deadline (Calls, Surfaces, the Server, the
	// wrappers, Store and Services); nil is system time. protocol.Clock is used
	// so it can also be handed to the Server.
	Clock protocol.Clock
}

// Host is the composition root.
type Host interface {
	// Run starts the Server and the plugins and blocks until ctx ends or a
	// fatal error occurs.
	Run(ctx context.Context) error
	// Close stops everything in the order pods.md fixes; idempotent.
	Close() error
}

// Occupant says which plugin occupies a surface (husk.surfaces occupants).
type Occupant struct {
	Surface protocol.SurfaceID `json:"surface"`
	Plugin  string             `json:"plugin"`
}

// Contribution is one entry a plugin put in a published slot
// (husk.surfaces contributions).
type Contribution struct {
	Slot    string `json:"slot"`
	ID      string `json:"id"`
	Plugin  string `json:"plugin"`
	Label   string `json:"label,omitempty"`
	OnClick string `json:"on_click,omitempty"`
}
