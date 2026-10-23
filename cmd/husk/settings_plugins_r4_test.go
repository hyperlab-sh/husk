package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestSettingsStoreQueryMatchesIndividualFields(t *testing.T) {
	for _, tt := range []struct {
		query string
		want  int
	}{
		{"", 1}, {"WRITER", 1}, {"NOTEBOOK", 1}, {"DRAFTS", 1}, {"NOTES", 1}, {"LOCAL", 1},
		{"ter note", 0}, {"book make", 0}, {"drafts notes", 0}, {"tes loc", 0},
		{"standalone", 0}, {"main-stage", 0}, {"distribution", 0},
	} {
		t.Run(tt.query, func(t *testing.T) {
			m, fe := pluginsFixture(t, "store")
			fe.CatalogRows.Entries = []contracts.CatalogEntry{{ID: "writer", Name: "Notebook", Description: "Make drafts", Tags: []string{"notes", "local"}, Source: "/opt/packages/standalone", SourceLayer: "distribution", Surfaces: []protocol.SurfaceID{protocol.SurfaceMainStage}}}
			fe.CatalogRev++
			p := m.settings.plugins
			p.query = tt.query
			p.dirty = true
			m.settle()
			if len(p.filtered) != tt.want {
				t.Fatalf("query %q kept %d entries, want %d", tt.query, len(p.filtered), tt.want)
			}
		})
	}
}

func TestSettingsPluginsListCursorKeys(t *testing.T) {
	for _, tab := range []string{"installed", "store"} {
		t.Run(tab, func(t *testing.T) {
			m, fe := pluginsFixture(t, tab)
			pods := map[string]any{}
			fe.CatalogRows.Entries = nil
			for i := 0; i < 40; i++ {
				id := fmt.Sprintf("p%02d", i)
				pods[id] = map[string]string{"status": "live"}
				fe.CatalogRows.Entries = append(fe.CatalogRows.Entries, contracts.CatalogEntry{ID: id, Name: id, Description: "Detail for " + id})
			}
			raw, err := json.Marshal(map[string]any{"plugins": pods})
			if err != nil {
				t.Fatal(err)
			}
			setSettingsSlice(fe, "husk.pods", string(raw))
			fe.CatalogRev++
			m.settle()
			page := max(1, m.settingsContentRect().Dy()/2)
			for _, step := range []struct {
				key  string
				want int
			}{
				{"down", 1}, {"ctrl+j", 2}, {"up", 1}, {"ctrl+k", 0},
				{"pgdown", page}, {"pgdown", 2 * page}, {"pgup", page},
				{"end", 39}, {"down", 39}, {"pgdown", 39}, {"home", 0}, {"up", 0}, {"pgup", 0},
			} {
				settingsPress(m, step.key)
				p := m.settings.plugins
				if p.cursor != step.want {
					t.Fatalf("%s cursor=%d, want %d", step.key, p.cursor, step.want)
				}
				if tab == "store" {
					id := fmt.Sprintf("p%02d", step.want)
					if p.selectedID != id || !strings.Contains(pluginText(m), "Detail for "+id) {
						t.Fatalf("%s detail did not follow %s", step.key, id)
					}
					if p.top != 0 {
						t.Fatal("list navigation left the filter and rows scrolled away")
					}
				}
			}
			if len(fe.PluginRemovals) != 0 {
				t.Fatal("list navigation removed a plugin")
			}
		})
	}
}

func TestSettingsInstalledConfirmChoiceKeysAndWheel(t *testing.T) {
	for _, key := range []string{"left", "right", "h", "l", "y", "n", "up", "down", "wheel-up", "wheel-down"} {
		t.Run(key, func(t *testing.T) {
			m, fe := pluginsFixture(t, "installed")
			settingsPress(m, "enter")
			if key == "n" {
				settingsPress(m, "y")
			}
			switch key {
			case "left":
				m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
			case "wheel-up", "wheel-down":
				button := tea.MouseWheelUp
				if key == "wheel-down" {
					button = tea.MouseWheelDown
				}
				r := m.settingsContentRect()
				m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y, Button: button})
			default:
				settingsPress(m, key)
			}
			p := m.settings.plugins
			if p.confirm == nil || p.confirmed != (key != "n") || len(fe.PluginRemovals) != 0 {
				t.Fatal("choice did not apply without answering")
			}
			settingsPress(m, "enter")
			want := 1
			if key == "n" {
				want = 0
			}
			if p.confirm != nil || len(fe.PluginRemovals) != want {
				t.Fatal("enter did not answer with the chosen button")
			}
		})
	}
}

