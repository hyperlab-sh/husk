package main

import (
	"reflect"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func mustDeclare(t *testing.T, r *publicationRegistry, owner, id string) {
	t.Helper()
	if err := r.declareSlot(owner, huskipc.SlotDeclarePayload{ID: id, Kind: "inline"}); err != nil {
		t.Fatal(err)
	}
}

func mustContribute(t *testing.T, r *publicationRegistry, owner, slot, id, content string) {
	t.Helper()
	if _, err := r.contribute(owner, huskipc.SlotContributePayload{SlotID: slot, ID: id, Content: content, OnClick: owner + ".click"}); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryBuiltinsAndOccupancy(t *testing.T) {
	r := newPublicationRegistry()
	for _, s := range huskipc.AllSlots {
		p := r.Slots["husk."+string(s.ID)]
		if p == nil || p.Owner != "husk" || p.Spec.Title != s.Title {
			t.Fatalf("missing core declaration for %s", s.ID)
		}
		if err := r.claimSurface("a", s.ID); err != nil {
			t.Fatal(err)
		}
		if err := r.claimSurface("b", s.ID); err == nil {
			t.Fatalf("allowed occupied slot %s", s.ID)
		}
	}
	if err := r.claimSurface("a", "invented"); err == nil {
		t.Fatal("allowed undeclared surface")
	}
	r.removePlugin("a")
	if len(r.Occupants) != 0 {
		t.Fatal("occupancy leaked")
	}
	if err := r.claimSurface("b", huskipc.SlotSidebarList); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrySlotValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec huskipc.SlotDeclarePayload
	}{
		{"foreign", huskipc.SlotDeclarePayload{ID: "b.action", Kind: "inline"}},
		{"prefix-confusion", huskipc.SlotDeclarePayload{ID: "ab.action", Kind: "inline"}},
		{"bare", huskipc.SlotDeclarePayload{ID: "action", Kind: "inline"}},
		{"empty-name", huskipc.SlotDeclarePayload{ID: "a.", Kind: "inline"}},
		{"invalid-kind", huskipc.SlotDeclarePayload{ID: "a.action", Kind: "pane"}},
		{"inline-rect", huskipc.SlotDeclarePayload{ID: "a.action", Kind: "inline", Rect: &huskipc.PublishedRect{Width: 1, Height: 1}}},
		{"region-no-rect", huskipc.SlotDeclarePayload{ID: "a.action", Kind: "region"}},
		{"region-negative", huskipc.SlotDeclarePayload{ID: "a.action", Kind: "region", Rect: &huskipc.PublishedRect{Surface: huskipc.SlotMainStage, X: -1, Width: 1, Height: 1}}},
		{"region-foreign-surface", huskipc.SlotDeclarePayload{ID: "a.action", Kind: "region", Rect: &huskipc.PublishedRect{Surface: huskipc.SlotSidePanel, Width: 1, Height: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPublicationRegistry()
			_ = r.claimSurface("a", huskipc.SlotMainStage)
			before := len(r.Slots)
			if err := r.declareSlot("a", tc.spec); err == nil {
				t.Fatal("invalid declaration accepted")
			}
			if len(r.Slots) != before {
				t.Fatal("failed declaration mutated registry")
			}
		})
	}
	r := newPublicationRegistry()
	mustDeclare(t, r, "a", "a.action")
	if err := r.declareSlot("a", huskipc.SlotDeclarePayload{ID: "a.action", Kind: "inline"}); err == nil {
		t.Fatal("duplicate declaration accepted")
	}
}

func TestRegistryContributeUpsertAndNamespace(t *testing.T) {
	r := newPublicationRegistry()
	mustDeclare(t, r, "a", "a.action")
	mustContribute(t, r, "b", "a.action", "b.first", "one")
	mustContribute(t, r, "c", "a.action", "c.first", "two")
	out, err := r.contribute("b", huskipc.SlotContributePayload{SlotID: "a.action", ID: "b.first", Content: "updated"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].PluginID != "a" || out[0].Envelope.Type != "slot_contributions" {
		t.Fatalf("bad publisher notification: %+v", out)
	}
	cs := out[0].Envelope.SlotContributions.Contributions
	if len(cs) != 2 || cs[0].Content != "updated" || cs[0].PluginID != "b" || cs[1].ID != "c.first" {
		t.Fatalf("bad upsert order: %+v", cs)
	}
	for _, c := range []huskipc.SlotContributePayload{
		{SlotID: "a.action", ID: "c.fake"}, {SlotID: "a.action", ID: "b.ok", OnClick: "c.fake"}, {SlotID: "missing", ID: "b.ok"},
	} {
		if _, err := r.contribute("b", c); err == nil {
			t.Fatalf("accepted invalid contribution: %+v", c)
		}
	}
	if _, err := r.contribute("a", huskipc.SlotContributePayload{SlotID: "a.action", ID: "a.self"}); err == nil {
		t.Fatal("accepted own-slot contribution")
	}
	// Notification is a snapshot, not a view into mutable registry storage.
	mustContribute(t, r, "b", "a.action", "b.first", "newer")
	if cs[0].Content != "updated" {
		t.Fatal("notification mutated after delivery")
	}
}

func TestRegistryNamedNamespaces(t *testing.T) {
	r := newPublicationRegistry()
	for _, publications := range []map[string]*namedPublication{r.Hooks, r.Events} {
		if err := declareNamed(publications, "a", "b.foreign"); err == nil {
			t.Fatal("foreign namespace accepted")
		}
		if err := declareNamed(publications, "a", "a.own"); err != nil {
			t.Fatal(err)
		}
		if err := declareNamed(publications, "a", "a.own"); err == nil {
			t.Fatal("duplicate named publication accepted")
		}
		if err := subscribe(publications, "b", "unknown"); err == nil {
			t.Fatal("unknown subscription accepted")
		}
	}
}

func TestRegistryHookFanOutAndResults(t *testing.T) {
	r := newPublicationRegistry()
	_ = declareNamed(r.Hooks, "a", "a.before")
	for _, plugin := range []string{"c", "b", "c"} {
		if err := subscribe(r.Hooks, plugin, "a.before"); err != nil {
			t.Fatal(err)
		}
	}
	call := huskipc.HookCallPayload{ID: "a.before", RequestID: "a.123", Data: map[string]any{"n": 1}}
	out, err := r.callHook("a", call)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].PluginID != "c" || out[1].PluginID != "b" {
		t.Fatalf("bad fan-out order: %+v", out)
	}
	if !reflect.DeepEqual(*out[0].Envelope.HookCall, call) {
		t.Fatal("call changed")
	}
	if _, err := r.callHook("a", call); err == nil {
		t.Fatal("duplicate pending request accepted")
	}
	result := huskipc.HookResultPayload{ID: call.ID, RequestID: call.RequestID, PluginID: "spoof", Data: map[string]any{"ok": true}}
	if _, err := r.hookResult("outsider", result); err == nil {
		t.Fatal("unsolicited result accepted")
	}
	out, err = r.hookResult("b", result)
	if err != nil || len(out) != 1 || out[0].PluginID != "a" || out[0].Envelope.HookResult.PluginID != "b" {
		t.Fatalf("bad async result: %+v %v", out, err)
	}
	if _, err := r.hookResult("b", result); err == nil {
		t.Fatal("duplicate result accepted")
	}
	if _, err := r.hookResult("c", result); err != nil {
		t.Fatal(err)
	}
	if len(r.Pending) != 0 {
		t.Fatal("completed request leaked")
	}
	if _, err := r.callHook("a", huskipc.HookCallPayload{ID: "a.before", RequestID: "b.foreign"}); err == nil {
		t.Fatal("foreign request namespace accepted")
	}
}

func TestRegistryEventRouting(t *testing.T) {
	r := newPublicationRegistry()
	_ = declareNamed(r.Events, "a", "a.changed")
	for _, plugin := range []string{"c", "b", "b"} {
		_ = subscribe(r.Events, plugin, "a.changed")
	}
	event := huskipc.EventPayload{PluginID: "spoof", Topic: "a.changed", Data: map[string]any{"state": "ready"}}
	out, err := r.emitEvent("a", event)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].PluginID != "c" || out[1].PluginID != "b" {
		t.Fatalf("routing leaked or duplicate deliveries: %+v", out)
	}
	for _, d := range out {
		if d.Envelope.EventEmit.PluginID != "a" || d.Envelope.EventEmit.Topic != event.Topic {
			t.Fatal("event identity not authenticated")
		}
	}
	if _, err := r.emitEvent("b", event); err == nil {
		t.Fatal("foreign emission accepted")
	}
	if _, err := r.emitEvent("a", huskipc.EventPayload{Topic: "a.undeclared"}); err == nil {
		t.Fatal("undeclared emission accepted")
	}
}

func TestRegistryCleanupOnExit(t *testing.T) {
	r := newPublicationRegistry()
	mustDeclare(t, r, "a", "a.action")
	mustDeclare(t, r, "b", "b.action")
	_ = r.claimSurface("b", huskipc.SlotMainStage)
	mustContribute(t, r, "b", "a.action", "b.action", "button B")
	mustContribute(t, r, "c", "a.action", "c.action", "button C")
	_ = declareNamed(r.Hooks, "a", "a.hook")
	_ = declareNamed(r.Hooks, "b", "b.hook")
	_ = declareNamed(r.Events, "a", "a.event")
	_ = declareNamed(r.Events, "b", "b.event")
	_ = subscribe(r.Hooks, "b", "a.hook")
	_ = subscribe(r.Hooks, "c", "a.hook")
	_ = subscribe(r.Events, "b", "a.event")
	_ = subscribe(r.Events, "c", "a.event")
	_, _ = r.callHook("a", huskipc.HookCallPayload{ID: "a.hook", RequestID: "a.pending"})
	out := r.removePlugin("b")
	if r.Slots["b.action"] != nil || r.Hooks["b.hook"] != nil || r.Events["b.event"] != nil || len(r.Occupants) != 0 {
		t.Fatal("declarations or occupancy leaked")
	}
	if !reflect.DeepEqual(r.Hooks["a.hook"].Subscribers, []string{"c"}) || !reflect.DeepEqual(r.Events["a.event"].Subscribers, []string{"c"}) {
		t.Fatal("subscriptions leaked")
	}
	if len(out) != 2 || out[0].PluginID != "a" || len(out[0].Envelope.SlotContributions.Contributions) != 1 || out[1].Envelope.HookResult.Error == "" {
		t.Fatalf("bad cleanup notifications: %+v", out)
	}
	out = r.removePlugin("c")
	if len(out) != 2 || len(out[0].Envelope.SlotContributions.Contributions) != 0 || len(r.Pending) != 0 {
		t.Fatal("empty contribution list or pending cleanup missing")
	}
	if again := r.removePlugin("c"); len(again) != 0 {
		t.Fatal("cleanup not idempotent")
	}
}

func TestRegistryHookOwnerExitCancelsCalls(t *testing.T) {
	r := newPublicationRegistry()
	_ = declareNamed(r.Hooks, "a", "a.hook")
	_ = subscribe(r.Hooks, "b", "a.hook")
	_ = subscribe(r.Hooks, "c", "a.hook")
	_, _ = r.callHook("d", huskipc.HookCallPayload{ID: "a.hook", RequestID: "d.pending"})
	out := r.removePlugin("a")
	if len(out) != 2 || len(r.Pending) != 0 {
		t.Fatalf("did not cancel calls on publisher exit: %+v", out)
	}
	for _, d := range out {
		if d.PluginID != "d" || d.Envelope.HookResult.Error == "" {
			t.Fatal("bad cancellation result")
		}
	}
}

func TestRegistryHookNoSubscribers(t *testing.T) {
	r := newPublicationRegistry()
	_ = declareNamed(r.Hooks, "a", "a.hook")
	out, err := r.callHook("a", huskipc.HookCallPayload{ID: "a.hook", RequestID: "a.pending"})
	if err != nil || len(out) != 0 || len(r.Pending) != 0 {
		t.Fatal("empty fan-out created pending work")
	}
}
