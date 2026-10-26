package surfaces

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
	statessurfaces "github.com/hyperlab-sh/husk/pkg/substrate/state/surfaces"
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

// testLogger routes engine diagnostics to the test log.
type testLogger struct{ t *testing.T }

func (l testLogger) Printf(format string, v ...any) { l.t.Logf(format, v...) }

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
	if opts.Clock == nil {
		opts.Clock = &fakeClock{now: time.Unix(0, 0)}
	}
	if opts.Logger == nil {
		opts.Logger = testLogger{t: t}
	}
	if opts.Liveness == nil {
		opts.Liveness = liveness{}
	}
	if opts.Publications == nil {
		opts.Publications = publications{owners: map[string]string{}}
	}
	if opts.Declarations == nil {
		opts.Declarations = pods // the pod stand-in serves the seam by default
	}
	eng, err := New(st, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return st, eng, pods
}

func TestNewRejectsMissingSeams(t *testing.T) {
	st := state.New(state.Options{})
	pods := newPod(t, st)
	full := Options{
		Clock:        &fakeClock{now: time.Unix(0, 0)},
		Logger:       testLogger{t: t},
		Liveness:     liveness{},
		Publications: publications{owners: map[string]string{}},
		Declarations: pods,
	}
	for _, seam := range []string{"Clock", "Logger", "Liveness", "Publications", "Declarations"} {
		opts := full
		switch seam {
		case "Clock":
			opts.Clock = nil
		case "Logger":
			opts.Logger = nil
		case "Liveness":
			opts.Liveness = nil
		case "Publications":
			opts.Publications = nil
		case "Declarations":
			opts.Declarations = nil
		}
		eng, err := New(st, opts)
		if err == nil {
			t.Fatalf("New with nil %s: expected an error, got engine %v", seam, eng)
		}
		if want := "surfaces: Options is missing " + seam; err.Error() != want {
			t.Fatalf("New with nil %s error = %q, want %q", seam, err, want)
		}
	}
	if _, err := New(st, full); err != nil {
		t.Fatalf("New with all seams: %v", err)
	}
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
		Clock:        &fakeClock{now: time.Unix(0, 0)},
		Logger:       testLogger{t: t},
		Liveness:     liveness{},
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

// TestToastBoundDefaultsTo32 pins the default: an Options with no bound
// queues DefaultMaxToasts toasts and drops the oldest at the bound.
func TestToastBoundDefaultsTo32(t *testing.T) {
	st, eng, _ := newEngine(t, Options{})
	_ = st
	for i := 0; i < DefaultMaxToasts+1; i++ {
		if err := eng.ToastShow("alpha.files", protocol.ToastShowParams{Level: LevelInfo, Title: "t"}); err != nil {
			t.Fatalf("show %d: %v", i, err)
		}
	}
	if got := eng.Stats().ToastsDropped; got != 1 {
		t.Fatalf("dropped = %d, want 1 (the default bound is %d)", got, DefaultMaxToasts)
	}
	active, ok := eng.ActiveToast()
	if !ok || active.Title != "t" {
		t.Fatalf("active = %+v ok=%v, want the youngest toast", active, ok)
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

func TestForeignResolutionWriteReResolves(t *testing.T) {
	st, eng, pods := newEngine(t, Options{Publications: publications{owners: map[string]string{"publisher.list": "publisher"}}})
	pods.launch("alpha.files")
	pods.launch("beta.notes")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	pods.declare("alpha.files", "publisher.list",
		Contribution{ID: "alpha.files.item", Plugin: "alpha.files", Label: "one"})
	settle(st, eng)

	res := resolution(t, st)
	if got := res.Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("occupant = %q", got)
	}
	if got := entryIDs(res.Contributions["publisher.list"]); len(got) != 1 || got[0] != "alpha.files.item" {
		t.Fatalf("contributions = %v", got)
	}

	// A foreign write clears the resolution. The engine's cached view no
	// longer describes it, but the declarations do: the next pass
	// re-resolves every slot from them.
	if _, err := st.Clear(state.SurfacesNamespace); err != nil {
		t.Fatalf("clear: %v", err)
	}
	settle(st, eng)

	res = resolution(t, st)
	if got := res.Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("occupant after clear = %q, want the claim re-resolved", got)
	}
	if got := entryIDs(res.Contributions["publisher.list"]); len(got) != 1 || got[0] != "alpha.files.item" {
		t.Fatalf("contributions after clear = %v", got)
	}
}

func TestCatalogChangedDropsSlotAfterLateCatalogUpdate(t *testing.T) {
	pubs := publications{owners: map[string]string{"publisher.list": "publisher"}}
	st, eng, pods := newEngine(t, Options{Publications: pubs})
	w := &sink{}
	eng.WatchSlot("publisher", "publisher.list", w)

	pods.launch("publisher")
	pods.claim("publisher", string(protocol.SurfaceSidebarList))
	pods.launch("alpha.files")
	pods.declare("alpha.files", "publisher.list", Contribution{ID: "alpha.files.item", Plugin: "alpha.files", Label: "one"})
	settle(st, eng)

	res := resolution(t, st)
	if got := entryIDs(res.Contributions["publisher.list"]); len(got) != 1 || got[0] != "alpha.files.item" {
		t.Fatalf("contributions = %v", got)
	}

	// The publisher's Pod tears down first: its declaration change
	// triggers a pass, but the host's catalog still lists the slot — that
	// update lands one Update later — so the pass keeps the slot.
	pods.teardown("publisher")
	settle(st, eng)
	res = resolution(t, st)
	if _, has := res.Contributions["publisher.list"]; !has {
		t.Fatal("slot dropped before the catalog changed")
	}

	// The catalog update is no declaration change: only CatalogChanged
	// marks the engine dirty, and the next pass drops the slot and tells
	// its watchers the slot is empty.
	delete(pubs.owners, "publisher.list")
	eng.CatalogChanged()
	settle(st, eng)

	res = resolution(t, st)
	if _, has := res.Contributions["publisher.list"]; has {
		t.Fatal("slot survived the catalog change")
	}
	msgs := w.slotMsgs(t)
	if len(msgs) == 0 {
		t.Fatal("watcher never notified")
	}
	last := msgs[len(msgs)-1]
	if last.SlotID != "publisher.list" || len(last.Contributions) != 0 {
		t.Fatalf("last notification = %+v, want the empty publisher.list", last)
	}

	// Catalog-only change: a plugin that only published (its Pod had no
	// declarations — or its catalog update simply lands with no other
	// change in flight) leaves State untouched, so nothing dirties the
	// engine. Re-register the slot — the standing declaration, nudged with
	// a value change, resolves again — then prove the engine does not poll
	// the catalog, and that CatalogChanged alone drops it.
	pubs.owners["publisher.list"] = "publisher"
	pods.declare("alpha.files", "publisher.list", Contribution{ID: "alpha.files.item", Plugin: "alpha.files", Label: "two"})
	settle(st, eng) // the declaration change marks the pass; the catalog lists the slot again
	settle(st, eng) // that pass wrote the resolution, which re-dirties the engine through the State listener; burn the cascade so the next settle is quiet
	res = resolution(t, st)
	if got := entryIDs(res.Contributions["publisher.list"]); len(got) != 1 || got[0] != "alpha.files.item" {
		t.Fatalf("contributions re-registered = %v", got)
	}
	delete(pubs.owners, "publisher.list")
	settle(st, eng) // no CatalogChanged: the engine never looks
	res = resolution(t, st)
	if _, has := res.Contributions["publisher.list"]; !has {
		t.Fatal("slot dropped without CatalogChanged: the engine polled the catalog")
	}
	eng.CatalogChanged()
	settle(st, eng)
	res = resolution(t, st)
	if _, has := res.Contributions["publisher.list"]; has {
		t.Fatal("catalog-only change survived without CatalogChanged marking the pass")
	}
	msgs = w.slotMsgs(t)
	last = msgs[len(msgs)-1]
	if last.SlotID != "publisher.list" || len(last.Contributions) != 0 {
		t.Fatalf("final notification = %+v, want the empty publisher.list", last)
	}
}

// TestReleaseDetectsForeignWriteInSameUpdate pins the release-side guard:
// a foreign clear and a release in the same Update must not let markRev
// absorb the foreign revision. The host's force-close path skips the
// occupant check, so it writes over the cleared resolution and would
// absorb the foreign revision; the guard clears the declarations cache
// instead, and the next pass re-resolves from the live declarations —
// restoring what the clear wiped. The claim declaration was never
// retracted (the clear wiped the occupant the close would have released),
// so the re-resolve re-occupies the surface from the standing claim.
func TestReleaseDetectsForeignWriteInSameUpdate(t *testing.T) {
	st, eng, pods := newEngine(t, Options{Publications: publications{owners: map[string]string{"publisher.list": "publisher"}}})

	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	pods.declare("alpha.files", "publisher.list", Contribution{ID: "alpha.files.item", Plugin: "alpha.files", Label: "one"})
	settle(st, eng)

	// Same Update: the foreign clear, then the host's force-release of the
	// cleared surface, before any Flush.
	if _, err := st.Clear(state.SurfacesNamespace); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := eng.CloseSurface(protocol.SurfaceMainStage); err != nil {
		t.Fatalf("force-release: %v", err)
	}
	settle(st, eng)

	res := resolution(t, st)
	if got := res.Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("occupant after rebuild = %q, want the standing claim re-resolved", got)
	}
	if got := entryIDs(res.Contributions["publisher.list"]); len(got) != 1 || got[0] != "alpha.files.item" {
		t.Fatalf("contributions after foreign clear = %v, want the declaration re-resolved", got)
	}
}