func TestSettingsStoreSurfaceKeysApplyLiveAndCommit(t *testing.T) {
	m, fe := pluginsFixture(t, "store")
	surfaces := []protocol.SurfaceID{
		protocol.SurfaceActivityRail, protocol.SurfaceSidebarNavigation, protocol.SurfaceSidebarList,
		protocol.SurfaceThreadRowStatus, protocol.SurfaceSidebarFooter, protocol.SurfaceMainHeader,
		protocol.SurfaceMainStage, protocol.SurfaceSidePanel, protocol.SurfaceAppOverlay,
		protocol.SurfaceModal, protocol.SurfaceFullCanvas, protocol.SurfaceWorkspaceStage,
	}
	fe.CatalogRows.Entries = nil
	for _, surface := range surfaces {
		fe.CatalogRows.Entries = append(fe.CatalogRows.Entries, contracts.CatalogEntry{ID: string(surface), Name: string(surface), Surfaces: []protocol.SurfaceID{surface}})
	}
	fe.CatalogRev++
	m.settle()
	settingsPress(m, "enter")
	p := m.settings.plugins
	if p.filter == nil || len(fe.PluginRemovals) != 0 {
		t.Fatal("Store enter did not open only the surface Select")
	}
	for _, step := range []struct{ key, want string }{
		{"down", "activity-rail"}, {"ctrl+j", "sidebar-navigation"}, {"up", "activity-rail"}, {"ctrl+k", ""},
		{"end", "workspace-stage"}, {"home", ""},
	} {
		settingsPress(m, step.key)
		if p.filter == nil || p.surface != step.want {
			t.Fatalf("%s surface=%q, want %q", step.key, p.surface, step.want)
		}
		want := 1
		if step.want == "" {
			want = 12
		}
		if len(p.filtered) != want || want == 1 && p.filtered[0].ID != step.want {
			t.Fatalf("%s did not filter live", step.key)
		}
	}
	// Check every option in registry order, not merely the first and last.
	for _, surface := range surfaces {
		settingsPress(m, "down")
		if p.surface != string(surface) || len(p.filtered) != 1 || p.filtered[0].ID != string(surface) {
			t.Fatalf("wrong live option for %s", surface)
		}
	}
	for _, key := range []string{"j", "k", "/", "pgdown", "x", "4"} {
		settingsPress(m, key)
		if p.surface != "workspace-stage" || p.query != "" || p.filter == nil {
			t.Fatalf("unlisted Select key %q changed state", key)
		}
	}
	settingsPress(m, "enter")
	if p.filter != nil || p.surface != "workspace-stage" || len(fe.PluginRemovals) != 0 {
		t.Fatal("enter did not commit only the filter")
	}
	settingsPress(m, "enter")
	settingsPress(m, "home")
	settingsPress(m, "esc")
	if m.settings == nil || p.filter != nil || p.surface != "workspace-stage" || len(p.filtered) != 1 {
		t.Fatal("escape did not restore the committed surface")
	}
	r := m.settingsContentRect()
	settingsClick(m, r.Min.X, r.Min.Y)
	if p.filter == nil {
		t.Fatal("filter-row click did not open the Select")
	}
}

func TestSettingsStorePrintableQueryAndEnter(t *testing.T) {
	m, fe := pluginsFixture(t, "store")
	for _, text := range []string{"4", "x", "Ω", " "} {
		settingsPress(m, text)
	}
	p := m.settings.plugins
	if p.query != "4xΩ " || m.settings.tab != "store" || p.filter != nil {
		t.Fatal("printable query keys activated a control")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if p.query != "4x" {
		t.Fatalf("backspace did not remove runes: %q", p.query)
	}
	settingsPress(m, "enter")
	if p.filter == nil || p.query != "4x" || len(fe.PluginRemovals) != 0 {
		t.Fatal("enter acted on a catalog entry or changed query")
	}
}

func TestSettingsPluginDialogsDismissBeforeTraversalAndClose(t *testing.T) {
	for _, dialog := range []string{"confirm", "surface", "profile"} {
		for _, key := range []string{"esc", "tab", "shift+tab", "ctrl+c"} {
			t.Run(dialog+"/"+key, func(t *testing.T) {
				tab := "installed"
				if dialog == "surface" {
					tab = "store"
				}
				m, fe := pluginsFixture(t, tab)
				s := m.settings
				p := s.plugins
				switch dialog {
				case "confirm":
					settingsPress(m, "enter")
					settingsPress(m, "y")
				case "surface":
					p.surface = "main-stage"
					p.dirty = true
					m.settle()
					settingsPress(m, "enter")
					settingsPress(m, "home")
					if p.surface != "" {
						t.Fatal("surface fixture did not change live")
					}
				case "profile":
					s.focus = settingsProfileFocus
					m.openSettingsProfiles()
					settingsPress(m, "down")
				}
				focus := s.focus
				settingsPress(m, key)
				if len(fe.PluginRemovals) != 0 || len(fe.ProfileUses) != 0 {
					t.Fatal("dismissal answered a dialog")
				}
				if key == "ctrl+c" {
					if m.settings != nil {
						t.Fatal("ctrl+c did not close the whole center")
					}
					return
				}
				if m.settings != s || p.confirm != nil || p.filter != nil || s.profileForm != nil {
					t.Fatal("dismissal closed the center or left a dialog open")
				}
				if dialog == "surface" && p.surface != "main-stage" {
					t.Fatal("dismissal failed to restore previous surface")
				}
				want := focus
				if key == "tab" {
					want = (focus + 1) % 3
				}
				if key == "shift+tab" {
					want = (focus + 2) % 3
				}
				if s.focus != want {
					t.Fatalf("%s focus=%d, want %d", key, s.focus, want)
				}
			})
		}
	}
}

func TestSettingsStoreSurfaceWheelAndInsideClickCancel(t *testing.T) {
	m, fe := pluginsFixture(t, "store")
	p := m.settings.plugins
	p.surface = "main-stage"
	p.dirty = true
	m.settle()
	settingsPress(m, "enter")
	settingsPress(m, "end")
	r := m.settingsContentRect()
	m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y + 2, Button: tea.MouseWheelUp})
	if p.filter == nil || p.surface != "full-canvas" {
		t.Fatal("wheel up did not apply previous option live")
	}
	m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y + 2, Button: tea.MouseWheelDown})
	if p.filter == nil || p.surface != "workspace-stage" {
		t.Fatal("wheel down did not apply next option live")
	}
	settingsClick(m, r.Min.X+2, r.Min.Y+2)
	if m.settings == nil || p.filter != nil || p.surface != "main-stage" || len(fe.PluginRemovals) != 0 {
		t.Fatal("inside Select click did not cancel and restore without picking")
	}
}
