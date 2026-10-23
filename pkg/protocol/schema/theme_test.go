package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestThemeANSIExampleValidates(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(schemaPath(t)), "design", "protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(b), "### ANSI palette roles (minor 2.4)")
	if !ok {
		t.Fatal("missing ANSI palette section")
	}
	_, section, ok = strings.Cut(section, "```json\n")
	if !ok {
		t.Fatal("missing ANSI palette example")
	}
	example, _, ok := strings.Cut(section, "\n```")
	if !ok {
		t.Fatal("unterminated ANSI palette example")
	}
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(example))
	if err != nil {
		t.Fatal(err)
	}
	if err := compileDef(t, "").Validate(value); err != nil {
		t.Fatal(err)
	}
	var msg protocol.Message
	if err := json.Unmarshal([]byte(example), &msg); err != nil {
		t.Fatal(err)
	}
	params, err := jsonschema.UnmarshalJSON(bytes.NewReader(msg.Params))
	if err != nil {
		t.Fatal(err)
	}
	s := compileDef(t, "#/$defs/theme.changed.params")
	if err := s.Validate(params); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ansi any
	}{
		{"string index", map[string]any{"ink": "15"}},
		{"above range", map[string]any{"ink": 16}},
		{"below range", map[string]any{"ink": -1}},
		{"name key", map[string]any{"name": 0}},
		{"unknown key", map[string]any{"future_role": 0}},
		{"null", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params.(map[string]any)["ansi"] = tc.ansi
			if err := s.Validate(wire(t, params)); err == nil {
				t.Fatal("schema accepted malformed ANSI roles")
			}
		})
	}
}
