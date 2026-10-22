package main

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// The fallback keymap used without a host must not drift from the keymap the
// host distributes, or a hermetic frame would advertise keys that do nothing.
func TestDefaultKeysMatchTheDistributionKeymap(t *testing.T) {
	svc := keybinding.New()
	seen := map[string]bool{}
	for _, b := range defaultBindings {
		if !seen[b.action] {
			seen[b.action] = true
			if want, got := svc.KeysFor(b.action), (defaultKeys{}).KeysFor(b.action); b.focus == "" && !slices.Equal(want, got) {
				t.Errorf("%s: distribution keys %v, fallback %v", b.action, want, got)
			}
		}
		res := svc.Resolve(keybinding.KeyContext{FocusSurface: b.focus}, []string{b.key})
		if res.Outcome != keybinding.Match || res.Binding.Action != b.action {
			t.Errorf("key %q (focus %q): distribution resolves %+v, fallback binds %s", b.key, b.focus, res, b.action)
		}
	}
}

// refresh reads Pods from State: a starting Pod is not live, a live one is,
// and an unchanged State revision costs nothing.
func TestRefreshFollowsPodsAndSkipsUnchangedState(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	fe.pods, fe.occ = map[string]string{"a": "starting"}, map[protocol.SurfaceID]string{}
	fe.publish()
	m.refresh()
	if m.plugins["a"] || m.podStatus["a"] != "starting" {
		t.Fatalf("starting Pod treated as live: plugins=%v status=%v", m.plugins, m.podStatus)
	}
	fe.pods["a"] = "live"
	fe.occ[protocol.SurfaceMainStage] = "a"
	fe.publish()
	if !m.refresh() || !m.plugins["a"] || m.catalog.Occupants[protocol.SurfaceMainStage] != "a" {
		t.Fatalf("live Pod and occupant not adopted: %v %v", m.plugins, m.catalog.Occupants)
	}
	// Same revision: a changed rail list is not even read.
	fe.Rails = []contracts.RailItem{{ID: "a.x", Plugin: "a", Label: "X", Placement: contracts.PlacementModal}}
	if m.refresh() || len(m.railItems) != 0 {
		t.Fatal("refresh re-read State that did not move")
	}
	fe.publish()
	if !m.refresh() || len(m.railItems) != 1 {
		t.Fatal("refresh missed the new revision")
	}
}

// Frontend-owned State is written when it changes and only then.
func TestFlushStateWritesOnlyOnChange(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	m.attachSlices()
	if !m.flushState() {
		t.Fatal("first flush wrote nothing")
	}
	geo, focus := fe.revision(state.GeometryNamespace), fe.revision(state.FocusNamespace)
	views := len(fe.Views)
	if geo == 0 || focus == 0 || views != 1 {
		t.Fatalf("first flush: geometry rev %d, focus rev %d, views %d", geo, focus, views)
	}
	if m.flushState() || fe.revision(state.GeometryNamespace) != geo || len(fe.Views) != views {
		t.Fatal("an unchanged model wrote State again")
	}
	m.Update(tea.WindowSizeMsg{Width: goldenW + 10, Height: goldenH})
	if fe.revision(state.GeometryNamespace) <= geo {
		t.Fatal("a resize did not write husk.geometry")
	}
	if fe.revision(state.FocusNamespace) != focus {
		t.Fatal("a resize rewrote an unchanged husk.focus")
	}
}

// Persisted dock preferences come back from husk.layout when the slices attach.
func TestLayoutHydratesFromState(t *testing.T) {
	src := goldenModel()
	src.showSidebar = false
	m := goldenModel()
	fe := withFE(m)
	fe.Snap = &state.Snapshot{Seq: 1, Slices: map[string]state.Entry{state.LayoutNamespace: {Value: src.layoutJSON()}}}
	if !m.showSidebar {
		t.Fatal("precondition: sidebar starts shown")
	}
	m.attachSlices()
	if m.showSidebar {
		t.Fatal("husk.layout was not applied")
	}
}

// seqKeys resolves "g" as the start of "g g", which toggles the sidebar.
type seqKeys struct{ defaultKeys }

func (seqKeys) Resolve(_ contracts.KeyContext, keys []string) contracts.KeyResolution {
	switch {
	case slices.Equal(keys, []string{"g"}):
		return contracts.KeyResolution{Outcome: contracts.KeyPrefix}
	case slices.Equal(keys, []string{"g", "g"}):
		return contracts.KeyResolution{Outcome: contracts.KeyMatch, Binding: &contracts.KeyEntry{Keys: keys, Action: "husk.sidebar.toggle"}}
	}
	return contracts.KeyResolution{}
}

func TestKeySequencePendingAndEscape(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	addPlugin(m, "p", map[protocol.SurfaceID][]string{protocol.SurfaceMainStage: {"x"}})
	m.keys = seqKeys{}
	m.focusedSlot = protocol.SurfaceMainStage

	press(m, "g")
	if len(m.pendingKeys) != 1 || len(fe.Keys) != 0 {
		t.Fatalf("a prefix must wait, not reach the occupant: pending=%v keys=%v", m.pendingKeys, fe.Keys)
	}
	press(m, "esc")
	if len(m.pendingKeys) != 0 || len(fe.Keys) != 0 {
		t.Fatalf("esc must cancel the pending sequence and be consumed: pending=%v keys=%v", m.pendingKeys, fe.Keys)
	}
	shown := m.showSidebar
	press(m, "g")
	press(m, "g")
	if len(fe.Invoked) == 0 || fe.Invoked[len(fe.Invoked)-1].Action != "husk.sidebar.toggle" || m.showSidebar == shown {
		t.Fatalf("the completed sequence did not run its action: %+v", fe.Invoked)
	}
	press(m, "z")
	if len(fe.Keys) != 1 {
		t.Fatalf("an unbound key must reach the occupant, got %v", fe.Keys)
	}
}

// orderHost records the order Close's two halves happen in.
type orderHost struct {
	stepHost
	log *[]string
}

func (h orderHost) Close() error { *h.log = append(*h.log, "close"); return nil }

// The post gate closes before Host.Close, so workers Close waits for never
// block on a loop that stopped reading.
func TestQuitClosesPostGateBeforeHost(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	var log []string
	m.attach(orderHost{stepHost{fe: fe}, &log}, func() { // like the real gate, closing twice is harmless
		if !slices.Contains(log, "gate") {
			log = append(log, "gate")
		}
	})
	m.quitCmd()
	m.shutdownHost() // idempotent
	if !slices.Equal(log, []string{"gate", "close"}) {
		t.Fatalf("shutdown order = %v, want gate then close once", log)
	}
}

// An engine toast shows until it expires; a local toast is not displaced.
func TestEngineToastShowsAndClears(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	fe.Toast = &contracts.Toast{Plugin: "p", Level: "info", Title: "T", Message: "M"}
	m.pollToast()
	if m.activeToast == nil || m.activeToast.Title != "T" || !m.engineToast {
		t.Fatalf("engine toast not shown: %+v", m.activeToast)
	}
	fe.Toast = nil
	m.pollToast()
	if m.activeToast != nil {
		t.Fatal("expired engine toast stayed up")
	}
	m.activeToast = &contracts.Toast{Title: "local"}
	fe.Toast = &contracts.Toast{Title: "engine"}
	m.pollToast()
	if m.activeToast.Title != "local" {
		t.Fatal("an engine toast displaced a local one")
	}
}
