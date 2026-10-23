package surfaces_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/state/surfaces"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The declaration value types are the frozen contracts shapes: identical by
// definition, so an adapter can hand one to the other without conversion.
var (
	_ contracts.Occupant     = surfaces.Occupant{}
	_ contracts.Contribution = surfaces.Contribution{}
)

const (
	pluginFiles  = "example.files"
	pluginEditor = "example.editor"
	pluginGit    = "example.git"
)

func registerBoth(t *testing.T, s *state.State, pluginID string) (*surfaces.OccupantsSlice, *surfaces.ContributionsSlice) {
	t.Helper()
	occ, err := surfaces.RegisterOccupants(s, pluginID)
	if err != nil {
		t.Fatalf("RegisterOccupants(%q): %v", pluginID, err)
	}
	con, err := surfaces.RegisterContributions(s, pluginID)
	if err != nil {
		t.Fatalf("RegisterContributions(%q): %v", pluginID, err)
	}
	return occ, con
}

func TestRegistrationShapesAndNamespaces(t *testing.T) {
	s := state.New(state.Options{})
	occ, con := registerBoth(t, s, pluginEditor)

	if got := occ.Namespace(); got != "example.editor.occupants" {
		t.Fatalf("occupants namespace = %q", got)
	}
	if got := con.Namespace(); got != "example.editor.contributions" {
		t.Fatalf("contributions namespace = %q", got)
	}
	if got := surfaces.OccupantsNamespace(pluginEditor); got != occ.Namespace() {
		t.Fatalf("OccupantsNamespace = %q, want the registered one", got)
	}
	if got := surfaces.ContributionsNamespace(pluginEditor); got != con.Namespace() {
		t.Fatalf("ContributionsNamespace = %q, want the registered one", got)
	}
	entry, ok := s.Get(occ.Namespace())
	if !ok || entry.Revision != 1 || entry.Owner != pluginEditor || entry.Scope != state.ScopeInstance ||
		entry.Persistent || entry.Hydrated || string(entry.Value) != "{}" {
		t.Fatalf("occupants entry: %+v", entry)
	}

	// The contracts shapes, verbatim: a claim names its surface and its
	// plugin; a contribution names slot, id, plugin and, when set, label and
	// on_click.
	if err := occ.Set(surfaces.Occupants{
		protocol.SurfaceSidebarList: {Surface: protocol.SurfaceSidebarList, Plugin: pluginEditor},
		protocol.SurfaceMainStage:   {Surface: protocol.SurfaceMainStage, Plugin: pluginEditor},
	}); err != nil {
		t.Fatalf("Set occupants: %v", err)
	}
	raw, err := json.Marshal(occ.Get())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"main-stage":{"surface":"main-stage","plugin":"example.editor"},` +
		`"sidebar-list":{"surface":"sidebar-list","plugin":"example.editor"}}`
	if string(raw) != want {
		t.Fatalf("occupants value = %s, want %s", raw, want)
	}

	if err := con.Set(surfaces.Contributions{
		"example.editor.toolbar": {{
			Slot:    "example.editor.toolbar",
			ID:      "example.editor.branch",
			Plugin:  pluginEditor,
			Label:   "main",
			OnClick: "example.editor.checkout",
		}},
	}); err != nil {
		t.Fatalf("Set contributions: %v", err)
	}
	raw, err = json.Marshal(con.Get())
	if err != nil {
		t.Fatal(err)
	}
	want = `{"example.editor.toolbar":[{"slot":"example.editor.toolbar","id":"example.editor.branch","plugin":"example.editor","label":"main","on_click":"example.editor.checkout"}]}`
	if string(raw) != want {
		t.Fatalf("contributions value = %s, want %s", raw, want)
	}

	// label and on_click are omitted when empty, as contracts declares.
	if err := con.Set(surfaces.Contributions{
		"example.editor.status": {{Slot: "example.editor.status", ID: "example.editor.count", Plugin: pluginEditor}},
	}); err != nil {
		t.Fatalf("Set bare contribution: %v", err)
	}
	raw, err = json.Marshal(con.Get())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"example.editor.status":[{"slot":"example.editor.status","id":"example.editor.count","plugin":"example.editor"}]}` {
		t.Fatalf("bare contribution = %s, want label and on_click omitted", got)
	}
}

