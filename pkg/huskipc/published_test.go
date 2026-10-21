package huskipc

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPublishedProtocolIsAdditive(t *testing.T) {
	// A legacy client's event envelope must retain its original key and fields.
	original := `{"type":"event_emit","event":{"plugin_id":"a","topic":"a.ready","data":{"ok":true}}}`
	var env Envelope
	if err := json.Unmarshal([]byte(original), &env); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal([]byte(original), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("legacy event wire shape changed: %s", encoded)
	}
	// Optional declarations must not appear on a legacy hello.
	hello, err := json.Marshal(HelloPayload{PluginID: "a", Slots: []SlotID{SlotSidebarList}})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(hello, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"published_slots", "hooks", "events"} {
		if _, found := keys[k]; found {
			t.Fatalf("new field %s present on legacy hello", k)
		}
	}
}

func TestPublishedHelloDeclarationsRoundTrip(t *testing.T) {
	want := HelloPayload{PluginID: "a", Slots: []SlotID{SlotMainStage},
		PublishedSlots: []SlotDeclarePayload{{ID: "a.row", Title: "Actions", Kind: "inline"}, {ID: "a.region", Kind: "region", Rect: &PublishedRect{Surface: SlotMainStage, X: 1, Y: 2, Width: 20, Height: 4}}},
		Hooks:          []HookDeclarePayload{{ID: "a.before"}}, Events: []EventDeclarePayload{{ID: "a.changed"}},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got HelloPayload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("hello declarations changed: %+v", got)
	}
}
