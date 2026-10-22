package host

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/ports/launch"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/services"
	svccontracts "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/internal/stem/eventbus"
	"github.com/hyperlab-sh/husk/internal/stem/surfaces"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	sstate "github.com/hyperlab-sh/husk/internal/substrate/state/surfaces"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

const servicesReasonPlugin = services.ReasonPlugin

// hostCaller is the Services identity of the host acting for itself.
var hostCaller = services.Caller{Kind: "host"}

// log writes a host notice to the injected logger.
func (r *Root) log(m pods.LogMsg) {
	if r.opts.logger == nil {
		return
	}
	level := m.Level
	if level == "" {
		level = "info"
	}
	msg := m.Message
	if m.PluginID != "" {
		msg = m.PluginID + ": " + msg
	}
	r.opts.logger(level, msg)
}

func (r *Root) logf(format string, args ...any) {
	r.log(pods.LogMsg{Level: "info", Message: fmt.Sprintf(format, args...)})
}

type printfLog struct{ r *Root }

func (p printfLog) Printf(format string, v ...any) { p.r.logf(format, v...) }

// publisher lets Services publish events from any goroutine, and Pods from
// the loop.
type publisher struct{ r *Root }

func (p publisher) Publish(name string, data any) {
	var obj map[string]any
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil || json.Unmarshal(raw, &obj) != nil {
			obj = nil
		}
	}
	// Coalescing is by name alone unless a key says otherwise: two plugins
	// that exit in one tick are two events, not one.
	var key string
	if name == pods.PluginExitedEvent {
		key, _ = obj["plugin_id"].(string)
	}
	publish := func() {
		p.r.bus.Publish(eventbus.Event{Name: name, Key: key, Source: eventbus.SourceHost, Data: obj})
	}
	// Pods publish pods.plugin-exited from inside Update, where a blocking
	// Post would wait on the loop it runs on; publish inline there.
	if g := p.r.loopGID.Load(); g != 0 && g == goroutineID() {
		publish()
		return
	}
	p.r.post(publish)
}

// storeClock adapts the protocol Clock the host is configured with to the
// Clock Store and Services take (they need timers with a channel), so one
// injected clock drives every host deadline.
type storeClock struct{ c protocol.Clock }

func (s storeClock) Now() time.Time { return s.c.Now() }

func (s storeClock) NewTimer(d time.Duration) svccontracts.Timer {
	ch := make(chan time.Time, 1)
	t := s.c.AfterFunc(d, func() {
		select {
		case ch <- s.c.Now():
		default:
		}
	})
	return &chanTimer{ch: ch, t: t}
}

type chanTimer struct {
	ch <-chan time.Time
	t  protocol.Timer
}

func (t *chanTimer) C() <-chan time.Time { return t.ch }
func (t *chanTimer) Stop() bool          { return t.t.Stop() }

func denyPrompt(secrets.Prompt) <-chan bool {
	ch := make(chan bool, 1)
	ch <- false
	return ch
}

// lateOutbox lets the Calls table be built before the Pod host exists.
type lateOutbox struct{ r *Root }

func (o lateOutbox) Send(plugin calls.PluginID, m protocol.Message) error {
	return o.r.pods.Send(plugin, m)
}

// sinks resolves a plugin's notifier for the Event bus.
type sinks struct{ r *Root }

func (s sinks) Notifier(id string) (contracts.Notifier, bool) {
	pod, ok := s.r.pods.Pod(id)
	if !ok {
		return nil, false
	}
	return podNotifier{pod}, true
}

type podNotifier struct{ pod *pods.Pod }

func (n podNotifier) Notify(m protocol.Message) error {
	if len(m.Params) == 0 {
		return n.pod.Notify(m.Method, nil)
	}
	return n.pod.Notify(m.Method, m.Params)
}

// ---- catalog view ----

// catalogView is the loop-confined read model of the declared plugin
// documents: who publishes which slot, which hooks exist and who subscribes.
// It implements surfaces.Publications and hooks.Subscriptions.
type catalogView struct {
	store          *store.Store
	slotOwner      map[string]string
	hookDeclared   map[string]bool
	hookSubs       map[string][]string
	onSlotsChanged func()
}

