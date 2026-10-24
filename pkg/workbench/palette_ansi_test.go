package workbench

import (
	"encoding/json"
	"image/color"
	"maps"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

// base16Pair decodes the real built-in definition, rather than a second palette.
func base16Pair(t *testing.T) (protocol.Palette, map[string]int) {
	t.Helper()
	roles := theme.BuiltIn()["themes"].(map[string]any)["base16"].(map[string]any)["palette"].(map[string]any)
	hexes, indices := map[string]string{"name": "base16"}, map[string]int{}
	for role, value := range roles {
		hex, index, err := theme.ResolveValue(value)
		if err != nil || index < 0 {
			t.Fatalf("base16 role %s: index %d, err %v", role, index, err)
		}
		hexes[role], indices[role] = hex, index
	}
	raw, err := json.Marshal(hexes)
	if err != nil {
		t.Fatal(err)
	}
	var p protocol.Palette
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p, indices
}

func base16Model(t *testing.T) *WorkbenchModel {
	t.Helper()
	m := goldenModel()
	p, idx := base16Pair(t)
	withFE(m).PaletteFn = func() (protocol.Palette, map[string]int) { return p, idx }
	m.refresh()
	return m
}

func TestDrawPaletteResolvesEveryRole(t *testing.T) {
	p, idx := base16Pair(t)
	if len(idx) != 19 {
		t.Fatalf("base16 has %d indexed roles, want 19", len(idx))
	}
	for _, indices := range []map[string]int{idx, nil} {
		draw := newDrawPalette(theme.Palette(p), indices)
		if draw.Name != p.Name {
			t.Fatalf("name = %q, want %q", draw.Name, p.Name)
		}
		served, drawn := reflect.ValueOf(p), reflect.ValueOf(draw)
		for i := 1; i < served.NumField(); i++ {
			field := served.Type().Field(i)
			role := strings.Split(field.Tag.Get("json"), ",")[0]
			got := drawn.FieldByName(field.Name).Interface().(color.Color)
			if indices != nil {
				if basic, ok := got.(ansi.BasicColor); !ok || int(basic) != idx[role] {
					t.Errorf("%s = %T(%v), want BasicColor(%d)", role, got, got, idx[role])
				}
			} else if !sameColor(got, lipgloss.Color(served.Field(i).String())) || indexedColor(got) {
				t.Errorf("hex %s = %T(%v), want truecolor", role, got, got)
			}
		}
	}
}

func TestBase16FrameUsesBasicColours(t *testing.T) {
	m := base16Model(t)
	m.stageMode = "hotspots" // draws a real frame border as well as text
	rows := frameCells(m.View().Content)
	// Every colour painted by core stays basic after widget styling, composition
	// and serialization. No xterm-default hex may leak back into the frame.
	for y, row := range rows {
		for x, cell := range row {
			for _, c := range []color.Color{cell.Style.Fg, cell.Style.Bg, cell.Style.UnderlineColor} {
				if c != nil {
					if _, ok := c.(ansi.BasicColor); !ok {
						t.Fatalf("cell %d,%d colour %T(%v), want basic", x, y, c, c)
					}
				}
			}
		}
	}
	check := func(name string, got color.Color, want int) {
		t.Helper()
		if basic, ok := got.(ansi.BasicColor); !ok || int(basic) != want {
			t.Errorf("%s = %T(%v), want BasicColor(%d)", name, got, got, want)
		}
	}
	rail := m.slotRects[protocol.SurfaceActivityRail]
	check("rail background", rows[rail.Y][rail.X].Style.Bg, 0)
	check("bar background", rows[0][m.width-1].Style.Bg, 0)
	check("brand ink", rows[0][1].Style.Fg, 15)
	active := 0
	for _, b := range m.railButtons() {
		if b.active {
			active++
			check("active icon accent", rows[rail.Y+b.startY+1][rail.X+3].Style.Fg, 5)
		}
	}
	if active != 1 {
		t.Fatalf("active tiles = %d, want 1", active)
	}
	r := m.slotRects[protocol.SurfaceMainStage]
	check("border", rows[r.Y][r.X].Style.Fg, 8)
}

func TestViewBackgroundRespectsCanvasIndex(t *testing.T) {
	for _, small := range []bool{false, true} {
		for _, indexed := range []bool{false, true} {
			m := goldenModel()
			if indexed {
				m = base16Model(t)
			}
			if small {
				m.width, m.height = 40, 10
			}
			got := m.View().BackgroundColor
			if indexed && got != nil {
				t.Errorf("small=%t: index canvas set terminal background %v", small, got)
			}
			if !indexed && !sameColor(got, m.palette.Canvas) {
				t.Errorf("small=%t: hex canvas background = %v, want %v", small, got, m.palette.Canvas)
			}
		}
	}
}

func TestRefreshComparesPaletteAndIndices(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	p := protocol.Palette(theme.Default())
	p.Ink = "#c0c0c0"
	idx := map[string]int{"ink": 7}
	reads := 0
	fe.PaletteFn = func() (protocol.Palette, map[string]int) { reads++; return p, idx }
	refresh := func(want bool) {
		t.Helper()
		before := reads
		if got := m.refresh(); got != want || reads != before+1 {
			t.Fatalf("refresh changed=%t, reads=%d; want changed=%t and one read", got, reads-before, want)
		}
	}
	refresh(true)
	if got, ok := m.palette.Ink.(ansi.BasicColor); !ok || got != 7 {
		t.Fatalf("ink = %T(%v), want BasicColor(7)", m.palette.Ink, m.palette.Ink)
	}
	refresh(false)
	previous := m.served
	idx = nil // identical served hex, but now the user chose a literal colour
	refresh(true)
	if m.served != previous || indexedColor(m.palette.Ink) || !sameColor(m.palette.Ink, lipgloss.Color(p.Ink)) {
		t.Fatalf("map-only change did not recolour: %T(%v)", m.palette.Ink, m.palette.Ink)
	}
	refresh(false)
	idx = map[string]int{} // nil and empty carry the same semantics
	refresh(false)
	p.Name = "changed"
	refresh(true)
	if m.palette.Name != p.Name || !maps.Equal(m.ansi, idx) {
		t.Fatal("palette-only change not retained")
	}
}

func TestBase16PaletteHasNoShadow(t *testing.T) {
	m := base16Model(t)
	base := frameCells(m.View().Content)
	m.showPalette = true
	popup := palettePopupRect(m)
	got := frameCells(m.View().Content)
	checked := 0
	for y, row := range got {
		for x := range row {
			if inShadow(popup, x, y) {
				checked++
				if !got[y][x].Equal(&base[y][x]) {
					t.Fatalf("base16 shadow changed cell %d,%d", x, y)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no shadow band checked")
	}
	// The popup itself is still painted in the terminal's basic colours.
	if _, ok := got[popup.Min.Y][popup.Min.X].Style.Fg.(ansi.BasicColor); !ok {
		t.Fatal("popup border lost its basic colour")
	}
}
