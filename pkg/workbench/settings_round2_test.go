package workbench

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

func TestSettingsIdleTickUsesCachedSources(t *testing.T) {
	m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.count", Type: "int", Default: int64(2), Owner: "husk"})
	settingsPress(m, "tab")
	g := m.settings.general
	lines := &g.lines[0]
	fe.SchemaReads, fe.SettingReads, fe.KeyListingReads = 0, 0, 0
	m.Update(modifierTickMsg{})
	m.Update(struct{}{}) // an unrelated host message must not render every field
	if fe.SchemaReads != 0 || fe.SettingReads != 0 || fe.KeyListingReads != 0 || &g.lines[0] != lines {
		t.Fatalf("idle refreshed sources or rendering: schema=%d settings=%d keys=%d", fe.SchemaReads, fe.SettingReads, fe.KeyListingReads)
	}
	fe.SettingValues = map[string]contracts.SettingValue{"husk.count": {Value: float64(1000000), Source: "user"}}
	fe.ServiceRev++
	m.Update(struct{}{})
	if g.fields["husk.count"].text != "1000000" || fe.SchemaReads != 1 || fe.SettingReads != 1 || fe.KeyListingReads != 0 {
		t.Fatal("reload did not refresh General lazily")
	}
	m.openSettings(map[string]any{"tab": "keys"})
	m.settle()
	if fe.KeyListingReads != 1 {
		t.Fatal("Keys did not read on entry")
	}
	m.Update(modifierTickMsg{})
	if fe.KeyListingReads != 1 {
		t.Fatal("Keys re-read on an idle tick")
	}
	m.openSettings(map[string]any{"tab": "general"})
	m.settle()
	fe.ServiceRev++
	m.settle()
	if fe.KeyListingReads != 1 {
		t.Fatal("hidden Keys re-read on reload")
	}
	m.openSettings(map[string]any{"tab": "keys"})
	m.settle()
	if fe.KeyListingReads != 2 {
		t.Fatal("Keys did not re-read after a hidden reload")
	}
}

func TestSettingsSchemaTypeChangeResetsField(t *testing.T) {
	m, fe := generalFixture(t, contracts.SettingEntry{Key: "plugins.p.count", Type: "string", Default: "old", Owner: "plugin:p"})
	g := m.settings.general
	old := g.fields["plugins.p.count"]
	editGeneral(m, old.entry.Key, "old unsaved text")
	fe.SchemaRows[0].Type = "int"
	fe.SchemaRows[0].Default = float64(1000000)
	fe.ServiceRev++
	m.settle()
	f := g.fields[old.entry.Key]
	value, err := f.value()
	if f == old || f.text != "1000000" || f.dirty() || err != nil || value != int64(1000000) {
		t.Fatalf("mixed old schema state: text=%q baseline=%#v value=%#v error=%v", f.text, f.baseline, value, err)
	}
}

func TestSettingsKeysEndThenUpMovesImmediately(t *testing.T) {
	m, fe := keysFixture(t)
	fe.KeyRows.Entries = nil
	for i := range 70 {
		fe.KeyRows.Entries = append(fe.KeyRows.Entries, contracts.KeyEntry{Keys: []string{fmt.Sprintf("key%02d", i)}, Action: "husk.settings.open", State: "effective"})
	}
	fe.ServiceRev++
	m.settle()
	settingsPress(m, "end")
	if m.settings.keyTop != m.settingsKeysMaxTop() {
		t.Fatal("end left scroll state beyond the viewport limit")
	}
	before := ansi.Strip(m.settingsKeysLines()[3])
	top := m.settings.keyTop
	settingsPress(m, "up")
	after := ansi.Strip(m.settingsKeysLines()[3])
	if m.settings.keyTop != top-1 || before == after {
		t.Fatalf("up after end did not move the first visible row: %q -> %q", before, after)
	}
	settingsPress(m, "end")
	for range 3 {
		settingsPress(m, "down")
		settingsPress(m, "pgdown")
	}
	r := m.settingsContentRect()
	for range 3 {
		m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y, Button: tea.MouseWheelDown})
	}
	if m.settings.keyTop != top {
		t.Fatal("down/page/wheel escaped the scroll limit")
	}
	m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y, Button: tea.MouseWheelUp})
	if m.settings.keyTop != top-1 {
		t.Fatal("wheel up after saturation did not move")
	}
	m.settings.keyQuery = "key00"
	m.settle()
	if m.settings.keyTop != 0 || !strings.Contains(ansi.Strip(strings.Join(m.settingsKeysLines(), "\n")), "key00") {
		t.Fatal("filter did not clamp the old scroll position")
	}
}
