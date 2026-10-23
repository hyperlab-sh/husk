package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func TestMergeTableAndOrigins(t *testing.T) {
	for _, tc := range []struct {
		name, lower, higher, want string
		clash                     bool
	}{
		{"maps", `{"a":{"x":1,"y":2}}`, `{"a":{"y":3,"z":4}}`, `{"a":{"x":1,"y":3,"z":4}}`, false},
		{"null", `{"a":1,"b":2}`, `{"a":null,"missing":null}`, `{"b":2}`, false},
		{"array", `{"a":[1,2]}`, `{"a":[3]}`, `{"a":[3]}`, false},
		{"scalar", `{"a":1}`, `{"a":2}`, `{"a":2}`, false},
		{"object to scalar", `{"a":{"b":1}}`, `{"a":2}`, `{"a":2}`, true},
		{"scalar to object", `{"a":1}`, `{"a":{"b":2}}`, `{"a":{"b":2}}`, true},
		{"absent object null", `{}`, `{"a":{"b":null}}`, `{"a":{}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fixture(t)
			s := e.open(t, "notes", func(o *Options) { o.Distribution = fstest.MapFS{"config.json": {Data: []byte(tc.lower)}} })
			writeConfig(t, s, []string{"config.json"}, tc.higher)
			got, err := s.Resolve("config", "")
			if err != nil {
				t.Fatal(err)
			}
			want, err := ParseDoc([]byte(tc.want))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Doc, want) {
				t.Fatalf("got %v want %v", got.Doc, want)
			}
			if (len(got.Problems) > 0) != tc.clash {
				t.Fatal(got.Problems)
			}
			for path, layer := range got.Origins {
				if strings.HasPrefix(path, "missing") || layer == "" {
					t.Fatal(got.Origins)
				}
			}
		})
	}
}
func TestLayerOrderFlagsTombstonesAndOneDistribution(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", func(o *Options) {
		o.Distribution = fstest.MapFS{"config.json": {Data: []byte(`{"v":"dist","keep":1}`)}, "profiles/work/config.json": {Data: []byte(`{"v":"dist-profile","dp":true}`)}}
		o.InstanceDocs = map[DocName]Doc{"config": {"v": "flag", "delete": nil}}
	})
	writeConfig(t, s, []string{"config.json"}, `{"v":"user","delete":true}`)
	writeConfig(t, s, []string{"profiles", "work", "config.json"}, `{"v":"profile","p":1}`)
	writeConfig(t, s, []string{"instances", "notes", "config.json"}, `{"v":"instance","i":1}`)
	layers, err := s.Layers("config", "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 4 {
		t.Fatal(layers)
	}
	for i, want := range []Layer{LayerDistribution, LayerUser, LayerProfile, LayerInstance} {
		if layers[i].Layer != want {
			t.Fatal(layers)
		}
	}
	r, err := s.Resolve("config", "work")
	if err != nil {
		t.Fatal(err)
	}
	if r.Doc["v"] != "flag" || r.Doc["dp"] != true || r.Origins["p"] != LayerProfile || r.Origins["v"] != LayerInstance {
		t.Fatal(r)
	}
	if _, exists := r.Doc["delete"]; exists {
		t.Fatal("flag null lost its tombstone", r)
	}
	layers[0].Doc["v"] = "mutated"
	r.Doc["v"] = "mutated"
	again, _ := s.Resolve("config", "work")
	if again.Doc["v"] != "flag" {
		t.Fatal("cached result aliased")
	}
	if profiles := s.Profiles(); !reflect.DeepEqual(profiles, []string{"work"}) {
		t.Fatal(profiles)
	}
	missing, err := s.Resolve("missing", "")
	if err != nil || len(missing.Doc) != 0 {
		t.Fatal(missing, err)
	}
}
func TestHuJSONPatchAndBadReloadPreservesGood(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	writeConfig(t, s, []string{"config.json"}, "{\n // preserve this comment\n \"keep\": 1,\n \"nested\": {\"x\": 1},\n \"remove\": true,\n}\n")
	before, err := s.Resolve("config", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Patch("config", LayerUser, "", Doc{"nested": Doc{"x": float64(2), "new": true}, "remove": nil}); err != nil {
		t.Fatal(err)
	}
	path, _ := pathInside(s.paths.Config, []string{"config.json"}, false)
	raw, _, err := readFile(path, MaxDocBytes, false)
	if err != nil || !strings.Contains(string(raw), "preserve this comment") {
		t.Fatalf("comments lost: %s %v", raw, err)
	}
	r, _ := s.Resolve("config", "")
	if r.Doc["nested"].(Doc)["x"] != float64(2) {
		t.Fatal(r)
	}
	writeConfig(t, s, []string{"config.json"}, "{ broken")
	s.Invalidate()
	bad, err := s.Resolve("config", "")
	if err != nil || !reflect.DeepEqual(bad.Doc, r.Doc) || len(bad.Problems) == 0 {
		t.Fatal(bad, err)
	}
	if err = s.Patch("config", LayerUser, "", Doc{"new": true}); err == nil {
		t.Fatal("repaired corrupt user file")
	}
	if len(before.Doc) == 0 {
		t.Fatal("initial load missing")
	}
	for _, tc := range []struct {
		n DocName
		p string
	}{{"../secret", ""}, {"config", "../work"}, {"bad.dot", ""}} {
		if _, err = s.Resolve(tc.n, tc.p); err == nil {
			t.Fatal("unsafe path accepted", tc)
		}
	}
	if err = s.Patch("config", LayerDistribution, "", Doc{}); err == nil {
		t.Fatal("patched distribution")
	}
}
func TestParseDocLimitsAndDeclaredIsolation(t *testing.T) {
	for _, raw := range []string{"null", "[]", "{", strings.Repeat(" ", MaxDocBytes+1), strings.Repeat(`{"a":`, MaxDepth+2) + "1" + strings.Repeat("}", MaxDepth+2)} {
		if _, err := ParseDoc([]byte(raw)); err == nil {
			t.Fatal("invalid document accepted")
		}
	}
	d, err := ParseDoc([]byte("{/* hi */\"array\":[1,2,],}"))
	if err != nil || len(d["array"].([]any)) != 2 {
		t.Fatal(d, err)
	}
	e := fixture(t)
	s := e.open(t, "notes", nil)
	input := Doc{"nested": Doc{"n": 1}}
	s.Declare(DeclaredKeymap, "example.notes", input)
	input["nested"].(Doc)["n"] = 2
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				s.Declare(DeclaredPlugins, "example.notes", Doc{"a": 1})
				_ = s.Declared(DeclaredPlugins)
			}
		})
	}
	wg.Wait()
	declared := s.Declared(DeclaredKeymap)
	if declared["example.notes"]["nested"].(Doc)["n"] != 1 {
		t.Fatal("declared input aliased")
	}
	declared["example.notes"]["nested"].(Doc)["n"] = 99
	if s.Declared(DeclaredKeymap)["example.notes"]["nested"].(Doc)["n"] != 1 {
		t.Fatal("declared output aliased")
	}
	if len(s.Declared("keymap")) != 1 {
		t.Fatal("declared keymap document is not stored under the name keymap")
	}
	writeConfig(t, s, []string{"keymap.json"}, `{"layer":"user"}`)
	r, err := s.Resolve("keymap", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Doc, Doc{"layer": "user"}) {
		t.Fatal("declared documents leaked into layers:", r.Doc)
	}
	layers, err := s.Layers("keymap", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 || layers[0].Layer != LayerUser || !reflect.DeepEqual(layers[0].Doc, Doc{"layer": "user"}) {
		t.Fatal("declared documents leaked into layers:", layers)
	}
	s.Undeclare("example.notes")
	if len(s.Declared(DeclaredKeymap)) != 0 || len(s.Declared(DeclaredPlugins)) != 0 {
		t.Fatal("Undeclare incomplete")
	}
	if _, err = json.Marshal(r.Doc); err != nil {
		t.Fatal(err)
	}
}
