package main

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

func TestSettingsCleanFooter(t *testing.T) {
	for _, tab := range settingsTabs {
		t.Run(tab, func(t *testing.T) {
			m, _ := settingsFixture(t, tab)
			if m.settings.dirty() {
				t.Fatal("U4 has no editable fields")
			}
			for _, focus := range []int{settingsTabsFocus, settingsContentFocus, settingsProfileFocus} {
				m.settings.focus = focus
				view := ansi.Strip(m.View().Content)
				if !strings.Contains(view, "esc close") || strings.Contains(view, "discards edits") {
					t.Fatalf("clean footer at focus %d: %s", focus, view)
				}
			}
		})
	}
}

func TestSettingsPaletteActionDeclines(t *testing.T) {
	m, _ := settingsFixture(t, "theme")
	center := m.settings
	_, err := m.actions["husk.palette.open"](nil)
	if !errors.Is(err, contracts.ErrDeclined) || m.showPalette || m.settings != center {
		t.Fatalf("palette action with center open: %v", err)
	}
	m.closeSettings()
	if _, err := m.actions["husk.palette.open"](nil); err != nil || !m.showPalette {
		t.Fatalf("palette action with center closed: %v", err)
	}
}

func TestSettingsProfileMouseOptions(t *testing.T) {
	for row, name := range []string{"", "home", "work"} {
		t.Run(fmt.Sprintf("row%d", row), func(t *testing.T) {
			m, fe := settingsFixture(t, "theme")
			m.openSettingsProfiles()
			m.settle()
			r := m.settingsProfileRect()
			lines := strings.Split(ansi.Strip(m.settings.profileView), "\n")
			label := name
			if label == "" {
				label = "(none)"
			}
			if !strings.Contains(lines[row+1], label) {
				t.Fatalf("option not rendered at click row: %q", lines[row+1])
			}
			m.Update(tea.MouseClickMsg{X: r.Min.X + 2, Y: r.Min.Y + row + 1, Button: tea.MouseLeft})
			if len(fe.ProfileUses) != 1 || fe.ProfileUses[0].Name != name {
				t.Fatalf("clicked %q: %+v", label, fe.ProfileUses)
			}
		})
	}
	t.Run("title", func(t *testing.T) {
		m, fe := settingsFixture(t, "theme")
		m.openSettingsProfiles()
		m.settle()
		r := m.settingsProfileRect()
		m.Update(tea.MouseClickMsg{X: r.Min.X + 2, Y: r.Min.Y, Button: tea.MouseLeft})
		if len(fe.ProfileUses) != 0 || m.settings.profileForm == nil {
			t.Fatal("title click submitted or dismissed the Select")
		}
	})
	t.Run("wheel", func(t *testing.T) {
		m, fe := settingsFixture(t, "theme")
		m.openSettingsProfiles()
		m.settle()
		r := m.settingsProfileRect()
		for _, step := range []struct {
			button tea.MouseButton
			want   string
		}{{tea.MouseWheelUp, "home"}, {tea.MouseWheelDown, "work"}} {
			m.Update(tea.MouseWheelMsg{X: r.Min.X, Y: r.Min.Y + 2, Button: step.button})
			if m.settings.profileChoice != step.want || len(fe.ProfileUses) != 0 {
				t.Fatalf("wheel %v: choice %q, writes %+v", step.button, m.settings.profileChoice, fe.ProfileUses)
			}
		}
	})
}

func TestSettingsProfileMouseScrolledRows(t *testing.T) {
	for _, position := range []string{"first", "middle", "last", "resized last"} {
		t.Run(position, func(t *testing.T) {
			m, fe := settingsFixture(t, "theme")
			fe.ProfileRows = nil
			for i := range 40 {
				fe.ProfileRows = append(fe.ProfileRows, contracts.ProfileInfo{Name: fmt.Sprintf("profile%02d", i), Source: "user", Active: i == 0})
			}
			m.settle()
			m.openSettingsProfiles()
			m.settle()
			settingsPress(m, "end")
			if position == "resized last" {
				m.Update(tea.WindowSizeMsg{Width: 84, Height: 24})
				m.Update(tea.WindowSizeMsg{Width: 144, Height: 40})
			}
			r := m.settingsProfileRect()
			m.Update(tea.MouseClickMsg{X: r.Min.X + 2, Y: r.Min.Y, Button: tea.MouseLeft})
			if len(fe.ProfileUses) != 0 || m.settings.profileForm == nil {
				t.Fatal("scrolled title click submitted or dismissed the Select")
			}
			if len(m.settings.profileNames) <= r.Dy() || m.settings.profileTop == 0 {
				t.Fatal("Select did not scroll")
			}
			row := 1
			if position == "middle" {
				row = r.Dy() / 2
			} else if strings.HasSuffix(position, "last") {
				row = r.Dy() - 1
			}
			lines := strings.Split(ansi.Strip(m.settings.profileView), "\n")
			line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[row]), "▸"))
			fields := strings.Fields(line)
			if len(fields) == 0 || !slices.Contains(m.settings.profileNames, fields[0]) {
				t.Fatalf("no rendered option at row %d: %q", row, lines[row])
			}
			want := fields[0] // the rendered row, not the host's predicted viewport offset
			m.Update(tea.MouseClickMsg{X: r.Min.X + 2, Y: r.Min.Y + row, Button: tea.MouseLeft})
			if len(fe.ProfileUses) != 1 || fe.ProfileUses[0].Name != want {
				t.Fatalf("row %d rendered %q, selected %+v; view %q", row, want, fe.ProfileUses, lines)
			}
		})
	}
}