// flakyPod wraps the pod stand-in so tests can fail Release on demand:
// the Pod unreachable, its declaration write refused.
type flakyPod struct {
	*pod
	failRelease bool
}

func (f *flakyPod) Release(plugin string, surface protocol.SurfaceID) error {
	if f.failRelease {
		return fmt.Errorf("pod unreachable")
	}
	return f.pod.Release(plugin, surface)
}

// TestReleaseFailureLeavesClaimStanding pins the honest failure path: when
// the claim retraction fails, the release is not marked freed — no re-award
// runs off a release that did not complete — and the surface stays empty
// until a later pass re-resolves the standing claim.
func TestReleaseFailureLeavesClaimStanding(t *testing.T) {
	st := state.New(state.Options{})
	pods := &flakyPod{pod: newPod(t, st)}
	eng, err := New(st, Options{
		Clock:        &fakeClock{now: time.Unix(0, 0)},
		Logger:       testLogger{t: t},
		Liveness:     liveness{},
		Publications: publications{owners: map[string]string{}},
		Declarations: pods,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	settle(st, eng)
	if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("occupant = %q", got)
	}

	// The retraction fails: the occupant is released from the resolution,
	// but the claim declaration still names the surface and the surface is
	// not marked freed, so no re-award hands it to a standing claim.
	pods.failRelease = true
	if err := eng.SurfaceClose("alpha.files", protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage}); err != nil {
		t.Fatalf("close: %v", err)
	}
	settle(st, eng)
	res := resolution(t, st)
	if _, occ := res.Occupants[protocol.SurfaceMainStage]; occ {
		t.Fatal("freed re-award ran off a failed release")
	}

	// The claim declaration still names the surface — a foreign write's
	// full re-resolve honestly re-occupies it from the standing claim.
	if _, err := st.Clear(state.SurfacesNamespace); err != nil {
		t.Fatalf("clear: %v", err)
	}
	settle(st, eng)
	res = resolution(t, st)
	if got := res.Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("standing claim did not re-resolve: %q", got)
	}
}

