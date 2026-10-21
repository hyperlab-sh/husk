// Package schema derives a JSON Schema (draft 2020-12) for the Husk wire
// protocol from the huskipc Go structs, and holds a small validator for the
// subset of JSON Schema the generator emits.
//
// The schema is descriptive of what the host accepts and emits:
//
//   - a property is "required" exactly when its json tag lacks omitempty, so
//     plugin authors no longer have to infer omitempty per field;
//   - objects never set additionalProperties:false, because the protocol is
//     additive (a minor bump adds optional fields that older peers ignore);
//   - the envelope requires the payload that matches its "type".
package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

const (
	dialect = "https://json-schema.org/draft/2020-12/schema"
	id      = "https://github.com/hyperlab-sh/husk/blob/main/docs/docs/wire-schema.json"
)

// Generate returns the schema document as indented JSON with a trailing
// newline. The output is deterministic.
func Generate() ([]byte, error) {
	g := &generator{defs: map[string]any{}}
	envName := g.define(reflect.TypeOf(huskipc.Envelope{}))

	root := map[string]any{
		"$schema":     dialect,
		"$id":         id,
		"title":       "Husk wire protocol",
		"description": fmt.Sprintf("One JSON line exchanged between Husk and a plugin. Protocol version %s. Generated from pkg/huskipc by go generate; do not edit.", huskipc.ProtocolVersion),
		"$ref":        "#/$defs/" + envName,
		"$defs":       g.defs,
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

type generator struct {
	defs map[string]any
}

// define registers a struct type under $defs and returns its name.
func (g *generator) define(t reflect.Type) string {
	name := t.Name()
	if _, ok := g.defs[name]; ok {
		return name
	}
	g.defs[name] = nil // reserve against recursion
	g.defs[name] = g.object(t)
	return name
}

type field struct {
	name      string
	omitempty bool
	typ       reflect.Type
}

// fields flattens a struct's JSON fields, inlining embedded structs the way
// encoding/json does.
func fields(t reflect.Type) []field {
	var out []field
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
			out = append(out, fields(f.Type)...)
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, field{name: name, omitempty: strings.Contains(opts, "omitempty"), typ: f.Type})
	}
	return out
}

func (g *generator) object(t reflect.Type) map[string]any {
	props := map[string]any{}
	var required []string
	for _, f := range fields(t) {
		props[f.name] = g.typeSchema(f.typ)
		if !f.omitempty {
			required = append(required, f.name)
		}
	}
	obj := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		obj["required"] = required
	}
	if t == reflect.TypeOf(huskipc.Envelope{}) {
		g.envelope(obj, t)
	}
	return obj
}

// envelope adds the message-type vocabulary to the Envelope definition: the
// "type" enum, and per type the payload property that must accompany it.
func (g *generator) envelope(obj map[string]any, t reflect.Type) {
	var types []string
	for _, f := range fields(t) {
		if f.name != "type" {
			types = append(types, f.name)
		}
	}
	sort.Strings(types)
	obj["properties"].(map[string]any)["type"] = map[string]any{
		"type":        "string",
		"description": "Message type. Each type is carried by the property of the same name.",
		"enum":        types,
	}
	rules := make([]any, 0, len(types))
	for _, ty := range types {
		rules = append(rules, map[string]any{
			"if":   map[string]any{"properties": map[string]any{"type": map[string]any{"const": ty}}, "required": []string{"type"}},
			"then": map[string]any{"required": []string{ty}},
		})
	}
	obj["allOf"] = rules
}

func (g *generator) typeSchema(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.Pointer:
		return g.typeSchema(t.Elem())
	case reflect.Struct:
		return map[string]any{"$ref": "#/$defs/" + g.define(t)}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		// A nil Go slice marshals as null, so a required slice also admits null.
		return map[string]any{"type": []string{"array", "null"}, "items": g.typeSchema(t.Elem())}
	case reflect.Map:
		// A nil Go map marshals as null, like a nil slice.
		return map[string]any{"type": []string{"object", "null"}, "additionalProperties": g.typeSchema(t.Elem())}
	case reflect.Interface:
		return map[string]any{}
	}
	panic(fmt.Sprintf("schema: unsupported wire type %s", t))
}
