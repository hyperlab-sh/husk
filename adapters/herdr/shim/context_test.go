package shim

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func shape(t *testing.T, fragment string) *jsonschema.Schema {
	t.Helper()
	var document any
	if err := json.Unmarshal(fakeherdr.Schema(), &document); err != nil {
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

func TestContextGoldensMatchCommittedSchema(t *testing.T) {
	r := newRig(t, "never", false, "")
	var document map[string]any
	if err := json.Unmarshal(fakeherdr.Schema(), &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://adapter.test/schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://adapter.test/schema.json#/schemas/request/$defs/PluginInvocationContext")
	if err != nil {
		t.Fatal(err)
	}
	allowed := document["schemas"].(map[string]any)["request"].(map[string]any)["$defs"].(map[string]any)["PluginInvocationContext"].(map[string]any)["properties"].(map[string]any)
	for _, tc := range []struct{ name, source, correlation, provided string }{{"api", "api", "request-1", ""}, {"startup", "startup", "plugin.startup", ""}, {"event", "api", "pane.focused", ""}, {"proxy", "api", "request-2", `{"invocation_source":"command_palette","correlation_id":"external","focused_pane_id":"provided","workspace_label":"","selected_text":"selection","workspace_id":null,"invented":"drop"}`}} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := BuildContext(context.Background(), upstream.New(upstream.Options{Socket: r.o.Socket, Dial: guardedDial(t, r.o.Socket)}), tc.correlation, tc.source, json.RawMessage(tc.provided))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(c)
			var value map[string]any
			_ = json.Unmarshal(raw, &value)
			for k := range value {
				if _, ok := allowed[k]; !ok {
					t.Fatalf("invented field %s", k)
				}
			}
			if err := schema.Validate(value); err != nil {
				t.Fatal(err)
			}
			got, _ := json.MarshalIndent(value, "", "  ")
			got = append(got, '\n')
			expected, err := os.ReadFile(filepath.Join("testdata", tc.name+".context.golden"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(expected) {
				t.Fatalf("context golden drift:\n%s\nwant:\n%s", got, expected)
			}
		})
	}
}

func TestStateDirectoryMatchesPinnedSource(t *testing.T) {
	for _, tc := range []struct {
		platform string
		env      map[string]string
		want     string
	}{{"linux", map[string]string{"HOME": "/home/u"}, "/home/u/.local/state/herdr"}, {"linux", map[string]string{"HOME": "/home/u", "XDG_STATE_HOME": "/state"}, "/state/herdr"}, {"linux", map[string]string{"XDG_STATE_HOME": ""}, "herdr"}, {"linux", map[string]string{}, "/temp/herdr-state"}, {"windows", map[string]string{"LOCALAPPDATA": "/local", "USERPROFILE": "/profile"}, "/local/herdr"}, {"windows", map[string]string{"USERPROFILE": "/profile"}, "/profile/AppData/Local/herdr"}} {
		got := StateDir(tc.platform, func(k string) (string, bool) { v, ok := tc.env[k]; return v, ok }, "/temp")
		if got != tc.want {
			t.Errorf("state dir=%s want=%s", got, tc.want)
		}
	}
	for id, want := range map[string]string{"example:a": "example%3Aa", "Example": "%45xample", "con": "%con", "example.": "example%2E", "simple": "simple"} {
		if got := PluginPathComponent(id); got != want {
			t.Errorf("component(%s)=%s want=%s", id, got, want)
		}
	}
	if got := PluginPathComponent(strings.Repeat("A", 120)); len(got) != 120 || !strings.HasSuffix(got, "-a4f4256159ea") {
		t.Fatalf("long component=%s", got)
	}
}
