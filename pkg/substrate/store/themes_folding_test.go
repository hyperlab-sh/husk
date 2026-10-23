package store

import (
	"reflect"
	"testing"
	"testing/fstest"
)

// Distribution parts compose with Store's ordinary merge rules before Theme
// treats each resolved layer's definitions as units.
func TestThemesDistributionPartsFoldPerRole(t *testing.T) {
	s := fixture(t).open(t, "unit", func(o *Options) {
		o.Distribution = fstest.MapFS{
			"themes.json":              {Data: []byte(`{"version":1,"themes":{"evening":{"palette":{"accent":"#111111","ink":"#222222"}}}}`)},
			"profiles/dev/themes.json": {Data: []byte(`{"themes":{"evening":{"palette":{"accent":"#333333","warning":"#444444"}}}}`)},
		}
		o.Bundle = fstest.MapFS{
			"themes.json":              {Data: []byte(`{"themes":{"evening":{"palette":{"accent":"#555555","info":"#666666"}}}}`)},
			"profiles/dev/themes.json": {Data: []byte(`{"themes":{"evening":{"palette":{"accent":"#777777","success":"#888888"}}}}`)},
		}
	})
	layers, err := s.Layers("themes", "dev")
	if err != nil || len(layers) != 1 || layers[0].Layer != LayerDistribution || layers[0].File != "" {
		t.Fatalf("layers=%+v err=%v", layers, err)
	}
	themes := layers[0].Doc["themes"].(Doc)
	got := themes["evening"].(Doc)["palette"]
	want := Doc{"accent": "#777777", "ink": "#222222", "warning": "#444444", "info": "#666666", "success": "#888888"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("folded roles=%+v want=%+v", got, want)
	}
}
