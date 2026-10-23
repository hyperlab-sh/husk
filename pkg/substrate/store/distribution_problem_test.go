package store

import (
	"testing"
	"testing/fstest"
)

func TestDistributionLoadProblemsNameRelativeFiles(t *testing.T) {
	for _, doc := range []DocName{"config", "catalog"} {
		for _, profile := range []string{"", "dev"} {
			file := string(doc) + ".json"
			if profile != "" {
				file = "profiles/" + profile + "/" + file
			}
			t.Run(file, func(t *testing.T) {
				s := fixture(t).open(t, "unit", func(o *Options) { o.Distribution = fstest.MapFS{file: {Data: []byte("{broken")}} })
				resolved, err := s.Resolve(doc, profile)
				if err != nil {
					t.Fatal(err)
				}
				if len(resolved.Problems) != 1 {
					t.Fatal(resolved.Problems)
				}
				p := resolved.Problems[0]
				if p.Code != "invalid_document" || p.Layer != LayerDistribution || p.File != file {
					t.Fatalf("got %+v want file %s", p, file)
				}
			})
		}
	}
}
