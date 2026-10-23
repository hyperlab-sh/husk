package catalog

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

type reader struct {
	layers     []store.LayerDoc
	problems   []store.Problem
	calls      []string
	resolveErr error
	layersErr  error
}

func (r *reader) Resolve(n store.DocName, p string) (store.Resolved, error) {
	r.calls = append(r.calls, "resolve:"+string(n)+":"+p)
	return store.Resolved{Problems: r.problems}, r.resolveErr
}
func (r *reader) Layers(n store.DocName, p string) ([]store.LayerDoc, error) {
	r.calls = append(r.calls, "layers:"+string(n)+":"+p)
	return r.layers, r.layersErr
}
func entry() map[string]any { return map[string]any{"source": "~/plugins/alpha"} }
func layer(entries map[string]any) store.LayerDoc {
	return store.LayerDoc{Layer: store.LayerUser, File: "catalog.json", Doc: store.Doc{"version": float64(1), "entries": entries}}
}

func TestCatalogFieldRulesAndLimits(t *testing.T) {
	cases := []struct {
		name, id, field string
		value           any
		valid           bool
	}{
		{"defaults", "alpha", "", nil, true},
		{"id case dots", "A.b-1_", "", nil, true},
		{"id length", "a" + strings.Repeat("b", 63), "", nil, true},
		{"bad id", "_alpha", "", nil, false},
		{"id too long", strings.Repeat("a", 65), "", nil, false},
		{"entry type", "alpha", "entry", true, false},
		{"unknown field", "alpha", "future", true, true},
		{"name unicode limit", "alpha", "name", strings.Repeat("é", 128), true},
		{"name too long", "alpha", "name", strings.Repeat("é", 129), false},
		{"name type", "alpha", "name", 42, false},
		{"description limit", "alpha", "description", strings.Repeat("x", 1024), true},
		{"description too long", "alpha", "description", strings.Repeat("x", 1025), false},
		{"description type", "alpha", "description", nil, false},
		{"source missing", "alpha", "source", nil, false},
		{"source empty", "alpha", "source", "", false},
		{"source absolute", "alpha", "source", "/opt/plugins/alpha", true},
		{"source limit", "alpha", "source", "/" + strings.Repeat("a", 1023), true},
		{"source too long", "alpha", "source", "/" + strings.Repeat("a", 1024), false},
		{"source relative", "alpha", "source", "plugins/alpha", false},
		{"source bad scheme", "alpha", "source", "ftp://example.invalid/a", false},
		{"source no host", "alpha", "source", "https://", false},
		{"source malformed", "alpha", "source", "https://%", false},
		{"source http", "alpha", "source", "http://example.invalid/a", true},
		{"source https", "alpha", "source", "https://example.invalid/a", true},
		{"source git", "alpha", "source", "git://example.invalid/a", true},
		{"source ssh", "alpha", "source", "ssh://example.invalid/a", true},
		{"source git ssh", "alpha", "source", "git+ssh://example.invalid/a", true},
		{"surfaces empty", "alpha", "surfaces", []any{}, true},
		{"surfaces all", "alpha", "surfaces", []any{"activity-rail", "sidebar-navigation", "sidebar-list", "thread-row-status", "sidebar-footer", "main-header", "main-stage", "side-panel", "app-overlay", "modal-surface", "full-canvas", "workspace-stage"}, true},
		{"surfaces unknown", "alpha", "surfaces", []any{"unknown"}, false},
		{"surfaces duplicate", "alpha", "surfaces", []any{"main-stage", "main-stage"}, false},
		{"surfaces type", "alpha", "surfaces", "main-stage", false},
		{"surfaces element type", "alpha", "surfaces", []any{42}, false},
		{"surfaces too many", "alpha", "surfaces", make([]any, 13), false},
		{"tags limit", "alpha", "tags", tags(16), true},
		{"tags too many", "alpha", "tags", tags(17), false},
		{"tags spelling", "alpha", "tags", []any{"A"}, false},
		{"tags empty", "alpha", "tags", []any{""}, false},
		{"tags length", "alpha", "tags", []any{strings.Repeat("a", 32)}, true},
		{"tags too long", "alpha", "tags", []any{strings.Repeat("a", 33)}, false},
		{"tags type", "alpha", "tags", true, false},
		{"tags element type", "alpha", "tags", []any{false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := entry()
			if tc.field != "" {
				raw[tc.field] = tc.value
			}
			var value any = raw
			if tc.field == "entry" {
				value = tc.value
			}
			r := &reader{layers: []store.LayerDoc{layer(map[string]any{tc.id: value})}}
			got := Read(r, "work", "/config")
			if tc.valid {
				if len(got.Entries) != 1 || len(got.Problems) != 0 {
					t.Fatalf("%+v", got)
				}
				if got.Entries[0].SourceLayer != "user" {
					t.Fatal(got)
				}
			} else {
				if len(got.Entries) != 0 || len(got.Problems) != 1 {
					t.Fatalf("%+v", got)
				}
				p := got.Problems[0]
				want := "catalog." + tc.id
				if tc.field != "" && tc.field != "entry" {
					want += "." + tc.field
				}
				if p.Key != want || p.Code != "invalid_value" || p.Severity != "warning" || p.Layer != "user" || p.Source.File != "catalog.json" {
					t.Fatalf("%+v want key %s", p, want)
				}
			}
			if !reflect.DeepEqual(r.calls, []string{"resolve:catalog:work", "layers:catalog:work"}) {
				t.Fatal(r.calls)
			}
		})
	}
}
func tags(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = fmt.Sprintf("tag-%d", i)
	}
	return out
}

