package store

import (
	"os"
	"testing"
	"testing/fstest"
)

func TestBundleTypeClashDoesNotFreezeReloadOrLoseLastGood(t *testing.T) {
	e := fixture(t)
	dir := t.TempDir()
	writeBootstrap(t, dir, "config.json", `{"value":1}`)
	s := e.open(t, "test", func(o *Options) {
		o.Distribution = fstest.MapFS{"config.json": {Data: []byte(`{"value":{"embedded":true}}`)}}
		o.Bundle = os.DirFS(dir)
	})
	for _, raw := range []string{`{"value":1}`, `{"value":2}`, `{`} {
		writeBootstrap(t, dir, "config.json", raw)
		s.Invalidate()
		r, err := s.Resolve("config", "")
		if err != nil {
			t.Fatal(err)
		}
		want := float64(2)
		if raw == `{"value":1}` {
			want = 1
		}
		if r.Doc["value"] != want {
			t.Fatalf("%s: resolved=%+v want value %v", raw, r, want)
		}
		found := false
		for _, p := range r.Problems {
			if p.Code == "type_clash" && p.Layer == LayerDistribution {
				found = true
			}
		}
		if !found {
			t.Fatalf("no distribution clash: %+v", r.Problems)
		}
	}
}
