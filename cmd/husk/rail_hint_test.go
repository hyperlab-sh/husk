package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/surfaces"
)

func TestRailShortcutLabel(t *testing.T) {
	cases := map[string]string{
		"f4":         "F4",
		"ctrl+g":     "Ctrl+g",
		"alt+X":      "Alt+X",
		"alt+x":      "Alt+x",
		"alt+ctrl+x": "Alt+Ctrl+x",
		"alt+enter":  "Alt+Enter",
		"d":          "d",
		"D":          "D",
		"ctrl++":     "Ctrl++",
		"+":          "+",
		" ":          " ",
		"":           "",
	}
	for in, want := range cases {
		if got := railShortcutLabel(in); got != want {
			t.Errorf("railShortcutLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// A plugin that leaves takes its rail items out of the rail buttons and the
// top-bar hint: refresh keeps only live plugins' items.
func TestDepartedPluginRailItemsArePruned(t *testing.T) {
	m := newWorkbenchModel()
	fe := withFE(m)
	fe.Rails = []contracts.RailItem{{ID: "gone.open", Plugin: "gone", Label: "Gone", Placement: contracts.PlacementModal}}
	fe.bind("f7", "gone.open")
	fe.Snap = snapshot(1, map[string]string{"gone": "live"}, surfaces.Surfaces{})
	m.refresh()
	m.width = 200
	if !strings.Contains(ansi.Strip(m.renderTopWorkbenchBar()), "F7 Gone") {
		t.Fatal("rail hint missing while the plugin is live")
	}
	fe.Snap = snapshot(2, map[string]string{"gone": "exited"}, surfaces.Surfaces{})
	m.refresh()
	for _, b := range m.railButtons() {
		if b.item.Plugin == "gone" {
			t.Fatalf("rail button survived the plugin leaving: %+v", b)
		}
	}
	if strings.Contains(ansi.Strip(m.renderTopWorkbenchBar()), "Gone") {
		t.Fatal("rail hint survived the plugin leaving")
	}
}

// Many declared rail shortcuts must not push the theme pill off the top bar:
// trailing hint entries are dropped and marked with an ellipsis.
func TestTopBarRailHintFitsWidth(t *testing.T) {
	m := newWorkbenchModel()
	fe := withFE(m)
	for i := range 30 {
		id := fmt.Sprintf("p.item%d", i)
		fe.Rails = append(fe.Rails, contracts.RailItem{ID: id, Plugin: "p", Label: fmt.Sprintf("Item number %d", i), Placement: contracts.PlacementModal})
		fe.bind(fmt.Sprintf("ctrl+%c", 'a'+i%26), id)
	}
	m.plugins["p"] = true
	m.railItems = fe.Rails
	for _, w := range []int{84, 144, 400} {
		m.width = w
		bar := ansi.Strip(m.renderTopWorkbenchBar())
		if got := ansi.StringWidth(bar); got > w {
			t.Fatalf("width %d: top bar is %d cells", w, got)
		}
		if !strings.Contains(bar, m.palette.Name) {
			t.Fatalf("width %d: theme pill pushed off: %q", w, bar)
		}
		if w == 144 && !strings.Contains(bar, "…") {
			t.Fatalf("width %d: truncated hint lacks ellipsis: %q", w, bar)
		}
		if strings.Contains(bar, "CTRL+") {
			t.Fatalf("width %d: shortcut not formatted: %q", w, bar)
		}
	}
	m.width = 400
	if bar := ansi.Strip(m.renderTopWorkbenchBar()); !strings.Contains(bar, "Ctrl+a Item number 0") {
		t.Fatalf("wide bar lacks formatted hint: %q", bar)
	}
}
