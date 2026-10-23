package surfaces

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	statessurfaces "github.com/hyperlab-sh/husk/internal/substrate/state/surfaces"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Clock reports the current time. Tests inject a fake; production injects
// time.Now. No timers or sleeps run inside the engine.
type Clock interface {
	Now() time.Time
}

// Liveness reports whether a plugin currently has a live process. The engine
// consults it before pushing notifications: a watcher whose plugin is not
// live is skipped until it returns. Nil treats every plugin as live.
type Liveness interface {
	Live(pluginID string) bool
}

// Publications is the engine's read-only view of the published-slot catalog:
// it maps a published slot id to its owning publisher plugin. The host
// implements it over the manifest catalog; the engine owns no catalog and
// never polls it. The host calls CatalogChanged whenever Owner's answer
// changes — a publisher registered, torn down, or its slots re-declared —
// so a catalog-only change, which is no declaration change, still reaches
// the next pass.
type Publications interface {
	Owner(slotID string) (publisher string, ok bool)
}

// Declarations is the seam through which the engine asks a plugin's Pod to
// change its declaration slices. The Pod holds the plugin's write capability
// (design/state.md invariant 4); the engine never writes another owner's
// slice. The engine calls these methods from its own loop-only methods, so
// implementations run on the loop and must write the declaration directly,
// never post-and-wait: the engine's request would deadlock on its own loop.
type Declarations interface {
	// Contribute upserts entry into plugin's <plugin_id>.contributions
	// declaration for slot.
	Contribute(plugin, slot string, entry Contribution) error

	// Release removes surface from plugin's <plugin_id>.occupants
	// declaration. surface.close and the host's force-close retract the
	// claim at its source, so a freed surface cannot be re-occupied by its
	// own stale declaration.
	Release(plugin string, surface protocol.SurfaceID) error
}

// Options configures the engine. Every seam is required: New rejects a
// nil Clock, Logger, Liveness, Publications or Declarations — a missing
// seam must never silently drop writes or fake liveness. Only the toast
// bounds are optional, with the defaults below.
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
	// DefaultToastTTL is the toast duration 0 selects.
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
	// Refused holds the most recent refusals (at most maxRefused), newest
	// last. A refused claim is a declaration, not a request: it gets no
	// reply code, only these counters, and it resolves on a later pass
	// once the surface frees.
	Refused []RefusedClaim
	// ToastsDropped counts toasts dropped by the queue bound.
	ToastsDropped uint64
	// ToastsExpired counts toasts expired by the TTL.
	ToastsExpired uint64
}

// RefusedClaim is one recorded claim refusal: the claiming plugin and the
// surface that was occupied at the time.
type RefusedClaim struct {
	Surface protocol.SurfaceID
	Plugin  string
	At      time.Time
}

// maxRefused bounds the refused-claim history in Stats.
const maxRefused = 32

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
	claims        Occupants
	contributions Contributions
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
	// liveDirty marks a pass that must re-check occupants and standing
	// claims against liveness: a Pod entered or left `live` (D70).
	liveDirty bool

	// lastRev is the resolution slice's revision as last committed by the
	// engine. A mismatch seen at the next pass means a foreign write.
	lastRev uint64

	// freed tracks surfaces released since the last pass (surface.close,
	// host force-close): a freed surface re-resolves standing claims.
	freed map[protocol.SurfaceID]bool

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
	var missing []string
	if opts.Clock == nil {
		missing = append(missing, "Clock")
	}
	if opts.Logger == nil {
		missing = append(missing, "Logger")
	}
	if opts.Liveness == nil {
		missing = append(missing, "Liveness")
	}
	if opts.Publications == nil {
		missing = append(missing, "Publications")
	}
	if opts.Declarations == nil {
		missing = append(missing, "Declarations")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("surfaces: Options is missing %s", strings.Join(missing, ", "))
	}
	if opts.MaxToasts <= 0 {
		opts.MaxToasts = DefaultMaxToasts
	}
	if opts.ToastTTL <= 0 {
		opts.ToastTTL = DefaultToastTTL
	}
	slice, err := statessurfaces.RegisterSurfaces(st)
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
		freed:          map[protocol.SurfaceID]bool{},
		slotWatchers:   map[string]map[*watcher]struct{}{},
		pluginWatchers: map[string]map[*watcher]struct{}{},
	}
	st.OnFlush(func(changes []state.Change, snap *state.Snapshot) {
		for i := range changes {
			if changes[i].Namespace == state.SurfacesNamespace {
				// The engine's own commits land here too; the next pass
				// re-checks the revision to tell foreign writes from them.
				e.dirty = true
				continue
			}
			if _, _, ok := splitDeclarationNamespace(changes[i].Namespace); ok {
				e.dirty = true
				return
			}
		}
	})
	e.dirty = true
	e.liveDirty = true
	if ent, ok := st.Snapshot().Slices[state.SurfacesNamespace]; ok {
		e.lastRev = ent.Revision
	}
	return e, nil
}

