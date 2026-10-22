package surfaces

import (
	"encoding/json"
	"log"
	"sort"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Clock reports the current time. Tests inject a fake; production injects
// time.Now. No timers or sleeps run inside the engine.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Liveness reports whether a plugin currently has a live process. The engine
// consults it before pushing notifications: a watcher whose plugin is not
// live is skipped until it returns. Nil treats every plugin as live.
type Liveness interface {
	Live(pluginID string) bool
}

// Publications is the engine's read-only view of the published-slot catalog:
// it maps a published slot id to its owning publisher plugin. The host
// implements it over the manifest catalog; the engine owns no catalog.
type Publications interface {
	Owner(slotID string) (publisher string, ok bool)
}

// Declarations is the seam through which the engine asks a plugin's Pod to
// change its declaration slices. The Pod holds the plugin's write capability
// (design/state.md invariant 4); the engine never writes another owner's
// slice. Implementations post the mutation to the loop and return once it is
// accepted; the resolution follows from the State listener.
type Declarations interface {
	// Contribute upserts entry into plugin's <plugin_id>.contributions
	// declaration for slot.
	Contribute(plugin, slot string, entry Contribution) error
}

// Options configures the engine. Nil seams take safe defaults: the real
// clock, all plugins live, no published slots, and declaration writes
// dropped (so tests can assert refusals without a Pod).
type Options struct {
	Clock        Clock
	Liveness     Liveness
	Logger       interface{ Printf(format string, v ...any) }
	Publications Publications
	Declarations Declarations
	// MaxToasts bounds the queued toasts; 0 selects the default of 32. When
	// the bound is hit the oldest toast is dropped and counted.
	MaxToasts int
	// ToastTTL is how long a toast stays active; 0 selects 3200ms.
	ToastTTL time.Duration
}

const (
	// DefaultMaxToasts is the per-instance toast queue bound.
	DefaultMaxToasts = 32
	// DefaultToastTTL matches the v1 toast duration.
	DefaultToastTTL = 3200 * time.Millisecond
)

// Toast levels, as styled by the host renderer.
const (
	LevelInfo    = "info"
	LevelSuccess = "success"
	LevelWarn    = "warn"
	LevelError   = "error"
)

// Stats counts engine events for the bounded health summary.
type Stats struct {
	// RefusedClaims counts claims refused because the surface was occupied.
	RefusedClaims uint64
	// ToastsDropped counts toasts dropped by the queue bound.
	ToastsDropped uint64
	// ToastsExpired counts toasts expired by the TTL.
	ToastsExpired uint64
}

// Toast is one queued transient notice. Toasts never enter State.
type Toast struct {
	Plugin  string
	Level   string
	Title   string
	Message string
	ShownAt time.Time
}

// pluginDecl caches one plugin's decoded declaration slices. A nil map means
// that declaration slice is not registered.
type pluginDecl struct {
	claims        map[string]string
	contributions map[string][]Contribution
}

// watcher is one registered notification target. plugin is the watcher's
// plugin id, or "" for a watcher that is not a plugin.
type watcher struct {
	plugin string
	n      contracts.Notifier
}

// Engine serves surface.update, surface.close, slot.contribute and
// toast.show, and owns the husk.surfaces resolution. All methods are
// loop-only.
type Engine struct {
	st    *state.State
	slice *state.Slice[Surfaces]
	opts  Options

	pubs  Publications
	decls Declarations
	live  Liveness
	log   interface{ Printf(format string, v ...any) }

	// decls cache: plugin id -> last resolved declarations.
	declsCache map[string]*pluginDecl
	dirty      bool

	slotWatchers   map[string]map[*watcher]struct{}
	pluginWatchers map[string]map[*watcher]struct{}

	toasts []Toast
	stats  Stats
}

// New creates the engine on st, registering husk.surfaces and the State
// listener that marks the resolution dirty. The initial sweep resolves any
// declarations registered before the engine, so registration order before
// and after New is equally valid: occupancy is first claim wins either way.
func New(st *state.State, opts Options) (*Engine, error) {
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	if opts.Logger == nil {
		opts.Logger = log.New(discard{}, "", 0)
	}
	if opts.Liveness == nil {
		opts.Liveness = allLive{}
	}
	if opts.Publications == nil {
		opts.Publications = noPublications{}
	}
	if opts.Declarations == nil {
		opts.Declarations = droppedDeclarations{}
	}
	if opts.MaxToasts <= 0 {
		opts.MaxToasts = DefaultMaxToasts
	}
	if opts.ToastTTL <= 0 {
		opts.ToastTTL = DefaultToastTTL
	}
	slice, err := state.Register(st, state.Spec{
		Namespace: state.SurfacesNamespace,
		Owner:     state.SurfacesOwner,
		Scope:     state.ScopeInstance,
	}, Surfaces{Occupants: map[string]string{}, Contributions: map[string][]Entry{}})
	if err != nil {
		return nil, err
	}
	e := &Engine{
		st:             st,
		slice:          slice,
		opts:           opts,
		pubs:           opts.Publications,
		decls:          opts.Declarations,
		live:           opts.Liveness,
		log:            opts.Logger,
		declsCache:     map[string]*pluginDecl{},
		slotWatchers:   map[string]map[*watcher]struct{}{},
		pluginWatchers: map[string]map[*watcher]struct{}{},
	}
	st.OnFlush(func(changes []state.Change, snap *state.Snapshot) {
		for i := range changes {
			if _, _, ok := splitDeclarationNamespace(changes[i].Namespace); ok {
				e.dirty = true
				return
			}
		}
	})
	e.dirty = true
	return e, nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

type allLive struct{}

func (allLive) Live(string) bool { return true }

type noPublications struct{}

func (noPublications) Owner(string) (string, bool) { return "", false }

type droppedDeclarations struct{}

func (droppedDeclarations) Contribute(string, string, Contribution) error { return nil }

// Flush runs one resolution pass: it diffs the Pod-written declaration
// slices against the last pass, writes the result to husk.surfaces and
// pushes slot.contributions notifications to the changed slots' watchers.
// The Stem loop calls it once per Update, after its own State flush; a pass
// with no declaration changes does no work. State writes happen here, on the
// loop, never inside the State listener.
func (e *Engine) Flush() {
	if !e.dirty {
		return
	}
	e.dirty = false
	snap := e.st.Snapshot()

	// Gather the current declarations, namespace-sorted: one flush resolves
	// in the same deterministic order State reports changes.
	names := make([]string, 0, len(snap.Slices))
	for ns := range snap.Slices {
		if _, _, ok := splitDeclarationNamespace(ns); ok {
			names = append(names, ns)
		}
	}
	sort.Strings(names)

	next := make(map[string]*pluginDecl, len(names))
	for _, ns := range names {
		plugin, kind, _ := splitDeclarationNamespace(ns)
		entry := snap.Slices[ns]
		if entry.Owner != plugin {
			continue // not the plugin's own declaration; ignore defensively
		}
		d := next[plugin]
		if d == nil {
			d = &pluginDecl{}
			next[plugin] = d
		}
		var err error
		switch kind {
		case occupantsSuffix:
			if d.claims, err = decodeOccupants(entry.Value); err != nil {
				e.log.Printf("surfaces: %s: malformed occupants declaration: %v", ns, err)
			}
		case contributionsSuffix:
			if d.contributions, err = decodeContributions(entry.Value); err != nil {
				e.log.Printf("surfaces: %s: malformed contributions declaration: %v", ns, err)
			}
		}
	}

	res := e.slice.Get()
	if res.Occupants == nil {
		res.Occupants = map[string]string{}
	}
	if res.Contributions == nil {
		res.Contributions = map[string][]Entry{}
	}
	changedSlots := map[string]bool{}

	plugins := make([]string, 0, len(next)+len(e.declsCache))
	seen := map[string]bool{}
	for plugin := range next {
		plugins = append(plugins, plugin)
		seen[plugin] = true
	}
	for plugin := range e.declsCache {
		if !seen[plugin] {
			plugins = append(plugins, plugin)
		}
	}
	sort.Strings(plugins)

	for _, plugin := range plugins {
		e.resolvePlugin(plugin, e.declsCache[plugin], next[plugin], &res, changedSlots)
	}

	if err := e.slice.Update(func(v *Surfaces) { *v = res }); err != nil {
		e.log.Printf("surfaces: husk.surfaces commit failed: %v", err)
	}

	// Watchers are notified outside State's flush callback: the Notifier is
	// a queued socket write and must not block the loop.
	for slot := range changedSlots {
		e.notifySlot(slot, res, next)
	}
}

// resolvePlugin folds one plugin's declaration change into res. old or new
// may be nil: nil old is a first arrival, nil new is a full removal.
func (e *Engine) resolvePlugin(plugin string, old, cur *pluginDecl, res *Surfaces, changedSlots map[string]bool) {
	oldClaims := map[string]string{}
	if old != nil && old.claims != nil {
		oldClaims = old.claims
	}
	newClaims := map[string]string{}
	if cur != nil && cur.claims != nil {
		newClaims = cur.claims
	}

	// New claims, surface-sorted so one flush resolves deterministically.
	fresh := make([]string, 0, len(newClaims))
	for surface := range newClaims {
		if _, had := oldClaims[surface]; !had {
			fresh = append(fresh, surface)
		}
	}
	sort.Strings(fresh)
	for _, surface := range fresh {
		if newClaims[surface] != plugin {
			continue // a claim naming another plugin is not this plugin's
		}
		switch occupant := res.Occupants[surface]; occupant {
		case "":
			res.Occupants[surface] = plugin // first claim wins
		case plugin:
			// Still ours from a release the Pod has not re-declared; no-op.
		default:
			e.stats.RefusedClaims++
			e.log.Printf("surfaces: %s occupied by %s; claim by %s refused", surface, occupant, plugin)
		}
	}

	// Dropped claims: release only what this plugin holds.
	for surface := range oldClaims {
		if _, still := newClaims[surface]; still {
			continue
		}
		if res.Occupants[surface] == plugin {
			delete(res.Occupants, surface)
		}
	}

	// Contributions: upsert in place, new entries append in arrival order,
	// gone entries drop.
	oldSlots := map[string][]Contribution{}
	if old != nil && old.contributions != nil {
		oldSlots = old.contributions
	}
	newSlots := map[string][]Contribution{}
	if cur != nil && cur.contributions != nil {
		newSlots = cur.contributions
	}

	slotSet := map[string]bool{}
	for slot := range oldSlots {
		slotSet[slot] = true
	}
	for slot := range newSlots {
		slotSet[slot] = true
	}
	slots := make([]string, 0, len(slotSet))
	for slot := range slotSet {
		slots = append(slots, slot)
	}
	sort.Strings(slots)

	for _, slot := range slots {
		oldEntries := indexByID(oldSlots[slot], plugin)
		newList := newSlots[slot]
		changed := false

		// Remove entries gone from the declaration.
		kept := res.Contributions[slot][:0:0]
		for _, entry := range res.Contributions[slot] {
			if entry.Plugin == plugin && !hasID(newList, plugin, entry.ID) {
				changed = true
				continue
			}
			kept = append(kept, entry)
		}
		res.Contributions[slot] = kept

		// Upsert the declared entries in declaration list order.
		for _, entry := range newList {
			if entry.Plugin != plugin {
				continue
			}
			if old, exists := oldEntries[entry.ID]; exists {
				if old.Label != entry.Label {
					changed = true // content replaced in place
				}
				continue
			}
			res.Contributions[slot] = append(res.Contributions[slot], Entry{Plugin: plugin, ID: entry.ID})
			changed = true
		}

		if len(res.Contributions[slot]) == 0 {
			delete(res.Contributions, slot)
		}
		if changed {
			changedSlots[slot] = true
		}
	}

	if cur != nil {
		e.declsCache[plugin] = cur
	} else {
		delete(e.declsCache, plugin)
	}
}

// indexByID maps the ids of entries owned by plugin, for quick membership.
func indexByID(entries []Contribution, plugin string) map[string]Contribution {
	ids := map[string]Contribution{}
	for _, entry := range entries {
		if entry.Plugin == plugin {
			ids[entry.ID] = entry
		}
	}
	return ids
}

func hasID(entries []Contribution, plugin, id string) bool {
	for _, entry := range entries {
		if entry.Plugin == plugin && entry.ID == id {
			return true
		}
	}
	return false
}

// notifySlot pushes one slot.contributions notification, carrying the slot's
// resolved order with each entry's declared content, to its watchers.
func (e *Engine) notifySlot(slot string, res Surfaces, decls map[string]*pluginDecl) {
	set := e.slotWatchers[slot]
	if len(set) == 0 {
		return
	}
	contribs := make([]protocol.SlotContribution, 0, len(res.Contributions[slot]))
	for _, entry := range res.Contributions[slot] {
		c := protocol.SlotContribution{
			SlotContributeParams: protocol.SlotContributeParams{SlotID: slot, ID: entry.ID},
			PluginID:             entry.Plugin,
		}
		if d := decls[entry.Plugin]; d != nil {
			for _, declared := range d.contributions[slot] {
				if declared.ID == entry.ID && declared.Plugin == entry.Plugin {
					c.Content = declared.Label
					break
				}
			}
		}
		contribs = append(contribs, c)
	}
	msg, err := notification(protocol.MethodSlotContributions, protocol.SlotContributionsParams{
		SlotID:        slot,
		Contributions: contribs,
	})
	if err != nil {
		e.log.Printf("surfaces: slot.contributions encode failed: %v", err)
		return
	}
	for w := range set {
		e.push(w, msg)
	}
}

// push delivers msg unless the watcher's plugin is not live.
func (e *Engine) push(w *watcher, msg protocol.Message) {
	if w.plugin != "" && !e.live.Live(w.plugin) {
		return
	}
	if err := w.n.Notify(msg); err != nil {
		e.log.Printf("surfaces: notification to %s failed: %v", w.plugin, err)
	}
}

func notification(method string, params any) (protocol.Message, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return protocol.Message{}, err
	}
	return protocol.Message{Method: method, Params: raw}, nil
}

// WatchSlot registers n to receive slot.contributions notifications for slot.
// plugin is the watching plugin id, or "" for a non-plugin watcher; a plugin
// watcher is skipped while its plugin is not live. The returned cancel
// unregisters; it is idempotent and safe to call from the loop.
func (e *Engine) WatchSlot(plugin, slot string, n contracts.Notifier) (cancel func()) {
	return e.watch(&e.slotWatchers, slot, &watcher{plugin: plugin, n: n})
}

// WatchPlugin registers n as plugin's delivery target: it receives
// surface.resized for the surfaces plugin occupies, and nothing else.
func (e *Engine) WatchPlugin(plugin string, n contracts.Notifier) (cancel func()) {
	return e.watch(&e.pluginWatchers, plugin, &watcher{plugin: plugin, n: n})
}

func (e *Engine) watch(reg *map[string]map[*watcher]struct{}, key string, w *watcher) (cancel func()) {
	set := (*reg)[key]
	if set == nil {
		set = map[*watcher]struct{}{}
		(*reg)[key] = set
	}
	set[w] = struct{}{}
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		delete(set, w)
		if len(set) == 0 {
			delete(*reg, key)
		}
	}
}

