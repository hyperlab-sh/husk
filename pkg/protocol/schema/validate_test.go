package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sync"
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
	hit := []protocol.RowHitTarget{{Line: 3, ID: "r1", Kind: "row", Action: "notes.open", Payload: map[string]string{"k": "v"}}}
	mods := protocol.ModifierState{Alt: true, Ctrl: true, Shift: true, Super: true}
	obj := map[string]any{"future": 1}
	yes := true
	return map[string]payload{
		protocol.MethodSessionPing:       {protocol.SessionPingParams{}, protocol.SessionPingResult{ID: "i", PID: 1, ProtocolVersion: protocol.Version, HostVersion: "dev", UptimeMS: 5}},
		protocol.MethodProtocolError:     {protocol.ProtocolErrorParams{Code: protocol.CodeParseError, Message: "bad", Method: "a.b", Data: json.RawMessage(`{"line":1}`)}, nil},
		protocol.MethodSurfaceUpdate:     {protocol.SurfaceUpdateParams{PluginID: "notes", Surface: protocol.SurfaceMainStage, Title: "t", Subtitle: "s", Badge: "b", Lines: []string{"x"}, HitTargets: hit, StatusMeta: map[string]any{"n": 1}, Capture: &yes}, nil},
		protocol.MethodSurfaceClose:      {protocol.SurfaceCloseParams{Surface: protocol.SurfaceModal}, nil},
		protocol.MethodSurfaceResized:    {protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: protocol.SurfaceMainStage, Width: 80, Height: 24, Focused: true, Visible: true}}}, nil},
		protocol.MethodInputKey:          {protocol.SurfaceKeyParams{Surface: protocol.SurfaceMainStage, Key: "ctrl+x", Modifiers: mods}, nil},
		protocol.MethodInputMouse:        {protocol.SurfaceMouseParams{Surface: protocol.SurfaceMainStage, X: 1, Y: 2, Button: "left", HitID: "r1", HitKind: "row", HitAction: "notes.open", HitPayload: map[string]string{"k": "v"}}, nil},
		protocol.MethodInputKeyState:     {protocol.KeyStateParams{Phase: protocol.KeyPhaseDown, Key: "a", Held: true, Modifiers: mods, HeldKeys: []string{"a"}, Surface: protocol.SurfaceMainStage}, nil},
		protocol.MethodThemeChanged:      {protocol.ThemeChangedParams{Palette: protocol.Palette{Name: "#000001", Canvas: "#000002", Surface: "#000003", SurfaceElevated: "#000004", SurfaceSelected: "#000005", SurfaceHover: "#000006", Slot: "#000007", Border: "#000008", BorderSubtle: "#000009", BorderFocused: "#00000a", Ink: "#00000b", InkMuted: "#00000c", InkSubtle: "#00000d", Accent: "#00000e", AccentSecondary: "#00000f", AccentSoft: "#000010", Success: "#000011", Warning: "#000012", Danger: "#000013", Info: "#000014"}}, nil},
		protocol.MethodToastShow:         {protocol.ToastShowParams{PluginID: "notes", Level: "info", Title: "t", Message: "m"}, nil},
		protocol.MethodSlotContribute:    {protocol.SlotContributeParams{SlotID: "s", ID: "c", Content: "x", OnClick: "notes.open"}, protocol.EmptyResult{}},
		protocol.MethodSlotContributions: {protocol.SlotContributionsParams{SlotID: "s", Contributions: []protocol.SlotContribution{{SlotContributeParams: protocol.SlotContributeParams{SlotID: "s", ID: "c", Content: "x", OnClick: "notes.open"}, PluginID: "notes"}}}, nil},
		protocol.MethodActionInvoke:      {obj, obj},
		protocol.MethodActionList:        {obj, obj},
		protocol.MethodEventEmit:         {obj, obj},
		protocol.MethodEventDeliver:      {obj, nil},
		protocol.MethodEventWatch:        {obj, obj},
		protocol.MethodHookCall:          {obj, obj},
		protocol.MethodHookList:          {obj, obj},
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
		protocol.MethodPluginRemove:      {obj, obj},
		protocol.MethodPluginAdd:         {obj, obj},
		protocol.MethodConfigReload:      {obj, obj},
	}
}

var (
	compilerOnce sync.Once
	compiler     *jsonschema.Compiler
	compilerErr  error
)

