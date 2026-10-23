package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// payload is one method's sample: params and result as the Go values a peer
// would marshal. A nil result means the method has no reply payload of its own.
type payload struct{ params, result any }

// payloads has one row for every protocol method. Typed rows must carry the
// exact Go type the generator maps the method to; placeholder rows (methods
// whose payloads belong to other specs, so the schema is an open object or
// anything) carry a plain object and are checked for the envelope only.
func payloads() map[string]payload {
	hit := []protocol.RowHitTarget{{Line: 0, ID: "r1", Kind: "row", Action: "notes.open", Payload: map[string]string{"k": "v"}}}
	obj := map[string]any{"future": 1}
	return map[string]payload{
		protocol.MethodSessionPing:       {protocol.SessionPingParams{}, protocol.SessionPingResult{ID: "i", PID: 1, ProtocolVersion: protocol.Version, HostVersion: "dev", UptimeMS: 5}},
		protocol.MethodProtocolError:     {protocol.ProtocolErrorParams{Code: protocol.CodeParseError, Message: "bad", Method: "a.b", Data: json.RawMessage(`{"line":1}`)}, nil},
		protocol.MethodSurfaceUpdate:     {protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage, Title: "t", Lines: []string{"x"}, HitTargets: hit, StatusMeta: map[string]any{"n": 1}}, nil},
		protocol.MethodSurfaceClose:      {protocol.SurfaceCloseParams{Surface: protocol.SurfaceModal}, nil},
		protocol.MethodSurfaceResized:    {protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: protocol.SurfaceMainStage, Width: 80, Height: 24, Focused: true, Visible: true}}}, nil},
		protocol.MethodInputKey:          {protocol.SurfaceKeyParams{Surface: protocol.SurfaceMainStage, Key: "ctrl+x", Modifiers: protocol.ModifierState{Ctrl: true}}, nil},
		protocol.MethodInputMouse:        {protocol.SurfaceMouseParams{Surface: protocol.SurfaceMainStage, X: 1, Y: 2, Button: "left", HitID: "r1", HitPayload: map[string]string{"k": "v"}}, nil},
		protocol.MethodInputKeyState:     {protocol.KeyStateParams{Phase: protocol.KeyPhaseDown, Key: "a", Held: true, HeldKeys: []string{"a"}, Surface: protocol.SurfaceMainStage}, nil},
		protocol.MethodThemeChanged:      {protocol.ThemeChangedParams{Palette: protocol.Palette{Name: "p", Canvas: "#000000", Slot: "#111111"}}, nil},
		protocol.MethodToastShow:         {protocol.ToastShowParams{Level: "info", Title: "t", Message: "m"}, nil},
		protocol.MethodSlotContribute:    {protocol.SlotContributeParams{SlotID: "s", ID: "c", Content: "x", OnClick: "notes.open"}, protocol.EmptyResult{}},
		protocol.MethodSlotContributions: {protocol.SlotContributionsParams{SlotID: "s", Contributions: []protocol.SlotContribution{{SlotContributeParams: protocol.SlotContributeParams{SlotID: "s", ID: "c", Content: "x"}, PluginID: "notes"}}}, nil},
		protocol.MethodActionInvoke:      {obj, obj},
		protocol.MethodActionList:        {obj, obj},
		protocol.MethodEventEmit:         {obj, obj},
		protocol.MethodEventDeliver:      {obj, nil},
		protocol.MethodEventWatch:        {obj, obj},
		protocol.MethodHookCall:          {obj, obj},
		protocol.MethodStateGet:          {obj, obj},
		protocol.MethodStateSet:          {obj, obj},
		protocol.MethodStatePatch:        {obj, obj},
		protocol.MethodStateSnapshot:     {obj, obj},
		protocol.MethodStateWatch:        {obj, obj},
		protocol.MethodStateClear:        {obj, obj},
		protocol.MethodStateChanged:      {obj, nil},
		protocol.MethodThemeGet:          {obj, obj},
		protocol.MethodThemeCheck:        {obj, obj},
		protocol.MethodSettingsGet:       {obj, obj},
		protocol.MethodSettingsList:      {obj, obj},
		protocol.MethodSettingsSchema:    {obj, obj},
		protocol.MethodSettingsValidate:  {obj, obj},
		protocol.MethodSecretsGet:        {obj, obj},
		protocol.MethodSecretsNames:      {obj, obj},
		protocol.MethodSecretsSet:        {obj, obj},
		protocol.MethodSecretsGrant:      {obj, obj},
		protocol.MethodSecretsRevoke:     {obj, obj},
		protocol.MethodSecretsReveal:     {obj, obj},
		protocol.MethodKeysList:          {obj, obj},
		protocol.MethodProfileList:       {obj, obj},
		protocol.MethodProfileUse:        {obj, obj},
		protocol.MethodPluginList:        {obj, obj},
		protocol.MethodPluginLog:         {obj, obj},
		protocol.MethodConfigReload:      {obj, obj},
	}
}

