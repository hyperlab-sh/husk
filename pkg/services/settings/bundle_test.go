package settings

import (
	"context"
	"testing"

	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestBundleSettingAllowedLayersIgnoredWarningsAndRestart(t *testing.T) {
	for _, ignored := range []store.Layer{store.LayerDistribution, store.LayerProfile} {
		for _, user := range []string{"", "chosen"} {
			t.Run(string(ignored)+"/"+user, func(t *testing.T) {
				s, err := New(Options{})
				if err != nil {
					t.Fatal(err)
				}
				input := func(allowed, disallowed string) c.ReloadInput {
					return c.ReloadInput{
						Doc: store.Resolved{Doc: store.Doc{"husk": store.Doc{"bundle": disallowed}}, Origins: map[string]store.Layer{"husk.bundle": ignored}},
						ConfigLayers: []store.LayerDoc{
							{Layer: store.LayerUser, File: "config.json", Doc: store.Doc{"husk": store.Doc{"bundle": allowed}}},
							{Layer: ignored, File: "profiles/work/config.json", Doc: store.Doc{"husk": store.Doc{"bundle": disallowed}}},
						},
					}
				}
				rebuild := func(in c.ReloadInput) c.Report {
					cand, r, err := s.Rebuild(context.Background(), in)
					if err != nil || cand == nil {
						t.Fatalf("rebuild=%+v %v", r, err)
					}
					if !cand.Apply().Applied {
						t.Fatal("not applied")
					}
					return r
				}
				check := func(r c.Report, wantRestart bool) {
					var warnings, restarts int
					for _, p := range r.Problems {
						if p.Code == "bundle_setting_ignored" {
							warnings++
							if p.Severity != "warning" || p.Layer != string(ignored) || p.Key != "husk.bundle" || p.Message != "husk.bundle is honoured from the user and instance layers only" || p.Source == nil || p.Source.File != "profiles/work/config.json" {
								t.Fatalf("ignored problem: %+v", p)
							}
						}
						if p.Code == "restart_required" {
							restarts++
						}
					}
					if warnings != 1 || (restarts == 1) != wantRestart || restarts > 1 {
						t.Fatalf("problems=%+v", r.Problems)
					}
					if snap := s.Snapshot(); snap.String("husk.bundle") != user || snap.Source("husk.bundle") != "user" {
						t.Fatalf("snapshot=%s source=%s", snap.String("husk.bundle"), snap.Source("husk.bundle"))
					}
				}
				check(rebuild(input(user, "ignored one")), false)
				check(rebuild(input(user, "ignored two")), false)
				check(rebuild(input("pending", "ignored three")), true)
			})
		}
	}
}

func TestBundleSettingInstanceOverridesUserAndNullClears(t *testing.T) {
	for _, value := range []any{"instance", nil} {
		s, err := New(Options{})
		if err != nil {
			t.Fatal(err)
		}
		in := c.ReloadInput{ConfigLayers: []store.LayerDoc{
			{Layer: store.LayerUser, Doc: store.Doc{"husk": store.Doc{"bundle": "user"}}},
			{Layer: store.LayerInstance, Doc: store.Doc{"husk": store.Doc{"bundle": value}}},
		}}
		cand, r, err := s.Rebuild(context.Background(), in)
		if err != nil || !cand.Apply().Applied || len(r.Problems) != 0 {
			t.Fatalf("rebuild=%+v %v", r, err)
		}
		want, source := "instance", "instance"
		if value == nil {
			want, source = "", "default"
		}
		if s.Snapshot().String("husk.bundle") != want || s.Snapshot().Source("husk.bundle") != source {
			t.Fatalf("value=%v snapshot=%s source=%s", value, s.Snapshot().String("husk.bundle"), s.Snapshot().Source("husk.bundle"))
		}
	}
}
