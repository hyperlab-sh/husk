// Package schema generates the additive draft 2020-12 schema for protocol v2.
// Envelope constraints are explicit; payload definitions follow Go JSON tags.
// Methods whose payloads belong to other specs have descriptive open schemas,
// not invented field requirements.
package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Generate returns deterministic, indented JSON with a trailing newline.
func Generate() ([]byte, error) {
	g := generator{defs: make(map[string]any)}
	g.define(reflect.TypeFor[protocol.Message]())
	g.define(reflect.TypeFor[protocol.FrameSpec]())
	methods := make(map[string]any)
	for _, method := range protocol.Methods() {
		params, result := payloadTypes(method)
		p := map[string]any{"type": "object", "description": "Payload owned by " + owner(method) + "; no additional field constraints specified here."}
		r := map[string]any{"description": "Result owned by " + owner(method) + "; no additional field constraints specified here."}
		if params != nil {
			p = g.typeSchema(params)
		}
		if result != nil {
			r = g.typeSchema(result)
		}
		g.defs[method+".params"] = p
		g.defs[method+".result"] = r
		methods[method] = map[string]any{"params": ref(method + ".params"), "result": ref(method + ".result"), "form": form(method)}
	}
	root := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         "https://github.com/hyperlab-sh/husk/blob/main/docs/docs/wire-schema-v2.json",
		"title":       "Husk wire protocol v2",
		"description": fmt.Sprintf("One UTF-8 JSON object per line, at most %d bytes. Protocol %s. Generated from pkg/protocol; do not edit. x-methods indexes payload schemas; unknown fields are accepted.", protocol.MaxLineBytes, protocol.Version),
		"$ref":        "#/$defs/Message", "$defs": g.defs, "x-methods": methods,
	}
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func owner(method string) string {
	ns, _, _ := strings.Cut(method, ".")
	switch ns {
	case "state":
		return "design/state.md"
	case "hook":
		return "design/hooks.md"
	case "action", "event", "slot":
		return "design/message-kinds.md"
	case "settings", "theme", "profile", "secrets", "config":
		return "design/services.md"
	case "plugin":
		return "design/pods.md"
	case "keys":
		return "design/keybindings.md"
	default:
		return "design/protocol.md"
	}
}

func form(method string) string {
	switch method {
	case protocol.MethodTerminalWrite, protocol.MethodTerminalDetach:
		return "request or notification"
	case protocol.MethodProtocolError, protocol.MethodEventDeliver, protocol.MethodStateChanged,
		protocol.MethodSurfaceUpdate, protocol.MethodSurfaceClose, protocol.MethodSurfaceResized,
		protocol.MethodInputKey, protocol.MethodInputMouse, protocol.MethodInputKeyState,
		protocol.MethodThemeChanged, protocol.MethodSlotContributions, protocol.MethodToastShow,
		protocol.MethodTerminalInput:
		return "notification"
	default:
		return "request"
	}
}

func payloadTypes(method string) (reflect.Type, reflect.Type) {
	switch method {
	case protocol.MethodEventDeliver:
		return reflect.TypeFor[protocol.EventDeliverParams](), nil
	case protocol.MethodSessionPing:
		return reflect.TypeFor[protocol.SessionPingParams](), reflect.TypeFor[protocol.SessionPingResult]()
	case protocol.MethodProtocolError:
		return reflect.TypeFor[protocol.ProtocolErrorParams](), nil
	case protocol.MethodSurfaceOpen:
		return reflect.TypeFor[protocol.SurfaceOpenParams](), reflect.TypeFor[protocol.SurfaceOpenResult]()
	case protocol.MethodSurfaceUpdate:
		return reflect.TypeFor[protocol.SurfaceUpdateParams](), nil
	case protocol.MethodSurfaceClose:
		return reflect.TypeFor[protocol.SurfaceCloseParams](), nil
	case protocol.MethodSurfaceResized:
		return reflect.TypeFor[protocol.SurfaceResizedParams](), nil
	case protocol.MethodTerminalAttach:
		return reflect.TypeFor[protocol.TerminalAttachParams](), reflect.TypeFor[protocol.TerminalAttachResult]()
	case protocol.MethodTerminalWrite:
		return reflect.TypeFor[protocol.TerminalWriteParams](), reflect.TypeFor[protocol.EmptyResult]()
	case protocol.MethodTerminalDetach:
		return reflect.TypeFor[protocol.TerminalDetachParams](), reflect.TypeFor[protocol.EmptyResult]()
	case protocol.MethodTerminalInput:
		return reflect.TypeFor[protocol.TerminalInputParams](), nil
	case protocol.MethodInputKey:
		return reflect.TypeFor[protocol.SurfaceKeyParams](), nil
	case protocol.MethodInputMouse:
		return reflect.TypeFor[protocol.SurfaceMouseParams](), nil
	case protocol.MethodInputKeyState:
		return reflect.TypeFor[protocol.KeyStateParams](), nil
	case protocol.MethodThemeChanged:
		return reflect.TypeFor[protocol.ThemeChangedParams](), nil
	case protocol.MethodToastShow:
		return reflect.TypeFor[protocol.ToastShowParams](), nil
	case protocol.MethodSlotContribute:
		return reflect.TypeFor[protocol.SlotContributeParams](), reflect.TypeFor[protocol.EmptyResult]()
	case protocol.MethodSlotContributions:
		return reflect.TypeFor[protocol.SlotContributionsParams](), nil
	default:
		return nil, nil
	}
}

