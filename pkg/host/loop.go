package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	portsservices "github.com/hyperlab-sh/husk/pkg/host/ports/services"
	"github.com/hyperlab-sh/husk/pkg/pods"
	"github.com/hyperlab-sh/husk/pkg/pods/server"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/stem/calls"
	"github.com/hyperlab-sh/husk/pkg/stem/eventbus"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

var errLoopGone = errors.New("host: the loop no longer runs")

// fnMsg carries a closure to the loop. The loop forwards it to Update like
// every other message the host posts.
type fnMsg struct{ fn func() }

// post runs fn on the loop; false when the loop no longer accepts work.
func (r *Root) post(fn func()) bool {
	r.postMu.Lock()
	if !r.live.Load() {
		r.buildPosts = append(r.buildPosts, fn)
		r.postMu.Unlock()
		return true
	}
	r.postMu.Unlock()
	return r.cfg.Post(fnMsg{fn: fn})
}

// drainBuildPosts keeps construction-time State writes on the builder. The
// transition to the caller's loop is atomic with accepting new callbacks.
// Writes stay pending until the caller's first Update flushes them; construction
// stages must never read their own posted writes back from a Snapshot.
func (r *Root) drainBuildPosts(handoff bool) {
	for {
		r.postMu.Lock()
		pending := r.buildPosts
		r.buildPosts = nil
		if len(pending) == 0 && handoff {
			r.live.Store(true)
		}
		r.postMu.Unlock()
		if len(pending) == 0 {
			return
		}
		for _, fn := range pending {
			fn()
		}
	}
}

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

// exec runs loop-confined fn: directly when the caller is the loop goroutine
// itself (Close called from Update), on the loop otherwise, and inline only
// when no loop can be running: before New returns nothing consumes the loop,
// and after the loop ended no one else touches the modules. "Ended" is read
// from Config.Post, whose contract is that once it returns false Update is
// never called again. A
// loop that is alive but does not run fn within loopWait is wedged; fn is then
// not run at all and the error says so.
func (r *Root) exec(fn func()) error {
	if !r.live.Load() {
		fn()
		return nil
	}
	if r.onLoop() {
		fn()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.loopWaitOr(loopWait))
	defer cancel()
	err := r.do(ctx, fn)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errLoopGone):
		fn()
		return nil
	}
	return fmt.Errorf("the loop is wedged (no run within %v): %w", r.opts.loopWaitOr(loopWait), err)
}

// onLoop reports whether the caller is the loop goroutine, learned on its
// first Update.
func (r *Root) onLoop() bool {
	g := r.loopGID.Load()
	return g != 0 && g == goroutineID()
}

