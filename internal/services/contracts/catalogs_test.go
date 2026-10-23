package contracts

import (
	"reflect"
	"testing"

	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

func TestCatalogsRailItems(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rails    any
		wantIDs  []string
		wantWarn bool
	}{
		{name: "absent"},
		{name: "valid", rails: []any{map[string]any{"id": "files.open"}}, wantIDs: []string{"files.open"}},
		{name: "invalid scalar", rails: 3, wantWarn: true},
		{name: "partially decoded array", rails: []any{map[string]any{"id": "files.open"}, 3}, wantWarn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := store.Doc{
				"id":                "files",
				"actions":           []any{map[string]any{"id": "files.run"}},
				"surfaces":          []any{map[string]any{"id": "main-stage"}},
				"settings_declared": []any{map[string]any{"key": "enabled", "type": "bool", "default": true}},
				"secret_requests":   []any{map[string]any{"name": "token", "purpose": "access files"}},
			}
			if tc.rails != nil {
				doc["rail_items"] = tc.rails
			}
			catalogs, problems := Catalogs(ReloadInput{Declared: map[store.DocName]map[string]store.Doc{
				store.DeclaredPlugins: {"files": doc},
			}})
			want := Catalog{
				ID:               "files",
				Actions:          []ActionDecl{{ID: "files.run"}},
				RailItems:        tc.wantIDs,
				Surfaces:         []SurfaceDecl{{ID: "main-stage"}},
				SettingsDeclared: []SettingDecl{{Key: "enabled", Type: "bool", Default: true}},
				SecretRequests:   []SecretRequest{{Name: "token", Purpose: "access files"}},
			}
			if got, ok := catalogs["files"]; !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("catalog = %+v (present=%v), want %+v", got, ok, want)
			}
			if !tc.wantWarn {
				if len(problems) != 0 {
					t.Fatalf("unexpected problems: %+v", problems)
				}
				return
			}
			if len(problems) != 1 || problems[0].Code != "invalid_plugin_catalog" || problems[0].Severity != "warning" || problems[0].Layer != "plugin:files" {
				t.Fatalf("rail item problems = %+v", problems)
			}
		})
	}
}
