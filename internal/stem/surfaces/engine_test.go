package surfaces

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	statessurfaces "github.com/hyperlab-sh/husk/internal/substrate/state/surfaces"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// fakeClock advances only when the test moves it.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// liveness is a pluggable Live view.
type liveness struct{ dead map[string]bool }

func (l liveness) Live(plugin string) bool { return !l.dead[plugin] }

// publications is a pluggable slot catalog.
type publications struct{ owners map[string]string }

func (p publications) Owner(slot string) (string, bool) {
	owner, ok := p.owners[slot]
	return owner, ok
}

// sink captures notifications for assertions.
type sink struct {
	msgs []protocol.Message
}

func (s *sink) Notify(m protocol.Message) error {
	s.msgs = append(s.msgs, m)
	return nil
}

func (s *sink) slotMsgs(t *testing.T) []protocol.SlotContributionsParams {
	t.Helper()
	var out []protocol.SlotContributionsParams
	for _, m := range s.msgs {
		if m.Method != protocol.MethodSlotContributions {
			t.Fatalf("unexpected method %q", m.Method)
		}
		var p protocol.SlotContributionsParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Fatalf("decode slot.contributions: %v", err)
		}
		out = append(out, p)
	}
	return out
}

func (s *sink) resizedMsgs(t *testing.T) []protocol.SurfaceResizedParams {
	t.Helper()
	var out []protocol.SurfaceResizedParams
	for _, m := range s.msgs {
		if m.Method != protocol.MethodSurfaceResized {
			t.Fatalf("unexpected method %q", m.Method)
		}
		var p protocol.SurfaceResizedParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Fatalf("decode surface.resized: %v", err)
		}
		out = append(out, p)
	}
	return out
}

// pod stands in for a plugin's Pod: it owns the plugin's declaration slices
// through the same typed wrappers a Pod uses, and implements the
// Declarations seam.
type pod struct {
	t       *testing.T
	st      *state.State
	plugins map[string]*podDecl
}

type podDecl struct {
	occ *statessurfaces.OccupantsSlice
	con *statessurfaces.ContributionsSlice
}

func newPod(t *testing.T, st *state.State) *pod {
	return &pod{t: t, st: st, plugins: map[string]*podDecl{}}
}

