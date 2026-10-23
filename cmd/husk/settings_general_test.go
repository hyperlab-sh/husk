package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

func generalFixture(t testing.TB, entries ...contracts.SettingEntry) (*WorkbenchModel, *fakeFE) {
	t.Helper()
	m, fe := settingsFixture(t, "")
	fe.SchemaRows = entries
	m.openSettings(map[string]any{"tab": "general"})
	return m, fe
}
func editGeneral(m *WorkbenchModel, key, text string) {
	f := m.settings.general.fields[key]
	f.text = text
	f.revision++
	f.syncWidget()
	m.cacheSettingsGeneral()
}
func TestSettingsGeneralGroupingOrderAndCollapsedPlugins(t *testing.T) {
	m, _ := generalFixture(t,
		contracts.SettingEntry{Key: "plugins.Z.nested.z", Type: "string", Default: "z", Owner: "plugin:Z"},
		contracts.SettingEntry{Key: "dist.tools.z", Type: "string", Default: "z", Owner: "dist"},
		contracts.SettingEntry{Key: "theme.name", Type: "string", Default: theme.Default().Name, Owner: "husk"},
		contracts.SettingEntry{Key: "husk.plugins.paths", Type: "strings", Default: []string{}, Owner: "husk"},
		contracts.SettingEntry{Key: "husk.bundle", Type: "string", Default: "", Owner: "husk"},
		contracts.SettingEntry{Key: "plugins.A.x", Type: "string", Default: "x", Owner: "plugin:A"},
		contracts.SettingEntry{Key: "plugins.Z.a", Type: "string", Default: "a", Owner: "plugin:Z"})
	g := m.settings.general
	var names []string
	for _, group := range g.groups {
		names = append(names, group.name)
	}
	if !reflect.DeepEqual(names, []string{"husk", "husk.plugins", "theme", "dist.tools", "plugins.A", "plugins.Z"}) {
		t.Fatalf("groups %v", names)
	}
	if !reflect.DeepEqual(g.groups[5].keys, []string{"plugins.Z.a", "plugins.Z.nested.z"}) {
		t.Fatal("plugin keys not sorted/grouped together")
	}
	for _, key := range g.targets() {
		if strings.HasPrefix(key, "plugins.") {
			t.Fatal("plugin field initially expanded")
		}
	}
	if slices.Contains(names, "plugins.unregistered") {
		t.Fatal("unregistered group")
	}
	g.active = "@plugins.Z"
	m.settings.focus = settingsContentFocus
	settingsPress(m, "enter")
	if !g.expanded["plugins.Z"] || !slices.Contains(g.targets(), "plugins.Z.a") {
		t.Fatal("plugin expansion")
	}
	settingsPress(m, "enter")
	if g.expanded["plugins.Z"] {
		t.Fatal("plugin collapse")
	}
}

func TestSettingsGeneralFieldTypesAndValues(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		initial any
		edit    string
		want    any
	}{
		{"string", "before", "after", "after"}, {"bool", false, "", true}, {"enum", "one", "two", "two"},
		{"int", int64(2), "42", int64(42)}, {"duration", 1500 * time.Microsecond, "20", int64(20)},
		{"strings", []string{"one", "two"}, "  alpha \n\n beta \n", []string{"alpha", "beta"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.value", Type: tc.kind, Default: tc.initial, Enum: []string{"one", "two"}, Owner: "husk"})
			f := m.settings.general.fields["husk.value"]
			good := false
			switch tc.kind {
			case "bool":
				_, good = f.widget.(*huh.Confirm)
			case "enum":
				_, good = f.widget.(*huh.Select[string])
			case "strings":
				_, good = f.widget.(*huh.Text)
				if f.text != "one\ntwo" {
					t.Fatal("strings are not lines")
				}
			default:
				_, good = f.widget.(*huh.Input)
			}
			if !good {
				t.Fatalf("wrong widget %T", f.widget)
			}
			if tc.kind == "duration" && f.text != "1" {
				t.Fatalf("duration not truncated to integral ms: %q", f.text)
			}
			m.settings.focus = settingsContentFocus
			m.cacheSettingsGeneral()
			switch tc.kind {
			case "bool":
				settingsPress(m, "right")
			case "enum":
				settingsPress(m, "end")
			default:
				editGeneral(m, f.entry.Key, "")
				m.Update(tea.PasteMsg{Content: tc.edit})
			}
			settingsPress(m, "ctrl+s")
			if len(fe.SettingWrites) != 1 || !reflect.DeepEqual(fe.SettingWrites[0].Value, tc.want) {
				t.Fatalf("write %+v want %#v", fe.SettingWrites, tc.want)
			}
		})
	}
}

