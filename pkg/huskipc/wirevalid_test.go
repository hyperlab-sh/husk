package huskipc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSlotIDValid(t *testing.T) {
	all := []SlotID{
		SlotActivityRail, SlotSidebarNavigation, SlotSidebarList, SlotThreadRowStatus,
		SlotSidebarFooter, SlotMainHeader, SlotMainStage, SlotSidePanel, SlotAppOverlay,
		SlotModalSurface, SlotFullCanvas, SlotWorkspaceStage,
	}
	for _, s := range all {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []SlotID{"", "Main-Stage", "main-stage ", "nope"} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
	for _, m := range AllSlots {
		if !m.ID.Valid() {
			t.Errorf("AllSlots entry %q is not Valid()", m.ID)
		}
	}
}

func TestKeyPhaseAndPlacementValid(t *testing.T) {
	for _, p := range []KeyPhase{"down", "up", "repeat", "sync"} {
		if !p.Valid() {
			t.Errorf("phase %q should be valid", p)
		}
	}
	for _, p := range []KeyPhase{"", "DOWN", "press"} {
		if p.Valid() {
			t.Errorf("phase %q should be invalid", p)
		}
	}
	for _, p := range []RailPlacement{"modal", "full-canvas", "stage"} {
		if !p.Valid() {
			t.Errorf("placement %q should be valid", p)
		}
	}
	for _, p := range []RailPlacement{"", "Modal", "fullcanvas"} {
		if p.Valid() {
			t.Errorf("placement %q should be invalid", p)
		}
	}
}

func TestPayloadValidate(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr string
	}{
		{"key_state ok", KeyStatePayload{Phase: KeyPhaseDown, Key: "alt", Slot: SlotMainStage}.Validate(), ""},
		{"key_state no slot ok", KeyStatePayload{Phase: KeyPhaseSync, Key: "ctrl"}.Validate(), ""},
		{"key_state bad phase", KeyStatePayload{Phase: "press", Key: "alt"}.Validate(), `unknown phase "press"`},
		{"key_state empty key", KeyStatePayload{Phase: KeyPhaseUp}.Validate(), "key is empty"},
		{"key_state bad slot", KeyStatePayload{Phase: KeyPhaseUp, Key: "a", Slot: "x"}.Validate(), `unknown slot "x"`},
		{"slot_key ok", SlotKeyPayload{Slot: SlotModalSurface, Key: "esc"}.Validate(), ""},
		{"slot_key bad slot", SlotKeyPayload{Slot: "", Key: "esc"}.Validate(), `unknown slot ""`},
		{"slot_key empty key", SlotKeyPayload{Slot: SlotMainStage}.Validate(), "key is empty"},
		{"rail ok", RailItemSpec{ID: "a", Placement: PlacementStage}.Validate(), ""},
		{"rail no id", RailItemSpec{Placement: PlacementStage}.Validate(), "id is empty"},
		{"rail bad placement", RailItemSpec{ID: "a", Placement: "float"}.Validate(), `unknown placement "float"`},
	}
	for _, tc := range tests {
		if tc.wantErr == "" {
			if tc.err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, tc.err)
			}
			continue
		}
		if tc.err == nil || !strings.Contains(tc.err.Error(), tc.wantErr) {
			t.Errorf("%s: error = %v, want containing %q", tc.name, tc.err, tc.wantErr)
		}
	}
}

// The named types must not change the JSON bytes.
func TestNamedWireTypesKeepJSON(t *testing.T) {
	ks, _ := json.Marshal(KeyStatePayload{Phase: KeyPhaseRepeat, Key: "a", Slot: SlotSidePanel})
	want := `{"phase":"repeat","key":"a","held":false,"modifiers":{"alt":false,"ctrl":false,"shift":false,"super":false},"slot":"side-panel"}`
	if string(ks) != want {
		t.Errorf("key_state JSON = %s\nwant %s", ks, want)
	}
	sk, _ := json.Marshal(SlotKeyPayload{Slot: SlotMainStage, Key: "f4"})
	want = `{"slot":"main-stage","key":"f4","modifiers":{"alt":false,"ctrl":false,"shift":false,"super":false}}`
	if string(sk) != want {
		t.Errorf("slot_key JSON = %s\nwant %s", sk, want)
	}
	var back SlotKeyPayload
	if err := json.Unmarshal([]byte(want), &back); err != nil || back.Slot != SlotMainStage {
		t.Errorf("round trip: %+v %v", back, err)
	}
}