func TestInitialSweepResolvesPreRegisteredDeclarations(t *testing.T) {
	st := state.New(state.Options{})
	pods := newPod(t, st)
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	st.Flush()

	eng, err := New(st, Options{
		Clock:        &fakeClock{now: time.Unix(0, 0)},
		Logger:       testLogger{t: t},
		Liveness:     liveness{},
		Publications: publications{owners: map[string]string{}},
		Declarations: pods,
	})
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

// UpdateView writes only the frontend half of husk.surfaces; the engine's
// resolution (occupants, contributions) survives it and the engine's next
// pass keeps the view.
func TestUpdateViewKeepsResolutionAndSurvivesNextPass(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	settle(st, eng)

	err := eng.UpdateView(func(v *contracts.SurfaceView) {
		v.Visible = []protocol.SurfaceID{protocol.SurfaceMainStage, protocol.SurfaceSidebarList}
		v.Placement = contracts.Placement{RailItem: "alpha.files.panel", Kind: "stage"}
		v.Overlays.Modal = contracts.ModalOverlay{Plugin: "alpha.files", RailItem: "alpha.files.panel"}
		v.Overlays.SlotExplorer = true
	})
	if err != nil {
		t.Fatal(err)
	}
	settle(st, eng)
	res := resolution(t, st)
	if got := res.Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("occupant lost by a view write: %q", got)
	}
	if len(res.Visible) != 2 || res.Placement.Kind != "stage" || res.Overlays.Modal.RailItem != "alpha.files.panel" || !res.Overlays.SlotExplorer {
		t.Fatalf("view = %+v", res)
	}

	// A resolution pass (a new claim) must not wipe the view.
	pods.launch("beta.notes")
	pods.claim("beta.notes", string(protocol.SurfaceSidebarList))
	settle(st, eng)
	res = resolution(t, st)
	if res.Occupants[protocol.SurfaceSidebarList].Plugin != "beta.notes" {
		t.Fatalf("new claim not resolved: %+v", res.Occupants)
	}
	if len(res.Visible) != 2 || res.Placement.Kind != "stage" || !res.Overlays.SlotExplorer {
		t.Fatalf("a resolution pass overwrote the frontend's view: %+v", res)
	}
}

