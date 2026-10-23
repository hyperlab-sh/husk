package dispatcher_test

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/stem/dispatcher"
)

const pluginsDocs = `{
  "notes": {"id":"notes","actions":[
    {"id":"notes.new","title":"New","public":false},
    {"id":"notes.pub","title":"Pub","public":true},
    {"id":"other.steal","title":"Steal","public":true},
    {"id":"notes.a.b","title":"Dotted name"},
    {"id":"notes.","title":"Empty name"}]},
  "other": {"id":"other","actions":[{"id":"other.thing"}]},
  "husk": {"actions":[{"id":"husk.evil","public":true}]},
  "empty": {"id":"empty"},
  "weird": {"actions":"nope"}
}`

func docCatalog(t *testing.T) *dispatcher.DocCatalog {
	t.Helper()
	var docs map[string]map[string]any
	if err := json.Unmarshal([]byte(pluginsDocs), &docs); err != nil {
		t.Fatal(err)
	}
	host := []dispatcher.ActionDef{{ID: "husk.quit", Owner: "ignored"}, {ID: "husk.pubbed", Public: true}, {ID: ""}}
	return dispatcher.NewDocCatalog(func() map[string]map[string]any { return docs }, func() []dispatcher.ActionDef { return host })
}

func TestDocCatalogLookup(t *testing.T) {
	c := docCatalog(t)
	tests := []struct {
		id   string
		want dispatcher.ActionDef
		ok   bool
	}{
		{"notes.new", dispatcher.ActionDef{ID: "notes.new", Owner: "notes"}, true},
		{"notes.pub", dispatcher.ActionDef{ID: "notes.pub", Owner: "notes", Public: true}, true},
		{"other.thing", dispatcher.ActionDef{ID: "other.thing", Owner: "other"}, true},
		{"husk.quit", dispatcher.ActionDef{ID: "husk.quit", Owner: "husk"}, true},
		{"husk.pubbed", dispatcher.ActionDef{ID: "husk.pubbed", Owner: "husk", Public: true}, true},
		{"other.steal", dispatcher.ActionDef{}, false}, // declared by notes, owned by other
		{"husk.evil", dispatcher.ActionDef{}, false},   // a plugin document cannot claim husk
		{"notes.a.b", dispatcher.ActionDef{}, false},
		{"notes.", dispatcher.ActionDef{}, false},
		{"nobody.x", dispatcher.ActionDef{}, false},
		{"nodot", dispatcher.ActionDef{}, false},
		{".x", dispatcher.ActionDef{}, false},
	}
	for _, tc := range tests {
		got, ok := c.Lookup(tc.id)
		if ok != tc.ok || got != tc.want {
			t.Errorf("Lookup(%q) = %+v, %v; want %+v, %v", tc.id, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDocCatalogList(t *testing.T) {
	var ids []string
	for _, d := range docCatalog(t).List() {
		ids = append(ids, d.ID)
	}
	want := []string{"husk.pubbed", "husk.quit", "notes.new", "notes.pub", "other.thing"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}

func TestDocCatalogNilSources(t *testing.T) {
	c := dispatcher.NewDocCatalog(nil, nil)
	if _, ok := c.Lookup("a.b"); ok || len(c.List()) != 0 {
		t.Fatal("empty catalog has entries")
	}
}

func TestDocCatalogReloads(t *testing.T) {
	docs := map[string]map[string]any{}
	c := dispatcher.NewDocCatalog(func() map[string]map[string]any { return docs }, nil)
	if _, ok := c.Lookup("a.b"); ok {
		t.Fatal("found before declare")
	}
	docs["a"] = map[string]any{"actions": []any{map[string]any{"id": "a.b"}}}
	if _, ok := c.Lookup("a.b"); !ok {
		t.Fatal("not found after declare")
	}
	delete(docs, "a")
	if _, ok := c.Lookup("a.b"); ok {
		t.Fatal("found after undeclare")
	}
}