// compileDef returns the committed schema, or one of its $defs when fragment is
// "#/$defs/<name>". The schema is read and registered once for the whole run.
func compileDef(t *testing.T, fragment string) *jsonschema.Schema {
	t.Helper()
	compilerOnce.Do(func() {
		raw, err := os.ReadFile(schemaPath(t))
		if err != nil {
			compilerErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			compilerErr = err
			return
		}
		compiler = jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		compilerErr = compiler.AddResource("wire-schema-v2.json", doc)
	})
	if compilerErr != nil {
		t.Fatal(compilerErr)
	}
	s, err := compiler.Compile("wire-schema-v2.json" + fragment)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// unpopulated lists every exported field, slice or map element reachable from
// v that is left at its zero value, following pointers and interfaces, so every
// optional property is exercised by a marshaled value.
func unpopulated(path string, v reflect.Value) []string {
	switch v.Kind() {
	case reflect.Struct:
		var out []string
		for i := 0; i < v.NumField(); i++ {
			if f := v.Type().Field(i); f.IsExported() {
				out = append(out, unpopulated(path+"."+f.Name, v.Field(i))...)
			}
		}
		return out
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return []string{path + " is nil"}
		}
		return unpopulated(path, v.Elem())
	case reflect.Slice, reflect.Map:
		if v.Len() == 0 {
			return []string{path + " is empty"}
		}
		var out []string
		if v.Kind() == reflect.Slice {
			for i := 0; i < v.Len(); i++ {
				out = append(out, unpopulated(path+"[]", v.Index(i))...)
			}
			return out
		}
		for _, k := range v.MapKeys() {
			out = append(out, unpopulated(path+"{}", v.MapIndex(k))...)
		}
		return out
	}
	if v.IsZero() {
		return []string{path + " is zero"}
	}
	return nil
}

func requireFullyPopulated(t *testing.T, path string, v reflect.Value) {
	t.Helper()
	for _, problem := range unpopulated(path, v) {
		t.Errorf("%s; populate it so the schema property is exercised", problem)
	}
}

func TestUnpopulatedFollowsPointersAndInterfaces(t *testing.T) {
	type inner struct{ A, B string }
	type outer struct {
		P *inner
		I any
	}
	if got := unpopulated("x", reflect.ValueOf(outer{P: &inner{A: "a", B: "b"}, I: &inner{A: "a", B: "b"}})); len(got) != 0 {
		t.Fatalf("fully populated reported %v", got)
	}
	for name, v := range map[string]outer{
		"nil pointer":       {I: &inner{A: "a", B: "b"}},
		"nil interface":     {P: &inner{A: "a", B: "b"}},
		"half pointer":      {P: &inner{A: "a"}, I: &inner{A: "a", B: "b"}},
		"half in interface": {P: &inner{A: "a", B: "b"}, I: &inner{B: "b"}},
	} {
		if len(unpopulated("x", reflect.ValueOf(v))) == 0 {
			t.Errorf("%s was accepted", name)
		}
	}
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
			} else {
				requireFullyPopulated(t, name+".params", reflect.ValueOf(row.params))
			}
			if rtype != nil {
				requireFullyPopulated(t, name+".result", reflect.ValueOf(row.result))
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

// TestEnvelopeVariantsValidate covers the envelope forms that do not depend on
// the method: a request carrying a token and an error reply with data.
func TestEnvelopeVariantsValidate(t *testing.T) {
	root := compileDef(t, "")
	id := protocol.NewStringID("1")
	for name, msg := range map[string]protocol.Message{
		"request with token": {ID: &id, Method: protocol.MethodSessionPing, Params: json.RawMessage(`{}`), Token: "tok"},
		"error reply":        {ID: &id, Error: &protocol.Error{Code: protocol.CodeNotFound, Message: "x", Data: json.RawMessage(`{"k":1}`)}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := root.Validate(wire(t, msg)); err != nil {
				t.Fatal(err)
			}
		})
	}
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

// TestRootSchemaRejectsMalformedEnvelopes shows the root schema refuses
// envelopes that mix the request, notification and reply forms.
func TestRootSchemaRejectsMalformedEnvelopes(t *testing.T) {
	root := compileDef(t, "")
	failure := map[string]any{"code": "failed", "message": "x"}
	for name, value := range map[string]any{
		"result and error":      map[string]any{"id": 1, "result": map[string]any{}, "error": failure},
		"reply without id":      map[string]any{"result": map[string]any{}},
		"error without id":      map[string]any{"error": failure},
		"reply carrying method": map[string]any{"id": 1, "method": "a.b", "result": map[string]any{}},
		"three segment method":  map[string]any{"id": 1, "method": "a.b.c"},
		"notification result":   map[string]any{"method": "a.b", "result": map[string]any{}},
		"empty object":          map[string]any{},
	} {
		t.Run(name, func(t *testing.T) {
			if err := root.Validate(wire(t, value)); err == nil {
				t.Fatal("root schema accepted a malformed envelope")
			}
		})
	}
}
