package settings

import (
	"errors"
	"testing"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

func TestDuplicateSettingSpellings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		text      string
		duplicate bool
		want      int64
	}{
		{"dotted first", `{"dist":{"a.b":1,"a":{"b":2},"other":9}}`, true, 7},
		{"nested first", `{"dist":{"a":{"b":2},"a.b":1,"other":9}}`, true, 7},
		{"equal values", `{"dist":{"a.b":2,"a":{"b":2},"other":9}}`, true, 7},
		{"dotted only", `{"dist":{"a.b":1,"other":9}}`, false, 1},
		{"nested only", `{"dist":{"a":{"b":2},"other":9}}`, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(Options{Schema: []Entry{
				{Key: "dist.a.b", Owner: "dist", Type: TypeInt, Default: int64(3)},
				{Key: "dist.other", Owner: "dist", Type: TypeInt, Default: int64(4)},
			}})
			if err != nil {
				t.Fatal(err)
			}
			lower, err := store.ParseDoc([]byte(`{"dist":{"a":{"b":7}}}`))
			if err != nil {
				t.Fatal(err)
			}
			upper, err := store.ParseDoc([]byte(tc.text))
			if err != nil {
				t.Fatal(err)
			}
			// Both leaf spellings survive Store's object merge.
			merged, err := store.ParseDoc([]byte(tc.text))
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := upper["dist"].(map[string]any)["a"]; !ok {
				merged["dist"].(map[string]any)["a"] = lower["dist"].(map[string]any)["a"]
			}
			in := c.ReloadInput{
				Doc: store.Resolved{Doc: merged, Origins: map[string]store.Layer{"dist.a.b": store.LayerUser, "dist.other": store.LayerUser}},
				ConfigLayers: []store.LayerDoc{
					{Layer: store.LayerDistribution, Doc: lower},
					{Layer: store.LayerUser, File: "config.json", Doc: upper},
				},
			}
			before := s.Snapshot()
			cand, report, err := s.Rebuild(t.Context(), in)
			if tc.duplicate {
				if !errors.Is(err, c.ErrInvalid) || cand != nil || s.Snapshot() != before {
					t.Fatalf("duplicate reload: candidate=%v report=%+v err=%v", cand, report, err)
				}
				cand, report, err = s.RebuildStartup(t.Context(), in)
				if !errors.Is(err, c.ErrInvalid) || cand == nil {
					t.Fatalf("startup: %+v %v", report, err)
				}
				if len(report.Problems) != 1 {
					t.Fatalf("problems=%+v", report.Problems)
				}
				p := report.Problems[0]
				if p.Code != "duplicate_key" || p.Severity != "error" || p.Key != "dist.a.b" || p.Layer != "user" || p.Source == nil || p.Source.Layer != "user" || p.Source.File != "config.json" {
					t.Fatalf("duplicate diagnostic=%+v", p)
				}
			} else if err != nil || cand == nil || len(report.Problems) != 0 {
				t.Fatalf("single spelling: %+v %v", report, err)
			}
			if !cand.Apply().Applied {
				t.Fatal("candidate did not apply")
			}
			snap := s.Snapshot()
			wantSource := "user"
			if tc.duplicate {
				wantSource = "distribution"
			}
			if snap.Int("dist.a.b") != tc.want || snap.Source("dist.a.b") != wantSource {
				t.Errorf("value=%d source=%s; want %d from %s", snap.Int("dist.a.b"), snap.Source("dist.a.b"), tc.want, wantSource)
			}
			if snap.Int("dist.other") != 9 || snap.Source("dist.other") != "user" {
				t.Error("unrelated key was reset")
			}
		})
	}
}
