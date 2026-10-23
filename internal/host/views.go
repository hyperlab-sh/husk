package host

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
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
	"github.com/hyperlab-sh/husk/internal/stem/hooks"
	"github.com/hyperlab-sh/husk/internal/stem/surfaces"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	sstate "github.com/hyperlab-sh/husk/internal/substrate/state/surfaces"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

const servicesReasonPlugin = services.ReasonPlugin

// hostCaller is the Services identity of the host acting for itself.
var hostCaller = services.Caller{Kind: protocol.CallerHost}

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
	// that exit, or are removed, in one tick are two events, not one.
	var key string
	if name == pods.PluginExitedEvent || name == pods.PluginRemovedEvent {
		key, _ = obj["plugin_id"].(string)
	}
	publish := func() {
		if name == pods.PluginExitedEvent {
			obj = p.r.podEntry(key, obj)
		}
		p.r.bus.Publish(eventbus.Event{Name: name, Key: key, Source: eventbus.SourceHost, Data: obj})
	}
	// Pods publish pods.plugin-exited from inside Update, where a blocking
	// Post would wait on the loop it runs on; publish inline there.
	if p.r.onLoop() {
		publish()
		return
	}
	p.r.post(publish)
}

// podEntry is the event data for a Pod's exit: its husk.pods entry as State
// holds it after the exit's liveness write (the merge keeps fields an earlier
// write set), plus plugin_id. fallback is used when the entry is missing.
// Loop only.
func (r *Root) podEntry(id string, fallback map[string]any) map[string]any {
	e, ok := r.st.Get(state.PodsNamespace)
	if !ok {
		return fallback
	}
	var doc struct {
		Plugins map[string]map[string]any `json:"plugins"`
	}
	if json.Unmarshal(e.Value, &doc) != nil || doc.Plugins[id] == nil {
		return fallback
	}
	entry := doc.Plugins[id]
	entry["plugin_id"] = id
	return entry
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
// It implements surfaces.Publications and hooks.Catalog.
type catalogView struct {
	declared       func() map[string]store.Doc
	logf           func(format string, args ...any)
	slotOwner      map[string]string
	hooks          map[string]hooks.HookDef
	hookSubs       map[string][]hooks.Subscription
	problems       map[string][]string // plugin id -> hook catalog problems
	badTimeout     map[string]bool     // hook ids already logged for a zero timeout
	onSlotsChanged func()
}

func newCatalogView(s *store.Store) *catalogView {
	return &catalogView{
		declared:   func() map[string]store.Doc { return s.Declared(store.DeclaredPlugins) },
		logf:       func(string, ...any) {},
		slotOwner:  map[string]string{},
		hooks:      hostHookDefs(),
		hookSubs:   map[string][]hooks.Subscription{},
		problems:   map[string][]string{},
		badTimeout: map[string]bool{},
	}
}

var _ surfaces.Publications = (*catalogView)(nil)

// Owner implements surfaces.Publications.
func (c *catalogView) Owner(slot string) (string, bool) {
	p, ok := c.slotOwner[slot]
	return p, ok
}

var _ hooks.Catalog = (*catalogView)(nil)

// hostHooks is the host's own hook list, declared under the husk owner.
var hostHooks = []hooks.HookDef{
	{ID: "husk.before-state-clear", Owner: hooks.HostOwner, Kind: hooks.Gate, Timeout: 2 * time.Second, Title: "Before state clear"},
}

// hostHookDefs returns the host hooks keyed by id.
func hostHookDefs() map[string]hooks.HookDef {
	defs := make(map[string]hooks.HookDef, len(hostHooks))
	for _, def := range hostHooks {
		defs[def.ID] = def
	}
	return defs
}

// Lookup implements hooks.Catalog.
func (c *catalogView) Lookup(hook string) (hooks.HookDef, bool) {
	def, ok := c.hooks[hook]
	return def, ok
}

// Subscribers implements hooks.Catalog: sorted by priority, then plugin id.
func (c *catalogView) Subscribers(hook string) []hooks.Subscription {
	return slices.Clone(c.hookSubs[hook])
}

// List implements hooks.Catalog: every hook, ordered by id.
func (c *catalogView) List() []hooks.HookDef {
	out := make([]hooks.HookDef, 0, len(c.hooks))
	for _, def := range c.hooks {
		out = append(out, def)
	}
	slices.SortFunc(out, func(a, b hooks.HookDef) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// Problems returns the hook catalog problems of each plugin from the last
// rebuild: refused declarations, by plugin id.
func (c *catalogView) Problems() map[string][]string { return maps.Clone(c.problems) }

type idDecl struct {
	ID string `json:"id"`
}

type hookDecl struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description"`
	TimeoutMs   int    `json:"timeout_ms"`
}

type hookReg struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
}

type catalogDoc struct {
	PublishedSlots    []idDecl   `json:"published_slots"`
	HooksDeclared     []hookDecl `json:"hooks_declared"`
	HookRegistrations []hookReg  `json:"hook_registrations"`
	Subscriptions     []string   `json:"subscriptions"`
	EventsDeclared    []struct {
		Name     string `json:"name"`
		Coalesce *bool  `json:"coalesce"`
	} `json:"events_declared"`
}

// refresh re-reads the declared documents and returns the Event bus table.
// When the slot-to-publisher answer changed it calls onSlotsChanged (D58).
func (c *catalogView) refresh() eventbus.Table {
	docs := c.declared()
	ids := make([]string, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	owners := map[string]string{}
	parsed := map[string]catalogDoc{}
	subs := map[string][]hooks.Subscription{}
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
		parsed[id] = d
		for _, s := range d.PublishedSlots {
			if _, taken := owners[s.ID]; !taken {
				owners[s.ID] = id
			}
		}
		for _, h := range d.HookRegistrations {
			subs[h.ID] = append(subs[h.ID], hooks.Subscription{Plugin: id, Priority: h.Priority})
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
	for _, list := range subs {
		slices.SortFunc(list, func(a, b hooks.Subscription) int {
			return cmp.Or(cmp.Compare(a.Priority, b.Priority), strings.Compare(a.Plugin, b.Plugin))
		})
	}
	defs, problems := c.buildHooks(ids, parsed)
	changed := len(owners) != len(c.slotOwner)
	for k, v := range owners {
		if c.slotOwner[k] != v {
			changed = true
		}
	}
	c.slotOwner, c.hooks, c.hookSubs = owners, defs, subs
	c.reportProblems(problems)
	if changed && c.onSlotsChanged != nil {
		c.onSlotsChanged()
	}
	return table
}

// buildHooks applies the two catalog rules of hooks.md in order over the
// loaded plugins (those with declared documents, whatever their liveness) and
// adds the host hooks. It returns the catalog and the problems by plugin id.
func (c *catalogView) buildHooks(ids []string, parsed map[string]catalogDoc) (map[string]hooks.HookDef, map[string][]string) {
	problems := map[string][]string{}
	problem := func(plugin, format string, args ...any) {
		problems[plugin] = append(problems[plugin], fmt.Sprintf(format, args...))
	}
	// Rule 1, namespace: a hook id belongs to the loaded plugin with the
	// longest id followed by a dot that prefixes it.
	nsOwner := func(hook string) string {
		best := ""
		for _, id := range ids {
			if len(id) > len(best) && strings.HasPrefix(hook, id+".") {
				best = id
			}
		}
		return best
	}
	type claim struct {
		plugin string
		decl   hookDecl
	}
	claims := map[string][]claim{}
	for _, id := range ids {
		for _, h := range parsed[id].HooksDeclared {
			if owner := nsOwner(h.ID); owner != id {
				if owner == "" {
					problem(id, "hook %q is outside its namespace", h.ID)
				} else {
					problem(id, "hook %q is inside the namespace of loaded plugin %q", h.ID, owner)
				}
				continue
			}
			claims[h.ID] = append(claims[h.ID], claim{id, h})
		}
	}
	// Rule 2, duplicate: still contested ids are refused for every declarer.
	defs := hostHookDefs()
	zero := map[string]bool{}
	for hook, list := range claims {
		if len(list) > 1 {
			for _, cl := range list {
				problem(cl.plugin, "hook %q is declared by more than one plugin", hook)
			}
			continue
		}
		cl := list[0]
		def := hooks.HookDef{
			ID: hook, Owner: cl.plugin, Kind: hooks.Kind(cl.decl.Kind),
			Timeout: time.Duration(cl.decl.TimeoutMs) * time.Millisecond,
			Title:   cl.decl.Title, Description: cl.decl.Description,
		}
		if def.Timeout <= 0 {
			zero[hook] = true
			if !c.badTimeout[hook] {
				c.logf("hooks: %s declares %q without a timeout; left out of the catalog", cl.plugin, hook)
			}
			continue
		}
		defs[hook] = def
	}
	c.badTimeout = zero
	return defs, problems
}

// reportProblems logs each problem that was not in the previous rebuild.
func (c *catalogView) reportProblems(now map[string][]string) {
	for plugin, list := range now {
		for _, p := range list {
			if !slices.Contains(c.problems[plugin], p) {
				c.logf("hooks: plugin %s: %s", plugin, p)
			}
		}
	}
	c.problems = now
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
