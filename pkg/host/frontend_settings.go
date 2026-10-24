package host

import (
	"bytes"
	"context"
	"encoding/json"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	ports "github.com/hyperlab-sh/husk/pkg/host/ports/services"
	portsstem "github.com/hyperlab-sh/husk/pkg/host/ports/stem"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	svccontracts "github.com/hyperlab-sh/husk/pkg/services/contracts"
)

// Themes implements contracts.Frontend. Any goroutine; snapshot only.
func (r *Root) Themes() []contracts.ThemeEntry {
	themes, err := (ports.Theme{Svc: r.rt.Theme}).List(context.Background(), contracts.Caller{Kind: protocol.CallerHost})
	if err != nil {
		r.logf("frontend: Themes: %v", err)
	}
	return themes
}

// ServicesRevision implements contracts.Frontend. Any goroutine; atomic read.
func (r *Root) ServicesRevision() uint64 { return r.servicesRevision.Load() }

// SettingsSchema implements contracts.Frontend. Any goroutine; snapshot only.
func (r *Root) SettingsSchema() []contracts.SettingEntry {
	entries, err := (ports.Settings{Svc: r.rt.Settings}).Schema(context.Background(), contracts.Caller{Kind: protocol.CallerHost}, "")
	if err != nil {
		r.logf("frontend: SettingsSchema: %v", err)
	}
	return entries
}

// Setting implements contracts.Frontend. Any goroutine; snapshot only.
func (r *Root) Setting(key string) (any, string, error) {
	return (ports.Settings{Svc: r.rt.Settings}).Get(context.Background(), contracts.Caller{Kind: protocol.CallerHost}, key)
}

// KeyListing implements contracts.Frontend. Any goroutine; off-loop calls
// block until a running, responsive loop captures the runtime catalog. This
// uses the socket adapter's port and CLI view, not a snapshot-only read.
func (r *Root) KeyListing() contracts.KeyListing {
	listing, err := r.keys().List(context.Background(), contracts.Caller{Kind: protocol.CallerCLI})
	if err != nil {
		r.logf("frontend: KeyListing: %v", err)
	}
	return listing
}

// Profiles implements contracts.Frontend. Any goroutine; snapshot only.
func (r *Root) Profiles() []contracts.ProfileInfo {
	profiles, err := (ports.Profile{Svc: r.rt.Profile}).List(context.Background(), contracts.Caller{Kind: protocol.CallerHost})
	if err != nil {
		r.logf("frontend: Profiles: %v", err)
	}
	return profiles
}

// ConfigDir implements contracts.Frontend. Any goroutine; resolved at boot.
func (r *Root) ConfigDir() string { return r.store.Paths().Config }

// frontendWrite runs a port on a tracked worker and delivers its reply only
// through the loop. A rejected spawn or closed post gate drops the reply.
func frontendWrite[T any](r *Root, call func(context.Context) (T, error), reply func(T, error)) {
	r.spawn(func() tea.Msg {
		result, err := call(r.closingCtx)
		if r.closingCtx.Err() != nil {
			var zero T
			result, err = zero, contracts.NewError(protocol.CodeCancelled, "husk is shutting down", map[string]any{"reason": "shutdown"})
		}
		if reply != nil {
			r.post(func() { reply(result, err) })
		}
		return nil
	})
}

// SetSetting implements contracts.Frontend. Call and reply on the loop; the
// port's Store write and reload wait run on the tracked worker.
func (r *Root) SetSetting(key string, value any, reply func(contracts.SettingSetResult, error)) {
	// A caller may reuse mutable form data as soon as this method returns.
	value = svccontracts.Clone(value)
	frontendWrite(r, func(ctx context.Context) (contracts.SettingSetResult, error) {
		return (ports.Settings{Svc: r.rt.Settings, Inputs: r.rt.Coordinator}).Set(ctx, contracts.Caller{Kind: protocol.CallerHost}, key, value)
	}, reply)
}

// UseProfile implements contracts.Frontend. Call and reply on the loop;
// the port runs on a tracked worker.
func (r *Root) UseProfile(name string, reply func(contracts.ProfileUseResult, error)) {
	frontendWrite(r, func(ctx context.Context) (contracts.ProfileUseResult, error) {
		return (ports.Profile{Svc: r.rt.Profile}).Use(ctx, contracts.Caller{Kind: protocol.CallerHost}, name)
	}, reply)
}

// RemovePlugin implements contracts.Frontend. Call and reply on the loop;
// the port runs on a tracked worker.
func (r *Root) RemovePlugin(id string, reply func(contracts.PluginRemoval, error)) {
	frontendWrite(r, func(ctx context.Context) (contracts.PluginRemoval, error) {
		return r.pods.Remove(ctx, contracts.Caller{Kind: protocol.CallerHost}, id)
	}, reply)
}

// AddPlugin implements contracts.Frontend. Call and reply on the loop; the
// port runs on a tracked worker.
func (r *Root) AddPlugin(source contracts.PluginSource, reply func(contracts.PluginAddition, error)) {
	frontendWrite(r, func(ctx context.Context) (contracts.PluginAddition, error) {
		return r.pods.Add(ctx, contracts.Caller{Kind: protocol.CallerHost}, source)
	}, reply)
}

// hostWatcher is the Notifier of a WatchEvents subscription. The bus calls it
// on the loop inside the host's end-of-Update flush, so fn runs there, not in
// a separate loop message.
type hostWatcher struct {
	r   *Root
	fn  func(protocol.EventDeliverParams)
	ctx context.Context // ends on cancel and on host shutdown
}

func (w *hostWatcher) Notify(m protocol.Message) error {
	var p protocol.EventDeliverParams
	dec := json.NewDecoder(bytes.NewReader(m.Params))
	dec.UseNumber() // verbatim numbers, as the bus and a wire watcher see them
	if err := dec.Decode(&p); err != nil {
		return err
	}
	run := func() {
		if w.ctx.Err() == nil {
			w.fn(p)
		}
	}
	if w.r.onLoop() {
		run()
		return nil
	}
	// Defensive: the bus only flushes on the loop, so this is not expected.
	if !w.r.post(run) {
		return errLoopGone
	}
	return nil
}

// WatchEvents implements contracts.Frontend. Loop only; cancel is idempotent
// and any-goroutine.
func (r *Root) WatchEvents(pattern string, fn func(protocol.EventDeliverParams)) (func(), error) {
	ctx, stop := context.WithCancel(r.closingCtx)
	w := &hostWatcher{r: r, fn: fn, ctx: ctx}
	if err := portsstem.NewEvents(r.bus, r.loopSeam).Watch(ctx, contracts.Caller{Kind: protocol.CallerHost}, pattern, w); err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}
