package host

import (
	"sync/atomic"
	"time"

	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/server"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Option adjusts New. The zero configuration is production.
type Option func(*options)

type options struct {
	version   string
	logger    func(level, msg string)
	prompt    func(secrets.Prompt) <-chan bool
	factory   pods.Factory
	modifiers func() protocol.ModifierState

	// Test seams.
	newServer  func(server.Config) server.Server
	afterStage func(stage string) error
	onClose    func(step string)
	loopWait   time.Duration // overrides the package loopWait when set
	afterFail  func(*Root)   // called with the partial root after New's cleanup
}

func (o *options) loopWaitOr(d time.Duration) time.Duration {
	if o.loopWait > 0 {
		return o.loopWait
	}
	return d
}

// WithVersion sets the build version the Server registers and session.ping
// reports.
func WithVersion(v string) Option { return func(o *options) { o.version = v } }

// WithLogger receives host log notices (level is info, warn or error). Nil
// drops them.
func WithLogger(fn func(level, msg string)) Option { return func(o *options) { o.logger = fn } }

// WithPrompt installs the secret-grant dialog. It must return its answer
// channel promptly. Without it every grant prompt is refused.
func WithPrompt(fn func(secrets.Prompt) <-chan bool) Option {
	return func(o *options) { o.prompt = fn }
}

// WithFactory replaces the process Wrapper factory (fake plugins in tests).
func WithFactory(f pods.Factory) Option { return func(o *options) { o.factory = f } }

// WithModifiers supplies Input's modifier state for the opening
// input.key_state line. Without it all modifiers are up.
func WithModifiers(fn func() protocol.ModifierState) Option {
	return func(o *options) { o.modifiers = fn }
}

type atomicBool struct{ v atomic.Bool }

func (b *atomicBool) Load() bool   { return b.v.Load() }
func (b *atomicBool) Store(v bool) { b.v.Store(v) }