func TestRegistrationRefusesReservedCoreOwner(t *testing.T) {
	s := state.New(state.Options{})
	if _, err := surfaces.RegisterOccupants(s, state.OwnerHusk); !errors.Is(err, state.ErrInvalidSpec) {
		t.Fatalf("RegisterOccupants(husk) = %v, want ErrInvalidSpec", err)
	}
	if _, err := surfaces.RegisterContributions(s, state.OwnerHusk); !errors.Is(err, state.ErrInvalidSpec) {
		t.Fatalf("RegisterContributions(husk) = %v, want ErrInvalidSpec", err)
	}

	occ, _ := registerBoth(t, s, pluginFiles)
	if _, err := surfaces.RegisterSurfaces(s); err != nil {
		t.Fatalf("RegisterSurfaces: %v", err)
	}
	if _, err := surfaces.RegisterSurfaces(s); !errors.Is(err, state.ErrDuplicate) {
		t.Fatalf("second RegisterSurfaces = %v, want ErrDuplicate", err)
	}
	if _, err := surfaces.RegisterOccupants(s, pluginFiles); !errors.Is(err, state.ErrDuplicate) {
		t.Fatalf("duplicate occupants = %v, want ErrDuplicate", err)
	}
	if _, ok := s.Get(occ.Namespace()); !ok {
		t.Fatal("failed registration removed the existing slice")
	}
}

// TestValidatedWrites pins the validated-write rule: the wrappers are the
// only write path, every write runs owner-parity validation, and a refused
// write leaves value, revision and snapshot untouched. This is also the
// plugin-request denial path: a plugin cannot write .occupants or
// .contributions, and its Pod's check compares the requested namespace
// against these wrappers' Namespace(), never an owner string.
func TestValidatedWrites(t *testing.T) {
	s := state.New(state.Options{})
	occ, con := registerBoth(t, s, pluginFiles)

	refusals := []struct {
		name  string
		write func() error
	}{
		{"occupant of another plugin", func() error {
			return occ.Set(surfaces.Occupants{protocol.SurfaceMainStage: {Surface: protocol.SurfaceMainStage, Plugin: pluginEditor}})
		}},
		{"occupant of an unknown surface", func() error {
			return occ.Set(surfaces.Occupants{"not-a-surface": {Surface: "not-a-surface", Plugin: pluginFiles}})
		}},
		{"occupant stored under another surface key", func() error {
			return occ.Set(surfaces.Occupants{protocol.SurfaceSidebarList: {Surface: protocol.SurfaceMainStage, Plugin: pluginFiles}})
		}},
		{"contribution stored under another slot key", func() error {
			return con.Set(surfaces.Contributions{"example.editor.toolbar": {
				{Slot: "example.editor.status", ID: "example.files.count", Plugin: pluginFiles},
			}})
		}},
		{"upsert whose slot does not name the target", func() error {
			return con.Upsert("example.editor.toolbar", surfaces.Contribution{Slot: "example.editor.status", ID: "example.files.count", Plugin: pluginFiles})
		}},
		{"contribution of another plugin", func() error {
			return con.Upsert("example.editor.toolbar", surfaces.Contribution{Slot: "example.editor.toolbar", ID: "example.files.count", Plugin: pluginEditor})
		}},
		{"contribution id outside the plugin", func() error {
			return con.Upsert("example.editor.toolbar", surfaces.Contribution{Slot: "example.editor.toolbar", ID: "example.editor.count", Plugin: pluginFiles})
		}},
		{"contribution on_click outside the plugin", func() error {
			return con.Upsert("example.editor.toolbar", surfaces.Contribution{Slot: "example.editor.toolbar", ID: "example.files.count", Plugin: pluginFiles, OnClick: "example.editor.open"})
		}},
		{"contribution without a slot field", func() error {
			return con.Set(surfaces.Contributions{"example.editor.toolbar": {{ID: "example.files.count", Plugin: pluginFiles}}})
		}},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := s.Get(occ.Namespace())
			if err := tc.write(); err == nil {
				t.Fatal("write accepted, want refusal")
			}
			after, _ := s.Get(occ.Namespace())
			if after.Revision != before.Revision || string(after.Value) != string(before.Value) {
				t.Fatalf("refused write committed: revision %d -> %d, value %s -> %s",
					before.Revision, after.Revision, before.Value, after.Value)
			}
		})
	}

	// A valid claim and a valid contribution commit.
	if err := occ.Set(surfaces.Occupants{protocol.SurfaceSidebarList: {Surface: protocol.SurfaceSidebarList, Plugin: pluginFiles}}); err != nil {
		t.Fatalf("valid claim refused: %v", err)
	}
	if err := con.Upsert("example.editor.status", surfaces.Contribution{
		Slot:    "example.editor.status",
		ID:      "example.files.count",
		Plugin:  pluginFiles,
		Label:   "12 files",
		OnClick: "example.files.open",
	}); err != nil {
		t.Fatalf("valid contribution refused: %v", err)
	}
	if got := con.Get()["example.editor.status"]; len(got) != 1 || got[0].Label != "12 files" {
		t.Fatalf("contribution not stored: %+v", got)
	}
}

