package services

import (
	"fmt"
	"maps"
	"reflect"
	"testing"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	stheme "github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
)

func TestBuiltInThemeLayer(t *testing.T) {
	th := stheme.New()
	for _, loaded := range []bool{false, true} {
		if loaded {
			r := apply(t, th, themeInput(t, `{}`, nil))
			if len(r.Problems) != 0 || r.Changed {
				t.Fatalf("default rebuild: %+v", r)
			}
		}
		rows, err := th.List(Caller{Kind: "cli"})
		if err != nil || len(rows) != 9 || th.Palette() != palette.Default() || th.Palette().Name != "charm" {
			t.Fatalf("palette=%+v rows=%+v err=%v", th.Palette(), rows, err)
		}
		for i, name := range palette.BuiltInNames() {
			if rows[i].Name != name || rows[i].Source != "built-in" || rows[i].Current != (name == "charm") {
				t.Fatal(rows)
			}
		}
	}
	in := themeInput(t, `{"theme":{"name":"dracula"}}`, nil)
	in.Themes = []store.LayerDoc{
		namedLayer(store.LayerDistribution, `{"themes":{"dracula":{"palette":{"ink":"#000000"}}}}`),
		namedLayer(store.LayerUser, `{"themes":{"dracula":{"palette":{"accent":"#FF79C6"}},"zulu":{"palette":{}},"alpha":{"palette":{}}}}`),
	}
	r := apply(t, th, in)
	rows, _ := th.List(Caller{Kind: "cli"})
	if len(r.Problems) != 0 || len(rows) != 11 || rows[2].Name != "dracula" || rows[2].Source != "user" || !rows[2].Current || rows[9].Name != "alpha" || rows[10].Name != "zulu" {
		t.Fatalf("rows=%+v report=%+v", rows, r)
	}
	want := palette.Default()
	want.Name, want.Accent = "dracula", "#ff79c6"
	if th.Palette() != want {
		t.Fatalf("whole user definition must inherit charm, not built-in dracula: %+v", th.Palette())
	}
}

func TestReplacedCharmIsInheritanceBase(t *testing.T) {
	for _, name := range []string{"", "charm", "partial", "missing"} {
		t.Run(name, func(t *testing.T) {
			in := themeInput(t, fmt.Sprintf(`{"theme":{"name":%q}}`, name), nil)
			in.Themes = []store.LayerDoc{namedLayer(store.LayerUser, `{"themes":{"charm":{"palette":{"ink":"#ffffff"}},"partial":{"palette":{"accent":"#ffffff"}}}}`)}
			th := stheme.New()
			r := apply(t, th, in)
			wantName := name
			if name == "" || name == "missing" {
				wantName = "charm"
			}
			if th.Palette().Ink != "#ffffff" || th.Palette().Name != wantName || hasProblem(r.Problems, "unknown_theme", "warning") != (name == "missing") {
				t.Fatalf("palette=%+v report=%+v", th.Palette(), r)
			}
			if name == "missing" && r.Problems[0].Message != "theme missing does not exist; using charm" {
				t.Fatal(r.Problems)
			}
		})
	}
	th := stheme.New()
	in := themeInput(t, `{}`, nil)
	in.Themes = []store.LayerDoc{namedLayer(store.LayerUser, `{"themes":{"charm":{"palette":{"ink":"#000000"}}}}`)}
	cand, r, err := th.Rebuild(t.Context(), in)
	if err == nil || cand != nil || !hasProblem(r.Problems, "low_contrast", "error") || th.Palette() != palette.Default() {
		t.Fatalf("broken replacement published: %+v %v", r, err)
	}
}

func TestThemeValueGrammar(t *testing.T) {
	for _, layer := range []store.Layer{store.LayerDistribution, store.LayerUser, store.LayerProfile, store.LayerInstance} {
		for _, value := range []string{`0`, `4`, `4.0`, `15`, `"#ABCDEF"`, `"4"`, `16`, `-1`, `4.5`, `"#fff"`, `true`, `null`} {
			t.Run(string(layer)+"/"+value, func(t *testing.T) {
				in := themeInput(t, `{"theme":{"name":"example"}}`, nil)
				in.Themes = []store.LayerDoc{namedLayer(layer, `{"themes":{"example":{"palette":{"accent":`+value+`}}}}`)}
				th := stheme.New()
				r := apply(t, th, in)
				valid := value == `0` || value == `4` || value == `4.0` || value == `15` || value == `"#ABCDEF"`
				if hasProblem(r.Problems, "invalid_theme", "warning") == valid || (th.Palette().Name == "example") != valid {
					t.Fatalf("value=%s palette=%+v report=%+v", value, th.Palette(), r)
				}
				_, indices := th.WireANSI()
				if valid && value != `"#ABCDEF"` {
					if _, ok := indices["accent"]; !ok {
						t.Fatal("accepted integer lost its map entry")
					}
				} else if valid && th.Palette().Accent != "#abcdef" {
					t.Fatal("hex not normalized")
				}
			})
		}
	}
	in := themeInput(t, `{}`, nil)
	in.Settings = integerOverride{in.Settings}
	_, r, err := stheme.New().Rebuild(t.Context(), in)
	if err == nil || !hasProblem(r.Problems, "invalid_color", "error") {
		t.Fatalf("integer override accepted: %+v", r)
	}
}

// A Theme input with an integer override must not turn it into an ANSI role;
// Settings normally rejects the non-string before Theme can see it.
type integerOverride struct{ c.SettingsSnapshot }

