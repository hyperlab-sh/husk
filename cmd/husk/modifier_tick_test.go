package main

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func tickModel(t *testing.T) (*WorkbenchModel, *protocol.ModifierState, *int) {
	t.Helper()
	m := newWorkbenchModel()
	m.recomputeLayout()
	os := &protocol.ModifierState{}
	polls := new(int)
	m.pollMods = func() (protocol.ModifierState, bool) {
		*polls++
		return *os, true
	}
	return m, os, polls
}

func TestModifierTickStartsOnInitAndOnlyOnce(t *testing.T) {
	m, _, _ := tickModel(t)
	if cmd := m.startModifierTick(); cmd == nil || !m.modTickOn {
		t.Fatal("first start must schedule a tick")
	}
	if cmd := m.startModifierTick(); cmd != nil {
		t.Fatal("second start must not create a second chain")
	}
}

func TestModifierTickContinuesWhileFocused(t *testing.T) {
	m, _, polls := tickModel(t)
	m.startModifierTick()
	_, cmd := m.Update(modifierTickMsg{})
	if cmd == nil || !m.modTickOn {
		t.Fatal("focused tick must reschedule")
	}
	if *polls != 1 {
		t.Fatalf("polls = %d, want 1", *polls)
	}
}

func TestModifierTickStopsOnBlurAndResumesOnFocus(t *testing.T) {
	m, _, polls := tickModel(t)
	m.startModifierTick()

	m.Update(tea.BlurMsg{})
	_, cmd := m.Update(modifierTickMsg{})
	if cmd != nil {
		t.Fatal("tick after blur must not reschedule")
	}
	if m.modTickOn {
		t.Fatal("chain flag must clear when the chain lapses")
	}
	if *polls != 0 {
		t.Fatalf("blurred tick polled the OS %d times", *polls)
	}

	_, cmd = m.Update(tea.FocusMsg{})
	if cmd == nil || !m.modTickOn {
		t.Fatal("focus must restart the chain")
	}
}

func TestModifierFocusBeforeLapseKeepsSingleChain(t *testing.T) {
	m, _, _ := tickModel(t)
	m.startModifierTick()
	m.Update(tea.BlurMsg{})
	if _, cmd := m.Update(tea.FocusMsg{}); cmd != nil {
		t.Fatal("focus while a tick is still in flight must not start a second chain")
	}
	if _, cmd := m.Update(modifierTickMsg{}); cmd == nil {
		t.Fatal("in-flight tick after refocus must continue the chain")
	}
}

func TestBlurClearsOSModifiersButKeepsLatchedAlt(t *testing.T) {
	m, os, _ := tickModel(t)
	os.Ctrl = true
	os.Alt = true
	m.Update(modifierTickMsg{})
	if !m.modifiers.Alt || !m.modifiers.Ctrl {
		t.Fatalf("modifiers not picked up: %+v", m.modifiers)
	}
	m.heldKeys["x"] = time.Now()

	m.Update(tea.BlurMsg{})
	if m.modifiers.Alt || m.modifiers.Ctrl {
		t.Fatalf("blur must clear OS modifiers, got %+v", m.modifiers)
	}
	if len(m.heldKeys) != 0 {
		t.Fatalf("blur must clear held keys, got %v", m.heldKeys)
	}

	m.altLatched = true
	m.modifiers.Alt = true
	m.Update(tea.BlurMsg{})
	if !m.modifiers.Alt {
		t.Fatal("latched Alt must survive blur")
	}
}

func TestBlurredTickIgnoresOSAlt(t *testing.T) {
	m, os, _ := tickModel(t)
	m.Update(tea.BlurMsg{})
	os.Alt = true // another app has Option held
	m.Update(modifierTickMsg{})
	if m.modifiers.Alt {
		t.Fatal("Alt from another focused app leaked into an unfocused Husk")
	}
}

func TestBlurBroadcastsKeyUpToPlugins(t *testing.T) {
	m, os, _ := tickModel(t)
	os.Alt = true
	m.Update(modifierTickMsg{})
	m.plugins["p"] = true
	fe := withFE(m)
	m.Update(tea.BlurMsg{})
	if len(fe.KeyStates) != 1 {
		t.Fatalf("expected exactly one key_state, got %+v", fe.KeyStates)
	}
	ks := fe.KeyStates[0]
	if ks.Plugin != "p" || ks.P.Key != "alt" || ks.P.Phase != "up" || ks.P.Held || ks.P.Modifiers.Alt {
		t.Fatalf("blur broadcast = %+v, want alt up", ks)
	}
}