func (p *pod) decl(plugin string) *podDecl {
	p.t.Helper()
	d, ok := p.plugins[plugin]
	if !ok {
		p.t.Fatalf("pod for %q not launched", plugin)
	}
	return d
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// launch registers both declaration slices, as the Pod does for a live plugin.
func (p *pod) launch(plugin string) {
	p.t.Helper()
	d := &podDecl{}
	var err error
	d.occ, err = statessurfaces.RegisterOccupants(p.st, plugin)
	if err != nil {
		p.t.Fatalf("register %s.occupants: %v", plugin, err)
	}
	d.con, err = statessurfaces.RegisterContributions(p.st, plugin)
	if err != nil {
		p.t.Fatalf("register %s.contributions: %v", plugin, err)
	}
	p.plugins[plugin] = d
}

func (p *pod) claim(plugin, surface string) {
	p.t.Helper()
	d := p.decl(plugin)
	claims := d.occ.Get()
	id := protocol.SurfaceID(surface)
	claims[id] = Occupant{Surface: id, Plugin: plugin}
	if err := d.occ.Set(claims); err != nil {
		p.t.Fatalf("claim %s: %v", surface, err)
	}
}

func (p *pod) unclaim(plugin, surface string) {
	p.t.Helper()
	d := p.decl(plugin)
	claims := d.occ.Get()
	delete(claims, protocol.SurfaceID(surface))
	if err := d.occ.Set(claims); err != nil {
		p.t.Fatalf("unclaim %s: %v", surface, err)
	}
}

// Contribute implements the Declarations seam: a typed upsert by id.
func (p *pod) Contribute(plugin, slot string, entry Contribution) error {
	return p.decl(plugin).con.Upsert(slot, entry)
}

// Release implements the Declarations seam: it removes the surface from the
// plugin's occupants declaration.
func (p *pod) Release(plugin string, surface protocol.SurfaceID) error {
	claims := p.decl(plugin).occ.Get()
	delete(claims, surface)
	return p.decl(plugin).occ.Set(claims)
}

// declare overwrites one slot's entry list, as a manifest declaration would.
func (p *pod) declare(plugin, slot string, entries ...Contribution) {
	p.t.Helper()
	d := p.decl(plugin)
	contribs := d.con.Get()
	for i := range entries {
		entries[i].Slot = slot
	}
	contribs[slot] = entries
	if err := d.con.Set(contribs); err != nil {
		p.t.Fatalf("declare: %v", err)
	}
}

// teardown unregisters both slices, as Pod cleanup does on owner death.
func (p *pod) teardown(plugin string) {
	p.t.Helper()
	d := p.decl(plugin)
	d.occ.Unregister()
	d.con.Unregister()
}

// settle runs one loop turn: State flush publishes declaration changes, then
// the engine resolves and notifies.
func settle(st *state.State, eng *Engine) {
	st.Flush()
	eng.Flush()
}

func resolution(t *testing.T, st *state.State) Surfaces {
	t.Helper()
	entry, ok := st.Get(state.SurfacesNamespace)
	if !ok {
		t.Fatal("husk.surfaces not registered")
	}
	var res Surfaces
	if err := json.Unmarshal(entry.Value, &res); err != nil {
		t.Fatalf("decode husk.surfaces: %v", err)
	}
	return res
}

func entryIDs(entries []Contribution) []string {
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	return ids
}

func codeOf(t *testing.T, err error) protocol.Code {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	var e *Error
	if !errorAs(err, &e) {
		t.Fatalf("error %v is not *surfaces.Error", err)
	}
	return e.Code
}

func errorAs(err error, target **Error) bool {
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
}

func newEngine(t *testing.T, opts Options) (*state.State, *Engine, *pod) {
	t.Helper()
	st := state.New(state.Options{})
	pods := newPod(t, st)
	if opts.Declarations == nil {
		opts.Declarations = pods // the pod stand-in serves the seam by default
	}
	eng, err := New(st, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return st, eng, pods
}

func TestOccupancyFirstWinsAndConflictRefused(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	settle(st, eng)

	pods.launch("beta.notes")
	pods.claim("beta.notes", string(protocol.SurfaceMainStage))
	settle(st, eng)

	res := resolution(t, st)
	if got := res.Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("occupant = %q, want alpha.files (first claim wins)", got)
	}
	if got := eng.Stats().RefusedClaims; got != 1 {
		t.Fatalf("refused claims = %d, want 1", got)
	}

	// The refused claimant may not render; the occupant may.
	err := eng.SurfaceUpdate("beta.notes", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage})
	if code := codeOf(t, err); code != protocol.CodeDenied {
		t.Fatalf("refused claimant update code = %v, want denied", code)
	}
	if err := eng.SurfaceUpdate("alpha.files", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage}); err != nil {
		t.Fatalf("occupant update: %v", err)
	}
}

func TestSurfaceUpdateValidation(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceSidebarList))
	settle(st, eng)

	tests := []struct {
		name   string
		plugin string
		params protocol.SurfaceUpdateParams
		want   protocol.Code
	}{
		{"invalid surface", "alpha.files", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceID("nope")}, protocol.CodeInvalidParams},
		{"no plugin identity", "", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceSidebarList}, protocol.CodeDenied},
		{"foreign unoccupied surface", "alpha.files", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceSidePanel}, protocol.CodeDenied},
		{"unclaimed plugin", "gamma.other", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceSidebarList}, protocol.CodeDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := codeOf(t, eng.SurfaceUpdate(tt.plugin, tt.params)); code != tt.want {
				t.Fatalf("code = %v, want %v", code, tt.want)
			}
		})
	}
}