func TestSnapshotIsolation(t *testing.T) {
	s := state.New(state.Options{})
	occ, _ := registerBoth(t, s, pluginEditor)

	before := s.Snapshot()
	if err := occ.Set(surfaces.Occupants{protocol.SurfaceMainStage: {Surface: protocol.SurfaceMainStage, Plugin: pluginEditor}}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	after := s.Snapshot()
	if before == after {
		t.Fatal("committed write did not publish a new snapshot")
	}
	oldEntry, ok := before.Slices[occ.Namespace()]
	if !ok || oldEntry.Revision != 1 || string(oldEntry.Value) != "{}" {
		t.Fatalf("held snapshot changed: %+v", oldEntry)
	}
	newEntry := after.Slices[occ.Namespace()]
	if newEntry.Revision != 2 || !strings.Contains(string(newEntry.Value), "main-stage") {
		t.Fatalf("new snapshot wrong: %+v", newEntry)
	}
	if got := occ.Get(); len(got) != 1 || got[protocol.SurfaceMainStage].Plugin != pluginEditor {
		t.Fatalf("Get after Set: %+v", got)
	}
	// Get decodes fresh: mutating the decoded value cannot touch the slice.
	decoded := occ.Get()
	decoded[protocol.SurfaceMainStage] = surfaces.Occupant{Surface: protocol.SurfaceMainStage, Plugin: "tampered"}
	if got := occ.Get(); got[protocol.SurfaceMainStage].Plugin != pluginEditor {
		t.Fatalf("Get returned shared storage: %+v", got)
	}
}

// TestContributionsOrdering pins the ordering the registry keeps per slot
// for one plugin: the order the Pod received the entries, a repeated id
// replaced in place, ids scoped to their slot, and an identical
// re-contribute committing nothing. The combined order across plugins is a
// different slice's job: the Surfaces engine records it in husk.surfaces
// (first observation across ticks; within one tick, by namespace then by
// entry position), and this package only carries the orderless
// declarations.
func TestContributionsOrdering(t *testing.T) {
	s := state.New(state.Options{})
	_, con := registerBoth(t, s, pluginGit)

	status, toolbar := "example.editor.status", "example.editor.toolbar"
	branch := surfaces.Contribution{Slot: status, ID: "example.git.branch", Plugin: pluginGit, Label: "main"}
	commit := surfaces.Contribution{Slot: toolbar, ID: "example.git.commit", Plugin: pluginGit, Label: "head"}
	if err := con.Upsert(status, branch); err != nil {
		t.Fatalf("Upsert branch: %v", err)
	}
	if err := con.Upsert(toolbar, commit); err != nil {
		t.Fatalf("Upsert commit: %v", err)
	}

	// Replacing branch with the same id keeps its position in its slot, as
	// today's contribute does.
	replaced := branch
	replaced.Label = "topic"
	if err := con.Upsert(status, replaced); err != nil {
		t.Fatalf("Upsert replacement: %v", err)
	}
	got := con.Get()
	if ids := slotIDs(got[status]); len(ids) != 1 || ids[0] != "example.git.branch" {
		t.Fatalf("status ids = %v, want [branch]", ids)
	}
	if got[status][0].Label != "topic" {
		t.Fatalf("replacement not applied in place: %+v", got[status][0])
	}
	if ids := slotIDs(got[toolbar]); len(ids) != 1 || ids[0] != "example.git.commit" {
		t.Fatalf("toolbar ids = %v, want [commit]", ids)
	}

	// Ids stay scoped to their slot: the same id may live in two slots, as
	// in the registry's independent per-slot lists. Each entry names its own
	// slot, so the copies differ in Slot while sharing the id.
	branchInToolbar := branch
	branchInToolbar.Slot = toolbar
	if err := con.Upsert(toolbar, branchInToolbar); err != nil {
		t.Fatalf("Upsert same id into another slot: %v", err)
	}
	got = con.Get()
	if ids := slotIDs(got[toolbar]); len(ids) != 2 || ids[0] != "example.git.commit" || ids[1] != "example.git.branch" {
		t.Fatalf("toolbar ids = %v, want [commit branch]", ids)
	}
	if ids := slotIDs(got[status]); len(ids) != 1 {
		t.Fatalf("status ids = %v, want [branch]", ids)
	}

	// An identical entry changes nothing.
	rev, _ := s.Get(con.Namespace())
	if err := con.Upsert(status, replaced); err != nil {
		t.Fatalf("Upsert no-op: %v", err)
	}
	if after, _ := s.Get(con.Namespace()); after.Revision != rev.Revision {
		t.Fatalf("no-op Upsert bumped revision %d -> %d", rev.Revision, after.Revision)
	}
}

func slotIDs(entries []surfaces.Contribution) []string {
	ids := make([]string, 0, len(entries))
	for _, c := range entries {
		ids = append(ids, c.ID)
	}
	return ids
}

func TestValidateOwnerParity(t *testing.T) {
	valid := surfaces.Contributions{
		"example.editor.status": {{
			Slot:    "example.editor.status",
			ID:      "example.git.branch",
			Plugin:  pluginGit,
			OnClick: "example.git.checkout",
		}},
	}
	if err := valid.Validate(pluginGit); err != nil {
		t.Fatalf("Validate own entries: %v", err)
	}
	if err := valid.Validate(pluginEditor); err == nil {
		t.Fatal("foreign owner accepted")
	}

	cases := []struct {
		name         string
		contribution surfaces.Contribution
		want         string
	}{
		{"foreign id", surfaces.Contribution{Slot: "s", ID: "example.editor.x", Plugin: pluginGit}, "must use example.git.<name>"},
		{"dotted name", surfaces.Contribution{Slot: "s", ID: "example.git.a.b", Plugin: pluginGit}, "must use example.git.<name>"},
		{"empty name", surfaces.Contribution{Slot: "s", ID: "example.git.", Plugin: pluginGit}, "must use example.git.<name>"},
		{"foreign on_click", surfaces.Contribution{Slot: "s", ID: "example.git.x", Plugin: pluginGit, OnClick: "example.editor.open"}, "own action"},
		{"empty slot", surfaces.Contribution{ID: "example.git.x", Plugin: pluginGit}, "names no slot"},
		{"slot parity", surfaces.Contribution{Slot: "other", ID: "example.git.x", Plugin: pluginGit}, `stored under "s"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := surfaces.Contributions{"s": {tc.contribution}}.Validate(pluginGit)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	occupants := surfaces.Occupants{protocol.SurfaceModal: {Surface: protocol.SurfaceModal, Plugin: pluginGit}}
	if err := occupants.Validate(pluginGit); err != nil {
		t.Fatalf("Validate own claim: %v", err)
	}
	if err := occupants.Validate(pluginFiles); err == nil {
		t.Fatal("foreign claim accepted")
	}
	err := (surfaces.Occupants{"made-up": {Surface: "made-up", Plugin: pluginGit}}).Validate(pluginGit)
	if err == nil || !strings.Contains(err.Error(), "defined host layout surface") {
		t.Fatalf("unknown surface = %v, want a defined-surface refusal", err)
	}
}

// TestUnregisterTearsDeclarationsDown pins the owner-cleanup path: a Pod
// teardown unregisters its plugin's declarations, the sibling slice stays,
// and re-registration continues from the tombstone revision instead of
// restarting at 1.
func TestUnregisterTearsDeclarationsDown(t *testing.T) {
	s := state.New(state.Options{})
	occ, con := registerBoth(t, s, pluginFiles)
	if err := occ.Set(surfaces.Occupants{protocol.SurfaceSidebarList: {Surface: protocol.SurfaceSidebarList, Plugin: pluginFiles}}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	lastEntry, _ := s.Get(occ.Namespace())
	last := lastEntry.Revision

	occ.Unregister()
	occ.Unregister() // idempotent

	if _, ok := s.Get(occ.Namespace()); ok {
		t.Fatal("unregistered occupants still readable")
	}
	if got := con.Get(); len(got) != 0 {
		t.Fatalf("sibling contributions affected: %+v", got)
	}
	// Re-registering continues from the tombstone revision.
	again, err := surfaces.RegisterOccupants(s, pluginFiles)
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	entry, ok := s.Get(again.Namespace())
	if !ok || entry.Revision != last+1 || string(entry.Value) != "{}" {
		t.Fatalf("re-registered entry: %+v, want revision %d", entry, last+1)
	}
}

// TestSurfacesResolutionSlice pins the husk.surfaces resolution slice: owner
// husk, instance scope, not persistent, initial revision 1 holding the empty
// resolution, and a value shape whose marshalled form is exactly the shape
// state.md documents. The Surfaces engine (stem-surfaces) is its only
// writer; this package only defines the slice.
func TestSurfacesResolutionSlice(t *testing.T) {
	s := state.New(state.Options{})
	slice, err := surfaces.RegisterSurfaces(s)
	if err != nil {
		t.Fatalf("RegisterSurfaces: %v", err)
	}
	if got := slice.Namespace(); got != "husk.surfaces" {
		t.Fatalf("namespace = %q", got)
	}
	entry, ok := s.Get("husk.surfaces")
	if !ok || entry.Owner != state.OwnerHusk || entry.Scope != state.ScopeInstance ||
		entry.Persistent || entry.Hydrated || entry.Revision != 1 {
		t.Fatalf("initial entry: %+v", entry)
	}
	var empty surfaces.Surfaces
	if err := json.Unmarshal(entry.Value, &empty); err != nil {
		t.Fatalf("initial value not a resolution object: %v", err)
	}
	if !reflect.DeepEqual(empty, surfaces.Surfaces{}) {
		t.Fatalf("initial value = %+v, want the empty resolution", empty)
	}

	if err := slice.Set(surfaces.Surfaces{
		Occupants: map[protocol.SurfaceID]surfaces.Occupant{
			protocol.SurfaceMainStage: {Surface: protocol.SurfaceMainStage, Plugin: pluginEditor},
		},
		Contributions: map[string][]surfaces.Contribution{
			"example.editor.toolbar": {
				{Slot: "example.editor.toolbar", ID: "example.git.branch", Plugin: pluginGit, Label: "main"},
			},
		},
		Capture:   map[protocol.SurfaceID]protocol.CaptureMode{protocol.SurfaceMainStage: protocol.CaptureText},
		Visible:   []protocol.SurfaceID{protocol.SurfaceActivityRail, protocol.SurfaceSidebarList, protocol.SurfaceMainHeader, protocol.SurfaceMainStage},
		Placement: surfaces.Placement{RailItem: "", Kind: ""},
		Overlays:  surfaces.Overlays{},
	}); err != nil {
		t.Fatalf("Set resolution: %v", err)
	}
	raw, err := json.Marshal(slice.Get())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"occupants":{"main-stage":{"surface":"main-stage","plugin":"example.editor"}},` +
		`"contributions":{"example.editor.toolbar":[{"slot":"example.editor.toolbar","id":"example.git.branch","plugin":"example.git","label":"main"}]},` +
		`"capture":{"main-stage":"text"},"terminals":null,` +
		`"visible":["activity-rail","sidebar-list","main-header","main-stage"],` +
		`"placement":{"rail_item":"","kind":""},` +
		`"overlays":{"modal":{"plugin":"","rail_item":""},"palette":false,"slot_explorer":false,"settings_center":""}}`
	if string(raw) != want {
		t.Fatalf("resolution value =\n%s\nwant\n%s", raw, want)
	}
}
