package store

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestBundleDistributionMemberOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		embedded bool
		want     []string
	}{
		{"bundle only", false, []string{"b", "a", "d", "c"}},
		{"with embedded", true, []string{"z", "a", "e", "b", "d", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t).open(t, "unit", func(o *Options) {
				o.Bundle = fstest.MapFS{
					"catalog.json":              {Data: []byte(`{"entries":{"b":{},"a":{}}}`)},
					"profiles/dev/catalog.json": {Data: []byte(`{"entries":{"d":{},"c":{},"b":{"name":"override"}}}`)},
				}
				if tc.embedded {
					o.Distribution = fstest.MapFS{
						"catalog.json":              {Data: []byte(`{"entries":{"z":{},"a":{}}}`)},
						"profiles/dev/catalog.json": {Data: []byte(`{"entries":{"e":{},"z":{"name":"override"}}}`)},
					}
				}
			})
			layers, err := s.Layers("catalog", "dev")
			if err != nil || len(layers) != 1 || layers[0].Layer != LayerDistribution {
				t.Fatalf("layers=%+v err=%v", layers, err)
			}
			if got := layers[0].OrderedMembers("/entries", layers[0].Doc["entries"].(Doc)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("member order=%v want=%v", got, tc.want)
			}
		})
	}
}

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
