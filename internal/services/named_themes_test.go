package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	stheme "github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
)

func namedLayer(layer store.Layer, raw string) store.LayerDoc {
	return store.LayerDoc{Layer: layer, File: "themes.json", Doc: doc(raw)}
}

func TestNamedThemeRules(t *testing.T) {
	for _, tc := range []struct{ name, definition, code string }{
		{"bad.name", `{"palette":{}}`, "invalid_theme"},
		{strings.Repeat("a", 33), `{"palette":{}}`, "invalid_theme"},
		{"", `{"palette":{}}`, "invalid_theme"},
		{palette.Default().Name, `{"palette":{}}`, "reserved_theme_name"},
		{"bad", `{"palette":{"not_a_role":"#ffffff"}}`, "invalid_theme"},
		{"bad", `{"palette":{"name":"other"}}`, "invalid_theme"},
		{"bad", `{"palette":{"ink":"red"}}`, "invalid_theme"},
		{"bad", `{"palette":{"ink":42}}`, "invalid_theme"},
		{"bad", `{"palette":[]}`, "invalid_theme"},
		{"bad", `false`, "invalid_theme"},
	} {
		t.Run(tc.name+tc.definition, func(t *testing.T) {
			in := themeInput(t, `{}`, nil)
			in.Themes = []store.LayerDoc{namedLayer(store.LayerUser, fmt.Sprintf(`{"themes":{%q:%s,"valid":{"palette":{}}}}`, tc.name, tc.definition))}
			th := stheme.New()
			r := apply(t, th, in)
			if len(r.Problems) != 1 || r.Problems[0].Code != tc.code || r.Problems[0].Severity != "warning" || r.Problems[0].Key != "themes."+tc.name || r.Problems[0].Source.File != "themes.json" {
				t.Fatalf("%+v", r.Problems)
			}
		})
	}
}

func TestNamedThemeReplacementSelectionAndOverrides(t *testing.T) {
	for _, layer := range []store.Layer{store.LayerUser, store.LayerProfile, store.LayerInstance} {
		t.Run(string(layer), func(t *testing.T) {
			in := themeInput(t, `{"theme":{"name":"Evening","palette":{"info":"#63C4FF"}}}`, map[string]store.Layer{"theme.name": layer, "theme.palette.info": store.LayerUser})
			in.Themes = []store.LayerDoc{
				namedLayer(store.LayerDistribution, `{"themes":{"Evening":{"palette":{"ink":"#000000"}}}}`),
				namedLayer(layer, `{"themes":{"Evening":{"palette":{"accent":"#F1EFFE"}}}}`),
			}
			th := stheme.New()
			apply(t, th, in)
			p := th.Palette()
			if p.Name != "Evening" || p.Ink != palette.Default().Ink || p.Accent != "#f1effe" || p.Info != "#63c4ff" {
				t.Fatalf("unit replacement/override: %+v", p)
			}
		})
	}
}

func TestNamedThemeUnknownAttributedToSelection(t *testing.T) {
	in := themeInput(t, `{"theme":{"name":"absent"}}`, map[string]store.Layer{"theme.name": store.LayerProfile}, store.LayerDoc{Layer: store.LayerProfile, File: "profiles/work/config.json"})
	th := stheme.New()
	r := apply(t, th, in)
	if len(r.Problems) != 1 || r.Problems[0].Code != "unknown_theme" || r.Problems[0].Layer != "profile" || r.Problems[0].Source.File != "profiles/work/config.json" || th.Palette().Name != palette.Default().Name {
		t.Fatalf("%+v", r)
	}
}

func TestNamedThemeLimitUsesDocumentOrder(t *testing.T) {
	for _, count := range []int{64, 65} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			themes := store.Doc{}
			var order []string
			for i := count; i > 0; i-- {
				name := fmt.Sprintf("t%02d", i)
				themes[name] = store.Doc{"palette": store.Doc{}}
				order = append(order, name)
			}
			in := themeInput(t, `{"theme":{"name":"t01"}}`, nil)
			in.Themes = []store.LayerDoc{{Layer: store.LayerUser, File: "themes.json", Doc: store.Doc{"themes": themes}, MemberOrder: map[string][]string{"/themes": order}}}
			th := stheme.New()
			r := apply(t, th, in)
			if count == 64 {
				if len(r.Problems) != 0 || th.Palette().Name != "t01" {
					t.Fatalf("%+v", r)
				}
				return
			}
			if !hasProblem(r.Problems, "too_many_themes", "warning") || !hasProblem(r.Problems, "unknown_theme", "warning") || th.Palette().Name != palette.Default().Name {
				t.Fatalf("65th not dropped: %+v", r)
			}
		})
	}
}

func TestNamedThemeContrastAttributionAndNoPartialPublication(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(fmt.Sprint(override), func(t *testing.T) {
			raw := `{"theme":{"name":"bad"}}`
			origins := map[string]store.Layer{}
			if override {
				raw = `{"theme":{"name":"bad","palette":{"ink":"#000000"}}}`
				origins["theme.palette.ink"] = store.LayerProfile
			}
			in := themeInput(t, raw, origins, store.LayerDoc{Layer: store.LayerProfile, File: "profiles/work/config.json"})
			in.Themes = []store.LayerDoc{namedLayer(store.LayerUser, `{"themes":{"bad":{"palette":{"ink":"#000000","accent":"#f1effe"}}}}`)}
			th := stheme.New()
			cand, r, err := th.Rebuild(context.Background(), in)
			if err == nil || cand != nil || th.Palette() != palette.Default() {
				t.Fatalf("bad palette published: %+v", r)
			}
			wantKey, wantFile, wantLayer := "themes.bad.ink", "themes.json", "user"
			if override {
				wantKey, wantFile, wantLayer = "theme.palette.ink", "profiles/work/config.json", "profile"
			}
			for _, p := range r.Problems {
				if p.Code == "low_contrast" && strings.HasPrefix(p.Message, "ink on surface ") {
					if p.Key != wantKey || p.Layer != wantLayer || p.Source.File != wantFile {
						t.Fatalf("%+v", p)
					}
					return
				}
			}
			t.Fatal("missing ink on surface diagnostic")
		})
	}
}

// Themes and config parse failures have the same flight-wide error semantics.
func TestUnparsableThemesFailsWholeReload(t *testing.T) {
	st := &themesFailureStore{fakeStore: &fakeStore{}}
	co, err := NewCoordinator(Options{Store: st, Services: fakeServices(), Clock: clock()})
	if err != nil {
		t.Fatal(err)
	}
	defer co.Close()
	reports, err := co.Reload(context.Background(), ReasonCLI)
	assertFlightError(t, err, "unavailable", "store_error")
	if len(reports) != 5 {
		t.Fatal(reports)
	}
	for _, r := range reports {
		if r.Applied || !hasProblem(r.Problems, "store_error", "error") {
			t.Fatalf("%+v", r)
		}
	}
}

type themesFailureStore struct{ *fakeStore }

func (s *themesFailureStore) Resolve(name store.DocName, profile string) (store.Resolved, error) {
	if name == "themes" {
		return store.Resolved{Problems: []store.Problem{{Code: "invalid_document", Layer: store.LayerUser, File: "themes.json"}}}, nil
	}
	return s.fakeStore.Resolve(name, profile)
}
