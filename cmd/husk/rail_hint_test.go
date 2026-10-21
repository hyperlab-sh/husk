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

// A disconnected plugin's rail items leave the rail buttons and the top-bar
// hint, and its shortcut stops matching.
func TestDisconnectPrunesRailItems(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	cp, _ := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "gone",
		RailItems: []huskipc.RailItemSpec{{ID: "gone.open", Label: "Gone", Shortcut: "f7", Placement: huskipc.PlacementModal}}})
	m.width = 200
	if !strings.Contains(ansi.Strip(m.renderTopWorkbenchBar()), "F7 Gone") {
		t.Fatal("rail hint missing before disconnect")
	}
	m.Update(pluginDisconnectedMsg{plugin: cp})
	for _, b := range m.railButtons() {
		if b.item.PluginID == "gone" {
			t.Fatalf("rail button survived disconnect: %+v", b)
		}
	}
	if strings.Contains(ansi.Strip(m.renderTopWorkbenchBar()), "Gone") {
		t.Fatal("rail hint survived disconnect")
	}
	if _, ok := m.railShortcutItem("f7"); ok {
		t.Fatal("shortcut of a disconnected plugin still matches")
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
