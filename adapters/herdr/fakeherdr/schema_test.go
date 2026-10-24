// The committed schema.json is verbatim output from the installed CLI 0.9.5:
//
//	herdr api schema --output adapters/herdr/fakeherdr/schema.json
//
// It reports schema_version 1 and protocol 22. Tests never invoke the CLI or
// contact a real server.
package fakeherdr

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func validator(t *testing.T, fragment string) *jsonschema.Schema {
	t.Helper()
	var document any
	if err := json.Unmarshal(Schema(), &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://adapter.test/schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://adapter.test/schema.json#/schemas/" + fragment)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func jsonValue(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestCommittedSchemaMetadata(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal(Schema(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["schema_version"] != float64(1) || raw["protocol"] != float64(22) || raw["title"] != "Herdr API" {
		t.Fatalf("unexpected provenance: schema_version=%v protocol=%v title=%v", raw["schema_version"], raw["protocol"], raw["title"])
	}
	copy := Schema()
	copy[0] = '!'
	if Schema()[0] != '{' {
		t.Fatal("Schema aliases embedded bytes")
	}
}

func TestSchemaShapes(t *testing.T) {
	success := validator(t, "success_response")
	request := validator(t, "request")
	c := loadCatalog()
	plugin := c.sample(c.ref("#/schemas/success_response/$defs/InstalledPluginInfo")).(map[string]any)
	plugin["plugin_id"] = "fixture"
	plugin["enabled"] = true
	s := &Server{catalog: c, opts: Options{Version: "0.9.5", Plugins: []map[string]any{plugin}}}
	for _, tt := range []struct {
		method string
		params map[string]any
		kind   string
	}{
		{"ping", map[string]any{}, "pong"}, {"plugin.list", map[string]any{}, "plugin_list"},
		{"pane.current", map[string]any{}, "pane_current"},
		{"workspace.get", map[string]any{"workspace_id": "workspace-1"}, "workspace_info"},
		{"tab.get", map[string]any{"tab_id": "tab-1"}, "tab_info"},
		{"popup.close", map[string]any{}, "ok"},
		{"notification.show", map[string]any{"title": "test"}, "notification_show"},
		{"plugin.pane.open", map[string]any{"plugin_id": "fixture", "entrypoint": "main"}, "plugin_pane_opened"},
		{"plugin.pane.open", map[string]any{"plugin_id": "fixture", "entrypoint": "main", "placement": "popup"}, "ok"},
	} {
		t.Run(tt.method+"_"+tt.kind, func(t *testing.T) {
			req := Request{ID: "shape", Method: tt.method, Params: tt.params}
			if err := request.Validate(jsonValue(t, req)); err != nil {
				t.Fatalf("request drift: %v", err)
			}
			result := s.respond(req)
			if result["type"] != tt.kind {
				t.Fatalf("type = %v, want %s", result["type"], tt.kind)
			}
			if err := success.Validate(jsonValue(t, map[string]any{"id": req.ID, "result": result})); err != nil {
				t.Fatalf("response drift: %v", err)
			}
		})
	}
	// Demonstrate that the oracle rejects shape drift, not merely valid JSON.
	broken := s.respond(Request{Method: "ping"})
	delete(broken, "version")
	if err := success.Validate(jsonValue(t, map[string]any{"id": "shape", "result": broken})); err == nil {
		t.Fatal("schema accepted pong without version")
	}
}

func TestEventAndErrorShapes(t *testing.T) {
	event := map[string]any{"event": "pane_focused", "data": map[string]any{"type": "pane_focused", "pane_id": "p", "workspace_id": "w"}}
	if err := validator(t, "event").Validate(jsonValue(t, event)); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"code": "unavailable", "message": "stream failed"}
	if err := validator(t, "error_response").Validate(jsonValue(t, map[string]any{"id": "1", "error": body})); err != nil {
		t.Fatal(err)
	}
	// The defensive id-less fixture reuses this body, but the schema does
	// not define its framing: error_response itself requires an id.
	if err := validator(t, "error_response/$defs/ErrorBody").Validate(jsonValue(t, body)); err != nil {
		t.Fatal(err)
	}
	delete(body, "code")
	if err := validator(t, "error_response/$defs/ErrorBody").Validate(jsonValue(t, body)); err == nil {
		t.Fatal("schema accepted error without code")
	}
}