// Resized pushes one surface.resized notification per watching plugin,
// listing only that plugin's occupied surfaces among sizes: geometry reaches
// occupants, never the whole workbench.
func (e *Engine) Resized(sizes []protocol.SurfaceSize) {
	res := e.slice.Get()
	byPlugin := map[string][]protocol.SurfaceSize{}
	for _, size := range sizes {
		if occupant := res.Occupants[string(size.Surface)]; occupant != "" {
			byPlugin[occupant] = append(byPlugin[occupant], size)
		}
	}
	for plugin, own := range byPlugin {
		set := e.pluginWatchers[plugin]
		if len(set) == 0 {
			continue
		}
		msg, err := notification(protocol.MethodSurfaceResized, protocol.SurfaceResizedParams{Surfaces: own})
		if err != nil {
			e.log.Printf("surfaces: surface.resized encode failed: %v", err)
			continue
		}
		for w := range set {
			e.push(w, msg)
		}
	}
}

// SurfaceUpdate serves surface.update. Only the occupant of p.Surface may
// render to it; a surface the caller does not occupy is denied. Content
// itself is routed to the host renderer: buffers are not State.
func (e *Engine) SurfaceUpdate(plugin string, p protocol.SurfaceUpdateParams) error {
	if plugin == "" {
		return errf(protocol.CodeDenied, "surface.update requires a plugin caller")
	}
	if !p.Surface.Valid() {
		return errf(protocol.CodeInvalidParams, "surface %q is not a host layout surface", p.Surface)
	}
	if occupant := e.slice.Get().Occupants[string(p.Surface)]; occupant != plugin {
		if occupant == "" {
			return errf(protocol.CodeDenied, "surface %q is unoccupied; %s has no claim on it", p.Surface, plugin)
		}
		return errf(protocol.CodeDenied, "surface %q is occupied by %s", p.Surface, occupant)
	}
	return nil
}

