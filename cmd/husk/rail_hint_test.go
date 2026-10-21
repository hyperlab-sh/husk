package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
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

// Many declared rail shortcuts must not push the theme pill off the top bar:
// trailing hint entries are dropped and marked with an ellipsis.
func TestTopBarRailHintFitsWidth(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	var items []huskipc.RailItemSpec
	for i := range 30 {
		items = append(items, huskipc.RailItemSpec{
			ID: fmt.Sprintf("p.item%d", i), Label: fmt.Sprintf("Item number %d", i),
			Shortcut: fmt.Sprintf("ctrl+%c", 'a'+i%26), Placement: huskipc.PlacementModal,
		})
	}
	addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p", RailItems: items})
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