func TestSettingsProfileSelectRethemes(t *testing.T) {
	for _, change := range []string{"palette", "ansi"} {
		t.Run(change, func(t *testing.T) {
			m, fe := settingsFixture(t, "theme")
			m.settle()
			m.openSettingsProfiles()
			m.settle()
			settingsPress(m, "up") // keep a choice different from the active profile
			before, choice := m.settings.profileView, m.settings.profileChoice
			pal := protocol.Palette(m.served)
			var indices map[string]int
			if change == "palette" {
				pal.Accent = "#123456"
			} else {
				indices = map[string]int{"accent": 5}
			}
			fe.PaletteFn = func() (protocol.Palette, map[string]int) { return pal, indices }
			m.settle()
			if m.settings.styled != pal || !maps.Equal(m.settings.indices, indices) || m.settings.profileView == before || m.settings.profileChoice != choice {
				t.Fatalf("open Select did not adopt the serving palette pair: styled=%v indices=%v unchanged=%v", m.settings.styled == pal, maps.Equal(m.settings.indices, indices), m.settings.profileView == before)
			}
		})
	}
}

func TestSettingsProfileSelectRebuildsOnReload(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(fmt.Sprintf("drop=%v", drop), func(t *testing.T) {
			m, fe := settingsFixture(t, "theme")
			m.openSettingsProfiles()
			settingsPress(m, "up") // home, while work remains active
			oldForm := m.settings.profileForm
			fe.ProfileRows = []contracts.ProfileInfo{{Name: "work", Source: "user", Active: true}, {Name: "new", Source: "user"}}
			want := "work"
			wantNames := []string{"", "new", "work"}
			if !drop {
				fe.ProfileRows = append(fe.ProfileRows, contracts.ProfileInfo{Name: "home", Source: "user"})
				want, wantNames = "home", []string{"", "home", "new", "work"}
			}
			m.settle()
			if m.settings.profileForm == nil || m.settings.profileForm == oldForm || m.settings.profileChoice != want || !reflect.DeepEqual(m.settings.profileNames, wantNames) || !strings.Contains(ansi.Strip(m.settings.profileView), "new") {
				t.Fatalf("reload did not rebuild and retain/fallback choice: %+v", m.settings)
			}
			if drop && strings.Contains(ansi.Strip(m.settings.profileView), "home") {
				t.Fatal("removed profile still drawn")
			}
		})
	}
}

type settingsReadCounter struct {
	contracts.Frontend
	themes, profiles int
}

func (c *settingsReadCounter) Themes() []contracts.ThemeEntry {
	c.themes++
	return c.Frontend.Themes()
}
func (c *settingsReadCounter) Profiles() []contracts.ProfileInfo {
	c.profiles++
	return c.Frontend.Profiles()
}

func TestSettingsRefreshOncePerSettle(t *testing.T) {
	m, fe := settingsFixture(t, "theme")
	m.openSettingsProfiles()
	counter := &settingsReadCounter{Frontend: fe}
	m.fe = counter
	m.attachSlices()
	m.served = theme.Palette{} // force more than one stabilization pass
	m.settle()
	if counter.themes != 1 || counter.profiles != 1 {
		t.Fatalf("one settle read themes %d and profiles %d times", counter.themes, counter.profiles)
	}
	counter.themes, counter.profiles = 0, 0
	m.Update(tea.MouseMotionMsg{X: 20, Y: 20})
	if counter.themes != 1 || counter.profiles != 1 {
		t.Fatalf("mouse motion read themes %d and profiles %d times", counter.themes, counter.profiles)
	}
}