// LivenessChanged marks the engine dirty: a plugin's liveness moved since
// the last pass. A claim holds a surface only while its Pod is live, so the
// next pass vacates the surfaces of occupants that left `live` and hands
// every free surface to its first live claimant in declaration order. The
// engine does not poll Liveness; the host calls this on the loop whenever
// husk.pods changes, like CatalogChanged.
func (e *Engine) LivenessChanged() {
	e.dirty = true
	e.liveDirty = true
}

// CatalogChanged marks the engine dirty: the published-slot catalog
// changed since the last pass. The engine never polls the catalog, and a
// catalog-only change is no declaration change — a publisher torn down
// changes its declarations first, and the catalog's own update may land an
// Update later — so without this call a dropped slot would wait for an
// unrelated pass to notice. The host calls it on the loop, like every
// engine entry point, whenever the catalog changes.
func (e *Engine) CatalogChanged() {
	e.dirty = true
}

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
	liveDirty := e.liveDirty
	e.liveDirty = false
	snap := e.st.Snapshot()

	// A foreign write to the resolution — state.Clear, the only handle-less
	// write, or anything outside the engine — is authoritative: the cached
	// declarations no longer describe the value, so this pass re-resolves
	// every slot from the current declarations instead of diffing against a
	// stale view.
	if rev := snap.Slices[state.SurfacesNamespace].Revision; rev != e.lastRev {
		e.log.Printf("surfaces: husk.surfaces changed outside the engine; re-resolving")
		e.declsCache = map[string]*pluginDecl{}
	}

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
		res.Occupants = Occupants{}
	}
	if res.Contributions == nil {
		res.Contributions = Contributions{}
	}
	if res.Capture == nil {
		res.Capture = map[protocol.SurfaceID]bool{}
	}
	changedSlots := map[string]bool{}
	occupiedBefore := make(map[protocol.SurfaceID]string, len(res.Occupants))
	for surface, occ := range res.Occupants {
		occupiedBefore[surface] = occ.Plugin
	}

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
	// New entries of one flush append in the plugins' pass order. The pass
	// order is the declaration namespace order — key "<plugin_id>"
	// + ".contributions" — not the bare plugin id: two dotted ids such as
	// "git" and "git-x" order by their full namespaces
	// ("git-x.contributions" < "git.contributions"), the order State itself
	// reports changed namespaces in.
	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i]+"."+contributionsSuffix < plugins[j]+"."+contributionsSuffix
	})

	// A claim holds its surface only while its Pod is live: an occupant that
	// left `live` gives the surface up here, and the freed pass below hands
	// it to the next live claimant. Its declaration stays (the Pod owns it).
	if liveDirty {
		for _, surface := range sortedOccupied(res.Occupants) {
			if plugin := res.Occupants[surface].Plugin; !e.live.Live(plugin) {
				delete(res.Occupants, surface)
				e.log.Printf("surfaces: %s left live; %s released", plugin, surface)
			}
		}
	}

	for _, plugin := range plugins {
		e.resolvePlugin(plugin, e.declsCache[plugin], next[plugin], &res, changedSlots)
	}

	// A surface freed since the last pass re-resolves standing claims:
	// claims are declarations, so a claim that arrived while the surface
	// was occupied was recorded and silently refused, and it wins the
	// surface once it frees — closest in the same namespace order.
	freed := make([]protocol.SurfaceID, 0)
	for surface := range e.freed {
		// Freed out-of-band (surface.close, host force-close) since the
		// last pass and not re-occupied by a declaration this pass.
		if res.Occupants[surface].Plugin == "" {
			freed = append(freed, surface)
		}
	}
	for surface, was := range occupiedBefore {
		if was != "" && res.Occupants[surface].Plugin == "" {
			freed = append(freed, surface)
		}
	}
	e.freed = map[protocol.SurfaceID]bool{}
	sort.Slice(freed, func(i, j int) bool { return freed[i] < freed[j] })
	for _, surface := range freed {
		for _, plugin := range plugins {
			d, ok := next[plugin]
			if !ok || d.claims[surface].Plugin != plugin || !e.live.Live(plugin) {
				continue
			}
			res.Occupants[surface] = Occupant{Surface: surface, Plugin: plugin}
			e.log.Printf("surfaces: %s freed; standing claim by %s resolves", surface, plugin)
			break
		}
	}

	// A claim declared before its Pod went live took nothing; when liveness
	// moves, every surface still free goes to its first live claimant in
	// declaration order. An occupant that is live stays: a later claimant
	// never preempts it.
	if liveDirty {
		for _, plugin := range plugins {
			d, ok := next[plugin]
			if !ok || !e.live.Live(plugin) {
				continue
			}
			for _, surface := range sortedClaims(d.claims) {
				if d.claims[surface].Plugin != plugin || res.Occupants[surface].Plugin != "" {
					continue
				}
				res.Occupants[surface] = Occupant{Surface: surface, Plugin: plugin}
				e.log.Printf("surfaces: %s free; live claim by %s resolves", surface, plugin)
			}
		}
	}

	// Capture belongs to one occupant's tenure: an entry survives the pass
	// only if the surface is held by the same non-empty plugin as before it.
	// A changed, vacated or dropped occupant, a hand-over in the same pass
	// and a Pod that left `live` all fail that test (D93).
	for surface := range res.Capture {
		if was := occupiedBefore[surface]; was == "" || res.Occupants[surface].Plugin != was {
			delete(res.Capture, surface)
		}
	}

	// A published slot whose publisher is no longer registered leaves the
	// resolution with all its entries, whatever the contributors declare:
	// the host's catalog drops the slot when the publisher's Pod is torn
	// down, and that teardown's declaration change triggers this pass.
	for slot := range res.Contributions {
		if _, ok := e.pubs.Owner(slot); !ok {
			delete(res.Contributions, slot)
			changedSlots[slot] = true
		}
	}

	if err := e.slice.Update(func(v *Surfaces) { *v = res }); err != nil {
		e.log.Printf("surfaces: husk.surfaces commit failed: %v", err)
	} else {
		e.markRev()
	}

	// Watchers are notified outside State's flush callback: the Notifier is
	// a queued socket write and must not block the loop.
	for slot := range changedSlots {
		e.notifySlot(slot, res)
	}
}

