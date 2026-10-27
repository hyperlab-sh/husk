package settings

import (
	"reflect"
	"testing"

	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestConvertersSettingIsLaunchOnly(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	input := func(paths []string) c.ReloadInput {
		return c.ReloadInput{Doc: store.Resolved{Doc: store.Doc{"husk": store.Doc{"plugins": store.Doc{"converters": paths}}}, Origins: map[string]store.Layer{"husk.plugins.converters": store.LayerUser}}}
	}
	candidate, _, err := s.RebuildStartup(t.Context(), input([]string{"first", "second"}))
	if err != nil || candidate == nil || !candidate.Apply().Applied {
		t.Fatalf("startup: %v", err)
	}
	s.FreezeStartConsumed()
	candidate, report, err := s.Rebuild(t.Context(), input([]string{"next"}))
	if err != nil || candidate == nil || !candidate.Apply().Applied {
		t.Fatalf("reload: %v", err)
	}
	if !reflect.DeepEqual(s.Snapshot().Strings("husk.plugins.converters"), []string{"first", "second"}) {
		t.Fatal("reload changed launch converters")
	}
	found := false
	for _, problem := range report.Problems {
		if problem.Key == "husk.plugins.converters" && problem.Code == "restart_required" && problem.Severity == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing restart warning: %+v", report.Problems)
	}
}
