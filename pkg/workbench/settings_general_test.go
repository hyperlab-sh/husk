package workbench

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
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
	m, fe := generalFixture(t,
		contracts.SettingEntry{Key: "plugins.Z.nested.z", Type: "string", Default: "z", Owner: "plugin:Z"},
		contracts.SettingEntry{Key: "dist.tools.z", Type: "string", Default: "z", Owner: "dist"},
		contracts.SettingEntry{Key: "theme.name", Type: "string", Default: theme.Default().Name, Owner: "husk"},
		contracts.SettingEntry{Key: "husk.plugins.paths", Type: "strings", Default: []string{}, Owner: "husk"},
		contracts.SettingEntry{Key: "husk.bundle", Type: "string", Default: "", Owner: "husk"},
		contracts.SettingEntry{Key: "plugins.A.x", Type: "string", Default: "x", Owner: "plugin:A"},
		contracts.SettingEntry{Key: "plugins.Z.a", Type: "string", Default: "a", Owner: "plugin:Z"})
	// A live Pod with retained raw values is not a settings schema.
	fe.pods = map[string]string{"ghost": "live"}
	fe.publish()
	fe.SettingValues = map[string]contracts.SettingValue{"ghost.x": {Value: "raw", Source: "user"}}
	fe.ServiceRev++
	m.settle()
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
	if slices.Contains(names, "plugins.ghost") || strings.Contains(strings.Join(g.targets(), " "), "ghost") {
		t.Fatal("loaded plugin or raw values manufactured an unregistered group/target")
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
		shown   string
	}{
		{"string", "before", "after", "after", "before"}, {"bool", false, "", true, ""}, {"enum", "one", "two", "two", "one"},
		{"int", int64(2), "42", int64(42), "2"}, {"duration", 1500 * time.Microsecond, "20", int64(20), "1"},
		{"int", float64(1_000_000), "1000001", int64(1_000_001), "1000000"},
		{"int", json.Number("1.5e3"), "1501", int64(1501), "1500"},
		{"int", int(1000000), "1000001", int64(1000001), "1000000"},
		{"int", json.Number("1000000"), "1000001", int64(1000001), "1000000"},
		{"duration", float64(1_000_000), "1000001", int64(1_000_001), "1000000"},
		{"duration", json.Number("1.5e3"), "1501", int64(1501), "1500"},
		{"strings", []string{"one", "two"}, "  alpha \n\n beta \n", []string{"alpha", "beta"}, "one\ntwo"},
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
			if f.text != tc.shown || f.dirty() {
				t.Fatalf("initial value %#v: text %q want %q, dirty %v", tc.initial, f.text, tc.shown, f.dirty())
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
				code := protocol.CodeUnavailable
				if failure == "invalid_value" {
					code = protocol.CodeInvalidParams
				}
				err = contracts.NewError(code, "refused", map[string]any{"reason": failure})
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
	fe.ServiceRev++
	m.settle()
	if g.fields["husk.a"].text != "dirty" || g.fields["husk.a"].widget != original || g.fields["husk.b"].text != "reload b" || g.fields["husk.b"].dirty() || g.active != "husk.b" {
		t.Fatal("reload lost edits/clean update/focus or unnecessarily rebuilt fields")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "from distribution") {
		t.Fatal("missing source annotation")
	}
	fe.SchemaRows = append(fe.SchemaRows, contracts.SettingEntry{Key: "plugins.new.a", Type: "bool", Default: false, Owner: "plugin:new"})
	fe.ServiceRev++
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
	settingsPress(m, "tab") // profile -> tab list
	settingsPress(m, "tab") // tab list -> first field, not the previous last field
	if g.active != "husk.a" || m.settings.focus != settingsContentFocus {
		t.Fatal("forward entry skipped the first field")
	}
	corner := m.settingsCornerRect()
	m.Update(tea.MouseClickMsg{X: corner.Min.X, Y: corner.Min.Y, Button: tea.MouseLeft})
	settingsPress(m, "shift+tab") // dismiss the Select and enter at the last field
	if g.active != "husk.b" || m.settings.focus != settingsContentFocus {
		t.Fatal("reverse entry skipped the last field")
	}
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
