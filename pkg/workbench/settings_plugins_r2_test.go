package workbench

import (
	"image"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

func settingsClick(m *WorkbenchModel, x, y int) {
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
}

func TestSettingsInstalledRemoveCellHitGeometry(t *testing.T) {
	for _, where := range []string{"text", "right margin", "last error"} {
		t.Run(where, func(t *testing.T) {
			m, fe := pluginsFixture(t, "installed")
			r := m.settingsContentRect()
			row := ansi.Strip(m.settings.plugins.lines[1])
			x, y := r.Min.X+strings.Index(row, "Remove"), r.Min.Y+1
			if where == "right margin" {
				x = r.Max.X - 1
			}
			if where == "last error" {
				y++
				x = r.Max.X - 1
			}
			settingsClick(m, x, y)
			if (m.settings.plugins.confirm != nil) != (where == "text") {
				t.Fatalf("%s incorrectly hit removal", where)
			}
			if len(fe.PluginRemovals) != 0 {
				t.Fatal("mouse skipped confirmation")
			}
		})
	}
}

func TestSettingsPluginDialogsCannotOutliveTheirTab(t *testing.T) {
	for _, tab := range []string{"installed", "store"} {
		for _, path := range []string{"action", "key defense", "refresh defense"} {
			t.Run(tab+"/"+path, func(t *testing.T) {
				m, fe := pluginsFixture(t, tab)
				settingsPress(m, "enter")
				switch path {
				case "action":
					if _, err := m.openSettings(map[string]any{"tab": "theme"}); err != nil {
						t.Fatal(err)
					}
				case "key defense":
					m.settings.tab = "theme"
					m.settings.focus = settingsContentFocus
				case "refresh defense":
					m.settings.tab = "theme"
					m.settings.focus = settingsContentFocus
					m.refreshSettingsPlugins()
				}
				// No settle before the first key: the key handler must defend itself too.
				m.settingsKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
				m.settingsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
				if len(fe.PluginRemovals) != 0 {
					t.Fatal("hidden dialog removed plugin")
				}
				if len(fe.SettingWrites) != 1 || fe.SettingWrites[0].Key != "theme.name" {
					t.Fatal("Theme did not handle enter")
				}
				if p := m.settings.plugins; p.confirm != nil || p.filter != nil {
					t.Fatal("hidden dialog survived tab switch")
				}
			})
		}
	}
}

func TestSettingsStorePinsInitialAndTypedHighlightOnPublication(t *testing.T) {
	for _, typed := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "typed"}[typed], func(t *testing.T) {
			m, fe := pluginsFixture(t, "store")
			if typed {
				settingsPress(m, "a")
			}
			p := m.settings.plugins
			want := p.filtered[p.cursor].ID
			fe.CatalogRows.Entries = append(fe.CatalogRows.Entries, contracts.CatalogEntry{ID: "aaa", Name: "Aaa", Source: "~/plugins/aaa"})
			fe.CatalogRev++
			m.settle()
			if p.filtered[p.cursor].ID != want {
				t.Fatal("publication moved the highlighted id")
			}
		})
	}
}

func TestSettingsStoreSurfaceEscapeCancelsAndEnterCommits(t *testing.T) {
	m, _ := pluginsFixture(t, "store")
	settingsPress(m, "enter")
	settingsPress(m, "down")
	settingsPress(m, "esc")
	if m.settings.plugins.surface != "" {
		t.Fatal("escape committed surface filter")
	}
	settingsPress(m, "enter")
	settingsPress(m, "down")
	settingsPress(m, "enter")
	if m.settings.plugins.surface != "activity-rail" {
		t.Fatal("enter did not commit surface")
	}
}

func TestSettingsInstalledRestoresClampedCursor(t *testing.T) {
	m, fe := pluginsFixture(t, "installed")
	setSettingsSlice(fe, "husk.pods", `{"plugins":{"alpha":{"status":"live"},"beta":{"status":"live"},"gamma":{"status":"live"}}}`)
	m.settle()
	settingsPress(m, "end")
	m.openSettings(map[string]any{"tab": "store"})
	m.settle()
	setSettingsSlice(fe, "husk.pods", `{"plugins":{"alpha":{"status":"live"}}}`)
	m.settle()
	m.openSettings(map[string]any{"tab": "installed"})
	m.settle()
	if m.settings.plugins.cursor != 0 {
		t.Fatal("restored cursor outside remaining rows")
	}
}

func TestSettingsInstalledCommandsUseBox(t *testing.T) {
	m, _ := pluginsFixture(t, "installed")
	text := pluginText(m)
	for _, want := range []string{"╭", "╰", "│ husk plugin add <path>", "│ husk plugin add --dir <dir>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("command box missing %q", want)
		}
	}
}

func TestSettingsInstalledConfirmMouseButtons(t *testing.T) {
	for _, button := range []string{"yes", "no", "description", "outside"} {
		t.Run(button, func(t *testing.T) {
			m, fe := pluginsFixture(t, "installed")
			settingsPress(m, "enter")
			lines, r := m.settingsPluginConfirmLayout()
			point := image.Pt(r.Min.X, r.Min.Y+1)
			switch button {
			case "yes", "no":
				plain := ansi.Strip(lines[len(lines)-1])
				at := strings.Index(plain, button)
				if at < 0 {
					t.Fatal("button not drawn")
				}
				point = image.Pt(r.Min.X+ansi.StringWidth(plain[:at]), r.Min.Y+len(lines)-1)
			case "description":
				plain := ansi.Strip(lines[1])
				at := strings.Index(plain, "not")
				if at < 0 {
					t.Fatal("description not drawn")
				}
				point = image.Pt(r.Min.X+ansi.StringWidth(plain[:at]), r.Min.Y+1)
			case "outside":
				point = image.Pt(r.Min.X, r.Min.Y-1)
			}
			settingsClick(m, point.X, point.Y)
			if (len(fe.PluginRemovals) == 1) != (button == "yes") {
				t.Fatalf("%s removal count: %d", button, len(fe.PluginRemovals))
			}
			if (m.settings.plugins.confirm != nil) != (button == "description") {
				t.Fatal("dialog click dismissal mismatch")
			}
		})
	}
}