// SurfaceClose serves surface.close from the surface's occupant: it releases
// the occupancy. The released surface is free for the next claim to arrive.
func (e *Engine) SurfaceClose(plugin string, p protocol.SurfaceCloseParams) error {
	if plugin == "" {
		return errf(protocol.CodeDenied, "surface.close requires a plugin caller")
	}
	if !p.Surface.Valid() {
		return errf(protocol.CodeInvalidParams, "surface %q is not a host layout surface", p.Surface)
	}
	return e.release(p.Surface, plugin)
}

// CloseSurface is the documented close path the host (Stem caller) uses to
// force-release any occupied surface.
func (e *Engine) CloseSurface(surface protocol.SurfaceID) error {
	if !surface.Valid() {
		return errf(protocol.CodeInvalidParams, "surface %q is not a host layout surface", surface)
	}
	return e.release(surface, "")
}

func (e *Engine) release(surface protocol.SurfaceID, only string) error {
	occupant := e.slice.Get().Occupants[string(surface)]
	if only != "" && occupant != only {
		if occupant == "" {
			return errf(protocol.CodeDenied, "surface %q is unoccupied", surface)
		}
		return errf(protocol.CodeDenied, "surface %q is occupied by %s", surface, occupant)
	}
	return e.slice.Update(func(v *Surfaces) { delete(v.Occupants, string(surface)) })
}