func TestUpdateViewRefusesInvalidSurfaceWithoutCommitting(t *testing.T) {
	st, eng, _ := newEngine(t, Options{})
	settle(st, eng)
	before := st.Snapshot().Slices[state.SurfacesNamespace].Revision
	err := eng.UpdateView(func(v *contracts.SurfaceView) {
		v.Visible = []protocol.SurfaceID{protocol.SurfaceMainStage, "nope"}
		v.Overlays.Palette = true
	})
	var serr *Error
	if !errors.As(err, &serr) || serr.Code != protocol.CodeInvalidParams {
		t.Fatalf("err = %v, want invalid_params", err)
	}
	if after := st.Snapshot().Slices[state.SurfacesNamespace].Revision; after != before {
		t.Fatalf("a refused view committed: revision %d -> %d", before, after)
	}
	if res := resolution(t, st); res.Overlays.Palette || len(res.Visible) != 0 {
		t.Fatalf("a refused view leaked: %+v", res)
	}
}

// A foreign write (state.Clear) before a view write must still re-resolve
// the occupants: the view write must not absorb it.
func TestUpdateViewDoesNotAbsorbAForeignWrite(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	settle(st, eng)
	if _, err := st.Clear(state.SurfacesNamespace); err != nil {
		t.Fatal(err)
	}
	if err := eng.UpdateView(func(v *contracts.SurfaceView) { v.Overlays.Palette = true }); err != nil {
		t.Fatal(err)
	}
	settle(st, eng)
	res := resolution(t, st)
	if res.Occupants[protocol.SurfaceMainStage].Plugin != "alpha.files" {
		t.Fatalf("the cleared occupancy was not re-resolved after a view write: %+v", res.Occupants)
	}
	if !res.Overlays.Palette {
		t.Fatalf("the view write was lost: %+v", res)
	}
}

// occupantOf is the resolved occupant of main-stage.
func occupantOf(t *testing.T, st *state.State) string {
	t.Helper()
	return resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin
}

// D70: a claim holds a surface only while its Pod is live.
func TestClaimFromPodThatIsNotLiveHoldsNothing(t *testing.T) {
	live := liveness{dead: map[string]bool{"alpha.broken": true}}
	st, eng, pods := newEngine(t, Options{Liveness: live})
	pods.launch("alpha.broken")
	pods.launch("beta.reference")
	pods.claim("alpha.broken", string(protocol.SurfaceMainStage))
	pods.claim("beta.reference", string(protocol.SurfaceMainStage))
	settle(st, eng)

	if got := occupantOf(t, st); got != "beta.reference" {
		t.Fatalf("occupant = %q, want beta.reference (the first claimant never went live)", got)
	}
	if err := eng.SurfaceUpdate("alpha.broken", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage}); err == nil {
		t.Fatal("a claimant that is not live may render")
	}
	if got := pods.decl("alpha.broken").occ.Get()[protocol.SurfaceMainStage].Plugin; got != "alpha.broken" {
		t.Fatalf("the engine rewrote the Pod's declaration: %q", got)
	}
}

