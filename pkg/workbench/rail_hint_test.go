package workbench

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/stem/surfaces"
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

// A plugin that leaves takes its rail items out of the rail buttons:
// refresh keeps only live plugins' items.
func TestDepartedPluginRailItemsArePruned(t *testing.T) {
	m := newWorkbenchModel()
	fe := withFE(m)
	fe.Rails = []contracts.RailItem{{ID: "gone.open", Plugin: "gone", Label: "Gone", Placement: contracts.PlacementModal}}
	fe.bind("f7", "gone.open")
	fe.Snap = snapshot(1, map[string]string{"gone": "live"}, surfaces.Surfaces{})
	m.refresh()
	m.width = 200
	if len(m.railButtons()) != 4 || m.railButtons()[1].shortcut != "F7" {
		t.Fatal("rail item missing while the plugin is live")
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

func TestTopBarIsBrandAndOneTransientPill(t *testing.T) {
	for _, tc := range []struct {
		name, drag, divider string
		hover               int
		alt                 bool
		want                string
	}{
		{"idle", "", "", -1, false, ""},
		{"alt", "", "", -1, true, "⌥ ALT HELD · Shortcuts Active"},
		{"rail beats alt", "", "", 0, true, " Home Workbench (F1)"},
		{"divider beats rail", "", "sidebar-width", 0, true, "⇄ Drag border: sidebar-width"},
		{"drag beats divider", "sidebar-width", "sidepanel-width", 0, true, "⇄ Dragging sidebar-width"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := goldenModel()
			m.activeDrag, m.hoverDivider, m.hoverRailIdx, m.modifiers.Alt = tc.drag, tc.divider, tc.hover, tc.alt
			for _, w := range []int{84, 144, 400} {
				m.width = w
				bar := ansi.Strip(m.renderTopWorkbenchBar())
				if ansi.StringWidth(bar) != w || !strings.HasPrefix(bar, " ◈ HUSK ") || strings.TrimSpace(bar[len(" ◈ HUSK "):]) != tc.want {
					t.Fatalf("width %d: unexpected bar %q, pill %q", w, bar, tc.want)
				}
			}
		})
	}
}
