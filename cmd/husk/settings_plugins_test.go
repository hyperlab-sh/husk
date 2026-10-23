package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func pluginsFixture(t testing.TB, tab string) (*WorkbenchModel, *fakeFE) {
	t.Helper()
	m, fe := settingsFixture(t, "")
	fe.ConfigPath = "/home/example/.config/husk"
	fe.Snap = &state.Snapshot{Slices: map[string]state.Entry{
		"husk.pods":       {Value: json.RawMessage(`{"plugins":{"alpha":{"status":"failed","dir":"/opt/plugins/alpha","last_error":"process failed"}}}`)},
		"husk.surfaces":   {Value: json.RawMessage(`{"occupants":{"main-stage":{"plugin":"alpha"},"sidebar-list":{"plugin":"other"}}}`)},
		"alpha.occupants": {Value: json.RawMessage(`{"main-stage":{},"sidebar-list":{}}`)},
	}}
	fe.CatalogRows = contracts.CatalogResult{Entries: []contracts.CatalogEntry{
		{ID: "web", Name: "Web", Description: "A remote plugin.", Source: "https://example.invalid/web", Surfaces: []protocol.SurfaceID{protocol.SurfaceSidePanel}, Tags: []string{"remote"}, SourceLayer: "user"},
		{ID: "alpha", Name: "Alpha", Description: "A local plugin.", Source: "~/plugins/alpha", Surfaces: []protocol.SurfaceID{protocol.SurfaceMainStage}, SourceLayer: "user"},
		{ID: "beta", Name: "Beta", Description: "Another local plugin.", Source: "/opt/plugins/beta", Surfaces: []protocol.SurfaceID{protocol.SurfaceSidebarList}, SourceLayer: "distribution"},
	}}
	m.openSettings(map[string]any{"tab": tab})
	m.settings.focus = settingsContentFocus
	m.settle()
	return m, fe
}
func pluginText(m *WorkbenchModel) string {
	return ansi.Strip(strings.Join(m.settings.plugins.lines, "\n"))
}

