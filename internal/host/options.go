package host

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/server"
	"github.com/hyperlab-sh/husk/internal/services/catalog"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Option adjusts New. The zero configuration is production.
type Option func(*options)

type options struct {
	version   string
	logger    func(level, msg string)
	prompt    func(context.Context, secrets.Prompt) secrets.Answer
	factory   pods.Factory
	modifiers func() protocol.ModifierState

	// Test seams.
	catalogReader catalog.Reader
	newServer     func(server.Config) server.Server
	afterStage    func(stage string) error
	onClose       func(step string)
	loopWait      time.Duration // overrides the package loopWait when set
	afterFail     func(*Root)   // called with the partial root after New's cleanup
	onSpawn       func(accepted bool)
	keyring       store.Keyring // replaces the OS keyring under Store when set
	workerWait    time.Duration // overrides storeCloseTimeout for the worker wait when set
}

func (o *options) workerWaitOr(d time.Duration) time.Duration {
	if o.workerWait > 0 {
		return o.workerWait
	}
	return d
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

// WithPrompt installs the secret-grant dialog. It runs off the loop, blocks
// until the user answers and must return once ctx is cancelled (the service
// closed the dialog). Without it every ungranted plugin read is answered
// user_denied and nothing is remembered.
func WithPrompt(fn func(ctx context.Context, p secrets.Prompt) secrets.Answer) Option {
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
