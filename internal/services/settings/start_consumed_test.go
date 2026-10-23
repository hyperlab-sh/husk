package settings

import (
	"errors"
	"reflect"
	"testing"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// startupSettingsInput carries real values from both allowed source layers.
func startupSettingsInput() c.ReloadInput {
	return c.ReloadInput{
		Doc: store.Resolved{
			Doc: store.Doc{
				"theme": store.Doc{"name": true, "allow_low_contrast": true},
				"husk":  store.Doc{"plugins": store.Doc{"paths": []string{"startup-plugin"}, "dirs": []string{"startup-directory"}}},
			},
			Origins: map[string]store.Layer{
				"theme.name": store.LayerUser, "theme.allow_low_contrast": store.LayerUser,
				"husk.plugins.paths": store.LayerUser, "husk.plugins.dirs": store.LayerInstance,
			},
		},
		ConfigLayers: []store.LayerDoc{
			{Layer: store.LayerUser, File: "config.json", Doc: store.Doc{"husk": store.Doc{"bundle": "startup-bundle"}}},
			{Layer: store.LayerInstance, Doc: store.Doc{"husk": store.Doc{"plugins": store.Doc{"dirs": []string{"startup-directory"}}}}},
		},
	}
}

func TestRebuildStartupPerKeyBaseline(t *testing.T) {
	for _, removal := range []bool{false, true} {
		t.Run(map[bool]string{false: "change", true: "removal"}[removal], func(t *testing.T) {
			s, err := New(Options{})
			if err != nil {
				t.Fatal(err)
			}
			before := s.Snapshot()
			cand, report, err := s.RebuildStartup(t.Context(), startupSettingsInput())
			if !errors.Is(err, c.ErrInvalid) || cand == nil || report.Gen != 0 {
				t.Fatalf("startup candidate=%v report=%+v err=%v", cand, report, err)
			}
			if s.Snapshot() != before {
				t.Fatal("RebuildStartup published before Apply")
			}
			if applied := cand.Apply(); !applied.Applied || applied.Gen != 0 || s.Generation() != 0 {
				t.Fatalf("startup initialization=%+v generation=%d", applied, s.Generation())
			}
			checkBaseline := func() {
				t.Helper()
				snap := s.Snapshot()
				for key, want := range map[string]any{
					"husk.plugins.paths": []any{"startup-plugin"},
					"husk.plugins.dirs":  []any{"startup-directory"},
					"husk.bundle":        "startup-bundle",
				} {
					got, ok := snap.Raw(key)
					if !ok || !reflect.DeepEqual(got, want) {
						t.Errorf("%s=%v want=%v", key, got, want)
					}
				}
				for key, want := range map[string]string{"husk.plugins.paths": "user", "husk.plugins.dirs": "instance", "husk.bundle": "user"} {
					if got := snap.Source(key); got != want {
						t.Errorf("source(%s)=%s want=%s", key, got, want)
					}
				}
			}
			checkBaseline()
			if snap := s.Snapshot(); snap.String("theme.name") != before.String("theme.name") || snap.Source("theme.name") != "default" || !snap.Bool("theme.allow_low_contrast") || snap.Source("theme.allow_low_contrast") != "user" {
				t.Fatal("startup did not default only the invalid key")
			}
			s.FreezeStartConsumed()
			in := startupSettingsInput()
			in.Doc.Doc["theme"] = store.Doc{"name": "charm"}
			in.Doc.Doc["husk"] = store.Doc{"plugins": store.Doc{"paths": []string{"pending-plugin"}, "dirs": []string{"pending-directory"}}}
			in.ConfigLayers[0].Doc = store.Doc{"husk": store.Doc{"bundle": "pending-bundle"}}
			if removal {
				in = c.ReloadInput{}
			}
			cand, report, err = s.Rebuild(t.Context(), in)
			if err != nil || cand == nil {
				t.Fatalf("reload=%+v err=%v", report, err)
			}
			warnings := map[string]int{}
			for _, p := range report.Problems {
				if p.Code == "restart_required" {
					warnings[p.Key]++
					if p.Severity != "warning" {
						t.Errorf("restart problem=%+v", p)
					}
				}
			}
			if want := (map[string]int{"husk.plugins.paths": 1, "husk.plugins.dirs": 1, "husk.bundle": 1}); !reflect.DeepEqual(warnings, want) {
				t.Fatalf("restart warnings=%v want=%v", warnings, want)
			}
			if !cand.Apply().Applied {
				t.Fatal("reload did not apply")
			}
			checkBaseline()
		})
	}
}

func TestRebuildStartupInvalidAfterInitializationIsAtomic(t *testing.T) {
	for _, mode := range []string{"published", "frozen"} {
		t.Run(mode, func(t *testing.T) {
			s, err := New(Options{})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "published" {
				cand, _, err := s.Rebuild(t.Context(), c.ReloadInput{})
				if err != nil || cand == nil || !cand.Apply().Applied || s.Generation() != 1 {
					t.Fatalf("initial load: %v", err)
				}
			} else {
				s.FreezeStartConsumed()
			}
			before := s.Snapshot()
			cand, report, err := s.RebuildStartup(t.Context(), startupSettingsInput())
			if !errors.Is(err, c.ErrInvalid) || cand != nil || s.Snapshot() != before || report.Gen != before.Generation() {
				t.Fatalf("noninitial startup candidate=%v report=%+v err=%v", cand, report, err)
			}
		})
	}
}

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