func compileDef(t *testing.T, fragment string) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(schemaPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("wire-schema-v2.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("wire-schema-v2.json" + fragment)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// wire marshals v and decodes it the way the validator expects to see JSON.
func wire(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMarshaledMessagesValidateAgainstCommittedSchema marshals a request,
// notification or reply (by the method's form) for every protocol method and
// validates the whole message against the committed schema, then the params and
// result against the method's own definitions.
func TestMarshaledMessagesValidateAgainstCommittedSchema(t *testing.T) {
	root := compileDef(t, "")
	rows := payloads()
	methods := protocol.Methods()
	for _, name := range methods {
		if _, ok := rows[name]; !ok {
			t.Errorf("method %s has no payload row", name)
		}
	}
	for name := range rows {
		if !slices.Contains(methods, name) {
			t.Errorf("payload row for unknown method %s", name)
		}
	}

	var placeholders []string
	for _, name := range methods {
		row, ok := rows[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			ptype, rtype := payloadTypes(name)
			if ptype != nil && reflect.TypeOf(row.params) != ptype {
				t.Fatalf("params row is %T, the schema maps %s to %v", row.params, name, ptype)
			}
			if rtype != nil && reflect.TypeOf(row.result) != rtype {
				t.Fatalf("result row is %T, the schema maps %s to %v", row.result, name, rtype)
			}
			if ptype == nil {
				placeholders = append(placeholders, name)
			}

			params, err := json.Marshal(row.params)
			if err != nil {
				t.Fatal(err)
			}
			id := protocol.NewStringID("1")
			msg := protocol.Message{Method: name, Params: params}
			if form(name) == "request" {
				msg.ID = &id
			}
			if err := root.Validate(wire(t, msg)); err != nil {
				t.Errorf("%s message: %v", form(name), err)
			}
			if err := compileDef(t, "#/$defs/"+name+".params").Validate(wire(t, row.params)); err != nil {
				t.Errorf("params: %v", err)
			}
			if form(name) != "request" {
				return
			}
			result, err := json.Marshal(row.result)
			if err != nil {
				t.Fatal(err)
			}
			if err := root.Validate(wire(t, protocol.Message{ID: &id, Result: result})); err != nil {
				t.Errorf("reply: %v", err)
			}
			if err := compileDef(t, "#/$defs/"+name+".result").Validate(wire(t, row.result)); err != nil {
				t.Errorf("result: %v", err)
			}
		})
	}
	t.Logf("envelope-only placeholder methods: %v", placeholders)
}

// TestSchemaRejectsMalformedMarshaledPayloads keeps the validation test honest:
// the same schema must refuse payloads that break a typed definition.
func TestSchemaRejectsMalformedMarshaledPayloads(t *testing.T) {
	for _, tc := range []struct {
		def   string
		value any
	}{
		{"session.ping.result", map[string]any{"id": "i"}},
		{"input.key_state.params", protocol.KeyStateParams{Phase: "future", Key: "a"}},
		{"surface.resized.params", protocol.SurfaceResizedParams{}},
		{"surface.update.params", map[string]any{"surface": 7, "lines": []string{}}},
	} {
		t.Run(tc.def, func(t *testing.T) {
			if err := compileDef(t, "#/$defs/"+tc.def).Validate(wire(t, tc.value)); err == nil {
				t.Fatal("schema accepted a malformed payload")
			}
		})
	}
}
