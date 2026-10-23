package store

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestLayerMemberOrderRetainsFileOrderAndIsDetached(t *testing.T) {
	s := fixture(t).open(t, "unit", func(o *Options) {
		o.Distribution = fstest.MapFS{"catalog.json": {Data: []byte(`{"version":1,"entries":{"z":{},"a":{}}}`)}, "profiles/dev/catalog.json": {Data: []byte(`{"entries":{"b":{},"a":{"name":"override"}}}`)}}
		o.InstanceDocs = map[DocName]Doc{"catalog": {"entries": Doc{"flag": Doc{}}}}
	})
	writeConfig(t, s, []string{"catalog.json"}, `{/* comment */ "version":1,"entries":{"y":{},"x":{},},}`)
	writeConfig(t, s, []string{"instances", "unit", "catalog.json"}, `{"entries":{"z":{}}}`)
	layers, err := s.Layers("catalog", "dev")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]string{{"z", "a", "b"}, {"y", "x"}, {"z", "flag"}} {
		if got := layers[i].OrderedMembers("/entries", layers[i].Doc["entries"].(Doc)); !reflect.DeepEqual(got, want) {
			t.Fatalf("layer %d: %v want %v", i, got, want)
		}
	}
	layers[1].MemberOrder["/entries"][0] = "changed"
	again, err := s.Layers("catalog", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if again[1].MemberOrder["/entries"][0] != "y" {
		t.Fatal("order aliases cache")
	}
}
