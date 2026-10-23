package host

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func terminalKeyStateFrontend(t *testing.T) (*harness, *fakes, contracts.Frontend, contracts.FrontendSlices) {
	t.Helper()
	noLeaks(t)
	beta := writeFrontendPlugin(t, "beta", []string{string(protocol.SurfaceMainStage)}, map[string]any{
		"rail_items": []any{map[string]any{"id": "beta.panel", "icon": "B", "label": "Panel", "placement": "modal"}},
	})
	h, f, fe := bootFrontendWith(t, nil, alphaPlugin(t), beta)
	var handles contracts.FrontendSlices
	var setupErr error
	h.loop(func() {
		handles, setupErr = fe.OwnSlices()
		if setupErr != nil {
			return
		}
		if _, setupErr = handles.Geometry.Set(geometryOf(map[string][2]int{
			string(protocol.SurfaceSidebarList): {20, 5},
			string(protocol.SurfaceMainStage):   {40, 10},
			string(protocol.SurfaceModal):       {20, 5},
		})); setupErr != nil {
			return
		}
		if _, setupErr = handles.Focus.Set(json.RawMessage(`{"surface":"sidebar-list","terminal_focused":true}`)); setupErr != nil {
			return
		}
		setupErr = fe.UpdateSurfaceView(func(v *contracts.SurfaceView) {
			v.Visible = []protocol.SurfaceID{protocol.SurfaceSidebarList, protocol.SurfaceMainStage}
		})
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	h.loop(func() {
		_, setupErr = h.root.engine.TerminalAttach("alpha", protocol.TerminalAttachParams{Surface: protocol.SurfaceSidebarList}, protocol.TerminalAttachResult{Width: 20, Height: 5}, nil)
	})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	return h, f, fe, handles
}

func recordedKeyStates(t *testing.T, f *fakes, plugin string) []protocol.KeyStateParams {
	t.Helper()
	p := f.proc(plugin)
	p.mu.Lock()
	defer p.mu.Unlock()
	var states []protocol.KeyStateParams
	for _, m := range p.sent {
		if m.Method != protocol.MethodInputKeyState {
			continue
		}
		var params protocol.KeyStateParams
		if err := json.Unmarshal(m.Params, &params); err != nil {
			t.Fatal(err)
		}
		states = append(states, params)
	}
	return states
}

func TestTerminalKeyStateReachesNonTerminalModal(t *testing.T) {
	h, f, fe, handles := terminalKeyStateFrontend(t)
	var err error
	h.loop(func() {
		if _, err = handles.Focus.Set(json.RawMessage(`{"surface":"modal-surface","fallback":"sidebar-list","terminal_focused":true}`)); err != nil {
			return
		}
		err = fe.UpdateSurfaceView(func(v *contracts.SurfaceView) {
			v.Visible = append(v.Visible, protocol.SurfaceModal)
			v.Overlays.Modal = contracts.ModalOverlay{Plugin: "beta", RailItem: "beta.panel"}
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	h.loop(func() {
		if decodeFocus(fe.Snapshot()) != protocol.SurfaceModal || fe.TerminalView(protocol.SurfaceModal) != nil || fe.TerminalView(protocol.SurfaceSidebarList) == nil {
			t.Error("expected a non-terminal modal over an attached terminal")
			return
		}
		// The frontend keeps the underlying focusedSlot in the payload even
		// though its effective-focus gate allows this non-terminal modal.
		params := protocol.KeyStateParams{Phase: protocol.KeyPhaseDown, Key: "x", Held: true, Surface: protocol.SurfaceSidebarList}
		for _, plugin := range []string{"alpha", "beta"} {
			if err := fe.SendKeyState(plugin, params); err != nil {
				t.Error(err)
			}
		}
	})
	for _, plugin := range []string{"alpha", "beta"} {
		got := recordedKeyStates(t, f, plugin)
		if len(got) != 1 || got[0].Key != "x" || got[0].Surface != protocol.SurfaceSidebarList {
			t.Errorf("%s did not receive modal key_state with the underlying surface: %+v", plugin, got)
		}
	}
}

func TestTerminalOccupantKeyStateResumesAfterFocusMoves(t *testing.T) {
	h, f, fe, handles := terminalKeyStateFrontend(t)
	var err error
	h.loop(func() {
		if decodeFocus(fe.Snapshot()) != protocol.SurfaceSidebarList {
			t.Error("terminal did not start focused")
		}
		_, err = handles.Focus.Set(json.RawMessage(`{"surface":"main-stage","terminal_focused":true}`))
	})
	if err != nil {
		t.Fatal(err)
	}
	h.loop(func() {
		if decodeFocus(fe.Snapshot()) != protocol.SurfaceMainStage || fe.TerminalView(protocol.SurfaceSidebarList) == nil {
			t.Error("focus must leave the terminal without detaching it")
			return
		}
		for _, surface := range []protocol.SurfaceID{"", protocol.SurfaceMainStage} {
			if err := fe.SendKeyState("alpha", protocol.KeyStateParams{Phase: protocol.KeyPhaseDown, Key: "x", Held: true, Surface: surface}); err != nil {
				t.Error(err)
			}
		}
	})
	got := recordedKeyStates(t, f, "alpha")
	if len(got) != 2 || got[0].Surface != "" || got[1].Surface != protocol.SurfaceMainStage {
		t.Fatalf("terminal occupant did not resume key_state for omitted and explicit surface: %+v", got)
	}
}