func TestReleaseOnCloseAndReclaim(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceModal))
	settle(st, eng)

	// A non-occupant cannot close.
	pods.launch("beta.notes")
	if code := codeOf(t, eng.SurfaceClose("beta.notes", protocol.SurfaceCloseParams{Surface: protocol.SurfaceModal})); code != protocol.CodeDenied {
		t.Fatalf("non-occupant close code = %v, want denied", code)
	}

	// The occupant releases through the documented close path.
	if err := eng.SurfaceClose("alpha.files", protocol.SurfaceCloseParams{Surface: protocol.SurfaceModal}); err != nil {
		t.Fatalf("occupant close: %v", err)
	}
	if _, occupied := resolution(t, st).Occupants[protocol.SurfaceModal]; occupied {
		t.Fatal("occupancy survived close")
	}

	// The released surface is free for the next arriving claim.
	pods.claim("beta.notes", string(protocol.SurfaceModal))
	settle(st, eng)
	if got := resolution(t, st).Occupants[protocol.SurfaceModal].Plugin; got != "beta.notes" {
		t.Fatalf("post-release occupant = %q, want beta.notes", got)
	}

	// The host force-releases through the same documented path.
	if err := eng.CloseSurface(protocol.SurfaceModal); err != nil {
		t.Fatalf("host close: %v", err)
	}
	if _, occupied := resolution(t, st).Occupants[protocol.SurfaceModal]; occupied {
		t.Fatal("occupancy survived host close")
	}
	settle(st, eng)

	// The close retracted the claim at its source: the occupant's
	// declaration no longer names the surface, and re-declaring the claim
	// is a fresh claim on a free surface.
	pods.claim("beta.notes", string(protocol.SurfaceModal))
	settle(st, eng)
	if got := resolution(t, st).Occupants[protocol.SurfaceModal].Plugin; got != "beta.notes" {
		t.Fatalf("post-host-close occupant = %q, want beta.notes", got)
	}
}

func TestCloseRetractsClaimDeclaration(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	settle(st, eng)

	// The occupant closes; the claim declaration is retracted at its source.
	if err := eng.SurfaceClose("alpha.files", protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage}); err != nil {
		t.Fatalf("close: %v", err)
	}
	settle(st, eng)

	if got := pods.decl("alpha.files").occ.Get()[protocol.SurfaceMainStage]; got.Plugin != "" {
		t.Fatalf("declaration still claims: %+v", got)
	}
	if _, occupied := resolution(t, st).Occupants[protocol.SurfaceMainStage]; occupied {
		t.Fatal("occupancy survived close")
	}

	// A full re-resolution cannot resurrect the released occupancy from a
	// stale declaration: the declaration no longer names the surface, and
	// the same plugin re-claiming is a fresh claim that resolves.
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	settle(st, eng)
	if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("re-claim occupant = %q, want alpha.files", got)
	}
}

func TestRefusedClaimResolvesWhenSurfaceFrees(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	pods.launch("alpha.files")
	pods.launch("beta.notes")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	settle(st, eng)

	// beta's claim arrives while the surface is occupied: it is a
	// declaration, refused silently — no reply code exists for it — and
	// only counted and recorded.
	pods.claim("beta.notes", string(protocol.SurfaceMainStage))
	settle(st, eng)

	stats := eng.Stats()
	if stats.RefusedClaims != 1 {
		t.Fatalf("refused claims = %d, want 1", stats.RefusedClaims)
	}
	if len(stats.Refused) != 1 || stats.Refused[0].Surface != protocol.SurfaceMainStage || stats.Refused[0].Plugin != "beta.notes" {
		t.Fatalf("refused history = %+v", stats.Refused)
	}
	if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("refused claim displaced the occupant: %q", got)
	}

	// The occupant closes; the standing refused claim resolves on the
	// next pass, with no new declaration from beta.
	if err := eng.SurfaceClose("alpha.files", protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage}); err != nil {
		t.Fatalf("close: %v", err)
	}
	settle(st, eng)

	if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != "beta.notes" {
		t.Fatalf("post-free occupant = %q, want beta.notes (standing claim resolves)", got)
	}
	if got := pods.decl("beta.notes").occ.Get()[protocol.SurfaceMainStage].Plugin; got != "beta.notes" {
		t.Fatalf("beta declaration = %+v", got)
	}
}

