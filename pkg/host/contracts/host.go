package contracts

import (
	"context"
	"io/fs"
	"time"

	"github.com/hyperlab-sh/husk/pkg/instance"
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

// RateLimitPerSecond is the per-plugin cap shared by every plugin-callable Services method.
const RateLimitPerSecond = 20

// Config is everything the composition root needs. host-root implements Host
// over it.
type Config struct {
	// Distribution carries embedded application defaults below bundle and user
	// configuration. Its root contains document files such as config.json.
	Distribution fs.FS
	// InstanceID is the pid or the --instance name.
	InstanceID string
	// RuntimeDir is the runtime path, checked/created after bundle validation.
	RuntimeDir string
	// Entry is the registration entry the Server writes once listening.
	Entry instance.Entry
	// InstanceDocs carries flag-derived instance-layer documents. Under "config",
	// cmd/husk builds {"husk":{"plugins":{"paths":[...],"dirs":[...]}}};
	// each array is present only when its flag was given at least once.
	InstanceDocs map[string]map[string]any
	// ProfileFlag is the --profile start flag, nil when it was not given. It
	// is a selection input of the startup reload only; the profile slice is
	// what later reloads read.
	ProfileFlag *string
	// Post posts a notice to the loop from any goroutine. It may block for
	// backpressure while the loop is running; once the loop owner has begun
	// shutdown (closed its post gate) it returns false without blocking, and
	// the owner closes that gate before calling Close from Update. Once it
	// returns false, no Update is in flight other than one currently calling
	// Close; the host runs its remaining shutdown steps inline on that
	// evidence.
	Post func(any) bool
	// Clock drives every host deadline (Calls, Surfaces, the Server, the
	// wrappers, Store and Services); nil is system time. protocol.Clock is used
	// so it can also be handed to the Server.
	Clock protocol.Clock
	// Content receives every surface.update the Surfaces engine accepted, on
	// the loop, with the plugin id and the params unchanged. It is the only
	// route surface content takes to a renderer: buffers are not State. Nil
	// drops content (a headless host). A denied or unoccupied update never
	// reaches it. It must not block and must not retain the Lines slice
	// beyond the call unless it copies it.
	Content ContentSink
}

// ContentSink receives accepted surface content (see Config.Content).
type ContentSink func(plugin string, params protocol.SurfaceUpdateParams)

// Host is the composition root.
type Host interface {
	// Run starts the Server and the plugins and blocks until ctx ends or a
	// fatal error occurs.
	Run(ctx context.Context) error
	// Close stops everything in the order pods.md fixes; idempotent.
	Close() error
	// Frontend returns the seams a frontend drives the host through. The
	// result is the same value for the host's lifetime.
	Frontend() Frontend
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
