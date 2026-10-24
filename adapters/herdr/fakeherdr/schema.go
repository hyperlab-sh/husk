// Package fakeherdr provides an in-process, schema-backed Unix socket server
// for adapter tests. It never invokes the installed CLI or a real server.
package fakeherdr

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed schema.json
var schemaBytes []byte

// Schema returns a detached copy of the bundled upstream API schema.
func Schema() []byte { return append([]byte(nil), schemaBytes...) }

type catalog struct{ root map[string]any }

func loadCatalog() catalog {
	var root map[string]any
	if err := json.Unmarshal(schemaBytes, &root); err != nil {
		panic(err)
	}
	return catalog{root: root}
}

func (c catalog) ref(path string) map[string]any {
	var node any = c.root
	for _, part := range strings.Split(strings.TrimPrefix(path, "#/"), "/") {
		node = node.(map[string]any)[part]
	}
	return node.(map[string]any)
}

// sample builds required fields from the actual schema, not a parallel set of
// response structs. Tests validate the completed replies against that schema.
func (c catalog) sample(s map[string]any) any {
	if ref, ok := s["$ref"].(string); ok {
		return c.sample(c.ref(ref))
	}
	if v, ok := s["const"]; ok {
		return v
	}
	if values, ok := s["enum"].([]any); ok {
		return values[0]
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		if branches, ok := s[key].([]any); ok {
			return c.sample(branches[0].(map[string]any))
		}
	}
	kind := s["type"]
	if kinds, ok := kind.([]any); ok {
		kind = kinds[0]
	}
	switch kind {
	case "object":
		result := map[string]any{}
		required, _ := s["required"].([]any)
		props, _ := s["properties"].(map[string]any)
		for _, key := range required {
			result[key.(string)] = c.sample(props[key.(string)].(map[string]any))
		}
		return result
	case "array":
		return []any{}
	case "string":
		return ""
	case "integer", "number":
		return float64(0)
	case "boolean":
		return false
	default:
		return nil
	}
}

func (c catalog) result(kind string) map[string]any {
	branches := c.ref("#/schemas/success_response/$defs/ResponseResult")["oneOf"].([]any)
	for _, branch := range branches {
		s := branch.(map[string]any)
		if s["properties"].(map[string]any)["type"].(map[string]any)["const"] == kind {
			return c.sample(s).(map[string]any)
		}
	}
	panic(fmt.Sprintf("response type %q missing from bundled schema", kind))
}
