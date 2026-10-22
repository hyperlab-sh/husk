package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/server"
	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

var errLoopGone = errors.New("host: the loop no longer runs")

// fnMsg carries a closure to the loop. The loop forwards it to Update like
// every other message the host posts.
type fnMsg struct{ fn func() }

// post runs fn on the loop; false when the loop no longer accepts work.
func (r *Root) post(fn func()) bool { return r.cfg.Post(fnMsg{fn: fn}) }

// do runs fn on the loop and returns once it has run. When ctx ends first fn
// is guaranteed not to run (claimed by exactly one side).
func (r *Root) do(ctx context.Context, fn func()) error {
	var claim atomic.Int32 // 0 pending, 1 running, 2 abandoned
	done := make(chan struct{})
	if !r.post(func() {
		if !claim.CompareAndSwap(0, 1) {
			return
		}
		defer close(done)
		fn()
	}) {
		return errLoopGone
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	case <-r.ctx.Done():
	}
	if claim.CompareAndSwap(0, 2) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return errLoopGone
	}
	<-done
	return nil
}

// exec runs fn on the loop if one is running, else inline: before New
// returns nothing consumes the loop, and after the loop ended no one else
// touches the loop-confined modules.
func (r *Root) exec(fn func()) {
	if r.live.Load() {
		ctx, cancel := context.WithTimeout(context.Background(), loopWait)
		defer cancel()
		if r.do(ctx, fn) == nil {
			return
		}
	}
	fn()
}

// loopSeam is the Loop the Stem ports take (ports/stem.Loop).
type loopSeam struct{ r *Root }

func (l loopSeam) Do(ctx context.Context, fn func()) error { return l.r.do(ctx, fn) }
func (l loopSeam) Post(fn func()) bool                     { return l.r.post(fn) }

// Update applies one posted message on the loop and runs the tick-end work.
func (r *Root) Update(msg any) bool {
	handled := true
	switch m := msg.(type) {
	case fnMsg:
		m.fn()
	case calls.ReplyMsg:
		if res, ok := r.table.Resolve(m.Plugin, m.Reply); ok {
			r.complete([]calls.Result{res})
		}
	case pods.ClosedMsg:
		reason := "plugin exited"
		if m.Reason != nil {
			reason = m.Reason.Error()
		}
		r.complete(r.table.DropPlugin(calls.PluginID(m.PluginID), reason))
	case calls.SweepMsg:
		r.complete(r.table.Sweep())
	case server.Notice:
		r.log(pods.LogMsg{Level: noticeLevel(m.Level), Message: "server: " + m.Message})
	case pods.TeardownMsg:
		r.pods.Handle(m)
		r.teardownPod(m.PluginID)
	case pods.PreparedMsg, pods.RegisterSlicesMsg:
		r.pods.Handle(m)
		r.refreshCatalog()
	default:
		handled = r.pods.Handle(msg)
	}
	if handled {
		r.tick()
	}
	return handled
}

// complete hands Calls results to their requesters.
func (r *Root) complete(results []calls.Result) {
	if len(results) == 0 {
		return
	}
	r.disp.Complete(results...)
	r.runner.Complete(results...)
}

// tick is the end of one Update: State flushes first, then the Surfaces
// resolution pass, then the Event bus; finally the Calls sweep is scheduled
// if anything is pending.
func (r *Root) tick() {
	// A partially built host (shutdown after a failed New) runs only the
	// flushes whose modules exist.
	if r.st != nil {
		r.st.Flush()
	}
	if r.engine != nil {
		r.engine.Flush()
	}
	if r.bus != nil {
		r.bus.Flush()
	}
	if r.table != nil {
		if delay, ok := r.table.NextSweep(); ok {
			r.clock.AfterFunc(delay, func() { r.cfg.Post(calls.SweepMsg{}) })
		}
	}
}

// refreshCatalog re-reads the declared documents after a launch gate or a
// teardown, tells the Surfaces engine when the published-slot catalog
// changed (D58) and swaps the Event bus table.
func (r *Root) refreshCatalog() {
	table := r.catalog.refresh()
	for _, p := range r.bus.SetTable(table) {
		r.logf("events: %v", p)
	}
}

// teardownPod is the host half of Pod teardown (pods.md): the declaration
// slices go with the Pod, its declared documents are removed and Services
// reload without them. Occupant entries of the removed owner drop at the
// engine's next flush, which tick runs.
func (r *Root) teardownPod(id string) {
	r.decl.drop(id)
	r.store.Undeclare(id)
	r.refreshCatalog()
	r.spawn(func() tea.Msg {
		_, _ = r.rt.Coordinator.Reload(r.ctx, servicesReasonPlugin)
		return nil
	})
}

// opening builds the notifications a Pod queues before anything else:
// theme.changed, one surface.resized with every surface the plugin occupies,
// input.key_state. Loop-only.
func (r *Root) opening(pluginID string) []protocol.Message {
	var out []protocol.Message
	add := func(method string, params any) {
		raw, err := json.Marshal(params)
		m := protocol.Message{Method: method, Params: raw}
		if err != nil {
			r.log(pods.LogMsg{PluginID: pluginID, Level: "warn", Message: fmt.Sprintf("opening %s: %v", method, err)})
			return
		}
		out = append(out, m)
	}
	add(protocol.MethodThemeChanged, protocol.ThemeChangedParams{Palette: r.rt.Theme.Get(hostCaller)})
	add(protocol.MethodSurfaceResized, protocol.SurfaceResizedParams{Surfaces: r.occupiedSizes(pluginID)})
	var mods protocol.ModifierState
	if r.opts.modifiers != nil {
		mods = r.opts.modifiers()
	}
	add(protocol.MethodInputKeyState, protocol.KeyStateParams{Phase: protocol.KeyPhaseSync, Key: "modifiers", Modifiers: mods})
	return out
}

// occupiedSizes lists the plugin's occupied surfaces from the husk.surfaces
// resolution with sizes from husk.geometry.
func (r *Root) occupiedSizes(pluginID string) []protocol.SurfaceSize {
	snap := r.st.Snapshot()
	return occupiedSizes(snap, pluginID)
}

func occupiedSizes(snap *state.Snapshot, pluginID string) []protocol.SurfaceSize {
	sizes := []protocol.SurfaceSize{}
	res, ok := decodeSurfaces(snap)
	if !ok {
		return sizes
	}
	geo := decodeGeometry(snap)
	focus := decodeFocus(snap)
	visible := map[protocol.SurfaceID]bool{}
	for _, s := range res.Visible {
		visible[s] = true
	}
	for _, id := range sortedSurfaces(res.Occupants) {
		if res.Occupants[id].Plugin != pluginID {
			continue
		}
		inner := geo[id]
		sizes = append(sizes, protocol.SurfaceSize{Surface: id, Width: inner.w, Height: inner.h, Focused: focus == id, Visible: visible[id]})
	}
	return sizes
}

func noticeLevel(l string) string {
	if l == "" {
		return "warn"
	}
	return l
}
