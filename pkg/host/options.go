package host

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/pods"
	"github.com/hyperlab-sh/husk/pkg/pods/server"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/services/catalog"
	"github.com/hyperlab-sh/husk/pkg/services/secrets"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

// Option adjusts New. The zero configuration is production.
type Option func(*options)

type options struct {
	version         string
	logger          func(level, msg string)
	prompt          func(context.Context, secrets.Prompt) secrets.Answer
	factory         pods.Factory
	modifiers       func() protocol.ModifierState
	pluginAdditions map[string][]string
	converters      []string
	onLoopStart     func(contracts.Frontend)

	// Test seams.
	catalogReader catalog.Reader
	newServer     func(server.Config) server.Server
	afterStage    func(stage string) error
	onClose       func(step string)
	loopWait      time.Duration // overrides the package loopWait when set
	afterFail     func(*Root)   // called with the partial root after New's cleanup
	onSpawn       func(accepted bool)
	keyring       store.Keyring         // replaces the OS keyring under Store when set
	onStoreFlush  func(context.Context) // sees the context handed to Store.Close
	workerWait    time.Duration         // overrides workerWaitTimeout for the worker wait when set
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

// WithPluginAdditions appends launch paths or dirs to the effective settings
// including the named-instance file. Keys are "paths" and "dirs"; replace
// flags remain in the instance documents instead.
func WithPluginAdditions(additions map[string][]string) Option {
	return func(o *options) { o.pluginAdditions = additions }
}

// WithConverters registers executable converters in flag order, before settings.
func WithConverters(paths []string) Option {
	return func(o *options) { o.converters = append([]string(nil), paths...) }
}

// WithOnLoopStart runs fn once, on the loop, before the host delivers any event
// to a WatchEvents subscriber: at the first Update (or Tick) whatever the
// message, ahead of the first end-of-Update event flush. fn receives the
// host's Frontend, so it can call loop-only methods such as WatchEvents. Nil is
// ignored (it never clears an earlier fn); otherwise a later option replaces an
// earlier one. A panic in fn is not
// recovered here.
func WithOnLoopStart(fn func(contracts.Frontend)) Option {
	return func(o *options) {
		if fn != nil {
			o.onLoopStart = fn
		}
	}
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