func TestSettingsInstalledListsClaimsHeldDirAndStateRefresh(t *testing.T) {
	m, fe := pluginsFixture(t, "installed")
	text := pluginText(m)
	for _, want := range []string{"alpha", "failed", "main-stage", "sidebar-list", "/opt/plugins/alpha", "process failed", "husk plugin add <path>", "husk plugin add --dir <dir>"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
	line := m.settings.plugins.lines[1]
	if !strings.Contains(line, "main-stage") || !strings.Contains(line, "sidebar-list") {
		t.Fatal("claim missing")
	}
	// Decode actual cells to distinguish held from refused claims and failure status.
	cells := frameCells(m.View().Content)
	r := m.settingsContentRect()
	raw := ansi.Strip(line)
	heldX := strings.Index(raw, "main-stage")
	refusedX := strings.Index(raw, "sidebar-list")
	if !sameColor(cells[r.Min.Y+1][r.Min.X+heldX].Style.Fg, m.palette.Ink) || !sameColor(cells[r.Min.Y+1][r.Min.X+refusedX].Style.Fg, m.palette.InkMuted) {
		t.Fatal("held/refused claim colours")
	}
	for status, colour := range map[string]string{"live": "success", "starting": "ink", "exited": "ink_muted", "failed": "danger"} {
		t.Run(status, func(t *testing.T) {
			fe.Snap.Slices["husk.pods"] = state.Entry{Value: json.RawMessage(fmt.Sprintf(`{"plugins":{"alpha":{"status":%q}}}`, status))}
			m.settle()
			if !strings.Contains(pluginText(m), "(command line)") {
				t.Fatal("implicit source missing")
			}
			cells := frameCells(m.View().Content)
			x := strings.Index(ansi.Strip(m.settings.plugins.lines[1]), status)
			want := m.palette.Ink
			switch colour {
			case "success":
				want = m.palette.Success
			case "ink_muted":
				want = m.palette.InkMuted
			case "danger":
				want = m.palette.Danger
			}
			if !sameColor(cells[r.Min.Y+1][r.Min.X+x].Style.Fg, want) {
				t.Fatal("status colour")
			}
		})
	}
	fe.Snap.Slices["husk.pods"] = state.Entry{Value: json.RawMessage(`{"plugins":{}}`)}
	m.settle()
	if len(m.settings.plugins.rows) != 0 || strings.Contains(pluginText(m), "alpha") {
		t.Fatal("removed row stayed")
	}
}
func TestSettingsInstalledRemoveYesNoPendingErrorAndLateReply(t *testing.T) {
	m, fe := pluginsFixture(t, "installed")
	settingsPress(m, "enter")
	if !strings.Contains(ansi.Strip(m.settings.plugins.confirmView), "not uninstalled") {
		t.Fatal("missing removal explanation")
	}
	settingsPress(m, "n")
	settingsPress(m, "enter")
	if len(fe.PluginRemovals) != 0 {
		t.Fatal("no removed plugin")
	}
	settingsPress(m, "x")
	settingsPress(m, "esc")
	if m.settings == nil || m.settings.plugins.confirm != nil {
		t.Fatal("esc closed center, not confirm")
	}
	settingsPress(m, "x")
	settingsPress(m, "y")
	settingsPress(m, "enter")
	if len(fe.PluginRemovals) != 1 || fe.PluginRemovals[0].ID != "alpha" {
		t.Fatal("yes did not remove exactly once")
	}
	if !strings.Contains(pluginText(m), "removing…") {
		t.Fatal("pending not drawn")
	}
	fe.PluginRemovals[0].Reply(contracts.PluginRemoval{}, contracts.NewError(protocol.CodeNotFound, "gone", nil))
	m.settle()
	if !strings.Contains(pluginText(m), "not_found: gone") {
		t.Fatal("error not shown")
	}
	settingsPress(m, "up")
	if strings.Contains(pluginText(m), "not_found") {
		t.Fatal("error not cleared")
	}
	settingsPress(m, "x")
	settingsPress(m, "y")
	settingsPress(m, "enter")
	settingsPress(m, "esc")
	m.openSettings(map[string]any{"tab": "installed"})
	fe.PluginRemovals[1].Reply(contracts.PluginRemoval{}, contracts.NewError(protocol.CodeConflict, "late", nil))
	m.settle()
	if strings.Contains(pluginText(m), "late") {
		t.Fatal("late reply touched new center")
	}
}
func TestSettingsStoreEmptyCatalogFiltersCommandsInstalledTakenAndProblems(t *testing.T) {
	m, fe := pluginsFixture(t, "store")
	text := pluginText(m)
	for _, want := range []string{"Alpha", "Web", "installed", "already loaded", "taken"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	settingsPress(m, "down")
	if !strings.Contains(pluginText(m), "husk plugin add /opt/plugins/beta") {
		t.Fatal("path command missing")
	}
	settingsPress(m, "down")
	if !strings.Contains(pluginText(m), "fetch it, then: husk plugin add <directory>") {
		t.Fatal("URL fetch command missing")
	}
	p := m.settings.plugins
	p.query = "REMOTE"
	p.selectedID = ""
	p.dirty = true
	m.settle()
	if len(p.filtered) != 1 || p.filtered[0].ID != "web" {
		t.Fatal("text filter")
	}
	p.query = ""
	p.surface = "main-stage"
	p.selectedID = ""
	p.dirty = true
	m.settle()
	if len(p.filtered) != 1 || p.filtered[0].ID != "alpha" {
		t.Fatal("surface filter")
	}
	p.query = "missing"
	p.dirty = true
	m.settle()
	if !strings.Contains(pluginText(m), "no entry matches") {
		t.Fatal("no match message")
	}
	p.query = ""
	p.surface = ""
	p.selectedID = ""
	p.dirty = true
	fe.CatalogRows = contracts.CatalogResult{}
	for i := range 12 {
		fe.CatalogRows.Problems = append(fe.CatalogRows.Problems, protocol.Problem{Code: fmt.Sprintf("invalid%02d", i), Message: "catalog.json (user): invalid entry", Severity: "warning"})
	}
	m.settle()
	text = pluginText(m)
	for _, want := range []string{"No catalog. This Husk ships none;", "/home/example/.config/husk/catalog.json", "\"version\": 1", "\"editor\"", "\"clock\"", "\"tree\"", "warning: invalid09", "and 2 more"} {
		if !strings.Contains(text, want) {
			t.Errorf("empty catalog missing %q", want)
		}
	}
	if strings.Contains(text, "invalid10") {
		t.Fatal("problem limit")
	}
	if len(fe.SettingWrites)+len(fe.PluginRemovals)+len(fe.ProfileUses)+len(fe.Invoked) != 0 {
		t.Fatal("catalog invoked a writer")
	}
}
func TestSettingsPluginsCacheReloadSelectPasteMouseAndViewPurity(t *testing.T) {
	m, fe := pluginsFixture(t, "store")
	p := m.settings.plugins
	before := &p.lines[0]
	m.settle()
	if before != &p.lines[0] {
		t.Fatal("idle tick recomputed content")
	}
	old := m.View().Content
	top := p.top
	if old != m.View().Content || p.top != top {
		t.Fatal("View changed state")
	}
	settingsPress(m, "enter")
	if p.filter == nil {
		t.Fatal("no surface Select")
	}
	settingsPress(m, "end")
	settingsPress(m, "enter")
	// Last option is workspace-stage, one of all twelve registry surfaces.
	if p.surface != "workspace-stage" {
		t.Fatalf("surface options: %s", p.surface)
	}
	p.surface = ""
	p.dirty = true
	m.settle()
	m.Update(tea.PasteMsg{Content: "re\r\nmote\t\x01"})
	if p.query != "re mote " {
		t.Fatal("paste did not use palette sanitation")
	}
	p.query = ""
	p.selectedID = "beta"
	p.dirty = true
	m.settle()
	fe.CatalogRows.Entries[0].Name = "Aardvark"
	fe.ServiceRev++
	m.settle()
	if p.filtered[p.cursor].ID != "beta" {
		t.Fatal("reload lost highlighted id")
	}
	// Worker completion after the reload is also visible.
	fe.CatalogRows.Entries = append(slicesCopy(fe.CatalogRows.Entries), contracts.CatalogEntry{ID: "later", Name: "Later", Source: "~/plugins/later"})
	m.settle()
	if len(p.filtered) != 4 {
		t.Fatal("late warmed catalog invisible")
	}
	r := m.settingsContentRect()
	m.settingsMouse(tea.MouseClickMsg{X: r.Min.X, Y: r.Min.Y + 2, Button: tea.MouseLeft})
	m.settle()
	if p.filtered[p.cursor].ID != "web" {
		t.Fatal("list mouse selection")
	}
	if len(fe.SettingWrites)+len(fe.PluginRemovals)+len(fe.ProfileUses)+len(fe.Invoked) != 0 {
		t.Fatal("read-only navigation called a writer")
	}
	if !reflect.DeepEqual(p.catalog, fe.CatalogRows) {
		t.Fatal("cached source differs")
	}
}
func slicesCopy(in []contracts.CatalogEntry) []contracts.CatalogEntry {
	return append([]contracts.CatalogEntry(nil), in...)
}