func TestReleaseOnOwnerDeathViaOwnerCleanup(t *testing.T) {
	st, eng, pods := newEngine(t, Options{Publications: publications{owners: map[string]string{"publisher.list": "publisher"}}})
	eng.WatchSlot("publisher", "publisher.list", &sink{})

	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	pods.launch("beta.notes")
	pods.claim("beta.notes", string(protocol.SurfaceSidebarList))
	pods.declare("alpha.files", "publisher.list", Contribution{ID: "alpha.files.item", Plugin: "alpha.files", Label: "one"})
	settle(st, eng)

	res := resolution(t, st)
	if got := res.Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("occupant = %q", got)
	}
	if got := entryIDs(res.Contributions["publisher.list"]); len(got) != 1 || got[0] != "alpha.files.item" {
		t.Fatalf("contributions = %v", got)
	}

	// Pod teardown unregisters the dead owner's declarations (the State
	// owner-cleanup path); resolution drops its entries, declarations are gone.
	pods.teardown("alpha.files")
	settle(st, eng)

	res = resolution(t, st)
	if _, occupied := res.Occupants[protocol.SurfaceMainStage]; occupied {
		t.Fatal("dead owner still occupies")
	}
	if _, has := res.Contributions["publisher.list"]; has {
		t.Fatal("dead owner's contribution survived")
	}
	if got := res.Occupants[protocol.SurfaceSidebarList].Plugin; got != "beta.notes" {
		t.Fatalf("survivor lost its surface: %q", got)
	}

	// On restart the plugin's Pod re-registers its declarations; they resolve again.
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	pods.declare("alpha.files", "publisher.list", Contribution{ID: "alpha.files.item", Plugin: "alpha.files", Label: "one"})
	settle(st, eng)

	res = resolution(t, st)
	if got := res.Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("re-registered occupant = %q", got)
	}
	if got := entryIDs(res.Contributions["publisher.list"]); len(got) != 1 || got[0] != "alpha.files.item" {
		t.Fatalf("re-registered contributions = %v", got)
	}
}

func TestSlotVanishesWhenPublisherLeaves(t *testing.T) {
	pubs := publications{owners: map[string]string{"publisher.toolbar": "publisher"}}
	st, eng, pods := newEngine(t, Options{Publications: pubs})
	seen := &sink{}
	eng.WatchSlot("contributor", "publisher.toolbar", seen)

	pods.launch("publisher")
	pods.launch("alpha.files")
	pods.declare("alpha.files", "publisher.toolbar",
		Contribution{ID: "alpha.files.tool", Plugin: "alpha.files", Label: "one"})
	settle(st, eng)

	res := resolution(t, st)
	if got := entryIDs(res.Contributions["publisher.toolbar"]); len(got) != 1 || got[0] != "alpha.files.tool" {
		t.Fatalf("contributions = %v", got)
	}

	// The publisher dies: its Pod tears down its declarations and the
	// host's catalog drops the slot; the contributors' entries vanish with
	// it, whatever they keep declaring.
	pods.teardown("publisher")
	delete(pubs.owners, "publisher.toolbar")
	settle(st, eng)

	res = resolution(t, st)
	if _, has := res.Contributions["publisher.toolbar"]; has {
		t.Fatalf("slot survived its publisher: %v", res.Contributions["publisher.toolbar"])
	}
	msgs := seen.slotMsgs(t)
	if len(msgs) != 2 {
		t.Fatalf("watcher notifications = %d, want 2", len(msgs))
	}
	if len(msgs[1].Contributions) != 0 {
		t.Fatalf("final notification = %v, want an emptied slot", msgs[1].Contributions)
	}
}

