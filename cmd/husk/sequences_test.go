package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// chordKeys resolves a table of space-separated sequences to actions. A
// Frozen context keeps only husk.quit, like the real keymap's filter.
type chordKeys struct {
	defaultKeys
	bind map[string]string
}

func (c chordKeys) Resolve(ctx contracts.KeyContext, keys []string) contracts.KeyResolution {
	joined := strings.Join(keys, " ")
	prefix := false
	for seq, action := range c.bind {
		if ctx.Frozen && action != "husk.quit" {
			continue
		}
		if seq == joined {
			return contracts.KeyResolution{Outcome: contracts.KeyMatch, Binding: &contracts.KeyEntry{Keys: keys, Action: action}}
		}
		if strings.HasPrefix(seq, joined+" ") {
			prefix = true
		}
	}
	if prefix {
		return contracts.KeyResolution{Outcome: contracts.KeyPrefix}
	}
	return contracts.KeyResolution{}
}

// timerRecorder stands in for the clock: it records every scheduled timeout
// and lets a test deliver them by hand.
type timerRecorder struct {
	durations []time.Duration
	msgs      []tea.Msg
}

func (r *timerRecorder) after(d time.Duration, msg tea.Msg) tea.Cmd {
	r.durations = append(r.durations, d)
	r.msgs = append(r.msgs, msg)
	return nil
}

// seqModel is a model with plugin p on main-stage and sidebar-list, focus on
// main-stage, the chord table below and a recording timer.
func seqModel(t *testing.T) (*WorkbenchModel, *fakeFE, *timerRecorder) {
	t.Helper()
	m := goldenModel()
	fe := withFE(m)
	hostPlugin(m, "p", protocol.SurfaceMainStage, protocol.SurfaceSidebarList)
	m.setFocus(protocol.SurfaceMainStage)
	m.keys = chordKeys{bind: map[string]string{
		"a b":   "husk.sidebar.toggle",
		"g g":   "husk.sidepanel.toggle",
		"g e":   "husk.home",
		"g p":   "p.act",
		"t t t": "husk.docks.cycle",
		"b":     "husk.hotspots.badges",
		"tab":   "husk.focus.next",
		"q":     "husk.quit",
	}}
	rec := &timerRecorder{}
	m.after = rec.after
	return m, fe, rec
}

func invokedActions(fe *fakeFE) []string {
	var out []string
	for _, in := range fe.Invoked {
		out = append(out, in.Action)
	}
	return out
}

func sentKeys(fe *fakeFE) []string {
	var out []string
	for _, k := range fe.Keys {
		out = append(out, k.P.Key)
	}
	return out
}

