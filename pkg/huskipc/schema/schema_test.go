package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

const committedPath = "../../../docs/docs/wire-schema.json"

func mustGenerate(t *testing.T) []byte {
	t.Helper()
	data, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func defs(t *testing.T, data []byte) map[string]map[string]any {
	t.Helper()
	var doc struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Defs
}

func requiredOf(def map[string]any) []string {
	var out []string
	for _, r := range def["required"].([]any) {
		out = append(out, r.(string))
	}
	sort.Strings(out)
	return out
}

func TestCommittedSchemaIsCurrent(t *testing.T) {
	committed, err := os.ReadFile(committedPath)
	if err != nil {
		t.Fatalf("read committed schema: %v (run `go generate ./pkg/huskipc`)", err)
	}
	if !bytes.Equal(committed, mustGenerate(t)) {
		t.Fatal("docs/docs/wire-schema.json is stale: run `go generate ./pkg/huskipc` and commit the result")
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	first := mustGenerate(t)
	for i := 0; i < 5; i++ {
		if !bytes.Equal(first, mustGenerate(t)) {
			t.Fatal("Generate output differs between runs")
		}
	}
}

func TestRequiredFollowsOmitempty(t *testing.T) {
	d := defs(t, mustGenerate(t))

	hello := requiredOf(d["HelloPayload"])
	want := []string{"description", "language", "name", "pid", "plugin_id", "slots", "version"}
	if !reflect.DeepEqual(hello, want) {
		t.Errorf("HelloPayload required = %v, want %v", hello, want)
	}
	for _, optional := range []string{"protocol_version", "rail_items", "commands", "keybindings", "frames", "published_slots"} {
		if _, ok := d["HelloPayload"]["properties"].(map[string]any)[optional]; !ok {
			t.Errorf("HelloPayload lacks optional property %q", optional)
		}
		for _, r := range hello {
			if r == optional {
				t.Errorf("%q must not be required", optional)
			}
		}
	}

	// FrameSpec.Border has no omitempty so it is required; label and close are not.
	if got := requiredOf(d["FrameSpec"]); !reflect.DeepEqual(got, []string{"border"}) {
		t.Errorf("FrameSpec required = %v, want [border]", got)
	}
	// Embedded SlotContributePayload is flattened into SlotContribution.
	if got := requiredOf(d["SlotContribution"]); !reflect.DeepEqual(got, []string{"content", "id", "plugin_id", "slot_id"}) {
		t.Errorf("SlotContribution required = %v", got)
	}
	// Envelope itself requires only the discriminator.
	if got := requiredOf(d["Envelope"]); !reflect.DeepEqual(got, []string{"type"}) {
		t.Errorf("Envelope required = %v, want [type]", got)
	}
}

func TestEnvelopeTypeEnumCoversEveryPayload(t *testing.T) {
	d := defs(t, mustGenerate(t))
	enum := d["Envelope"]["properties"].(map[string]any)["type"].(map[string]any)["enum"].([]any)
	got := map[string]bool{}
	for _, e := range enum {
		got[e.(string)] = true
	}
	typ := reflect.TypeOf(huskipc.Envelope{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "type" {
			continue
		}
		if !got[name] {
			t.Errorf("type enum is missing %q", name)
		}
		delete(got, name)
	}
	if len(got) != 0 {
		t.Errorf("type enum has entries with no Envelope field: %v", got)
	}
}

func validate(t *testing.T, doc string) []string {
	t.Helper()
	errs, err := Validate(mustGenerate(t), []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return errs
}

func TestValidateRejectsBadMessages(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string // substring of one reported violation
	}{
		{"missing type", `{"toast":{}}`, `missing required property "type"`},
		{"unknown type", `{"type":"bogus"}`, "is not one of"},
		{"type without its payload", `{"type":"hello"}`, `missing required property "hello"`},
		{"hello without plugin_id", `{"type":"hello","hello":{"name":"n","version":"1","language":"go","pid":1,"description":"d","slots":[]}}`, `$.hello: missing required property "plugin_id"`},
		{"pid not an integer", `{"type":"hello","hello":{"plugin_id":"p","name":"n","version":"1","language":"go","pid":1.5,"description":"d","slots":[]}}`, "$.hello.pid: want type integer"},
		{"slot id not a string", `{"type":"slot_resize","slot_resize":{"slot_sizes":{"main-stage":{"width":1,"height":1,"focused":true,"visible":"yes"}},"focused_slot":"main-stage"}}`, "$.slot_resize.slot_sizes.main-stage.visible: want type boolean"},
		{"nested array element", `{"type":"slot_update","slot_update":{"plugin_id":"p","slot":"main-stage","lines":["ok",3]}}`, "$.slot_update.lines[1]: want type string"},
		{"payload is not an object", `{"type":"toast","toast":"hi"}`, "$.toast: want type object"},
		{"close_modal is a boolean", `{"type":"close_modal","close_modal":{}}`, "$.close_modal: want type boolean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := validate(t, tc.doc)
			for _, e := range errs {
				if strings.Contains(e, tc.want) {
					return
				}
			}
			t.Fatalf("want a violation containing %q, got %q", tc.want, errs)
		})
	}
}

func TestValidateAcceptsAdditiveAndOptionalShapes(t *testing.T) {
	cases := map[string]string{
		"unknown envelope field":  `{"type":"close_modal","close_modal":true,"future":1}`,
		"unknown payload field":   `{"type":"toast","toast":{"plugin_id":"p","level":"info","title":"t","message":"m","future":{"a":1}}}`,
		"null lines (nil slice)":  `{"type":"slot_update","slot_update":{"plugin_id":"p","slot":"main-stage","lines":null}}`,
		"empty slots array":       `{"type":"hello","hello":{"plugin_id":"p","name":"n","version":"1","language":"go","pid":1,"description":"d","slots":[]}}`,
		"hello with all optional": `{"type":"hello","hello":{"protocol_version":"1.0","plugin_id":"p","name":"n","version":"1","language":"go","pid":1,"description":"d","slots":["main-stage"],"frames":{"main-stage":{"border":true,"label":"x"}}}}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if errs := validate(t, doc); len(errs) != 0 {
				t.Fatalf("unexpected violations: %q", errs)
			}
		})
	}
}

// TestGoMarshaledEnvelopesValidate marshals one envelope per message type with
// the real structs, so a divergence between the encoder and the schema is a
// test failure rather than a plugin author's surprise.
func TestGoMarshaledEnvelopesValidate(t *testing.T) {
	ctx := huskipc.WorkspaceContextPayload{WorkspaceID: "w", WorkspaceLabel: "l", Cwd: "/"}
	envs := []huskipc.Envelope{
		{Type: "hello", Hello: &huskipc.HelloPayload{ProtocolVersion: huskipc.ProtocolVersion, PluginID: "p", Name: "n", Version: "1", Language: "go", PID: 1, Description: "d", Slots: []huskipc.SlotID{huskipc.SlotMainStage},
			PublishedSlots: []huskipc.SlotDeclarePayload{{ID: "s", Title: "t", Description: "d", Kind: "region", Rect: &huskipc.PublishedRect{Surface: huskipc.SlotMainStage}}},
			Frames:         map[huskipc.SlotID]huskipc.FrameSpec{huskipc.SlotMainStage: {Border: true}}}},
		{Type: "welcome", Welcome: &huskipc.WelcomePayload{ProtocolVersion: huskipc.ProtocolVersion, HostVersion: "dev", SlotSizes: map[huskipc.SlotID]huskipc.SlotSize{huskipc.SlotMainStage: {Width: 1, Height: 1}}, Context: ctx}},
		{Type: "slot_update", SlotUpdate: &huskipc.SlotUpdatePayload{PluginID: "p", Slot: huskipc.SlotMainStage}},
		{Type: "slot_resize", SlotResize: &huskipc.SlotResizePayload{}},
		{Type: "slot_key", SlotKey: &huskipc.SlotKeyPayload{Slot: "main-stage", Key: "a"}},
		{Type: "key_state", KeyState: &huskipc.KeyStatePayload{Phase: "down", Key: "alt"}},
		{Type: "slot_mouse", SlotMouse: &huskipc.SlotMousePayload{Slot: huskipc.SlotMainStage, Button: "left"}},
		{Type: "command_invoke", CommandInvoke: &huskipc.CommandInvokePayload{CommandID: "c"}},
		{Type: "theme_changed", ThemeChanged: &huskipc.ThemeChangedPayload{}},
		{Type: "workspace_context", WorkspaceContext: &ctx},
		{Type: "rail_activated", RailActivated: &huskipc.RailActivatedPayload{RailID: "r", Placement: huskipc.PlacementModal, Context: ctx}},
		{Type: "close_modal", CloseModal: true},
		{Type: "event", EventEmit: &huskipc.EventPayload{PluginID: "p", Topic: "t", Data: map[string]any{"a": 1}}},
		{Type: "toast", Toast: &huskipc.ToastPayload{PluginID: "p", Level: "info"}},
		{Type: "slot_declare", SlotDeclare: &huskipc.SlotDeclarePayload{ID: "s", Kind: "inline"}},
		{Type: "slot_contribute", SlotContribute: &huskipc.SlotContributePayload{SlotID: "s", ID: "i"}},
		{Type: "slot_contributions", SlotContributions: &huskipc.SlotContributionsPayload{SlotID: "s", Contributions: []huskipc.SlotContribution{{PluginID: "p"}}}},
		{Type: "hook_declare", HookDeclare: &huskipc.HookDeclarePayload{ID: "h"}},
		{Type: "hook_subscribe", HookSubscribe: &huskipc.HookSubscribePayload{ID: "h"}},
		{Type: "hook_call", HookCall: &huskipc.HookCallPayload{ID: "h", RequestID: "r"}},
		{Type: "hook_result", HookResult: &huskipc.HookResultPayload{ID: "h", RequestID: "r"}},
		{Type: "event_declare", EventDeclare: &huskipc.EventDeclarePayload{ID: "e"}},
		{Type: "event_subscribe", EventSubscribe: &huskipc.EventSubscribePayload{ID: "e"}},
		{Type: "protocol_error", ProtocolError: &huskipc.ProtocolErrorPayload{MessageType: "x", Message: "m"}},
	}
	seen := map[string]bool{}
	for _, env := range envs {
		seen[env.Type] = true
		line, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if errs := validate(t, string(line)); len(errs) != 0 {
			t.Errorf("%s: %s\n  %q", env.Type, line, errs)
		}
	}
	// Every envelope field is exercised, so a new message type must be added here.
	typ := reflect.TypeOf(huskipc.Envelope{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name != "type" && !seen[name] {
			t.Errorf("no marshaled sample for message type %q", name)
		}
	}
}

// TestDolphinFixturesValidate checks the example envelopes the Rust plugin
// crate ships (copied verbatim into testdata) against the schema. They carry
// extra palette keys (space_*), which the additive protocol must tolerate.
func TestDolphinFixturesValidate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "dolphin-envelopes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envs []json.RawMessage
	if err := json.Unmarshal(data, &envs); err != nil {
		t.Fatal(err)
	}
	if len(envs) != 14 {
		t.Fatalf("fixture has %d envelopes, want 14", len(envs))
	}
	types := map[string]bool{}
	for i, raw := range envs {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			t.Fatal(err)
		}
		types[head.Type] = true
		if errs := validate(t, string(raw)); len(errs) != 0 {
			t.Errorf("fixture %d (%s): %q", i, head.Type, errs)
		}
	}
	if len(types) != 14 {
		t.Errorf("fixture covers %d distinct types, want 14: %v", len(types), types)
	}
}