func TestClaimDeclaredBeforePodGoesLiveResolvesWhenItDoes(t *testing.T) {
	live := liveness{dead: map[string]bool{"alpha.files": true}}
	st, eng, pods := newEngine(t, Options{Liveness: live})
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	settle(st, eng)
	if got := occupantOf(t, st); got != "" {
		t.Fatalf("a Pod that is still starting holds %q", got)
	}

	delete(live.dead, "alpha.files")
	eng.LivenessChanged()
	settle(st, eng)
	if got := occupantOf(t, st); got != "alpha.files" {
		t.Fatalf("occupant after the Pod went live = %q, want alpha.files", got)
	}
}

func TestOccupantLeavingLiveHandsSurfaceToNextClaimant(t *testing.T) {
	live := liveness{dead: map[string]bool{}}
	st, eng, pods := newEngine(t, Options{Liveness: live})
	pods.launch("alpha.files")
	pods.launch("beta.notes")
	pods.launch("gamma.logs")
	for _, p := range []string{"alpha.files", "beta.notes", "gamma.logs"} {
		pods.claim(p, string(protocol.SurfaceMainStage))
	}
	settle(st, eng)
	if got := occupantOf(t, st); got != "alpha.files" {
		t.Fatalf("occupant = %q, want alpha.files", got)
	}

	live.dead["alpha.files"] = true
	eng.LivenessChanged()
	settle(st, eng)
	if got := occupantOf(t, st); got != "beta.notes" {
		t.Fatalf("occupant after alpha left live = %q, want beta.notes (next claimant in declaration order)", got)
	}
	if err := eng.SurfaceUpdate("beta.notes", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage}); err != nil {
		t.Fatalf("new occupant update: %v", err)
	}
	if got := pods.decl("alpha.files").occ.Get()[protocol.SurfaceMainStage].Plugin; got != "alpha.files" {
		t.Fatalf("alpha's declaration was dropped: %q", got)
	}

	// The chain continues, and when nobody is live the surface is empty.
	live.dead["beta.notes"] = true
	eng.LivenessChanged()
	settle(st, eng)
	if got := occupantOf(t, st); got != "gamma.logs" {
		t.Fatalf("occupant after beta left live = %q, want gamma.logs", got)
	}
	live.dead["gamma.logs"] = true
	eng.LivenessChanged()
	settle(st, eng)
	if got := occupantOf(t, st); got != "" {
		t.Fatalf("occupant with no live claimant = %q, want none", got)
	}
}

func TestLiveOccupantIsNotPreemptedWhenEarlierClaimantComesBack(t *testing.T) {
	live := liveness{dead: map[string]bool{"alpha.files": true}}
	st, eng, pods := newEngine(t, Options{Liveness: live})
	pods.launch("alpha.files")
	pods.launch("beta.notes")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	pods.claim("beta.notes", string(protocol.SurfaceMainStage))
	settle(st, eng)
	if got := occupantOf(t, st); got != "beta.notes" {
		t.Fatalf("occupant = %q, want beta.notes", got)
	}

	delete(live.dead, "alpha.files")
	eng.LivenessChanged()
	settle(st, eng)
	if got := occupantOf(t, st); got != "beta.notes" {
		t.Fatalf("a live occupant was preempted: %q", got)
	}
}

func bptr(b bool) *protocol.CaptureValue {
	v := protocol.CaptureFalse
	if b {
		v = protocol.CaptureTrue
	}
	return &v
}

// captureOn reports the committed capture map of husk.surfaces.
func captureOn(t *testing.T, st *state.State) map[protocol.SurfaceID]bool {
	t.Helper()
	out := map[protocol.SurfaceID]bool{}
	for id, mode := range resolution(t, st).Capture {
		out[id] = mode != protocol.CaptureNone
	}
	return out
}

