package main

import (
	"context"
	"errors"
	"strings"
	"time"

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

// A host worker may post before the program exists. The post waits for it and
// is delivered once the program runs; it is not reported as a gone loop.
func TestPostBeforeProgramIsDeliveredAfterSet(t *testing.T) {
	prog := newProgramRef()
	m, _ := dumpModel()
	posted := make(chan bool, 1)
	reg := register(m, "a", []protocol.SurfaceID{protocol.SurfaceMainStage})
	put := slotMsg(m, "a", protocol.SurfaceMainStage, "EARLY")
	go func() { posted <- prog.post(hostStep(func() { reg(); put() })) }()
	// Launched>0 keeps the run open until the early post has landed.
	_, err := runDumpFrame(m, dumpFrameOptions{Program: prog, Launched: 1, Quiet: 1, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-posted; !got {
		t.Fatal("an early post was refused")
	}
	if su := m.slotUpdates[protocol.SurfaceMainStage]; su == nil || su.Lines[0] != "EARLY" {
		t.Fatal("the early post never reached the loop")
	}
}

// Once the gate closes a post is refused at once, before or after the program.
func TestPostAfterGateIsRefusedWithoutBlocking(t *testing.T) {
	prog := newProgramRef()
	prog.closeGate()
	if prog.post(struct{}{}) {
		t.Fatal("post succeeded through a closed gate")
	}
	prog.closeGate() // idempotent
}

// failRunHost is a host whose Run fails at once, like a socket that cannot bind.
type failRunHost struct{ stepHost }

func (failRunHost) Run(context.Context) error { return errors.New("bind failed") }

// A host that dies on its own ends the program, and main reads the cause.
func TestHostRunFailureQuitsTheProgram(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	m.attach(failRunHost{stepHost{fe: fe}}, nil)
	_, cmd := m.Update(hostRunEndedMsg{err: errors.New("bind failed")})
	if m.hostErr == nil || cmd == nil {
		t.Fatalf("hostErr=%v cmd=%v, want the program to quit", m.hostErr, cmd)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		if b, ok := cmd().(tea.BatchMsg); !ok || len(b) == 0 {
			t.Fatalf("not a quit: %T", cmd())
		}
	}
	// A clean end after Close is not an error.
	m2 := goldenModel()
	m2.attach(stepHost{fe: withFE(m2)}, nil)
	m2.shutdownHost()
	if _, cmd := m2.Update(hostRunEndedMsg{err: context.Canceled}); cmd != nil || m2.hostErr != nil {
		t.Fatal("a Run that ended because of Close was treated as a failure")
	}
}

// rebound answers KeysFor from a table, as a user keymap that moved the
// host's own keys would.
type rebound struct {
	defaultKeys
	keys map[string][]string
}

func (r rebound) KeysFor(action string) []string { return r.keys[action] }

// The rail, its hover pill and the palette advertise the keys the keymap
// binds, not the distribution's: a rebound or disabled key shows as such.
func TestRailAndPaletteFollowTheKeymap(t *testing.T) {
	m := goldenModel()
	m.keys = rebound{keys: map[string][]string{
		"husk.home":                  {"f9"},
		"husk.palette.open":          {"ctrl+o"},
		"husk.hotspots.toggle_stage": nil, // disabled
	}}
	got := map[string]string{}
	for _, b := range m.railButtons() {
		got[b.kind] = b.shortcut
	}
	if got["home"] != "F9" || got["palette"] != "ctrl+o" || got["hotspots"] != "" {
		t.Fatalf("rail shortcuts = %v", got)
	}
	var home paletteCommand
	for _, c := range m.builtinCommands() {
		if c.ID == "husk.home" {
			home = c
		}
	}
	if !strings.Contains(home.Title, "F9") || strings.Contains(home.Title, "F1") || home.Shortcut != "f9" {
		t.Fatalf("home command = %+v", home)
	}
	if parenthesized("") != "" || parenthesized("v") != "(v)" {
		t.Fatal("hover pill parentheses")
	}
}

// A live plugin's declared actions are palette rows that run through the
// host with their id; a plugin that is not live contributes none.
func TestPaletteListsLivePluginActions(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	fe.Acts = []contracts.PluginAction{
		{Plugin: "alpha", ID: "alpha.deploy", Title: "Deploy it", Description: "ships"},
		{Plugin: "alpha", ID: "alpha.bare"},
		{Plugin: "ghost", ID: "ghost.boo", Title: "Boo"},
	}
	addPlugin(m, "alpha", nil)
	byID := map[string]paletteCommand{}
	for _, c := range m.allCommands() {
		byID[c.ID] = c
	}
	d, ok := byID["alpha.deploy"]
	if !ok || d.Title != "Deploy it" || d.Category != "alpha" || d.PluginID != "alpha" || d.Description != "ships" {
		t.Fatalf("alpha.deploy row = %+v (ok=%v)", d, ok)
	}
	if byID["alpha.bare"].Title != "alpha.bare" {
		t.Fatalf("untitled action should fall back to its id: %+v", byID["alpha.bare"])
	}
	if _, ok := byID["ghost.boo"]; ok {
		t.Fatal("action of a plugin that is not live was listed")
	}
	m.showPalette, m.paletteQuery = true, "deploy"
	if got := m.filteredCommands(); len(got) != 1 || got[0].ID != "alpha.deploy" {
		t.Fatalf("filter = %+v", got)
	}
	m.paletteCursor = 0
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(fe.Invoked) == 0 || fe.Invoked[len(fe.Invoked)-1].Action != "alpha.deploy" {
		t.Fatalf("enter did not invoke the action: %v", fe.Invoked)
	}
}