// resolvePlugin folds one plugin's declaration change into res. old or cur
// may be nil: nil old is a first arrival, nil cur is a full removal.
func (e *Engine) resolvePlugin(plugin string, old, cur *pluginDecl, res *Surfaces, changedSlots map[string]bool) {
	var oldClaims, newClaims Occupants
	if old != nil {
		oldClaims = old.claims
	}
	if cur != nil {
		newClaims = cur.claims
	}

	// New claims, surface-sorted so one flush resolves deterministically.
	fresh := make([]protocol.SurfaceID, 0, len(newClaims))
	for surface := range newClaims {
		if _, had := oldClaims[surface]; !had {
			fresh = append(fresh, surface)
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i] < fresh[j] })
	for _, surface := range fresh {
		if occ := newClaims[surface]; occ.Plugin != plugin {
			continue // a claim naming another plugin is not this plugin's
		}
		switch occupant := res.Occupants[surface].Plugin; occupant {
		case "":
			// First live claim wins; a claim from a Pod that is not live
			// stands in its declaration and resolves when the Pod goes live.
			if e.live.Live(plugin) {
				res.Occupants[surface] = Occupant{Surface: surface, Plugin: plugin}
			}
		case plugin:
			// Still ours from a release the Pod has not re-declared; no-op.
		default:
			e.recordRefusal(surface, plugin)
			e.log.Printf("surfaces: %s occupied by %s; claim by %s refused", surface, occupant, plugin)
		}
	}

	// Dropped claims: release only what this plugin holds.
	for surface := range oldClaims {
		if _, still := newClaims[surface]; still {
			continue
		}
		if res.Occupants[surface].Plugin == plugin {
			delete(res.Occupants, surface)
		}
	}

	// Contributions: the engine records the per-slot order. An entry
	// keeps its first-observed position across ticks; a re-declared entry
	// updates its content in place; an entry gone from the declaration
	// leaves the sequence, and observing it again appends anew. New entries
	// of one flush append in this pass's plugin order (namespace order),
	// each group in the plugin's declared array order.
	var oldSlots, newSlots Contributions
	if old != nil {
		oldSlots = old.contributions
	}
	if cur != nil {
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
		newList := newSlots[slot]
		changed := false

		// Entries gone from the declaration leave the sequence.
		kept := res.Contributions[slot][:0:0]
		for _, entry := range res.Contributions[slot] {
			if entry.Plugin == plugin && !hasID(newList, entry.ID) {
				changed = true
				continue
			}
			kept = append(kept, entry)
		}
		res.Contributions[slot] = kept

		// Declared entries in array order: a known id updates the stored
		// entry in place, a newly observed id appends.
		for _, entry := range newList {
			found := false
			for i := range res.Contributions[slot] {
				stored := &res.Contributions[slot][i]
				if stored.Plugin == plugin && stored.ID == entry.ID {
					if *stored != entry {
						*stored = entry // replaced in place
						changed = true
					}
					found = true
					break
				}
			}
			if !found {
				res.Contributions[slot] = append(res.Contributions[slot], entry)
				changed = true
			}
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

// sortedOccupied lists the occupied surfaces in a stable order.
func sortedOccupied(occ Occupants) []protocol.SurfaceID {
	out := make([]protocol.SurfaceID, 0, len(occ))
	for surface := range occ {
		out = append(out, surface)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sortedClaims lists a declaration's claimed surfaces in a stable order.
func sortedClaims(claims Occupants) []protocol.SurfaceID { return sortedOccupied(claims) }

// hasID reports whether id is among entries.
func hasID(entries []Contribution, id string) bool {
	for _, entry := range entries {
		if entry.ID == id {
			return true
		}
	}
	return false
}

// notifySlot pushes one slot.contributions notification, carrying the slot's
// resolved order with each entry's declared content, to its watchers.
func (e *Engine) notifySlot(slot string, res Surfaces) {
	set := e.slotWatchers[slot]
	if len(set) == 0 {
		return
	}
	contribs := make([]protocol.SlotContribution, 0, len(res.Contributions[slot]))
	for _, entry := range res.Contributions[slot] {
		contribs = append(contribs, protocol.SlotContribution{
			SlotContributeParams: protocol.SlotContributeParams{
				SlotID: slot, ID: entry.ID, Content: entry.Label, OnClick: entry.OnClick,
			},
			PluginID: entry.Plugin,
		})
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
		if occupant := res.Occupants[size.Surface].Plugin; occupant != "" {
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
// itself is routed to the host renderer: buffers are not State. A refused
// update sets nothing; an accepted one records p.Capture when present (nil
// leaves the value unchanged, false removes the entry, true sets it).
func (e *Engine) SurfaceUpdate(plugin string, p protocol.SurfaceUpdateParams) error {
	if plugin == "" {
		return errf(protocol.CodeDenied, "surface.update requires a plugin caller")
	}
	if !p.Surface.Valid() {
		return errf(protocol.CodeInvalidParams, "surface %q is not a host layout surface", p.Surface)
	}
	if occupant := e.slice.Get().Occupants[p.Surface].Plugin; occupant != plugin {
		if occupant == "" {
			return errf(protocol.CodeDenied, "surface %q is unoccupied; %s has no claim on it", p.Surface, plugin)
		}
		return errf(protocol.CodeDenied, "surface %q is occupied by %s", p.Surface, occupant)
	}
	if p.Capture == nil || e.slice.Get().Capture[p.Surface] == *p.Capture {
		return nil
	}
	e.absorbForeign()
	if err := e.slice.Update(func(v *Surfaces) {
		if *p.Capture {
			if v.Capture == nil {
				v.Capture = map[protocol.SurfaceID]bool{}
			}
			v.Capture[p.Surface] = true
		} else {
			delete(v.Capture, p.Surface)
		}
	}); err != nil {
		return err
	}
	e.markRev()
	return nil
}

// SurfaceClose serves surface.close from the surface's occupant: it
// releases the occupancy and retracts the claim declaration at its source.
// The released surface is free for the next claim to arrive or resolve.
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
	occupant := e.slice.Get().Occupants[surface].Plugin
	if only != "" && occupant != only {
		if occupant == "" {
			return errf(protocol.CodeDenied, "surface %q is unoccupied", surface)
		}
		return errf(protocol.CodeDenied, "surface %q is occupied by %s", surface, occupant)
	}
	e.absorbForeign()
	if err := e.slice.Update(func(v *Surfaces) {
		delete(v.Occupants, surface)
		delete(v.Capture, surface)
	}); err != nil {
		return err
	}
	e.markRev()
	if occupant == "" {
		return nil
	}
	// Retract the claim at its source: the occupant's own declaration no
	// longer names the surface, so a later full pass cannot re-resolve the
	// released occupancy from the stale declaration. The write runs on
	// this loop, and the surface re-resolves standing claims next pass.
	// A failed retraction leaves the claim declaration standing: the
	// surface is not marked freed, so no re-award runs off a release that
	// did not complete — the claim re-resolves when a later pass sees it.
	if err := e.decls.Release(occupant, surface); err != nil {
		e.log.Printf("surfaces: retracting %s's claim declaration failed: %v", occupant, err)
		return nil
	}
	e.freed[surface] = true
	return nil
}

// absorbForeign handles a revision mismatch before an out-of-pass commit.
// A mismatch means a foreign write landed between the engine's last commit
// and this one — state.clear in the same Update. markRev after the commit
// would absorb it and Flush's guard would never fire, so clear the
// declarations cache here: the next pass treats every plugin as changed and
// re-resolves from the live declarations, the same response Flush's own
// guard has.
func (e *Engine) absorbForeign() {
	if ent, ok := e.st.Snapshot().Slices[state.SurfacesNamespace]; ok && ent.Revision != e.lastRev {
		e.log.Printf("surfaces: husk.surfaces changed outside the engine; re-resolving")
		e.declsCache = map[string]*pluginDecl{}
	}
}

// UpdateView applies fn to the frontend-owned half of husk.surfaces
// (visibility, placement, overlays) and commits it; occupants and
// contributions stay the engine's. Visible entries must be host layout
// surfaces: an invalid result is refused and nothing commits. Loop-only.
func (e *Engine) UpdateView(fn func(*contracts.SurfaceView)) error {
	cur := e.slice.Get()
	view := contracts.SurfaceView{Visible: slices.Clone(cur.Visible), Placement: cur.Placement, Overlays: cur.Overlays}
	fn(&view)
	for _, id := range view.Visible {
		if !id.Valid() {
			return errf(protocol.CodeInvalidParams, "visible surface %q is not a host layout surface", id)
		}
	}
	e.absorbForeign()
	if err := e.slice.Update(func(v *Surfaces) {
		v.Visible, v.Placement, v.Overlays = view.Visible, view.Placement, view.Overlays
	}); err != nil {
		return err
	}
	e.markRev()
	return nil
}

// markRev records the resolution slice's current revision as the engine's
// own, after a successful engine commit; a later mismatch is a foreign write.
func (e *Engine) markRev() {
	if ent, ok := e.st.Snapshot().Slices[state.SurfacesNamespace]; ok {
		e.lastRev = ent.Revision
	}
}

// recordRefusal keeps the bounded refusal history beside the counter: a
// refused claim gets no reply, only this record, and it resolves on a later
// pass once the surface frees.
func (e *Engine) recordRefusal(surface protocol.SurfaceID, plugin string) {
	e.stats.RefusedClaims++
	e.stats.Refused = append(e.stats.Refused, RefusedClaim{
		Surface: surface, Plugin: plugin, At: e.opts.Clock.Now(),
	})
	if excess := len(e.stats.Refused) - maxRefused; excess > 0 {
		e.stats.Refused = e.stats.Refused[excess:]
	}
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
	return e.decls.Contribute(plugin, p.SlotID, Contribution{
		Slot: p.SlotID, ID: p.ID, Plugin: plugin, Label: p.Content, OnClick: p.OnClick,
	})
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