type generator struct{ defs map[string]any }

func ref(name string) map[string]any { return map[string]any{"$ref": "#/$defs/" + name} }

func (g *generator) define(t reflect.Type) string {
	name := t.Name()
	if _, exists := g.defs[name]; exists {
		return name
	}
	g.defs[name] = nil
	props := make(map[string]any)
	var required []string
	var fields func(reflect.Type)
	fields = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
				fields(f.Type)
				continue
			}
			if name == "" {
				name = f.Name
			}
			props[name] = g.typeSchema(f.Type)
			if !strings.Contains(opts, "omitempty") {
				required = append(required, name)
			}
		}
	}
	fields(t)
	obj := map[string]any{"type": "object", "properties": props}
	if len(required) != 0 {
		obj["required"] = required
	}
	if t == reflect.TypeFor[protocol.Message]() {
		props["method"] = map[string]any{"type": "string", "pattern": "^[a-z0-9_-]+\\.[a-z0-9_-]+$", "maxLength": protocol.MaxMethodBytes}
		props["params"] = map[string]any{"type": "object"}
		obj["oneOf"] = []any{
			variant([]string{"id", "method"}, "result", "error"),
			variant([]string{"method"}, "id", "result", "error"),
			variant([]string{"id", "result"}, "method", "params", "error", "token"),
			variant([]string{"id", "error"}, "method", "params", "result", "token"),
		}
	}
	if t == reflect.TypeFor[protocol.RowHitTarget]() {
		props["col_start"].(map[string]any)["minimum"] = 0
		props["col_end"].(map[string]any)["minimum"] = 1
		obj["dependentRequired"] = map[string]any{
			"col_start": []string{"col_end"},
			"col_end":   []string{"col_start"},
		}
		// Standard JSON Schema cannot compare sibling numeric properties.
		// RowHitTarget.Validate enforces the relative bound at ingestion.
		obj["description"] = "Optional surface-relative cell range [col_start, col_end); both or neither, with col_end > col_start >= 0. The host validates the relative bound."
	}
	if t == reflect.TypeFor[protocol.SurfaceOpenParams]() {
		props["placement"] = map[string]any{"type": "string", "enum": []string{"modal", "canvas"}}
		props["name"] = map[string]any{"type": "string", "pattern": "^[a-z0-9_-]{1,32}$", "maxLength": 32}
		props["title"] = map[string]any{"type": "string", "maxLength": 80}
	}
	if t == reflect.TypeFor[protocol.SurfaceResizedParams]() {
		// The v2 snapshot contract explicitly requires an array, not null.
		props["surfaces"].(map[string]any)["type"] = "array"
	}
	if t == reflect.TypeFor[protocol.Error]() || t == reflect.TypeFor[protocol.ProtocolErrorParams]() {
		props["data"] = map[string]any{"type": "object"}
	}
	g.defs[name] = obj
	return name
}

func variant(required []string, absent ...string) map[string]any {
	var forbidden []any
	for _, name := range absent {
		forbidden = append(forbidden, map[string]any{"required": []string{name}})
	}
	return map[string]any{"required": required, "not": map[string]any{"anyOf": forbidden}}
}

func (g *generator) typeSchema(t reflect.Type) map[string]any {
	if t == reflect.TypeFor[protocol.ANSIRoles]() {
		var roles []string
		palette := reflect.TypeFor[protocol.Palette]()
		for i := 0; i < palette.NumField(); i++ {
			role, _, _ := strings.Cut(palette.Field(i).Tag.Get("json"), ",")
			if role != "name" {
				roles = append(roles, role)
			}
		}
		return map[string]any{
			"type":                 "object",
			"propertyNames":        map[string]any{"enum": roles},
			"additionalProperties": map[string]any{"type": "integer", "minimum": 0, "maximum": 15},
		}
	}
	if t == reflect.TypeFor[protocol.CaptureValue]() {
		return map[string]any{"oneOf": []any{map[string]any{"type": "boolean"}, map[string]any{"type": "string", "enum": []string{"text", "all"}}}}
	}
	if t == reflect.TypeFor[protocol.SurfaceDimension]() {
		return map[string]any{"oneOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "string", "pattern": "^[0-9]+%$"}}}
	}
	if t == reflect.TypeFor[json.RawMessage]() {
		return map[string]any{}
	}
	if t == reflect.TypeFor[protocol.ID]() {
		return map[string]any{"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "integer", "minimum": -9007199254740991, "maximum": 9007199254740991}}}
	}
	if t == reflect.TypeFor[protocol.Code]() {
		return map[string]any{
			"type":           "string",
			"description":    "Known codes are listed in x-known-values. Unknown future codes are accepted and decoded as internal.",
			"x-known-values": protocol.Codes(),
		}
	}
	if t == reflect.TypeFor[protocol.KeyPhase]() {
		return map[string]any{"type": "string", "enum": []protocol.KeyPhase{protocol.KeyPhaseDown, protocol.KeyPhaseUp, protocol.KeyPhaseRepeat, protocol.KeyPhaseSync}}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return g.typeSchema(t.Elem())
	case reflect.Struct:
		return ref(g.define(t))
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
		return map[string]any{"type": []string{"array", "null"}, "items": g.typeSchema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": []string{"object", "null"}, "additionalProperties": g.typeSchema(t.Elem())}
	case reflect.Interface:
		return map[string]any{}
	default:
		panic(fmt.Sprintf("schema: unsupported type %v", t))
	}
}
