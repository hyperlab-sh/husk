package settings

import (
	"context"
	"reflect"
	"testing"

	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestPluginAdditionsLowerLayersAndRestart(t *testing.T) {
	plugins := func(paths any) store.Doc {
		return store.Doc{"husk": store.Doc{"plugins": store.Doc{"paths": paths}}}
	}
	for _, tc := range []struct {
		name   string
		layers []store.LayerDoc
		want   []string
	}{
		{"distribution", []store.LayerDoc{{Layer: store.LayerDistribution, Doc: plugins([]string{"a"})}}, []string{"a", "added"}},
		{"user replaces distribution", []store.LayerDoc{{Layer: store.LayerDistribution, Doc: plugins([]string{"a"})}, {Layer: store.LayerUser, Doc: plugins([]string{"b"})}}, []string{"b", "added"}},
		{"selected profile wins", []store.LayerDoc{{Layer: store.LayerUser, Doc: plugins([]string{"a"})}, {Layer: store.LayerProfile, Doc: plugins([]string{"b"})}}, []string{"b", "added"}},
		{"named instance wins", []store.LayerDoc{{Layer: store.LayerUser, Doc: plugins([]string{"a"})}, {Layer: store.LayerInstance, File: "instances/example/config.json", Doc: plugins([]string{"b"})}}, []string{"b", "added"}},
		{"null deletes", []store.LayerDoc{{Layer: store.LayerUser, Doc: plugins([]string{"a"})}, {Layer: store.LayerProfile, Doc: plugins(nil)}}, []string{"added"}},
		{"ancestor replaced", []store.LayerDoc{{Layer: store.LayerUser, Doc: plugins([]string{"a"})}, {Layer: store.LayerProfile, Doc: store.Doc{"husk": "replaced"}}}, []string{"added"}},
		{"unrelated sibling", []store.LayerDoc{{Layer: store.LayerUser, Doc: plugins([]string{"a"})}, {Layer: store.LayerProfile, Doc: store.Doc{"husk": store.Doc{"other": true}}}}, []string{"a", "added"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(Options{PluginAdditions: map[string][]string{"paths": {"added"}}})
			if err != nil {
				t.Fatal(err)
			}
			in := c.ReloadInput{ConfigLayers: tc.layers}
			candidate, _, err := s.RebuildStartup(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if !candidate.Apply().Applied {
				t.Fatal("startup not applied")
			}
			if got := s.Snapshot().Strings("husk.plugins.paths"); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("paths = %v, want %v", got, tc.want)
			}
			s.FreezeStartConsumed()
			in.ConfigLayers = []store.LayerDoc{{Layer: store.LayerUser, Doc: plugins([]string{"changed"})}}
			candidate, report, err := s.Rebuild(context.Background(), in)
			if err != nil || !candidate.Apply().Applied {
				t.Fatalf("reload: %v", err)
			}
			if got := s.Snapshot().Strings("husk.plugins.paths"); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("restart baseline changed: %v", got)
			}
			found := false
			for _, p := range report.Problems {
				if p.Code == "restart_required" && p.Key == "husk.plugins.paths" {
					found = true
				}
			}
			if !found {
				t.Fatal("missing restart_required")
			}
		})
	}
}