// goroutineID reads the current goroutine's id from its stack header. It is
// used only to recognize the loop goroutine (onLoop: exec and the event
// publisher), never per message on a hot path.
func goroutineID() int64 {
	var buf [32]byte
	b := buf[:runtime.Stack(buf[:], false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i > 0 {
		id, _ := strconv.ParseInt(string(b[:i]), 10, 64)
		return id
	}
	return 0
}

// loopSeam is the Loop the Stem ports take (ports/stem.Loop).
type loopSeam struct{ r *Root }

func (l loopSeam) Do(ctx context.Context, fn func()) error { return l.r.do(ctx, fn) }
func (l loopSeam) Post(fn func()) bool                     { return l.r.post(fn) }

// Update applies one posted message on the loop and runs the tick-end work.
func (r *Root) Update(msg any) bool {
	if r.loopGID.Load() == 0 {
		r.loopGID.Store(goroutineID())
	}
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
	announcements := r.afterState
	r.afterState = nil
	for _, publish := range announcements {
		publish()
	}
	if r.rt != nil {
		// A flight the State listener or a profile.use requested in this tick
		// launches now, so every request of the tick joined it.
		r.rt.Coordinator.Tick()
	}
	if r.engine != nil {
		// A resolution the engine commits here (a claim that resolves, a
		// release) lands in State after this tick's flush, so the listener
		// only sees it next Update; read the revision around Flush instead
		// and push in this tick.
		before := r.surfacesRevision()
		r.engine.Flush()
		if r.surfacesRevision() != before {
			r.sizesDirty = true
		}
		r.pushResized()
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

// publishStateChanges connects State's tick-end facts to the Event bus. Values
// stay on state.watch; event subscribers receive only revision metadata.
func (r *Root) publishStateChanges(changes []state.Change, _ *state.Snapshot) {
	for _, change := range changes {
		data := map[string]any{"slice": change.Namespace, "owner": change.Owner, "revision": change.Revision}
		if change.Removed {
			data["removed"] = true
		}
		r.bus.Publish(eventbus.Event{Name: "state.changed", Key: change.Namespace, Source: eventbus.SourceHost, Data: data})
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

// lifecycle is the host half of plugin addition and removal.
type lifecycle struct{ r *Root }

var _ pods.Workers = lifecycle{}

// Added attaches the same delivery bookkeeping used by startup Pods.
func (m lifecycle) Added(id string) { m.r.watchPod(id) }

// Undeclare is steps 5 and 6's host half, in the teardown tick: the
// declaration slices go with the Pod, its declared documents are removed and
// every reader derived from them refreshes before the reload. Occupant entries
// of the removed owner drop at the engine's flush, which tick runs.
func (m lifecycle) Undeclare(id string) {
	r := m.r
	if cancel := r.podWatch[id]; cancel != nil {
		cancel()
		delete(r.podWatch, id)
	}
	r.decl.drop(id)
	r.store.Undeclare(id)
	r.refreshCatalog()
}

// Reload is step 7's reload: reason plugin under the Host's context. The
// Coordinator hands back its reports with the aggregate incomplete error, so
// the reports are the answer whenever there are any.
func (m lifecycle) Reload(ctx context.Context) ([]contracts.ReloadReport, error) {
	reports, err := m.r.rt.Coordinator.Reload(ctx, servicesReasonPlugin)
	if len(reports) > 0 {
		return portsservices.Reports(reports), nil
	}
	return nil, err
}

// Go starts a tracked worker (refused once shutdown began).
func (m lifecycle) Go(cmd tea.Cmd) { m.r.spawn(cmd) }

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
	pal, indices := r.rt.Theme.WireANSI()
	add(protocol.MethodThemeChanged, protocol.ThemeChangedParams{Palette: pal, ANSI: indices})
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

// markSizesDirty is the State flush listener: a write to the frontend's
// husk.geometry or husk.focus, or to husk.surfaces (visibility), may change
// what an occupant is told about its surfaces.
func (r *Root) markSizesDirty(changes []state.Change, snap *state.Snapshot) {
	for i := range changes {
		switch changes[i].Namespace {
		case state.GeometryNamespace, state.FocusNamespace:
			r.sizesDirty = true
		case state.SurfacesNamespace:
			r.sizesDirty = true
			// Observe the release before the engine resolves standing claims.
			// A yielder with no alternative can regain the same surface in
			// this tick; it must hear surface.resized even at unchanged size.
			if res, ok := decodeSurfaces(snap); ok {
				for id := range r.lastSizes {
					if res.Occupants[id].Plugin == "" {
						delete(r.lastSizes, id)
					}
				}
			}
		}
	}
}

// watchPod registers the Pod as the delivery target of its occupant's
// surface.resized pushes. A claim holds a surface only while the Pod is
// live, so the occupant cannot learn its size from the opening and hears it
// from here once it takes the surface. Loop only.
func (r *Root) watchPod(id string) {
	if r.podWatch == nil {
		r.podWatch = map[string]func(){}
	}
	if pod, ok := r.pods.Pod(id); ok {
		r.podWatch[id] = r.engine.WatchPlugin(id, podNotifier{pod})
	}
}

// markLivenessChanged is the State flush listener that tells the Surfaces
// engine a Pod entered or left a status: a claim holds a surface only while
// its Pod is live (D70), so any husk.pods write may move occupancy. The
// engine resolves it in the same tick's pass. Secrets drops a departed Pod's
// pending reads here too, without waiting for a connection close or reload.
func (r *Root) markLivenessChanged(changes []state.Change, snap *state.Snapshot) {
	for i := range changes {
		if changes[i].Namespace == state.PodsNamespace {
			r.engine.LivenessChanged()
			var doc struct {
				Plugins map[string]struct {
					Status contracts.PodStatus `json:"status"`
				} `json:"plugins"`
			}
			if entry, ok := snap.Slices[state.PodsNamespace]; ok {
				if json.Unmarshal(entry.Value, &doc) != nil {
					return
				}
			}
			next := map[string]bool{}
			for id, p := range doc.Plugins {
				if p.Status == contracts.PodLive {
					next[id] = true
				}
			}
			for id := range r.secretLive {
				if !next[id] {
					r.rt.Secrets.DropPlugin(id)
				}
			}
			r.secretLive = next
			return
		}
	}
}

// pushResized tells the occupants of surfaces whose size, focus or
// visibility changed since the last push (Engine.Resized sends each plugin
// only its own surfaces). It is driven by the frontend's geometry and focus
// writes (state.md, Surfaces resolution), after the tick's State flush and
// the engine's resolution pass, so it reads the committed snapshot. A tick
// that changed none of those namespaces does nothing.
func (r *Root) pushResized() {
	if !r.sizesDirty {
		return
	}
	r.sizesDirty = false
	snap := r.st.Snapshot()
	res, ok := decodeSurfaces(snap)
	if !ok {
		return
	}
	plugins := map[string]bool{}
	for _, occ := range res.Occupants {
		plugins[occ.Plugin] = true
	}
	next := map[protocol.SurfaceID]pushedSize{}
	var changed []protocol.SurfaceSize
	for plugin := range plugins {
		for _, size := range occupiedSizes(snap, plugin) {
			next[size.Surface] = pushedSize{Plugin: plugin, Size: size}
		}
	}
	for _, id := range sortedSurfaces(res.Occupants) {
		// A new occupant of an unchanged surface must be told too: its
		// opening listed only what it occupied then.
		if p, ok := next[id]; ok && r.lastSizes[id] != p {
			changed = append(changed, p.Size)
		}
	}
	r.lastSizes = next
	if len(changed) > 0 {
		r.engine.Resized(changed)
	}
}

// pushedSize is what one surface's occupant was last told.
type pushedSize struct {
	Plugin string
	Size   protocol.SurfaceSize
}

// surfacesRevision is the husk.surfaces revision of the committed snapshot.
func (r *Root) surfacesRevision() uint64 {
	return r.st.Snapshot().Slices[state.SurfacesNamespace].Revision
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
