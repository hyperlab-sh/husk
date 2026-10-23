package services

import (
	"context"
	"fmt"
	"testing"

	stheme "github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
)

func TestThemeListServingSnapshotOrderSourceCurrent(t *testing.T) {
	for _, name := range []string{"", palette.Default().Name, "unknown", "alpha", "zulu"} {
		t.Run(name, func(t *testing.T) {
			in := themeInput(t, fmt.Sprintf(`{"theme":{"name":%q}}`, name), nil)
			in.Themes = []store.LayerDoc{
				namedLayer(store.LayerUser, `{"themes":{"zulu":{"palette":{}},"alpha":{"palette":{}}}}`),
				namedLayer(store.LayerProfile, `{"themes":{"zulu":{"palette":{}}}}`),
			}
			th := stheme.New()
			apply(t, th, in)
			rows, err := th.List(Caller{Kind: "plugin", PluginID: "unit"})
			want := name
			if want == "" || want == "unknown" {
				want = palette.Default().Name
			}
			if err != nil || len(rows) != 3 || rows[0].Name != palette.Default().Name || rows[0].Source != "built-in" || rows[1].Name != "alpha" || rows[1].Source != "user" || rows[2].Name != "zulu" || rows[2].Source != "profile" {
				t.Fatalf("%+v %v", rows, err)
			}
			n := 0
			for _, r := range rows {
				if r.Current {
					n++
					if r.Name != want {
						t.Fatalf("current=%+v want %s", r, want)
					}
				}
			}
			if n != 1 {
				t.Fatal(rows)
			}
			rows[0].Name = "mutated"
			// A prepared but unpublished candidate cannot change a serving list.
			in.Themes = nil
			th.Rebuild(context.Background(), in)
			again, _ := th.List(Caller{Kind: "cli"})
			if len(again) != 3 || again[0].Name != palette.Default().Name {
				t.Fatal(again)
			}
		})
	}
}