func TestSettingsGeneralOutOfRangeIntegerRefusedInline(t *testing.T) {
	low, high := int64(1), int64(10)
	for _, kind := range []string{"int", "duration"} {
		for _, text := range []string{"0", "11", "1.5", "text"} {
			t.Run(kind+text, func(t *testing.T) {
				m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.count", Type: kind, Default: int64(3), Min: &low, Max: &high, Owner: "husk"})
				editGeneral(m, "husk.count", text)
				m.settings.focus = settingsContentFocus
				settingsPress(m, "ctrl+s")
				if len(fe.SettingWrites) != 0 || !m.settings.dirty() || !strings.Contains(ansi.Strip(m.View().Content), "invalid_value:") {
					t.Fatal("invalid integer not refused inline before write")
				}
			})
		}
	}
}

func TestSettingsGeneralSaveDirtyKeysSequentiallyAndIgnoreSecondSave(t *testing.T) {
	m, fe := generalFixture(t,
		contracts.SettingEntry{Key: "theme.z", Type: "string", Default: "z", Owner: "husk"},
		contracts.SettingEntry{Key: "husk.a", Type: "string", Default: "a", Owner: "husk"},
		contracts.SettingEntry{Key: "husk.clean", Type: "string", Default: "clean", Owner: "husk"})
	editGeneral(m, "theme.z", "new z")
	editGeneral(m, "husk.a", "new a")
	m.settings.focus = settingsContentFocus
	settingsPress(m, "ctrl+s")
	settingsPress(m, "ctrl+s")
	if len(fe.SettingWrites) != 1 || fe.SettingWrites[0].Key != "husk.a" || !m.settings.general.saving {
		t.Fatal("not one-at-a-time in key order")
	}
	fe.replySetting(0, contracts.SettingSetResult{Value: "new a"}, nil)
	if len(fe.SettingWrites) != 2 || fe.SettingWrites[1].Key != "theme.z" || m.settings.general.fields["husk.a"].dirty() {
		t.Fatal("reply did not advance one write")
	}
	fe.replySetting(1, contracts.SettingSetResult{Value: "new z"}, nil)
	if m.settings.general.saving || m.settings.dirty() {
		t.Fatal("save not finished/clean")
	}
	settingsPress(m, "ctrl+s")
	if len(fe.SettingWrites) != 2 {
		t.Fatal("clean keys were written")
	}
}

func TestSettingsGeneralRestartInvalidValueAndStoreErrorContinue(t *testing.T) {
	for _, failure := range []string{"store_error", "invalid_value", "report"} {
		t.Run(failure, func(t *testing.T) {
			m, fe := generalFixture(t,
				contracts.SettingEntry{Key: "husk.a", Type: "string", Default: "a", Owner: "husk"},
				contracts.SettingEntry{Key: "husk.b", Type: "string", Default: "b", Owner: "husk", Apply: "restart"})
			editGeneral(m, "husk.a", "new a")
			editGeneral(m, "husk.b", "new b")
			m.settings.focus = settingsContentFocus
			settingsPress(m, "ctrl+s")
			var err error
			result := contracts.SettingSetResult{}
			if failure == "report" {
				result.Reports = []contracts.ReloadReport{{Service: "settings", Problems: []protocol.Problem{{Key: "husk.a", Severity: "error", Code: "invalid_value", Message: "refused"}}}}
			} else {
				err = contracts.NewError(protocol.CodeUnavailable, "refused", map[string]any{"reason": failure})
			}
			fe.replySetting(0, result, err)
			if len(fe.SettingWrites) != 2 || !m.settings.general.fields["husk.a"].dirty() {
				t.Fatal("failed field clean or save stopped")
			}
			fe.replySetting(1, contracts.SettingSetResult{Value: "new b", Reports: []contracts.ReloadReport{{Service: "settings", Problems: []protocol.Problem{{Key: "husk.b", Severity: "warning", Code: "restart_required"}}}, {Service: "theme", Problems: []protocol.Problem{{Key: "husk.b", Severity: "error", Code: "low_contrast", Message: "not a General error"}}}}}, nil)
			text := ansi.Strip(m.View().Content)
			code := failure
			if code == "report" {
				code = "invalid_value"
			}
			if !strings.Contains(text, code+": refused") || !strings.Contains(text, "restart required") || strings.Contains(text, "low_contrast") || m.settings.general.fields["husk.b"].dirty() {
				t.Fatalf("bad per-field result: %s", text)
			}
			m.closeSettings()
			m.openSettings(map[string]any{"tab": "general"})
			if !strings.Contains(ansi.Strip(m.View().Content), "restart required") {
				t.Fatal("restart warning lost before process restart")
			}
		})
	}
}

func TestSettingsGeneralEscapeDiscardsAndDropsPendingReply(t *testing.T) {
	m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.a", Type: "string", Default: "a", Owner: "husk"})
	editGeneral(m, "husk.a", "unsaved")
	if !strings.Contains(ansi.Strip(m.View().Content), "discards edits") {
		t.Fatal("dirty footer missing")
	}
	settingsPress(m, "esc")
	if m.settings != nil || len(fe.SettingWrites) != 0 {
		t.Fatal("escape saved or did not close")
	}
	m.openSettings(map[string]any{"tab": "general"})
	editGeneral(m, "husk.a", "pending")
	m.saveSettingsGeneral()
	settingsPress(m, "esc")
	m.openSettings(map[string]any{"tab": "general"})
	fe.replySetting(0, contracts.SettingSetResult{Value: "pending"}, nil)
	if m.settings.general.fields["husk.a"].text != "a" || m.settings.dirty() {
		t.Fatal("late reply affected reopened form")
	}
}