func newCatalogView(s *store.Store) *catalogView {
	return &catalogView{store: s, slotOwner: map[string]string{}, hookDeclared: map[string]bool{}, hookSubs: map[string][]string{}}
}

var _ surfaces.Publications = (*catalogView)(nil)

// Owner implements surfaces.Publications.
func (c *catalogView) Owner(slot string) (string, bool) {
	p, ok := c.slotOwner[slot]
	return p, ok
}

// Subscribers implements hooks.Subscriptions.
func (c *catalogView) Subscribers(hook string) ([]string, bool) {
	if !c.hookDeclared[hook] {
		return nil, false
	}
	return slices.Clone(c.hookSubs[hook]), true
}

type idDecl struct {
	ID string `json:"id"`
}

type catalogDoc struct {
	PublishedSlots    []idDecl `json:"published_slots"`
	HooksDeclared     []idDecl `json:"hooks_declared"`
	HookRegistrations []idDecl `json:"hook_registrations"`
	Subscriptions     []string `json:"subscriptions"`
	EventsDeclared    []struct {
		Name     string `json:"name"`
		Coalesce *bool  `json:"coalesce"`
	} `json:"events_declared"`
}

// refresh re-reads the declared documents and returns the Event bus table.
// When the slot-to-publisher answer changed it calls onSlotsChanged (D58).
func (c *catalogView) refresh() eventbus.Table {
	docs := c.store.Declared(store.DeclaredPlugins)
	ids := make([]string, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	owners := map[string]string{}
	declared := map[string]bool{}
	subs := map[string][]string{}
	table := eventbus.Table{Subscribe: map[string][]string{}}
	for _, id := range ids {
		raw, err := json.Marshal(docs[id])
		if err != nil {
			continue
		}
		var d catalogDoc
		if json.Unmarshal(raw, &d) != nil {
			continue
		}
		for _, s := range d.PublishedSlots {
			if _, taken := owners[s.ID]; !taken {
				owners[s.ID] = id
			}
		}
		for _, h := range d.HooksDeclared {
			declared[h.ID] = true
		}
		for _, h := range d.HookRegistrations {
			subs[h.ID] = append(subs[h.ID], id)
		}
		if len(d.Subscriptions) > 0 {
			table.Subscribe[id] = d.Subscriptions
		}
		for _, e := range d.EventsDeclared {
			if e.Coalesce != nil && !*e.Coalesce {
				table.NoCoalesce = append(table.NoCoalesce, e.Name)
			}
		}
	}
	changed := len(owners) != len(c.slotOwner)
	for k, v := range owners {
		if c.slotOwner[k] != v {
			changed = true
		}
	}
	c.slotOwner, c.hookDeclared, c.hookSubs = owners, declared, subs
	if changed && c.onSlotsChanged != nil {
		c.onSlotsChanged()
	}
	return table
}

// ---- declarations ----

// declarations holds each Pod's occupants and contributions declaration
// slices and implements surfaces.Declarations. Loop-only.
type declarations struct {
	st  *state.State
	occ map[string]*sstate.OccupantsSlice
	con map[string]*sstate.ContributionsSlice
}

func newDeclarations(st *state.State) *declarations {
	return &declarations{st: st, occ: map[string]*sstate.OccupantsSlice{}, con: map[string]*sstate.ContributionsSlice{}}
}

var _ surfaces.Declarations = (*declarations)(nil)

// ensure registers the plugin's two declaration slices once.
func (d *declarations) ensure(plugin string) error {
	if d.occ[plugin] != nil {
		return nil
	}
	o, err := sstate.RegisterOccupants(d.st, plugin)
	if err != nil {
		return err
	}
	c, err := sstate.RegisterContributions(d.st, plugin)
	if err != nil {
		o.Unregister()
		return err
	}
	d.occ[plugin], d.con[plugin] = o, c
	return nil
}

// claim declares the surfaces the manifest claims.
func (d *declarations) claim(spec manifest.Spec) error {
	if err := d.ensure(spec.ID); err != nil {
		return err
	}
	claims := sstate.Occupants{}
	for _, s := range spec.Surfaces {
		claims[s.ID] = sstate.Occupant{Surface: s.ID, Plugin: spec.ID}
	}
	return d.occ[spec.ID].Set(claims)
}

// Contribute implements surfaces.Declarations.
func (d *declarations) Contribute(plugin, slot string, entry surfaces.Contribution) error {
	if err := d.ensure(plugin); err != nil {
		return err
	}
	return d.con[plugin].Upsert(slot, entry)
}

// Release implements surfaces.Declarations.
func (d *declarations) Release(plugin string, surface protocol.SurfaceID) error {
	o := d.occ[plugin]
	if o == nil {
		return nil
	}
	cur := o.Get()
	if _, ok := cur[surface]; !ok {
		return nil
	}
	delete(cur, surface)
	return o.Set(cur)
}

// drop unregisters the plugin's declaration slices (Pod teardown).
func (d *declarations) drop(plugin string) {
	if o := d.occ[plugin]; o != nil {
		o.Unregister()
	}
	if c := d.con[plugin]; c != nil {
		c.Unregister()
	}
	delete(d.occ, plugin)
	delete(d.con, plugin)
}

// ---- launch loop ----

// hostLoop extends the launch Loop with the declaration slices: before the
// catalog documents and the reload, each plugin's occupants and contributions
// slices are registered on the loop with the manifest's surface claims.
type hostLoop struct {
	launch.Loop
	r *Root
}

func (l hostLoop) Prepare(ctx context.Context, gate uint64, specs []manifest.Spec) pods.Preparation {
	failed := map[string]error{}
	done := make(chan struct{})
	ok := l.r.post(func() {
		defer close(done)
		for _, spec := range specs {
			if _, live := l.r.pods.Pod(spec.ID); !live {
				failed[spec.ID] = fmt.Errorf("plugin %s was torn down before its launch", spec.ID)
				continue
			}
			if err := l.r.decl.claim(spec); err != nil {
				failed[spec.ID] = fmt.Errorf("declaration slices: %w", err)
			}
		}
	})
	if !ok {
		for _, spec := range specs {
			failed[spec.ID] = errLoopGone
		}
		return pods.Preparation{Failed: failed}
	}
	select {
	case <-done:
	case <-ctx.Done():
		prep := pods.Preparation{Failed: map[string]error{}}
		for _, spec := range specs {
			prep.Failed[spec.ID] = ctx.Err()
		}
		return prep
	}
	rest := specs[:0:0]
	for _, spec := range specs {
		if failed[spec.ID] == nil {
			rest = append(rest, spec)
		}
	}
	prep := l.Loop.Prepare(ctx, gate, rest)
	if prep.Failed == nil {
		prep.Failed = map[string]error{}
	}
	for id, err := range failed {
		prep.Failed[id] = err
	}
	return prep
}

// ---- State reads for the opening ----

type rectSize struct{ w, h int }

func decodeSurfaces(snap *state.Snapshot) (sstate.Surfaces, bool) {
	var s sstate.Surfaces
	e, ok := snap.Slices[state.SurfacesNamespace]
	if !ok || json.Unmarshal(e.Value, &s) != nil {
		return s, false
	}
	return s, true
}

func decodeGeometry(snap *state.Snapshot) map[protocol.SurfaceID]rectSize {
	out := map[protocol.SurfaceID]rectSize{}
	e, ok := snap.Slices[state.GeometryNamespace]
	if !ok {
		return out
	}
	var g struct {
		Rects map[protocol.SurfaceID]struct {
			Inner struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			} `json:"inner"`
		} `json:"rects"`
	}
	if json.Unmarshal(e.Value, &g) != nil {
		return out
	}
	for id, r := range g.Rects {
		out[id] = rectSize{r.Inner.Width, r.Inner.Height}
	}
	return out
}

func decodeFocus(snap *state.Snapshot) protocol.SurfaceID {
	e, ok := snap.Slices[state.FocusNamespace]
	if !ok {
		return ""
	}
	var f struct {
		Surface protocol.SurfaceID `json:"surface"`
	}
	_ = json.Unmarshal(e.Value, &f)
	return f.Surface
}

func sortedSurfaces(m map[protocol.SurfaceID]sstate.Occupant) []protocol.SurfaceID {
	ids := make([]protocol.SurfaceID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