func (s integerOverride) Source(key string) string {
	if key == "theme.palette.ink" {
		return "user"
	}
	return s.SettingsSnapshot.Source(key)
}
func (s integerOverride) String(key string) string {
	if key == "theme.palette.ink" {
		return ""
	}
	return s.SettingsSnapshot.String(key)
}
func (s integerOverride) Raw(key string) (any, bool) {
	if key == "theme.palette.ink" {
		return float64(4), true
	}
	return s.SettingsSnapshot.Raw(key)
}

func TestOverrideKeepsThemeSlot(t *testing.T) {
	for _, tc := range []struct{ name, roles, overrides, slot string }{
		{"rose-pine", "", `,"palette":{"accent":"#ffffff"}`, "#21202e"},
		{"base16", "", `,"palette":{"accent":"#ffffff"}`, "#000000"},
		{"partial", `{"canvas":"#101018"}`, "", palette.SlotBackground("#101018")},
		{"partial", `{"canvas":"#101018"}`, `,"palette":{"canvas":"#12121a"}`, palette.SlotBackground("#12121a")},
		{"partial", `{}`, `,"palette":{"slot_background":"#222222"}`, "#222222"},
	} {
		t.Run(tc.name+tc.overrides, func(t *testing.T) {
			in := themeInput(t, `{"theme":{"name":"`+tc.name+`"`+tc.overrides+`}}`, nil)
			if tc.roles != "" {
				in.Themes = []store.LayerDoc{namedLayer(store.LayerUser, `{"themes":{"partial":{"palette":`+tc.roles+`}}}`)}
			}
			th := stheme.New()
			apply(t, th, in)
			p, indices := th.WireANSI()
			if p.Slot != tc.slot {
				t.Fatalf("slot=%s want=%s", p.Slot, tc.slot)
			}
			if tc.name == "base16" {
				if index, ok := indices["slot_background"]; !ok || index != 0 {
					t.Fatalf("slot lost index: %v", indices)
				}
				if _, ok := indices["accent"]; ok || len(indices) != 18 {
					t.Fatalf("hex override retained index: %v", indices)
				}
			}
		})
	}
}

func TestIndexCanvasSlot(t *testing.T) {
	for _, canvas := range []int{0, 4, 15} {
		in := themeInput(t, `{"theme":{"name":"indexed"}}`, nil)
		in.Themes = []store.LayerDoc{namedLayer(store.LayerUser, fmt.Sprintf(`{"themes":{"indexed":{"palette":{"canvas":%d,"ink":7,"ink_muted":7,"ink_subtle":7,"accent":7,"accent_secondary":7,"success":7,"warning":7,"danger":7,"info":7,"border":7,"border_focused":7}}}}`, canvas))}
		th := stheme.New()
		apply(t, th, in)
		p, indices := th.WireANSI()
		if n, ok := indices["slot_background"]; !ok || n != canvas || p.Slot != p.Canvas {
			t.Fatalf("canvas=%d palette=%+v map=%v", canvas, p, indices)
		}
	}
}

func TestThemeIndexSnapshot(t *testing.T) {
	th := stheme.New()
	in := themeInput(t, `{"theme":{"name":"base16"}}`, nil)
	r := apply(t, th, in)
	p, indices := th.WireANSI()
	values := palette.BuiltIn()["themes"].(map[string]any)["base16"].(map[string]any)["palette"].(map[string]any)
	if !r.Changed || len(r.Problems) != 0 || len(indices) != 19 || p.Name != "base16" || th.Wire() != p {
		t.Fatalf("report=%+v palette=%+v indices=%v", r, p, indices)
	}
	for role, value := range values {
		hex, index, _ := palette.ResolveValue(value)
		if palette.Roles(palette.Palette(p))[role] != hex || indices[role] != index {
			t.Fatalf("role %s disagrees with built-in: %v", role, indices)
		}
	}
	indices["ink"] = 0
	indices["name"] = 0
	_, again := th.WireANSI()
	if again["ink"] != 15 || len(again) != 19 {
		t.Fatal("WireANSI leaked snapshot map")
	}
	if r = apply(t, th, in); r.Changed {
		t.Fatal("identical pair changed")
	}
	r = apply(t, th, themeInput(t, `{}`, nil))
	p, indices = th.WireANSI()
	if !r.Changed || p.Name != "charm" || len(indices) != 0 {
		t.Fatalf("back to charm: %+v %v", r, indices)
	}
	in = themeInput(t, `{"theme":{"name":"indexed"}}`, nil)
	in.Themes = []store.LayerDoc{namedLayer(store.LayerUser, `{"themes":{"indexed":{"palette":{"ink":7}}}}`)}
	apply(t, th, in)
	before, oldMap := th.WireANSI()
	in.Themes = []store.LayerDoc{namedLayer(store.LayerUser, `{"themes":{"indexed":{"palette":{"ink":"#c0c0c0"}}}}`)}
	r = apply(t, th, in)
	after, newMap := th.WireANSI()
	if before != after || maps.Equal(oldMap, newMap) || !r.Changed || !reflect.DeepEqual(oldMap, map[string]int{"ink": 7}) {
		t.Fatalf("index-only change: %+v before=%v after=%v", r, oldMap, newMap)
	}
}

func TestIndexThemeHexOverridesStillChecked(t *testing.T) {
	in := themeInput(t, `{"theme":{"name":"base16","palette":{"ink":"#000000","canvas":"#000000"}}}`, nil)
	th := stheme.New()
	cand, r, err := th.Rebuild(t.Context(), in)
	if err == nil || cand != nil || !hasProblem(r.Problems, "low_contrast", "error") || th.Palette() != palette.Default() {
		t.Fatalf("hex-hex override pair escaped contrast: %+v", r)
	}
}