func TestSettingsGeneralReloadRefreshesCleanAndKeepsDirtyAndFocus(t *testing.T) {
	m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.a", Type: "string", Default: "a", Owner: "husk"}, contracts.SettingEntry{Key: "husk.b", Type: "string", Default: "b", Owner: "husk"})
	g := m.settings.general
	original := g.fields["husk.a"].widget
	editGeneral(m, "husk.a", "dirty")
	g.active = "husk.b"
	fe.SettingValues = map[string]contracts.SettingValue{"husk.a": {Value: "reload a", Source: "profile"}, "husk.b": {Value: "reload b", Source: "distribution"}}
	m.settle()
	if g.fields["husk.a"].text != "dirty" || g.fields["husk.a"].widget != original || g.fields["husk.b"].text != "reload b" || g.fields["husk.b"].dirty() || g.active != "husk.b" {
		t.Fatal("reload lost edits/clean update/focus or unnecessarily rebuilt fields")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "from distribution") {
		t.Fatal("missing source annotation")
	}
	fe.SchemaRows = append(fe.SchemaRows, contracts.SettingEntry{Key: "plugins.new.a", Type: "bool", Default: false, Owner: "plugin:new"})
	m.settle()
	if g.active != "husk.b" || g.fields["husk.a"].text != "dirty" || len(g.groups) != 2 {
		t.Fatal("schema reload lost edits/focus")
	}
}

func TestSettingsGeneralFieldTraversalAndDigitInput(t *testing.T) {
	m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.a", Type: "string", Default: "", Owner: "husk"}, contracts.SettingEntry{Key: "husk.b", Type: "string", Default: "", Owner: "husk"})
	settingsPress(m, "tab")
	settingsPress(m, "3")
	g := m.settings.general
	if m.settingsTab() != "general" || g.fields["husk.a"].text != "3" {
		t.Fatal("digit switched tab instead of typing")
	}
	first := g.fields["husk.a"].widget
	settingsPress(m, "tab")
	if m.settings.focus != settingsContentFocus || g.active != "husk.b" {
		t.Fatal("tab did not advance inside form")
	}
	settingsPress(m, "tab")
	if m.settings.focus != settingsProfileFocus {
		t.Fatal("last field did not leave form")
	}
	settingsPress(m, "shift+tab")
	settingsPress(m, "shift+tab")
	settingsPress(m, "shift+tab")
	if m.settings.focus != settingsTabsFocus || g.fields["husk.a"].widget != first || len(fe.SettingWrites) != 0 {
		t.Fatal("first field did not leave form or traversal submitted/rebuilt")
	}
}

func TestSettingsGeneralPendingSaveKeepsNewerEdit(t *testing.T) {
	m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.a", Type: "string", Default: "a", Owner: "husk"})
	editGeneral(m, "husk.a", "sent")
	m.saveSettingsGeneral()
	editGeneral(m, "husk.a", "newer")
	fe.replySetting(0, contracts.SettingSetResult{Value: "sent"}, nil)
	f := m.settings.general.fields["husk.a"]
	if f.text != "newer" || !f.dirty() {
		t.Fatal("reply erased an edit made while saving")
	}
}

func TestSettingsGeneralMouseScrollPaletteAndViewPurity(t *testing.T) {
	m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.a", Type: "string", Default: "a", Owner: "husk"}, contracts.SettingEntry{Key: "husk.b", Type: "strings", Default: []string{"b"}, Owner: "husk"})
	r := m.settingsContentRect()
	cells := frameCells(m.View().Content)
	if !sameColor(cells[r.Min.Y+2][r.Min.X].Style.Fg, m.palette.Ink) {
		t.Fatal("blurred input leaked a widget default colour")
	}
	m.Update(tea.MouseClickMsg{X: r.Min.X + 1, Y: r.Min.Y + 1, Button: tea.MouseLeft})
	if m.settings.focus != settingsContentFocus || m.settings.general.active != "husk.a" {
		t.Fatal("field click did not focus")
	}
	m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y, Button: tea.MouseWheelDown})
	if m.settings.general.active != "husk.b" {
		t.Fatal("wheel did not scroll to next field")
	}
	before := strings.Join(m.settings.general.lines, "\n")
	m.served.Accent = "#123456"
	fe.PaletteFn = func() (protocol.Palette, map[string]int) {
		return protocol.Palette(m.served), map[string]int{"accent": 5}
	}
	m.settle()
	if strings.Join(m.settings.general.lines, "\n") == before {
		t.Fatal("General did not recolor")
	}
	cached := slices.Clone(m.settings.general.lines)
	first := m.View().Content
	if m.View().Content != first || !reflect.DeepEqual(cached, m.settings.general.lines) {
		t.Fatal("General View mutated cached form")
	}
}