func TestContributionOrderAndReplace(t *testing.T) {
	st, eng, pods := newEngine(t, Options{Publications: publications{owners: map[string]string{"publisher.list": "publisher"}}})
	watching := &sink{}
	other := &sink{}
	eng.WatchSlot("publisher", "publisher.list", watching)
	eng.WatchSlot("publisher", "publisher.other", other)

	pods.launch("alpha.files")
	pods.launch("beta.notes")
	pods.declare("alpha.files", "publisher.list",
		Contribution{ID: "alpha.files.a1", Plugin: "alpha.files", Label: "first"})
	settle(st, eng)
	pods.declare("beta.notes", "publisher.list",
		Contribution{ID: "beta.notes.b1", Plugin: "beta.notes", Label: "second"})
	settle(st, eng)
	pods.declare("alpha.files", "publisher.list",
		Contribution{ID: "alpha.files.a1", Plugin: "alpha.files", Label: "first"},
		Contribution{ID: "alpha.files.a2", Plugin: "alpha.files", Label: "third"})
	settle(st, eng)

	res := resolution(t, st)
	want := []string{"alpha.files.a1", "beta.notes.b1", "alpha.files.a2"}
	if got := entryIDs(res.Contributions["publisher.list"]); !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v (arrival order across plugins)", got, want)
	}

	// Replace in place: the same id re-declared keeps its position and the
	// resolution carries the new content.
	pods.declare("alpha.files", "publisher.list",
		Contribution{ID: "alpha.files.a2", Plugin: "alpha.files", Label: "third!"},
		Contribution{ID: "alpha.files.a1", Plugin: "alpha.files", Label: "first"})
	settle(st, eng)
	res = resolution(t, st)
	if got := entryIDs(res.Contributions["publisher.list"]); !equalStrings(got, want) {
		t.Fatalf("replace-in-place reordered: %v, want %v", got, want)
	}
	if got := res.Contributions["publisher.list"][2].Label; got != "third!" {
		t.Fatalf("replaced label = %q, want third!", got)
	}

	// Only the changed slot's watchers were notified.
	msgs := watching.slotMsgs(t)
	if len(msgs) == 0 {
		t.Fatal("watcher never notified")
	}
	last := msgs[len(msgs)-1]
	if last.SlotID != "publisher.list" {
		t.Fatalf("slot = %q", last.SlotID)
	}
	if len(last.Contributions) != 3 || last.Contributions[2].Content != "third!" {
		t.Fatalf("notification content = %+v", last.Contributions)
	}
	if len(other.msgs) != 0 {
		t.Fatalf("unrelated slot watcher notified: %d", len(other.msgs))
	}
}

func TestContributionOrderWithinOneFlush(t *testing.T) {
	st, eng, pods := newEngine(t, Options{Publications: publications{owners: map[string]string{"publisher.list": "publisher"}}})
	pods.launch("beta.notes")
	pods.launch("alpha.files")

	// Both plugins declare new entries between flushes: within the one
	// tick, new entries order by namespace (alpha before beta), each group
	// in its declared array order — the declaration lists carry no
	// cross-plugin order.
	pods.declare("beta.notes", "publisher.list",
		Contribution{ID: "beta.notes.b1", Plugin: "beta.notes", Label: "b1"},
		Contribution{ID: "beta.notes.b2", Plugin: "beta.notes", Label: "b2"})
	pods.declare("alpha.files", "publisher.list",
		Contribution{ID: "alpha.files.a1", Plugin: "alpha.files", Label: "a1"})
	settle(st, eng)

	want := []string{"alpha.files.a1", "beta.notes.b1", "beta.notes.b2"}
	if got := entryIDs(resolution(t, st).Contributions["publisher.list"]); !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v (namespace order within one tick)", got, want)
	}
}

