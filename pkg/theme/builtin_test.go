package theme

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func builtInPalette(t *testing.T, name string) (Palette, map[string]int) {
	t.Helper()
	values := BuiltIn()["themes"].(map[string]any)[name].(map[string]any)["palette"].(map[string]any)
	roles := map[string]string{"name": name}
	indices := map[string]int{}
	for role, value := range values {
		hex, index, err := ResolveValue(value)
		if err != nil {
			t.Fatalf("%s.%s: %v", name, role, err)
		}
		roles[role] = hex
		if index >= 0 {
			indices[role] = index
		}
	}
	b, _ := json.Marshal(roles)
	var p Palette
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p, indices
}

func TestBuiltInThemesMatchDocument(t *testing.T) {
	// Find the repository from this source file, without relying on the test's
	// working directory or embedding a machine-specific location.
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(source)))
	spec, err := os.ReadFile(filepath.Join(root, "docs", "docs", "design", "themes.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(spec), "### The document")
	if !ok {
		t.Fatal("document section missing")
	}
	_, block, _ := strings.Cut(section, "```json\n")
	block, _, _ = strings.Cut(block, "```")
	var want map[string]any
	if err := json.Unmarshal([]byte(block), &want); err != nil {
		t.Fatal(err)
	}
	got := BuiltIn()
	if !reflect.DeepEqual(got, want) {
		t.Fatal("embedded themes differ from the documented values")
	}
	names := []string{"charm", "charm-light", "dracula", "base16", "catppuccin-mocha", "catppuccin-latte", "rose-pine", "rose-pine-moon", "rose-pine-dawn"}
	if !reflect.DeepEqual(BuiltInNames(), names) || len(got["themes"].(map[string]any)) != len(names) {
		t.Fatal("built-in names/order changed")
	}
	for _, name := range names {
		values := got["themes"].(map[string]any)[name].(map[string]any)["palette"].(map[string]any)
		if len(values) != 19 || values["name"] != nil {
			t.Fatalf("%s: need exactly nineteen colour roles, no name", name)
		}
		for role := range Roles(Default()) {
			if role != "name" && values[role] == nil {
				t.Fatalf("%s missing %s", name, role)
			}
		}
	}
	got["themes"].(map[string]any)["charm"] = nil
	if BuiltIn()["themes"].(map[string]any)["charm"] == nil {
		t.Fatal("BuiltIn leaked mutable data")
	}
	names = BuiltInNames()
	names[0] = "mutated"
	if BuiltInNames()[0] != "charm" {
		t.Fatal("BuiltInNames leaked mutable data")
	}
}

func TestBuiltInThemesValidate(t *testing.T) {
	for _, name := range BuiltInNames() {
		t.Run(name, func(t *testing.T) {
			p, indices := builtInPalette(t, name)
			want := 0
			if name == "base16" {
				want = 19
			}
			if len(indices) != want {
				t.Fatalf("indices=%v want %d", indices, want)
			}
			for role, hex := range Roles(p) {
				if role != "name" {
					if _, err := RelativeLuminance(hex); err != nil || hex != strings.ToLower(hex) {
						t.Fatalf("%s = %q: %v", role, hex, err)
					}
				}
			}
		})
	}
}

func TestIndexResolvesToXtermDefault(t *testing.T) {
	want := []string{"#000000", "#800000", "#008000", "#808000", "#000080", "#800080", "#008080", "#c0c0c0", "#808080", "#ff0000", "#00ff00", "#ffff00", "#0000ff", "#ff00ff", "#00ffff", "#ffffff"}
	for n, hex := range want {
		got, index, err := ResolveValue(float64(n))
		r, g, b, _ := ansi.BasicColor(n).RGBA()
		basic := fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
		if err != nil || got != hex || got != basic || index != n {
			t.Errorf("index %d = %s/%d, want %s (%s): %v", n, got, index, hex, basic, err)
		}
	}
}

func TestBuiltInThemesContrast(t *testing.T) {
	for _, name := range BuiltInNames() {
		p, indices := builtInPalette(t, name)
		if violations := CheckContrast(p, indices); len(violations) != 0 {
			t.Errorf("%s: %+v", name, violations)
		}
	}
	// Only pairs with an index side are skipped; hex overrides remain checked.
	p, indices := builtInPalette(t, "base16")
	p.Ink, p.Canvas = "#000000", "#000000"
	delete(indices, "ink")
	delete(indices, "canvas")
	if len(CheckContrast(p, indices)) == 0 {
		t.Fatal("hex-hex pair skipped")
	}
	for _, side := range []string{"ink", "canvas"} {
		violations := CheckContrast(p, map[string]int{side: 0})
		for _, v := range violations {
			if v.Foreground == side || v.Background == side {
				t.Fatalf("index pair measured: %+v", v)
			}
		}
	}
}

func TestBuiltInThemesBorderFocusedDistinct(t *testing.T) {
	for _, name := range BuiltInNames() {
		p, indices := builtInPalette(t, name)
		if p.Border == p.BorderFocused || (name == "base16" && indices["border"] == indices["border_focused"]) {
			t.Errorf("%s: focused and unfocused edges match", name)
		}
	}
}

func TestBuiltInThemesLadder(t *testing.T) {
	for _, name := range BuiltInNames() {
		if name == "base16" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			p, _ := builtInPalette(t, name)
			dark := relLuminance(t, p.Canvas) < 0.5
			for i, pair := range [][2]string{{p.Canvas, p.Surface}, {p.Surface, p.Slot}, {p.Slot, p.SurfaceElevated}, {p.Slot, p.SurfaceHover}, {p.SurfaceElevated, p.SurfaceSelected}, {p.Slot, p.SurfaceSelected}} {
				a, b := relLuminance(t, pair[0]), relLuminance(t, pair[1])
				if (dark && a > b) || (!dark && a < b) {
					t.Errorf("ladder direction %s -> %s", pair[0], pair[1])
				}
				if i > 0 && contrast(t, pair[0], pair[1]) < 1.08 {
					t.Errorf("ladder separation %s -> %s", pair[0], pair[1])
				}
			}
		})
	}
}

func TestDefaultIsCharm(t *testing.T) {
	p, _ := builtInPalette(t, "charm")
	if got := Default(); got != p || got.Name != "charm" {
		t.Fatalf("default=%+v want=%+v", got, p)
	}
}
