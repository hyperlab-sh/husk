package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func generated(t *testing.T) []byte {
	t.Helper()
	b, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func schemaPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "docs", "docs", "wire-schema-v2.json")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestCommittedSchemaIsCurrent(t *testing.T) {
	b, err := os.ReadFile(schemaPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, generated(t)) {
		t.Fatal("wire-schema-v2.json is stale: run go generate ./pkg/protocol")
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	first := generated(t)
	for i := 0; i < 5; i++ {
		if !bytes.Equal(first, generated(t)) {
			t.Fatal("nondeterministic output")
		}
	}
}

func document(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(generated(t), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestMethodsAndReferences(t *testing.T) {
	doc := document(t)
	defs := doc["$defs"].(map[string]any)
	methods := doc["x-methods"].(map[string]any)
	if len(methods) != len(protocol.Methods()) {
		t.Fatal("method index incomplete")
	}
	for _, name := range protocol.Methods() {
		entry := methods[name].(map[string]any)
		for _, kind := range []string{"params", "result"} {
			if _, ok := defs[name+"."+kind]; !ok {
				t.Fatalf("missing %s.%s", name, kind)
			}
			if entry[kind].(map[string]any)["$ref"] != "#/$defs/"+name+"."+kind {
				t.Fatal(entry)
			}
		}
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if r, ok := v["$ref"].(string); ok {
				if _, exists := defs[r[len("#/$defs/"):]]; !exists {
					t.Errorf("dangling reference %s", r)
				}
			}
			if v["additionalProperties"] == false {
				t.Error("schema rejects additive fields")
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(doc)
}

func TestTerminalWriteAndDetachSchemaContracts(t *testing.T) {
	doc := document(t)
	methods := doc["x-methods"].(map[string]any)
	defs := doc["$defs"].(map[string]any)
	for _, method := range []string{protocol.MethodTerminalWrite, protocol.MethodTerminalDetach} {
		t.Run(method, func(t *testing.T) {
			if got := methods[method].(map[string]any)["form"]; got != "request or notification" {
				t.Fatalf("form = %v, want request or notification", got)
			}
			result := defs[method+".result"].(map[string]any)
			if result["$ref"] != "#/$defs/EmptyResult" {
				t.Fatal("acknowledgement must use the empty-object result schema")
			}
			if !schemaAccepts(result, map[string]any{}, defs) || schemaAccepts(result, nil, defs) || schemaAccepts(result, "not an object", defs) {
				t.Fatal("acknowledgement schema must accept {} and reject non-objects")
			}
		})
	}
}

func TestPayloadRequiredFieldsAndEnums(t *testing.T) {
	defs := document(t)["$defs"].(map[string]any)
	ping := defs["SessionPingResult"].(map[string]any)
	want := []any{"id", "pid", "protocol_version", "host_version", "uptime_ms"}
	if !reflect.DeepEqual(ping["required"], want) {
		t.Fatal(ping["required"])
	}
	keyProps := defs["SurfaceKeyParams"].(map[string]any)["properties"].(map[string]any)
	if keyProps["surface"] == nil || keyProps["slot"] != nil {
		t.Fatal(keyProps)
	}
	resizeProps := defs["SurfaceResizedParams"].(map[string]any)["properties"].(map[string]any)
	if resizeProps["surfaces"].(map[string]any)["type"] != "array" {
		t.Fatal("surface.resized snapshot must be an array, not null")
	}
	codeSchema := defs["Error"].(map[string]any)["properties"].(map[string]any)["code"].(map[string]any)
	codes := codeSchema["x-known-values"].([]any)
	if len(codes) != len(protocol.Codes()) || codeSchema["enum"] != nil || codeSchema["type"] != "string" {
		t.Fatal(codeSchema)
	}
}

// schemaAccepts exercises the envelope subset independently of Message's decoder.
func schemaAccepts(schema map[string]any, value any, defs map[string]any) bool {
	if r, ok := schema["$ref"].(string); ok {
		return schemaAccepts(defs[r[len("#/$defs/"):]].(map[string]any), value, defs)
	}
	if one, ok := schema["oneOf"].([]any); ok {
		n := 0
		for _, option := range one {
			if schemaAccepts(option.(map[string]any), value, defs) {
				n++
			}
		}
		if n != 1 {
			return false
		}
	}
	if anyOf, ok := schema["anyOf"].([]any); ok {
		n := 0
		for _, option := range anyOf {
			if schemaAccepts(option.(map[string]any), value, defs) {
				n++
			}
		}
		if n == 0 {
			return false
		}
	}
	if not, ok := schema["not"].(map[string]any); ok && schemaAccepts(not, value, defs) {
		return false
	}
	if ty, ok := schema["type"].(string); ok {
		switch ty {
		case "object":
			if _, ok := value.(map[string]any); !ok {
				return false
			}
		case "string":
			if _, ok := value.(string); !ok {
				return false
			}
		case "integer":
			if n, ok := value.(float64); !ok || n != float64(int64(n)) {
				return false
			}
		}
	}
	if obj, ok := value.(map[string]any); ok {
		if required, ok := schema["required"].([]any); ok {
			for _, key := range required {
				if _, exists := obj[key.(string)]; !exists {
					return false
				}
			}
		}
		if props, ok := schema["properties"].(map[string]any); ok {
			for key, prop := range props {
				if v, exists := obj[key]; exists && !schemaAccepts(prop.(map[string]any), v, defs) {
					return false
				}
			}
		}
	}
	if str, ok := value.(string); ok {
		if pattern, ok := schema["pattern"].(string); ok && !regexp.MustCompile(pattern).MatchString(str) {
			return false
		}
		if max, ok := schema["maxLength"].(float64); ok && float64(len(str)) > max {
			return false
		}
	}
	if n, ok := value.(float64); ok {
		if min, ok := schema["minimum"].(float64); ok && n < min {
			return false
		}
		if max, ok := schema["maximum"].(float64); ok && n > max {
			return false
		}
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, option := range enum {
			if reflect.DeepEqual(option, value) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestEnvelopeSchema(t *testing.T) {
	doc := document(t)
	defs := doc["$defs"].(map[string]any)
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{"id":7,"method":"action.invoke","params":{}}`, true},
		{`{"method":"input.key","params":{"future":1},"jsonrpc":"2.0","future":42}`, true},
		{`{"id":"x","result":null}`, true},
		{`{"id":"x","result":[1]}`, true},
		{`{"id":1,"error":{"code":"failed","message":"x","data":{"future":1}}}`, true},
		{`{"id":1,"error":{"code":"future_minor_code","message":"x"}}`, true},
		{`{}`, false}, {`[]`, false}, {`{"id":1}`, false},
		{`{"id":null,"method":"a.b"}`, false},
		{`{"id":9007199254740992,"method":"a.b"}`, false},
		{`{"id":1,"method":"a.b.c"}`, false},
		{`{"method":"a.b","params":[]}`, false},
		{`{"id":1,"result":{},"error":{"code":"failed","message":"x"}}`, false},
		{`{"id":1,"result":{},"params":{}}`, false},
		{`{"id":1,"error":null}`, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.raw), &value); err != nil {
				t.Fatal(err)
			}
			if got := schemaAccepts(doc, value, defs); got != tc.valid {
				t.Fatalf("valid=%v want %v", got, tc.valid)
			}
		})
	}
}

func TestHookMethodsAreOwnedByTheHooksSpec(t *testing.T) {
	defs := document(t)["$defs"].(map[string]any)
	for _, name := range []string{protocol.MethodHookCall, protocol.MethodHookList} {
		for _, kind := range []string{"params", "result"} {
			desc := defs[name+"."+kind].(map[string]any)["description"].(string)
			if !strings.Contains(desc, "design/hooks.md") {
				t.Errorf("%s.%s description %q does not name design/hooks.md", name, kind, desc)
			}
		}
	}
}