func TestWithinTickOrderIsDeclarationNamespaceOrder(t *testing.T) {
	st, eng, pods := newEngine(t, Options{Publications: publications{owners: map[string]string{"publisher.list": "publisher"}}})
	pods.launch("git")
	pods.launch("git-x")
	pods.launch("a")
	pods.launch("a.b")

	// "git" < "git-x" as plugin ids, but "git-x.contributions" <
	// "git.contributions" as namespaces ('-' sorts before '.'): the pass
	// keys on the declaration namespace, not the bare id.
	pods.declare("git", "publisher.list",
		Contribution{ID: "git.one", Plugin: "git", Label: "one"})
	pods.declare("git-x", "publisher.list",
		Contribution{ID: "git-x.one", Plugin: "git-x", Label: "one"})
	settle(st, eng)

	if got := entryIDs(resolution(t, st).Contributions["publisher.list"]); !equalStrings(got, []string{"git-x.one", "git.one"}) {
		t.Fatalf("git order = %v, want git-x before git (namespace order)", got)
	}

	// Same for dotted ids: "a.b.contributions" < "a.contributions".
	pods.declare("a", "publisher.list",
		Contribution{ID: "a.one", Plugin: "a", Label: "one"})
	pods.declare("a.b", "publisher.list",
		Contribution{ID: "a.b.one", Plugin: "a.b", Label: "one"})
	settle(st, eng)

	// The earlier tick's entries keep their first-observed positions; the
	// new pair appends in its own namespace order (a.b before a).
	want := []string{"git-x.one", "git.one", "a.b.one", "a.one"}
	if got := entryIDs(resolution(t, st).Contributions["publisher.list"]); !equalStrings(got, want) {
		t.Fatalf("dotted order = %v, want %v (a.b before a in its tick)", got, want)
	}
}

func TestRemovedEntryLeavesSequence(t *testing.T) {
	st, eng, pods := newEngine(t, Options{Publications: publications{owners: map[string]string{"publisher.list": "publisher"}}})
	pods.launch("alpha.files")
	pods.launch("beta.notes")
	pods.declare("alpha.files", "publisher.list",
		Contribution{ID: "alpha.files.a1", Plugin: "alpha.files", Label: "one"})
	pods.declare("beta.notes", "publisher.list",
		Contribution{ID: "beta.notes.b1", Plugin: "beta.notes", Label: "two"})
	settle(st, eng)
	if got := entryIDs(resolution(t, st).Contributions["publisher.list"]); !equalStrings(got, []string{"alpha.files.a1", "beta.notes.b1"}) {
		t.Fatalf("initial order = %v", got)
	}

	// A removed entry leaves the sequence.
	pods.declare("alpha.files", "publisher.list")
	settle(st, eng)
	if got := entryIDs(resolution(t, st).Contributions["publisher.list"]); !equalStrings(got, []string{"beta.notes.b1"}) {
		t.Fatalf("after removal = %v", got)
	}

	// Observing it again appends anew — first-observed order, not the old
	// position.
	pods.declare("alpha.files", "publisher.list",
		Contribution{ID: "alpha.files.a1", Plugin: "alpha.files", Label: "one"})
	settle(st, eng)
	if got := entryIDs(resolution(t, st).Contributions["publisher.list"]); !equalStrings(got, []string{"beta.notes.b1", "alpha.files.a1"}) {
		t.Fatalf("after re-add = %v, want re-observed at the end", got)
	}
}

