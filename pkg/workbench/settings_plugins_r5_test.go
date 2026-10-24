package workbench

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestSettingsStoreWheelReachesDetailAndProblems(t *testing.T) {
	for _, loaded := range []bool{false, true} {
		t.Run(fmt.Sprintf("loaded=%t", loaded), func(t *testing.T) {
			m, fe := pluginsFixture(t, "store")
			id, command := "long", "husk plugin add /opt/plugins/long"
			if loaded {
				id, command = "alpha", "already loaded"
			}
			fe.CatalogRows.Entries = []contracts.CatalogEntry{
				{ID: id, Name: "A Long", Description: strings.Repeat("description ", 200), Source: "/opt/plugins/long", SourceLayer: "user", Surfaces: []protocol.SurfaceID{protocol.SurfaceMainStage}, Tags: []string{"long"}},
				{ID: "next", Name: "B Next", Description: "Next entry detail.", Source: "/opt/plugins/next"},
			}
			for i := 0; i < 12; i++ {
				fe.CatalogRows.Problems = append(fe.CatalogRows.Problems, protocol.Problem{Severity: "warning", Code: "invalid_value", Message: fmt.Sprintf("catalog issue %d", i)})
			}
			fe.CatalogRev++
			m.settle()
			p := m.settings.plugins
			r := m.settingsContentRect()
			maxTop := max(0, len(p.lines)-r.Dy())
			if maxTop < 2 || p.selectedID != id {
				t.Fatal("fixture lacks a long selected detail")
			}
			// pluginText includes offscreen cached lines. Check the rendered viewport
			// as well, so this test cannot pass without bringing them onto the screen.
			visible := ansi.Strip(m.View().Content)
			if strings.Contains(visible, command) || strings.Contains(visible, "warning: invalid_value:") {
				t.Fatal("fixture's trailing lines were already visible")
			}
			markers := []string{"tags: long", "source (user): /opt/plugins/long", command, "warning: invalid_value: catalog issue 0", "warning: invalid_value: catalog issue 9", "and 2 more"}
			seen := map[string]bool{}
			for i := 0; i < maxTop+2; i++ {
				m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y, Button: tea.MouseWheelDown})
				if p.top != min(i+1, maxTop) {
					t.Fatalf("wheel down offset=%d, want %d", p.top, min(i+1, maxTop))
				}
				if p.cursor != 0 || p.selectedID != id {
					t.Fatal("body scrolling moved the entry cursor")
				}
				visible = ansi.Strip(m.View().Content)
				for _, marker := range markers {
					if strings.Contains(visible, marker) {
						seen[marker] = true
					}
				}
			}
			for _, marker := range markers {
				if !strings.Contains(pluginText(m), marker) || !seen[marker] {
					t.Fatalf("wheel never exposed %q", marker)
				}
			}
			if strings.Contains(pluginText(m), "catalog issue 10") {
				t.Fatal("catalog problem cap changed")
			}
			m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y, Button: tea.MouseWheelUp})
			if p.top != maxTop-1 {
				t.Fatal("wheel up did not scroll exactly one line")
			}
			for _, button := range []tea.MouseButton{tea.MouseWheelLeft, tea.MouseWheelRight} {
				m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y, Button: button})
				if p.top != maxTop-1 || p.selectedID != id {
					t.Fatal("horizontal wheel changed Store scroll or selection")
				}
			}
			settingsPress(m, "down")
			if p.top != 0 || p.cursor != 1 || p.selectedID != "next" || !strings.Contains(ansi.Strip(m.View().Content), "Next entry detail.") {
				t.Fatal("cursor key did not reset scrolling and show the next detail")
			}
			m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y, Button: tea.MouseWheelUp})
			if p.top != 0 || p.selectedID != "next" {
				t.Fatal("wheel up at the top moved the cursor or offset")
			}
		})
	}
}

func TestSettingsPluginDialogsIgnoreHorizontalWheel(t *testing.T) {
	for _, tab := range []string{"installed", "store"} {
		for _, button := range []tea.MouseButton{tea.MouseWheelLeft, tea.MouseWheelRight} {
			t.Run(fmt.Sprintf("%s/%d", tab, button), func(t *testing.T) {
				m, fe := pluginsFixture(t, tab)
				settingsPress(m, "enter")
				p := m.settings.plugins
				if tab == "store" {
					settingsPress(m, "down")
				}
				confirm, filter := p.confirm, p.filter
				surface, confirmed, cursor, top := p.surface, p.confirmed, p.cursor, p.top
				r := m.settingsContentRect()
				m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y + 2, Button: button})
				if p.confirm != confirm || p.filter != filter || p.surface != surface || p.confirmed != confirmed || p.cursor != cursor || p.top != top {
					t.Fatal("horizontal wheel changed an open plugin dialog")
				}
				if len(fe.PluginRemovals) != 0 {
					t.Fatal("horizontal wheel removed a plugin")
				}
			})
		}
	}
}
