package protocol

import (
	"encoding/json"
	"testing"
)

func TestCallerKindValid(t *testing.T) {
	for _, k := range []CallerKind{CallerCLI, CallerPlugin, CallerInput, CallerHost} {
		if !k.Valid() {
			t.Errorf("%q rejected", k)
		}
	}
	for _, k := range []CallerKind{"", "CLI", "stem", "plugin ", "user"} {
		if k.Valid() {
			t.Errorf("%q accepted", k)
		}
	}
}

func TestCallerWireShape(t *testing.T) {
	for _, tc := range []struct {
		in   Caller
		want string
	}{
		{Caller{Kind: CallerCLI, ConnID: 7}, `{"kind":"cli"}`},
		{Caller{Kind: CallerPlugin, PluginID: "Notes.v2", ConnID: 9}, `{"kind":"plugin","plugin_id":"Notes.v2"}`},
		{Caller{Kind: CallerInput}, `{"kind":"input"}`},
		{Caller{Kind: CallerHost}, `{"kind":"host"}`},
	} {
		b, err := json.Marshal(tc.in)
		if err != nil || string(b) != tc.want {
			t.Errorf("marshal %+v = %s, %v; want %s", tc.in, b, err, tc.want)
		}
	}
}

func TestFocusContextWireShape(t *testing.T) {
	b, _ := json.Marshal(FocusContext{})
	if string(b) != `{"surface":"","owner":""}` {
		t.Fatalf("zero focus = %s", b)
	}
	b, _ = json.Marshal(FocusContext{Surface: "sidebar-list", Owner: "notes"})
	if string(b) != `{"surface":"sidebar-list","owner":"notes"}` {
		t.Fatalf("focus = %s", b)
	}
}