func TestCatalogWholeEntryReplacementAndIsolatedLayers(t *testing.T) {
	t.Run("invalid higher entry retains lower definition", func(t *testing.T) {
		lower := layer(map[string]any{"alpha": map[string]any{"source": "/opt/alpha", "name": "Distribution"}})
		lower.Layer = store.LayerDistribution
		higher := layer(map[string]any{"alpha": map[string]any{"source": "relative"}})
		got := Read(&reader{layers: []store.LayerDoc{lower, higher}}, "", "/config")
		if len(got.Entries) != 1 || got.Entries[0].SourceLayer != "distribution" || got.Entries[0].Name != "Distribution" || got.Entries[0].Source != "/opt/alpha" {
			t.Fatal(got)
		}
		if len(got.Problems) != 1 {
			t.Fatal(got)
		}
		p := got.Problems[0]
		if p.Code != "invalid_value" || p.Key != "catalog.alpha.source" || p.Layer != "user" || p.Source.File != "catalog.json" {
			t.Fatal(p)
		}
	})
	for _, version := range []any{nil, float64(2), "1", true} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			lower := layer(map[string]any{"alpha": map[string]any{"source": "/opt/alpha", "name": "Old", "tags": []any{"old"}}, "other": entry()})
			lower.Layer = store.LayerDistribution
			higher := layer(map[string]any{"alpha": entry(), "bad": false})
			bad := layer(map[string]any{"other": entry()})
			bad.Layer = store.LayerProfile
			bad.File = "profiles/dev/catalog.json"
			bad.Doc["version"] = version
			got := Read(&reader{layers: []store.LayerDoc{lower, higher, bad}}, "", "/config")
			if len(got.Entries) != 2 || got.Entries[0].Name != "alpha" || len(got.Entries[0].Tags) != 0 || got.Entries[0].SourceLayer != "user" || got.Entries[1].SourceLayer != "distribution" {
				t.Fatal(got)
			}
			if len(got.Problems) != 2 || got.Problems[1].Key != "catalog.version" {
				t.Fatal(got)
			}
		})
	}
	for _, entries := range []any{nil, []any{}, true} {
		l := layer(nil)
		l.Doc["entries"] = entries
		got := Read(&reader{layers: []store.LayerDoc{l}}, "", "/config")
		if len(got.Problems) != 1 || got.Problems[0].Key != "catalog.entries" {
			t.Fatal(got)
		}
	}
}

func TestCatalogReadErrorsDoNotExposePaths(t *testing.T) {
	for _, stage := range []string{"resolve", "layers"} {
		t.Run(stage, func(t *testing.T) {
			r := &reader{}
			failure := errors.New("open /home/private/catalog.json: permission denied")
			if stage == "resolve" {
				r.resolveErr = failure
			} else {
				r.layersErr = failure
			}
			got := Read(r, "", "/config")
			if len(got.Problems) != 1 {
				t.Fatal(got)
			}
			p := got.Problems[0]
			if p.Code != "store_error" || p.Severity != "warning" || p.Layer != "" || p.Message != "catalog.json: configuration could not be loaded" || p.Source.File != "catalog.json" {
				t.Fatal(p)
			}
		})
	}
}

func TestCatalogEntryLimitUsesDocumentOrder(t *testing.T) {
	entries := map[string]any{}
	order := []string{}
	for i := 256; i >= 0; i-- {
		id := fmt.Sprintf("p%03d", i)
		entries[id] = entry()
		order = append(order, id)
	}
	l := layer(entries)
	l.MemberOrder = map[string][]string{"/entries": order}
	got := Read(&reader{layers: []store.LayerDoc{l}}, "", "/config")
	if len(got.Entries) != 256 || len(got.Problems) != 1 || got.Problems[0].Key != "catalog.entries" || got.Problems[0].Message != "more than 256 entries; the rest ignored" {
		t.Fatal(got)
	}
	for _, e := range got.Entries {
		if e.ID == "p000" {
			t.Fatal("retained 257th entry")
		}
	}
}

func TestCatalogLoadProblemsAndEmptyBuiltin(t *testing.T) {
	empty := Read(&reader{}, "", "/config")
	if len(empty.Entries) != 0 || len(empty.Problems) != 0 {
		t.Fatal(empty)
	}
	r := &reader{problems: []store.Problem{{Code: "invalid_document", Layer: store.LayerProfile, File: "/config/profiles/dev/catalog.json", Message: "parse failure", Line: 4}, {Code: "type_clash"}}, layers: []store.LayerDoc{layer(map[string]any{"alpha": entry()})}}
	got := Read(r, "", "/config")
	if len(got.Entries) != 1 || len(got.Problems) != 1 {
		t.Fatal(got)
	}
	p := got.Problems[0]
	if p.Code != "store_error" || p.Severity != "warning" || p.Source.File != "profiles/dev/catalog.json" || p.Source.Line != 4 || !strings.Contains(p.Message, "parse failure") {
		t.Fatal(p)
	}
}

func TestImportsStayWithinAllowList(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"github.com/hyperlab-sh/husk/pkg/protocol": true, "github.com/hyperlab-sh/husk/internal/substrate/store": true, "github.com/hyperlab-sh/husk/internal/services/settings": true}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".go" || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(strings.Split(path, "/")[0], ".") && !allowed[path] {
				t.Errorf("forbidden import %s", path)
			}
		}
	}
}