func TestSlotContribute(t *testing.T) {
	st := state.New(state.Options{})
	pods := newPod(t, st)
	eng, err := New(st, Options{
		Publications: publications{owners: map[string]string{"publisher.list": "publisher"}},
		Declarations: pods,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	watching := &sink{}
	eng.WatchSlot("publisher", "publisher.list", watching)

	pods.launch("alpha.files")
	settle(st, eng)

	// Refusals are coded replies.
	if code := codeOf(t, eng.SlotContribute("alpha.files", protocol.SlotContributeParams{SlotID: "missing.slot", ID: "alpha.files.x"})); code != protocol.CodeNotFound {
		t.Fatal("unknown slot: want not_found")
	}
	if code := codeOf(t, eng.SlotContribute("publisher", protocol.SlotContributeParams{SlotID: "publisher.list", ID: "publisher.x"})); code != protocol.CodeDenied {
		t.Fatal("own slot: want denied")
	}
	if code := codeOf(t, eng.SlotContribute("alpha.files", protocol.SlotContributeParams{SlotID: "publisher.list", ID: "beta.notes.x"})); code != protocol.CodeDenied {
		t.Fatal("foreign id: want denied")
	}
	if code := codeOf(t, eng.SlotContribute("alpha.files", protocol.SlotContributeParams{SlotID: "publisher.list", ID: "sloppy"})); code != protocol.CodeInvalidParams {
		t.Fatal("malformed id: want invalid_params")
	}
	if code := codeOf(t, eng.SlotContribute("alpha.files", protocol.SlotContributeParams{SlotID: "publisher.list", ID: "alpha.files.x", OnClick: "beta.notes.act"})); code != protocol.CodeDenied {
		t.Fatal("foreign on_click: want denied")
	}
	if code := codeOf(t, eng.SlotContribute("alpha.files", protocol.SlotContributeParams{ID: "alpha.files.x"})); code != protocol.CodeInvalidParams {
		t.Fatal("missing slot: want invalid_params")
	}
	if len(watching.msgs) != 0 {
		t.Fatal("refusals notified the watcher")
	}

	// The accepted request delegates to the Pod's declaration write; the
	// resolution and notification follow from the State listener.
	err = eng.SlotContribute("alpha.files", protocol.SlotContributeParams{
		SlotID: "publisher.list", ID: "alpha.files.item", Content: "hello", OnClick: "alpha.files.open",
	})
	if err != nil {
		t.Fatalf("contribute: %v", err)
	}
	settle(st, eng)

	res := resolution(t, st)
	if got := entryIDs(res.Contributions["publisher.list"]); !equalStrings(got, []string{"alpha.files.item"}) {
		t.Fatalf("contributions = %v", got)
	}
	msgs := watching.slotMsgs(t)
	if len(msgs) != 1 {
		t.Fatalf("notifications = %d, want 1", len(msgs))
	}
	c := msgs[0].Contributions[0]
	if c.PluginID != "alpha.files" || c.ID != "alpha.files.item" || c.Content != "hello" {
		t.Fatalf("notification entry = %+v", c)
	}

	// A second contribution with the same id replaces the entry in place:
	// the declaration upserts and the resolution keeps the position.
	err = eng.SlotContribute("alpha.files", protocol.SlotContributeParams{
		SlotID: "publisher.list", ID: "alpha.files.item", Content: "second",
	})
	if err != nil {
		t.Fatalf("re-contribute: %v", err)
	}
	if err := eng.SlotContribute("alpha.files", protocol.SlotContributeParams{
		SlotID: "publisher.list", ID: "alpha.files.tail", Content: "third",
	}); err != nil {
		t.Fatalf("second entry contribute: %v", err)
	}
	settle(st, eng)

	res = resolution(t, st)
	list := res.Contributions["publisher.list"]
	if got := entryIDs(list); !equalStrings(got, []string{"alpha.files.item", "alpha.files.tail"}) {
		t.Fatalf("after replace, contributions = %v", got)
	}
	if list[0].Label != "second" {
		t.Fatalf("replaced label = %q, want %q", list[0].Label, "second")
	}
}

func TestToastBoundAndTTL(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	st, eng, _ := newEngine(t, Options{Clock: clock, MaxToasts: 2})
	_ = st

	show := func(level, title string) error {
		return eng.ToastShow("alpha.files", protocol.ToastShowParams{Level: level, Title: title, Message: "m"})
	}
	if code := codeOf(t, show("loud", "x")); code != protocol.CodeInvalidParams {
		t.Fatal("invalid level: want invalid_params")
	}
	if err := show(LevelWarn, "one"); err != nil {
		t.Fatalf("show one: %v", err)
	}
	if err := show(LevelError, "two"); err != nil {
		t.Fatalf("show two: %v", err)
	}
	if err := show(LevelInfo, "three"); err != nil {
		t.Fatalf("show three: %v", err)
	}
	if got := eng.Stats().ToastsDropped; got != 1 {
		t.Fatalf("dropped = %d, want 1 (oldest dropped at the bound)", got)
	}
	active, ok := eng.ActiveToast()
	if !ok || active.Title != "two" {
		t.Fatalf("active = %+v ok=%v, want the second toast", active, ok)
	}

	// TTL expiry is lazy and driven only by the injected clock.
	clock.advance(DefaultToastTTL)
	if _, ok := eng.ActiveToast(); ok {
		t.Fatal("toast survived its TTL")
	}
	if _, ok := eng.ActiveToast(); ok {
		t.Fatal("queue not drained")
	}
	if got := eng.Stats().ToastsExpired; got != 2 {
		t.Fatalf("expired = %d, want 2", got)
	}
}

func TestResizedReachesOnlyOccupants(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	pods.launch("beta.notes")
	settle(st, eng)

	alpha, beta := &sink{}, &sink{}
	eng.WatchPlugin("alpha.files", alpha)
	eng.WatchPlugin("beta.notes", beta)

	eng.Resized([]protocol.SurfaceSize{
		{Surface: protocol.SurfaceMainStage, Width: 80, Height: 24},
		{Surface: protocol.SurfaceSidebarList, Width: 30, Height: 24},
	})

	msgs := alpha.resizedMsgs(t)
	if len(msgs) != 1 || len(msgs[0].Surfaces) != 1 || msgs[0].Surfaces[0].Surface != protocol.SurfaceMainStage {
		t.Fatalf("alpha got %+v, want only main-stage", msgs)
	}
	if len(beta.msgs) != 0 {
		t.Fatalf("non-occupant received geometry: %+v", beta.msgs)
	}
}

func TestWatcherLivenessAndCancel(t *testing.T) {
	st, eng, pods := newEngine(t, Options{Liveness: liveness{dead: map[string]bool{"beta.notes": true}}})
	liveWatcher, deadWatcher, canceled := &sink{}, &sink{}, &sink{}
	cancel := eng.WatchSlot("alpha.files", "publisher.list", canceled)
	eng.WatchSlot("beta.notes", "publisher.list", deadWatcher)
	eng.WatchSlot("alpha.files", "publisher.list", liveWatcher)
	cancel()

	pods.launch("alpha.files")
	pods.declare("alpha.files", "publisher.list", Contribution{ID: "alpha.files.item", Plugin: "alpha.files", Label: "one"})
	settle(st, eng)

	if len(deadWatcher.msgs) != 0 {
		t.Fatal("dead plugin watcher notified")
	}
	if len(canceled.msgs) != 0 {
		t.Fatal("canceled watcher notified")
	}
	if len(liveWatcher.msgs) != 1 {
		t.Fatalf("live watcher notifications = %d, want 1", len(liveWatcher.msgs))
	}
}

func TestInitialSweepResolvesPreRegisteredDeclarations(t *testing.T) {
	st := state.New(state.Options{})
	pods := newPod(t, st)
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	st.Flush()

	eng, err := New(st, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	eng.Flush()

	if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("occupant = %q, want alpha.files (initial sweep)", got)
	}
}

func TestNotifierUsesContractsFakes(t *testing.T) {
	rec := fakes.NewRecorder()
	st, eng, pods := newEngine(t, Options{})
	eng.WatchSlot("publisher", "publisher.list", fakes.NewNotifier(rec))

	pods.launch("alpha.files")
	pods.declare("alpha.files", "publisher.list", Contribution{ID: "alpha.files.item", Plugin: "alpha.files", Label: "one"})
	settle(st, eng)

	found := false
	for _, call := range rec.Calls() {
		if call.Method == "Notifier.Notify" && len(call.Args) == 1 {
			if m, ok := call.Args[0].(string); ok && m == protocol.MethodSlotContributions {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("fakes.Notifier never recorded slot.contributions")
	}
}

func TestDroppedClaimReleasesOccupancy(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceSidebarFooter))
	settle(st, eng)

	pods.unclaim("alpha.files", string(protocol.SurfaceSidebarFooter))
	settle(st, eng)

	if _, occupied := resolution(t, st).Occupants[protocol.SurfaceSidebarFooter]; occupied {
		t.Fatal("occupancy survived a dropped claim")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
