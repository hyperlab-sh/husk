package surfaces_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/state/surfaces"
)

const (
	pluginFiles  = "example.files"
	pluginEditor = "example.editor"
	pluginGit    = "example.git"
)

func registerBoth(t *testing.T, s *state.State, pluginID string) (*state.Slice[surfaces.Occupants], *state.Slice[surfaces.Contributions]) {
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
	// The shapes state.md gives, verbatim, both round-trip as JSON objects.
	specOccupants := surfaces.Occupants{"sidebar-list": "example.files", "main-stage": pluginEditor}
	if err := occ.Set(specOccupants); err != nil {
		t.Fatalf("Set occupants: %v", err)
	}
	raw, err := json.Marshal(occ.Get())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"main-stage":"example.editor","sidebar-list":"example.files"}`
	if string(raw) != want {
		t.Fatalf("occupants shape = %s, want %s", raw, want)
	}

	specContributions := surfaces.Contributions{
		"example.editor.toolbar": {{ID: "example.git.branch", Plugin: pluginGit, Label: "main"}},
	}
	if err := con.Set(specContributions); err != nil {
		t.Fatalf("Set contributions: %v", err)
	}
	raw, err = json.Marshal(con.Get())
	if err != nil {
		t.Fatal(err)
	}
	want = `{"example.editor.toolbar":[{"id":"example.git.branch","plugin":"example.git","label":"main"}]}`
	if string(raw) != want {
		t.Fatalf("contributions shape = %s, want %s", raw, want)
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
	if _, err := surfaces.RegisterOccupants(s, "bad id"); !errors.Is(err, state.ErrInvalidSpec) {
		t.Fatalf("RegisterOccupants(bad id) = %v, want ErrInvalidSpec", err)
	}
	if _, err := surfaces.RegisterOccupants(s, pluginEditor); err != nil {
		t.Fatal(err)
	}
	if _, err := surfaces.RegisterOccupants(s, pluginEditor); !errors.Is(err, state.ErrDuplicate) {
		t.Fatalf("duplicate = %v, want ErrDuplicate", err)
	}
}

// TestOwnerCheckedWrites pins the authority model: a Pod's slice handles are
// namespace-bound, so one Pod's writes can never land in another Pod's
// occupant slice, and a crashed Pod's unregister leaves the other Pod's
// slices intact.
func TestOwnerCheckedWrites(t *testing.T) {
	s := state.New(state.Options{})
	filesOcc, filesCon := registerBoth(t, s, pluginFiles)
	editorOcc, _ := registerBoth(t, s, pluginEditor)

	if err := filesOcc.Set(surfaces.Occupants{"sidebar-list": pluginFiles}); err != nil {
		t.Fatal(err)
	}
	if err := editorOcc.Set(surfaces.Occupants{"main-stage": pluginEditor}); err != nil {
		t.Fatal(err)
	}

	// The value one Pod wrote names only itself; the other Pod's slice kept
	// its own value and revision.
	entry, _ := s.Get(editorOcc.Namespace())
	if string(entry.Value) != `{"main-stage":"example.editor"}` || entry.Revision != 2 {
		t.Fatalf("editor occupants after files' write: %+v", entry)
	}
	// The denial path a Server implements: a plugin asking to write another
	// pod's slice is refused by exact namespace comparison before any handle
	// is consulted.
	requested := surfaces.OccupantsNamespace(pluginEditor)
	if requested == filesOcc.Namespace() {
		t.Fatal("files's request for the editor's slice would not be refused by namespace comparison")
	}

	// Crash cleanup: the owner-cleanup path unregisters the crashed Pod's
	// slices; the survivor keeps its value and revision.
	filesOcc.Unregister()
	filesCon.Unregister()
	if _, ok := s.Get(filesOcc.Namespace()); ok {
		t.Fatal("crashed pod's occupants slice survived unregister")
	}
	if _, ok := s.Get(surfaces.ContributionsNamespace(pluginFiles)); ok {
		t.Fatal("crashed pod's contributions slice survived unregister")
	}
	entry, _ = s.Get(editorOcc.Namespace())
	if entry.Revision != 2 || string(entry.Value) != `{"main-stage":"example.editor"}` {
		t.Fatalf("editor occupants after files teardown: %+v", entry)
	}

	// Re-registration continues above the tombstone revision.
	again, err := surfaces.RegisterOccupants(s, pluginFiles)
	if err != nil {
		t.Fatal(err)
	}
	if entry, _ = s.Get(again.Namespace()); entry.Revision != 3 {
		t.Fatalf("re-registered revision = %d, want 3", entry.Revision)
	}
}

// TestSnapshotIsolation pins that a committed write publishes a new snapshot
// and never touches snapshots a reader already holds.
func TestSnapshotIsolation(t *testing.T) {
	s := state.New(state.Options{})
	occ, _ := registerBoth(t, s, pluginFiles)

	before := s.Snapshot()
	if err := occ.Set(surfaces.Occupants{"sidebar-list": pluginFiles}); err != nil {
		t.Fatal(err)
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
	if newEntry.Revision != 2 || string(newEntry.Value) != `{"sidebar-list":"example.files"}` {
		t.Fatalf("new snapshot wrong: %+v", newEntry)
	}
	// Another pod's registration is likewise absent from the held snapshot.
	if _, err := surfaces.RegisterOccupants(s, pluginEditor); err != nil {
		t.Fatal(err)
	}
	if _, ok := before.Slices[surfaces.OccupantsNamespace(pluginEditor)]; ok {
		t.Fatal("held snapshot grew after a later registration")
	}
}

// TestContributionsOrdering pins the ordering state.md's array shape implies
// and today's contribute implements: arrival order, with a repeated id
// replaced in place, and an identical re-contribute committing nothing.
func TestContributionsOrdering(t *testing.T) {
	s := state.New(state.Options{})
	_, con := registerBoth(t, s, pluginGit)

	ids := func(publication string) []string {
		var out []string
		for _, c := range con.Get()[publication] {
			out = append(out, c.ID)
		}
		return out
	}
	pub := "example.editor.toolbar"
	statusbar := "example.editor.statusbar"
	want := []string(nil)
	check := func(publication, step string) {
		t.Helper()
		got := ids(publication)
		if len(got) != len(want) {
			t.Fatalf("%s: ids = %v, want %v", step, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: ids = %v, want %v", step, got, want)
			}
		}
	}

	// Arrival order across three upserts, including two publications.
	err := con.Update(func(v *surfaces.Contributions) {
		v.Upsert(pub, surfaces.Contribution{ID: "example.git.branch", Plugin: pluginGit, Label: "main"})
	})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"example.git.branch"}
	check(pub, "first upsert")

	err = con.Update(func(v *surfaces.Contributions) {
		v.Upsert(statusbar, surfaces.Contribution{ID: "example.git.mode", Plugin: pluginGit, Label: "branch"})
		v.Upsert(pub, surfaces.Contribution{ID: "example.git.ahead", Plugin: pluginGit, Label: "0"})
	})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"example.git.branch", "example.git.ahead"}
	check(pub, "second and third upserts")
	want = []string{"example.git.mode"}
	check(statusbar, "statusbar upsert")

	// Same id, new label: replaced in place, order kept.
	err = con.Update(func(v *surfaces.Contributions) {
		v.Upsert(pub, surfaces.Contribution{ID: "example.git.branch", Plugin: pluginGit, Label: "feature"})
	})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"example.git.branch", "example.git.ahead"}
	check(pub, "in-place replacement")
	want = []string{"example.git.mode"}
	check(statusbar, "in-place replacement leaves statusbar")
	got := con.Get()[pub]
	if got[0].Label != "feature" {
		t.Fatalf("replacement label = %q, want feature", got[0].Label)
	}

	// An identical re-contribute changes the encoding, so State commits
	// nothing: the revision does not move.
	revision := func() uint64 { e, _ := s.Get(con.Namespace()); return e.Revision }
	before := revision()
	err = con.Update(func(v *surfaces.Contributions) {
		v.Upsert(pub, surfaces.Contribution{ID: "example.git.branch", Plugin: pluginGit, Label: "feature"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if after := revision(); after != before {
		t.Fatalf("no-op upsert moved revision %d -> %d", before, after)
	}
}

func TestValidateOwnerParity(t *testing.T) {
	occ := surfaces.Occupants{"sidebar-list": pluginFiles}
	if err := occ.Validate(pluginFiles); err != nil {
		t.Fatalf("own occupancy rejected: %v", err)
	}
	err := occ.Validate(pluginEditor)
	if err == nil {
		t.Fatal("foreign occupant accepted")
	}

	good := surfaces.Contributions{"pub": []surfaces.Contribution{{ID: pluginGit + ".branch", Plugin: pluginGit, Label: "main"}}}
	if err := good.Validate(pluginGit); err != nil {
		t.Fatalf("own contribution rejected: %v", err)
	}
	for name, bad := range map[string]surfaces.Contributions{
		"foreign plugin": {"pub": []surfaces.Contribution{{ID: pluginGit + ".branch", Plugin: pluginEditor}}},
		"foreign id":     {"pub": []surfaces.Contribution{{ID: pluginEditor + ".branch", Plugin: pluginGit}}},
		"bare id":        {"pub": []surfaces.Contribution{{ID: "branch", Plugin: pluginGit}}},
		"empty name":     {"pub": []surfaces.Contribution{{ID: pluginGit + ".", Plugin: pluginGit}}},
		"dotted name":    {"pub": []surfaces.Contribution{{ID: pluginGit + ".a.b", Plugin: pluginGit}}},
	} {
		if err := bad.Validate(pluginGit); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}
