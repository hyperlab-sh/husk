package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func keysFixture(t testing.TB) (*WorkbenchModel, *fakeFE) {
	t.Helper()
	m, fe := settingsFixture(t, "")
	fe.KeyRows = contracts.KeyListing{Entries: []contracts.KeyEntry{
		{Keys: []string{"ctrl+k", "ctrl+s"}, Action: "husk.settings.open", Source: protocol.Source{Layer: "user"}, State: "effective"},
		{Keys: []string{"x"}, Action: "p.open", Focus: "main-stage", Plugin: "p", Source: protocol.Source{Layer: "focus-local", Plugin: "p"}, State: "shadowed", ShadowedBy: &protocol.Source{Layer: "profile"}},
		{Keys: []string{"y"}, Action: "other.open", Plugin: "other", Source: protocol.Source{Layer: "distribution"}, State: "disabled", Disable: true},
	}}
	m.openSettings(map[string]any{"tab": "keys"})
	m.settings.focus = settingsContentFocus
	return m, fe
}
func TestSettingsKeysListsShadowedFiltersAndNeverWrites(t *testing.T) {
	m, fe := keysFixture(t)
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"KEY", "SCOPE", "ACTION", "LAYER", "STATE", "ctrl+k ctrl+s", "shadowed by", "edit keymap.json to change a key"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q", want)
		}
	}
	for query, want := range map[string]string{"CTRL+K": "husk.settings.open", "p.open": "p.open", "OTHER": "other.open"} {
		m.settings.keyQuery = query
		rows := m.settingsKeyRows()
		if len(rows) != 1 || rows[0].Action != want {
			t.Fatalf("filter %q: %+v", query, rows)
		}
	}
	m.settings.keyQuery = ""
	settingsPress(m, "ctrl+s")
	settingsPress(m, "enter")
	settingsPress(m, "x")
	if m.settings.keyQuery != "x" {
		t.Fatal("filter did not type")
	}
	if len(fe.SettingWrites) != 0 || len(fe.PluginRemovals) != 0 || len(fe.ProfileUses) != 0 || len(fe.Invoked) != 0 {
		t.Fatal("Keys called a writer")
	}
	// Shadowed rows use muted ink, not just a state label.
	m.settings.keyQuery = "p.open"
	if !strings.Contains(strings.Join(m.settingsKeysLines(), "\n"), "shadowed") {
		t.Fatal("shadowed row hidden")
	}
	r := m.settingsContentRect()
	cells := frameCells(m.View().Content)
	if !sameColor(cells[r.Min.Y+3][r.Min.X].Style.Fg, m.palette.InkMuted) {
		t.Fatal("shadowed row is not muted")
	}
}
func TestSettingsKeysPaletteQueryPasteReloadAndProblemLimit(t *testing.T) {
	m, fe := keysFixture(t)
	settingsPress(m, "界")
	settingsPress(m, "ctrl+a")
	if m.settings.keyQuery != "界" {
		t.Fatal("query accepted a chord or rejected Unicode")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m.Update(tea.PasteMsg{Content: "P.\r\nopen\t\x01"})
	if m.settings.keyQuery != "P. open " {
		t.Fatalf("paste not palette-compatible: %q", m.settings.keyQuery)
	}
	m.settings.keyQuery = "ctrl+k"
	fe.KeyRows.Entries[0].Action = "husk.palette.open"
	fe.KeyRows.Problems = nil
	for i := range 22 {
		fe.KeyRows.Problems = append(fe.KeyRows.Problems, protocol.Problem{Severity: "warning", Code: fmt.Sprintf("problem%02d", i), Message: "diagnostic"})
	}
	m.settle()
	if m.settings.keyQuery != "ctrl+k" || len(m.settingsKeyRows()) != 1 || m.settingsKeyRows()[0].Action != "husk.palette.open" {
		t.Fatal("reload did not preserve/reapply filter")
	}
	lines := strings.Join(m.settingsKeysLines(), "\n")
	if !strings.Contains(lines, "warning: problem19: diagnostic") || strings.Contains(lines, "problem20") || !strings.Contains(lines, "and 2 more") {
		t.Fatal("problem cap/count missing")
	}
	first := m.View().Content
	before := m.settings.keyTop
	if m.View().Content != first || m.settings.keyTop != before {
		t.Fatal("Keys View mutated scroll")
	}
	m.settings.keyQuery = ""
	m.settings.keyTop = 100
	m.settle()
	if !reflect.DeepEqual(m.settings.keyListing, fe.KeyRows) {
		t.Fatal("full listing was not preserved")
	}
}