// SlotContribute serves slot.contribute: it validates the namespace rules —
// ids are "<plugin_id>.<name>", on_click names one of the caller's actions —
// refuses unknown slots and the publisher contributing to its own slot, then
// delegates the write to the caller's Pod declaration. The resolution and
// watcher notifications follow from the State listener.
func (e *Engine) SlotContribute(plugin string, p protocol.SlotContributeParams) error {
	if plugin == "" {
		return errf(protocol.CodeDenied, "slot.contribute requires a plugin caller")
	}
	if p.SlotID == "" {
		return errf(protocol.CodeInvalidParams, "slot_id is required")
	}
	if err := ownName(plugin, p.ID); err != nil {
		return err
	}
	if p.OnClick != "" {
		if err := ownName(plugin, p.OnClick); err != nil {
			return err
		}
	}
	publisher, ok := e.pubs.Owner(p.SlotID)
	if !ok {
		return errf(protocol.CodeNotFound, "unknown slot %q", p.SlotID)
	}
	if publisher == plugin {
		return errf(protocol.CodeDenied, "contributions must target another publisher")
	}
	return e.decls.Contribute(plugin, p.SlotID, Contribution{ID: p.ID, Plugin: plugin, Label: p.Content})
}

// ToastShow serves toast.show from a plugin or the CLI (plugin "" attributes
// the toast to the host). The queue is bounded; the oldest toast is dropped
// and counted when the bound is hit.
func (e *Engine) ToastShow(plugin string, p protocol.ToastShowParams) error {
	switch p.Level {
	case LevelInfo, LevelSuccess, LevelWarn, LevelError:
	default:
		return errf(protocol.CodeInvalidParams, "level %q is not a toast level", p.Level)
	}
	e.toasts = append(e.toasts, Toast{
		Plugin: plugin, Level: p.Level, Title: p.Title, Message: p.Message,
		ShownAt: e.opts.Clock.Now(),
	})
	if excess := len(e.toasts) - e.opts.MaxToasts; excess > 0 {
		e.toasts = e.toasts[excess:]
		e.stats.ToastsDropped += uint64(excess)
	}
	return nil
}

// ActiveToast expires stale toasts and returns the oldest live one, or false
// when none remains. Expiry is lazy: it advances with the injected clock and
// runs no timers.
func (e *Engine) ActiveToast() (Toast, bool) {
	now := e.opts.Clock.Now()
	ttl := e.opts.ToastTTL
	for len(e.toasts) > 0 {
		front := e.toasts[0]
		if now.Sub(front.ShownAt) < ttl {
			break
		}
		e.toasts = e.toasts[1:]
		e.stats.ToastsExpired++
	}
	if len(e.toasts) == 0 {
		return Toast{}, false
	}
	front := e.toasts[0]
	return front, true
}

// Stats reports the engine counters.
func (e *Engine) Stats() Stats { return e.stats }