func TestCaptureLifecycle(t *testing.T) {
	const stage = protocol.SurfaceMainStage
	// Each case starts with alpha.files holding main-stage, beta.notes
	// standing behind it with a claim, and capture set by alpha.
	tests := []struct {
		name string
		act  func(t *testing.T, st *state.State, eng *Engine, pods *pod, live liveness)
		want bool // capture[main-stage] after the action
	}{
		{"set by true", func(t *testing.T, st *state.State, eng *Engine, pods *pod, live liveness) {}, true},
		{"cleared by false", func(t *testing.T, st *state.State, eng *Engine, pods *pod, live liveness) {
			if err := eng.SurfaceUpdate("alpha.files", protocol.SurfaceUpdateParams{Surface: stage, Capture: bptr(false)}); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"unchanged by absent", func(t *testing.T, st *state.State, eng *Engine, pods *pod, live liveness) {
			if err := eng.SurfaceUpdate("alpha.files", protocol.SurfaceUpdateParams{Surface: stage}); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"true again is idempotent", func(t *testing.T, st *state.State, eng *Engine, pods *pod, live liveness) {
			if err := eng.SurfaceUpdate("alpha.files", protocol.SurfaceUpdateParams{Surface: stage, Capture: bptr(true)}); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"refused update from a non-occupant sets nothing", func(t *testing.T, st *state.State, eng *Engine, pods *pod, live liveness) {
			if err := eng.SurfaceUpdate("beta.notes", protocol.SurfaceUpdateParams{Surface: stage, Capture: bptr(false)}); err == nil {
				t.Fatal("foreign update accepted")
			}
		}, true},
		{"cleared by surface.close", func(t *testing.T, st *state.State, eng *Engine, pods *pod, live liveness) {
			if err := eng.SurfaceClose("alpha.files", protocol.SurfaceCloseParams{Surface: stage}); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"cleared by host CloseSurface", func(t *testing.T, st *state.State, eng *Engine, pods *pod, live liveness) {
			if err := eng.CloseSurface(stage); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"cleared when the claim is dropped", func(t *testing.T, st *state.State, eng *Engine, pods *pod, live liveness) {
			pods.unclaim("alpha.files", string(stage))
		}, false},
		{"cleared when the occupant leaves live (hand-over in the same pass)", func(t *testing.T, st *state.State, eng *Engine, pods *pod, live liveness) {
			live.dead["alpha.files"] = true
			eng.LivenessChanged()
		}, false},
		{"dropped by a foreign write", func(t *testing.T, st *state.State, eng *Engine, pods *pod, live liveness) {
			if _, err := st.Clear(state.SurfacesNamespace); err != nil {
				t.Fatal(err)
			}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live := liveness{dead: map[string]bool{}}
			st, eng, pods := newEngine(t, Options{Liveness: live})
			pods.launch("alpha.files")
			pods.launch("beta.notes")
			pods.claim("alpha.files", string(stage))
			pods.claim("beta.notes", string(stage))
			settle(st, eng)
			if err := eng.SurfaceUpdate("alpha.files", protocol.SurfaceUpdateParams{Surface: stage, Capture: bptr(true)}); err != nil {
				t.Fatal(err)
			}
			settle(st, eng)
			if !captureOn(t, st)[stage] {
				t.Fatal("capture not set by true")
			}

			tt.act(t, st, eng, pods, live)
			settle(st, eng)
			if got := captureOn(t, st)[stage]; got != tt.want {
				t.Fatalf("capture[main-stage] = %v, want %v (map %v)", got, tt.want, captureOn(t, st))
			}
			// The new occupant after a hand-over starts with capture off.
			if occ := occupantOf(t, st); occ != "" && occ != "alpha.files" && captureOn(t, st)[stage] {
				t.Fatalf("capture leaked to the new occupant %q", occ)
			}
		})
	}
}

func TestCaptureRefusedUpdateSetsNothing(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	pods.launch("alpha.files")
	pods.claim("alpha.files", string(protocol.SurfaceSidebarList))
	settle(st, eng)
	for name, tc := range map[string]struct {
		plugin  string
		surface protocol.SurfaceID
	}{
		"unoccupied surface": {"alpha.files", protocol.SurfaceSidePanel},
		"invalid surface":    {"alpha.files", protocol.SurfaceID("nope")},
		"not the occupant":   {"beta.notes", protocol.SurfaceSidebarList},
		"no plugin":          {"", protocol.SurfaceSidebarList},
	} {
		t.Run(name, func(t *testing.T) {
			if err := eng.SurfaceUpdate(tc.plugin, protocol.SurfaceUpdateParams{Surface: tc.surface, Capture: bptr(true)}); err == nil {
				t.Fatal("update accepted")
			}
			settle(st, eng)
			if got := captureOn(t, st); len(got) != 0 {
				t.Fatalf("capture = %v after a refused update", got)
			}
		})
	}
}

func TestCaptureHandOverInTheSameTickAndUpdateViewKeepsIt(t *testing.T) {
	live := liveness{dead: map[string]bool{}}
	st, eng, pods := newEngine(t, Options{Liveness: live})
	pods.launch("alpha.files")
	pods.launch("beta.notes")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	pods.claim("beta.notes", string(protocol.SurfaceSidebarList))
	settle(st, eng)
	for _, s := range []protocol.SurfaceID{protocol.SurfaceMainStage, protocol.SurfaceSidebarList} {
		plugin := map[protocol.SurfaceID]string{protocol.SurfaceMainStage: "alpha.files", protocol.SurfaceSidebarList: "beta.notes"}[s]
		if err := eng.SurfaceUpdate(plugin, protocol.SurfaceUpdateParams{Surface: s, Capture: bptr(true)}); err != nil {
			t.Fatal(err)
		}
	}
	settle(st, eng)

	// A view write leaves both entries alone.
	if err := eng.UpdateView(func(v *contracts.SurfaceView) {
		v.Visible = []protocol.SurfaceID{protocol.SurfaceMainStage}
	}); err != nil {
		t.Fatal(err)
	}
	settle(st, eng)
	if got := captureOn(t, st); !got[protocol.SurfaceMainStage] || !got[protocol.SurfaceSidebarList] || len(got) != 2 {
		t.Fatalf("capture after UpdateView = %v, want both surfaces", got)
	}

	// Only the surface whose occupant left live loses its entry.
	live.dead["beta.notes"] = true
	eng.LivenessChanged()
	settle(st, eng)
	if got := captureOn(t, st); !got[protocol.SurfaceMainStage] || got[protocol.SurfaceSidebarList] {
		t.Fatalf("capture after beta left live = %v, want main-stage only", got)
	}
}

func TestCommittedSurfacesAlwaysCarriesCaptureAndTerminals(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	check := func(phase string) {
		t.Helper()
		entry, ok := st.Get(state.SurfacesNamespace)
		if !ok {
			t.Fatal("husk.surfaces not registered")
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(entry.Value, &raw); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"capture", "terminals"} {
			if got := string(raw[key]); got != "{}" {
				t.Fatalf("%s: %s on the wire = %q, want {}", phase, key, got)
			}
		}
	}
	check("registration")
	pods.launch("alpha.files")
	settle(st, eng)
	check("resolution")
	// A resolution pass must also normalize an empty foreign write, not
	// merely inherit the maps from registration.
	if err := eng.slice.Set(Surfaces{}); err != nil {
		t.Fatal(err)
	}
	eng.CatalogChanged()
	settle(st, eng)
	check("empty foreign write")
}

// warnRecorder records Warnf lines apart from Printf ones.
type warnRecorder struct {
	testLogger
	warns []string
}

func (w *warnRecorder) Warnf(format string, v ...any) {
	w.warns = append(w.warns, fmt.Sprintf(format, v...))
}

func TestRefusedClaimWritesOneWarnLine(t *testing.T) {
	rec := &warnRecorder{}
	st, eng, pods := newEngine(t, Options{Logger: rec})
	rec.testLogger = testLogger{t: t}
	pods.launch("alpha.files")
	pods.launch("beta.notes")
	pods.claim("alpha.files", string(protocol.SurfaceMainStage))
	settle(st, eng)
	pods.claim("beta.notes", string(protocol.SurfaceMainStage))
	settle(st, eng)

	if len(rec.warns) != 1 {
		t.Fatalf("warn lines = %q, want exactly one", rec.warns)
	}
	for _, want := range []string{"beta.notes", "alpha.files", string(protocol.SurfaceMainStage)} {
		if !strings.Contains(rec.warns[0], want) {
			t.Fatalf("warn line %q does not name %q", rec.warns[0], want)
		}
	}
}