func TestSequenceTable(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, m *WorkbenchModel, fe *fakeFE, rec *timerRecorder)
		// pending is what the model keeps afterwards.
		pending     []string
		wantInvoked []string
		wantKeys    []string
		wantTimers  int
	}{
		{
			name: "prefix waits and arms the 1500 ms timer",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "a")
			},
			pending:    []string{"a"},
			wantTimers: 1,
		},
		{
			name: "timeout discards without replay or dispatch",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, rec *timerRecorder) {
				press(m, "a")
				m.Update(rec.msgs[0])
			},
			wantTimers: 1,
		},
		{
			name: "a stale timeout is ignored and the current one fires",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, rec *timerRecorder) {
				press(m, "t")
				press(m, "t") // every pending key restarts the timer
				if len(rec.msgs) != 2 || rec.msgs[0] == rec.msgs[1] {
					t.Fatalf("second pending key must arm a new generation, got %v", rec.msgs)
				}
				m.Update(rec.msgs[0])
				if !slices.Equal(m.pendingKeys, []string{"t", "t"}) {
					t.Fatalf("a stale generation cleared the sequence: %v", m.pendingKeys)
				}
				m.Update(rec.msgs[1])
			},
			wantTimers: 2,
		},
		{
			name: "a timeout after the sequence completed is ignored",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, rec *timerRecorder) {
				press(m, "g")
				press(m, "g")
				m.Update(rec.msgs[0])
			},
			wantInvoked: []string{"husk.sidepanel.toggle"},
			wantTimers:  1,
		},
		{
			name: "esc cancels and is consumed, not resolved",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "a")
				press(m, "esc")
			},
			wantTimers: 1,
		},
		{
			name: "match clears and dispatches",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "a")
				press(m, "b")
			},
			wantInvoked: []string{"husk.sidebar.toggle"},
			wantTimers:  1,
		},
		{
			name: "breaking key that is bound fires alone",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "g")
				press(m, "b")
			},
			wantInvoked: []string{"husk.hotspots.badges"},
			wantTimers:  1,
		},
		{
			name: "breaking key that is unbound reaches the occupant",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "g")
				press(m, "z")
			},
			wantKeys:   []string{"z"},
			wantTimers: 1,
		},
		{
			name: "breaking key that is a prefix starts a new sequence with a new timer",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "a")
				press(m, "g")
			},
			pending:    []string{"g"},
			wantTimers: 2,
		},
		{
			name: "focus moved by a host action",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "a")
				m.setFocus(protocol.SurfaceSidebarList)
			},
			wantTimers: 1,
		},
		{
			name: "focus moved by tab",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "a")
				press(m, "tab")
			},
			wantInvoked: []string{"husk.focus.next"},
			wantTimers:  1,
		},
		{
			name: "focus moved by a click",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "a")
				r := m.slotRects[protocol.SurfaceSidebarList]
				m.Update(tea.MouseClickMsg{X: r.InnerX + 1, Y: r.InnerY + 1, Button: tea.MouseLeft})
				if m.focusedSlot != protocol.SurfaceSidebarList {
					t.Fatalf("the click did not move focus: %s", m.focusedSlot)
				}
			},
			wantTimers: 1,
		},
		{
			name: "modal opens",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "a")
				m.openModal("p", "p.panel")
			},
			wantTimers: 1,
		},
		{
			name: "modal closes",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				m.openModal("p", "p.panel")
				press(m, "a")
				m.closeModal()
			},
			wantTimers: 1,
		},
		{
			name: "palette opens",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "a")
				m.openPalette()
			},
			wantTimers: 1,
		},
		{
			name: "occupant of the focused surface leaves",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "a")
				hostLeave(m, "p")
			},
			wantTimers: 1,
		},
		{
			name: "paste discards, then travels as one bracket-wrapped key",
			run: func(t *testing.T, m *WorkbenchModel, _ *fakeFE, _ *timerRecorder) {
				press(m, "a")
				m.Update(tea.PasteMsg{Content: "b"})
			},
			wantKeys:   []string{"[b]"},
			wantTimers: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, fe, rec := seqModel(t)
			tc.run(t, m, fe, rec)
			if !slices.Equal(m.pendingKeys, tc.pending) {
				t.Errorf("pending = %v, want %v", m.pendingKeys, tc.pending)
			}
			if got := invokedActions(fe); !slices.Equal(got, tc.wantInvoked) {
				t.Errorf("invoked = %v, want %v", got, tc.wantInvoked)
			}
			if got := sentKeys(fe); !slices.Equal(got, tc.wantKeys) {
				t.Errorf("keys to the occupant = %v, want %v", got, tc.wantKeys)
			}
			if len(rec.durations) != tc.wantTimers {
				t.Errorf("timers armed = %d, want %d", len(rec.durations), tc.wantTimers)
			}
			for _, d := range rec.durations {
				if d != 1500*time.Millisecond {
					t.Errorf("timer duration = %s, want 1.5s", d)
				}
			}
		})
	}
}

// A pending sequence outlives neither the old generation's timer nor the
// terminal shrinking: the next key resolves under the Frozen filter.
func TestFrozenMidSequenceResolvesTheNextKeyUnderTheFilter(t *testing.T) {
	m, fe, _ := seqModel(t)
	press(m, "a")
	m.width, m.height = minTermWidth-1, minTermHeight-1
	if !m.layoutFrozen() {
		t.Fatal("precondition: the terminal is frozen")
	}
	press(m, "b") // a b would toggle the sidebar; under Frozen only quit survives
	if len(m.pendingKeys) != 0 || len(fe.Invoked) != 0 || len(fe.Keys) != 0 {
		t.Fatalf("frozen: pending=%v invoked=%v keys=%v, want nothing", m.pendingKeys, fe.Invoked, fe.Keys)
	}
	press(m, "q")
	if got := invokedActions(fe); !slices.Equal(got, []string{"husk.quit"}) {
		t.Fatalf("invoked = %v, want husk.quit", got)
	}
}

