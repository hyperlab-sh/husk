package workbench

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func hintKeys(m *workbenchModel) []string {
	var out []string
	for _, h := range m.footerHints() {
		out = append(out, h.key)
	}
	return out
}

// With no plugins connected the chrome advertises only host features: the rail
// is Home, the slot lab and the palette, the palette lists only workbench
// commands, and the footer has no navigation keys of any application.
func TestZeroPluginsChromeIsHostOnly(t *testing.T) {
	m := newModel()
	m.width, m.height = 160, 40
	m.recomputeLayout()

	var kinds []string
	for _, b := range m.railButtons() {
		kinds = append(kinds, b.kind)
		if b.kind == "plugin" {
			t.Fatalf("plugin rail button with zero plugins: %+v", b)
		}
	}
	if got, want := strings.Join(kinds, ","), "home,hotspots,palette"; got != want {
		t.Fatalf("rail kinds = %s, want %s", got, want)
	}

	for _, c := range m.allCommands() {
		if c.PluginID != "husk.core" || !strings.HasPrefix(c.ID, "husk.") {
			t.Errorf("non-host command with zero plugins: %+v", c)
		}
		for _, banned := range []string{"bb", strings.Join([]string{"Her", "dr"}, ""), "tv.", "fleet", "F2", "F3", "F4"} {
			if strings.Contains(c.Title, banned) || strings.Contains(c.Description, banned) {
				t.Errorf("command %q mentions %q", c.ID, banned)
			}
		}
	}

	if got, want := strings.Join(hintKeys(m), "|"), "F1|drag/= |[ ] /|ctrl+p|q"; got != want {
		t.Fatalf("footer keys = %q, want %q", got, want)
	}
	bar := ansi.Strip(m.renderBottomKeybindBar())
	for _, banned := range []string{"j/k", "focus", "nav"} {
		if strings.Contains(bar, banned) {
			t.Errorf("footer %q advertises %q with zero plugins", bar, banned)
		}
	}
}

// Hints the host reports for the focused surface appear in the footer when the
// focus context names that surface's owner, and are capped.
func TestFooterHintsFollowHostHints(t *testing.T) {
	m := newModel()
	m.width, m.height = 200, 40
	addPublicationTestPlugin(m, hello{PluginID: "side", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
	fe := withFE(m)
	fe.Acts = []contracts.PluginAction{{Plugin: "side", ID: "side.new"}, {Plugin: "side", ID: "side.refresh"}}
	fe.HintsFn = func(ctx contracts.KeyContext) []contracts.KeyHint {
		if ctx.FocusSurface != string(protocol.SurfaceSidebarList) || ctx.FocusOwner != "side" {
			return nil
		}
		return []contracts.KeyHint{
			{Keys: []string{"n"}, Action: "side.new", Text: "new item"},
			{Keys: []string{"ctrl+r"}, Action: "side.refresh", Text: "refresh"},
		}
	}
	m.focusedSlot = protocol.SurfaceSidebarList

	if got, want := strings.Join(hintKeys(m), "|"), "F1|n|Ctrl+r|drag/= |[ ] /|ctrl+p|q"; got != want {
		t.Fatalf("footer keys = %q, want %q", got, want)
	}
	bar := ansi.Strip(m.renderBottomKeybindBar())
	if !strings.Contains(bar, "n new item") || !strings.Contains(bar, "Ctrl+r refresh") {
		t.Fatalf("footer missing plugin hints: %q", bar)
	}

	m.focusedSlot = protocol.SurfaceSidePanel // nobody owns it
	if strings.Contains(strings.Join(hintKeys(m), "|"), "Ctrl+r") {
		t.Fatal("hints leaked to a surface the plugin does not own")
	}
}

// A plugin cannot crowd the footer: at most maxPluginFooterHints are shown.
func TestFooterHintsCapPluginBindings(t *testing.T) {
	m := newModel()
	fe := withFE(m)
	m.plugins["p"] = true
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		fe.Acts = append(fe.Acts, contracts.PluginAction{Plugin: "p", ID: "p." + k})
	}
	fe.HintsFn = func(contracts.KeyContext) []contracts.KeyHint {
		var hs []contracts.KeyHint
		for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
			hs = append(hs, contracts.KeyHint{Keys: []string{k}, Action: "p." + k, Text: "do " + k})
		}
		return hs
	}
	m.focusedSlot = protocol.SurfaceSidebarList
	if got, want := len(m.footerHints()), 1+maxPluginFooterHints+4; got != want {
		t.Fatalf("hint count = %d, want %d", got, want)
	}
}

// The slot map's j/k hint appears only while it is the focused stage.
func TestFooterHintsSlotMapNav(t *testing.T) {
	m := newModel()
	m.stageMode = "hotspots"
	m.focusedSlot = protocol.SurfaceMainStage
	if !strings.Contains(strings.Join(hintKeys(m), "|"), "j/k") {
		t.Fatal("slot map focused but no j/k hint")
	}
	m.focusedSlot = protocol.SurfaceSidebarList
	if strings.Contains(strings.Join(hintKeys(m), "|"), "j/k") {
		t.Fatal("j/k hint shown while slot map is not focused")
	}
}
