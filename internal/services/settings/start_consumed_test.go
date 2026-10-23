package settings

import (
	"reflect"
	"testing"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

func TestStartConsumedFreeze(t *testing.T) {
	for _, mode := range []string{"rebuild", "preview", "check"} {
		t.Run(mode, func(t *testing.T) {
			s, err := New(Options{Schema: []Entry{{Key: "dist.restart", Owner: "dist", Type: TypeString, Default: "default", Apply: "restart"}}})
			if err != nil {
				t.Fatal(err)
			}
			before := s.Snapshot()
			s.FreezeStartConsumed()
			frozen := s.Snapshot()
			s.FreezeStartConsumed()
			if s.Snapshot() != frozen || before.Generation() != 0 || s.Generation() != 0 {
				t.Fatal("freeze is not idempotent at generation zero")
			}
			in := c.ReloadInput{
				Doc:          store.Resolved{Doc: store.Doc{"husk": store.Doc{"plugins": store.Doc{"paths": []string{"pending"}, "dirs": []string{"pending-dir"}}}, "dist": store.Doc{"restart": "first"}}, Origins: map[string]store.Layer{"husk.plugins.paths": store.LayerUser, "husk.plugins.dirs": store.LayerUser}},
				ConfigLayers: []store.LayerDoc{{Layer: store.LayerUser, File: "config.json", Doc: store.Doc{"husk": store.Doc{"bundle": "pending-bundle"}}}},
			}
			check := func(ps []c.Problem) {
				t.Helper()
				got := map[string]int{}
				for _, p := range ps {
					if p.Code == "restart_required" {
						got[p.Key]++
						if p.Severity != "warning" {
							t.Errorf("problem=%+v", p)
						}
					}
				}
				want := map[string]int{"husk.plugins.paths": 1, "husk.plugins.dirs": 1, "husk.bundle": 1}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("restart warnings=%v want=%v", got, want)
				}
			}
			switch mode {
			case "rebuild":
				for range 2 {
					cand, report, err := s.Rebuild(t.Context(), in)
					if err != nil {
						t.Fatal(err)
					}
					check(report.Problems)
					if !cand.Apply().Applied {
						t.Fatal("not applied")
					}
					if !s.current.Load().startConsumedFrozen {
						t.Fatal("rebuild lost freeze marker")
					}
					if s.Snapshot().String("husk.bundle") != "" || len(s.Snapshot().Strings("husk.plugins.paths")) != 0 || s.Snapshot().String("dist.restart") != "first" {
						t.Fatal("restart baseline changed or unrelated first load was frozen")
					}
				}
			case "preview":
				snap, report, err := s.Preview(t.Context(), c.Caller{Kind: "cli"}, in)
				if err != nil || !report.OK {
					t.Fatalf("preview=%+v %v", report, err)
				}
				check(report.Problems)
				if snap.String("husk.bundle") != "" || len(snap.Strings("husk.plugins.dirs")) != 0 {
					t.Fatal("preview lost baseline")
				}
			case "check":
				report, err := s.Validate(t.Context(), c.Caller{Kind: "cli"}, in)
				if err != nil || !report.OK {
					t.Fatalf("check=%+v %v", report, err)
				}
				check(report.Problems)
			}
			if mode != "rebuild" && s.Snapshot() != frozen {
				t.Fatal("dry run published")
			}
		})
	}
}