// A key binding is the input caller with the focus of that very key press; a
// palette choice and a click are the host's own.
func TestInvocationKindAndFocus(t *testing.T) {
	m, fe, _ := seqModel(t)
	press(m, "b")
	if len(fe.Invoked) != 1 {
		t.Fatalf("invoked = %v", fe.Invoked)
	}
	in := fe.Invoked[0]
	if in.Kind != protocol.CallerInput || in.Focus == nil || *in.Focus != (protocol.FocusContext{Surface: "main-stage", Owner: "p"}) {
		t.Errorf("key binding: kind=%q focus=%v, want input with main-stage/p", in.Kind, in.Focus)
	}

	m.openModal("p", "p.panel")
	fe.Invoked = nil
	press(m, "b")
	if in := fe.Invoked[0]; in.Focus == nil || *in.Focus != (protocol.FocusContext{Surface: "modal-surface", Owner: "p"}) {
		t.Errorf("with a modal open the snapshot is %v, want modal-surface/p", in.Focus)
	}
	m.closeModal()

	fe.Invoked = nil
	m.executeCommand("husk.hotspots.toggle_stage")
	m.run("husk.sidebar.toggle", nil)
	if len(fe.Invoked) != 2 {
		t.Fatalf("invoked = %v", fe.Invoked)
	}
	for _, in := range fe.Invoked {
		if in.Kind != protocol.CallerHost || in.Focus != nil {
			t.Errorf("%s: kind=%q focus=%v, want host with no focus", in.Action, in.Kind, in.Focus)
		}
	}
}

// husk.home declines when there is nothing to leave, so esc reaches the
// occupant; once a stage mode is up it applies and the key is consumed.
func TestHomeDeclinesGenerically(t *testing.T) {
	m, fe, _ := seqModel(t)
	m.keys = fe // the built-in esc -> husk.home
	press(m, "esc")
	if got := sentKeys(fe); !slices.Equal(got, []string{"esc"}) {
		t.Fatalf("esc on the home layout: keys = %v, want it delivered to the occupant", got)
	}
	if got := invokedActions(fe); !slices.Equal(got, []string{"husk.home"}) {
		t.Fatalf("the declined action must still have been asked: %v", got)
	}

	fe.Keys, fe.Invoked = nil, nil
	m.stageMode = "hotspots"
	m.setFocus(protocol.SurfaceMainStage)
	press(m, "esc")
	if len(fe.Keys) != 0 || m.stageMode != "plugin" {
		t.Fatalf("esc with a stage mode up: keys=%v stage=%q, want it consumed and home shown", sentKeys(fe), m.stageMode)
	}
}

func TestDeclinedSequenceSendsOnlyTheCompletingKey(t *testing.T) {
	m, fe, _ := seqModel(t)
	press(m, "g")
	press(m, "e") // g e -> husk.home, which declines on the home layout
	if got := sentKeys(fe); !slices.Equal(got, []string{"e"}) {
		t.Fatalf("keys = %v, want only the completing key, no replay of g", got)
	}
	if len(m.pendingKeys) != 0 {
		t.Fatalf("pending = %v", m.pendingKeys)
	}
}

// A plugin action answers later and cannot decline: the key is consumed.
func TestPluginActionConsumesTheKey(t *testing.T) {
	m, fe, _ := seqModel(t)
	press(m, "g")
	press(m, "p")
	if got := invokedActions(fe); !slices.Equal(got, []string{"p.act"}) || fe.Invoked[0].Kind != protocol.CallerInput {
		t.Fatalf("invoked = %+v, want p.act as input", fe.Invoked)
	}
	if len(fe.Keys) != 0 {
		t.Fatalf("keys = %v, want the key consumed", sentKeys(fe))
	}
}

// Without a host the model honours the same sentinel on its own handlers.
func TestNoHostPathDeclinesThroughTheSentinel(t *testing.T) {
	m := goldenModel()
	if m.fe != nil {
		t.Fatal("precondition: no host attached")
	}
	if m.resolveKey("esc") {
		t.Fatal("esc on the home layout must be declined and left to the occupant")
	}
	m.stageMode = "hotspots"
	if !m.resolveKey("esc") || m.stageMode != "plugin" {
		t.Fatalf("esc with a stage mode up: stage=%q, want it consumed and home to run", m.stageMode)
	}
}
