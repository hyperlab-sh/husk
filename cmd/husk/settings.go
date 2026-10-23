package main

import (
	"errors"
	"fmt"
	"image"
	"maps"
	"reflect"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

var settingsTabs = []string{"general", "keys", "theme", "store", "installed"}

const (
	settingsTabsFocus = iota
	settingsContentFocus
	settingsProfileFocus
)

// settingsCenter lives only on the loop. Captured pointer identity fences
// callbacks from a closed center, including when another center has opened.
type settingsCenter struct {
	tab              string
	focus            int
	themes           []contracts.ThemeEntry
	cursor, top      int
	pending          bool
	problem, written string
	profiles         []contracts.ProfileInfo
	active           string
	switching        bool
	profileError     string
	profileForm      *huh.Form
	profileChoice    string
	profileNames     []string
	profileTop       int
	profileView      string // huh rendering is cached on Update; View remains pure
	styled           protocol.Palette
	indices          map[string]int
}

func (m *WorkbenchModel) settingsTab() string {
	if m.settings == nil {
		return ""
	}
	return m.settings.tab
}

func (m *WorkbenchModel) openSettings(args map[string]any) (map[string]any, error) {
	tab := ""
	if value, ok := args["tab"]; ok {
		var valid bool
		tab, valid = value.(string)
		if !valid || !slices.Contains(settingsTabs, tab) {
			return nil, contracts.NewError(protocol.CodeInvalidParams, fmt.Sprintf("unknown settings tab %v", value), nil)
		}
	}
	if m.layoutFrozen() || m.modalOwner().Open() || m.secretPrompt != nil || m.showPalette {
		return nil, contracts.ErrDeclined
	}
	if m.settings != nil {
		if tab != "" {
			m.settings.tab = tab
			m.lastSettingsTab = tab
		}
		return nil, nil
	}
	if tab == "" {
		tab = m.lastSettingsTab
	}
	if tab == "" {
		tab = "general"
	}
	m.clearPending()
	m.activeDrag, m.hoverDivider = "", ""
	m.settings = &settingsCenter{tab: tab}
	if tab == "theme" {
		m.settings.focus = settingsContentFocus
	}
	m.lastSettingsTab = tab
	m.refreshSettings()
	return nil, nil
}

func (m *WorkbenchModel) closeSettings() {
	if m.settings != nil {
		m.lastSettingsTab = m.settings.tab
	}
	m.settings = nil
	m.clearPending()
}

// refreshSettings reads immutable serving snapshots, never Store. It also
// catches reloads that changed no palette or occupancy and keeps the cursor by name.
func (m *WorkbenchModel) refreshSettings() {
	s := m.settings
	if s == nil {
		return
	}
	if m.layoutFrozen() {
		m.closeSettings()
		return
	}
	if m.fe != nil {
		themes := m.fe.Themes()
		if !reflect.DeepEqual(themes, s.themes) {
			name := ""
			if s.cursor < len(s.themes) {
				name = s.themes[s.cursor].Name
			}
			s.themes = slices.Clone(themes)
			s.cursor = 0
			for i, entry := range themes {
				if entry.Current {
					s.cursor = i
				}
			}
			for i, entry := range themes {
				if entry.Name == name {
					s.cursor = i
					break
				}
			}
		}
		profiles := slices.Clone(m.fe.Profiles())
		slices.SortFunc(profiles, func(a, b contracts.ProfileInfo) int { return strings.Compare(a.Name, b.Name) })
		if !reflect.DeepEqual(profiles, s.profiles) {
			s.profiles = profiles
			s.active = ""
			for _, p := range profiles {
				if p.Active {
					s.active = p.Name
					break
				}
			}
			if s.profileForm != nil {
				choice := s.profileChoice
				if choice != "" && !slices.ContainsFunc(profiles, func(p contracts.ProfileInfo) bool { return p.Name == choice }) {
					choice = s.active
				}
				m.buildSettingsProfiles(choice)
			}
		}
	}
	if s.profileForm != nil && (s.styled != protocol.Palette(m.served) || !maps.Equal(s.indices, m.ansi)) {
		s.profileForm.WithTheme(settingsHuhTheme(m.palette))
		s.styled, s.indices = protocol.Palette(m.served), maps.Clone(m.ansi)
	}
	if s.profileForm != nil {
		r := m.settingsProfileRect()
		s.profileForm.WithWidth(r.Dx()).WithHeight(r.Dy())
		s.profileView = s.profileForm.View()
		s.profileTop = settingsListTop(s.profileTop, max(0, slices.Index(s.profileNames, s.profileChoice)), len(s.profileNames), r.Dy()-1)
	}
	s.top = settingsListTop(s.top, s.cursor, len(s.themes), m.settingsContentRect().Dy()-4)
}

func settingsListTop(top, cursor, count, height int) int {
	height = max(1, height)
	if cursor < top {
		top = cursor
	}
	if cursor >= top+height {
		top = cursor - height + 1
	}
	return max(0, min(top, count-height))
}

func (m *WorkbenchModel) settingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := m.settings
	key := legacyKeyString(msg)
	s.profileError = ""
	if s.profileForm != nil && (key == "tab" || key == "shift+tab") {
		s.profileForm = nil
	}
	if key == "ctrl+c" {
		m.closeSettings()
		return m, nil
	}
	if s.profileForm != nil {
		if key == "esc" {
			s.profileForm = nil
			return m, nil
		}
		if key == "enter" {
			// Commit the highlighted option to its accessor, but discard huh's
			// next-field command: the host owns submission and the async write.
			s.profileForm.Update(msg)
			m.useSettingsProfile()
			return m, nil
		}
		// Closed key set: do not expose huh's filter/quit or submission keys.
		switch key {
		case "up", "down", "ctrl+k", "ctrl+j", "home", "end", "pgup", "pgdown":
			if key == "pgup" {
				msg = tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
			}
			if key == "pgdown" {
				msg = tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
			}
			_, cmd := s.profileForm.Update(msg)
			// Synchronize the accessor after Home/End too; huh's End path
			// updates its cursor before it updates the bound value.
			s.profileForm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			s.profileView = s.profileForm.View()
			return m, cmd
		}
		return m, nil
	}
	switch key {
	case "esc", "ctrl+c":
		m.closeSettings()
		return m, nil
	case "tab":
		s.focus = (s.focus + 1) % 3
		return m, nil
	case "shift+tab":
		s.focus = (s.focus + 2) % 3
		return m, nil
	}
	switch s.focus {
	case settingsTabsFocus:
		at := slices.Index(settingsTabs, s.tab)
		switch key {
		case "up", "ctrl+k":
			at = (at + 4) % 5
		case "down", "ctrl+j":
			at = (at + 1) % 5
		case "enter", "right":
			s.focus = settingsContentFocus
		case "1", "2", "3", "4", "5":
			at = int(key[0] - '1')
		}
		s.tab, m.lastSettingsTab = settingsTabs[at], settingsTabs[at]
	case settingsProfileFocus:
		if key == "enter" || key == " " {
			m.openSettingsProfiles()
		}
	case settingsContentFocus:
		if s.tab == "theme" && !s.pending {
			switch key {
			case "enter":
				m.selectSettingsTheme()
			case "up", "ctrl+k":
				s.cursor = max(0, s.cursor-1)
			case "down", "ctrl+j":
				s.cursor = min(len(s.themes)-1, s.cursor+1)
			case "home":
				s.cursor = 0
			case "end":
				s.cursor = max(0, len(s.themes)-1)
			case "pgup":
				s.cursor = max(0, s.cursor-max(1, m.settingsContentRect().Dy()-4))
			case "pgdown":
				s.cursor = min(len(s.themes)-1, s.cursor+max(1, m.settingsContentRect().Dy()-4))
			}
			s.cursor = max(0, s.cursor)
		}
	}
	return m, nil
}

func (m *WorkbenchModel) selectSettingsTheme() {
	s := m.settings
	if s.pending || m.fe == nil || s.cursor >= len(s.themes) {
		return
	}
	name := s.themes[s.cursor].Name
	s.pending, s.problem, s.written = true, "", ""
	m.fe.SetSetting("theme.name", name, func(result contracts.SettingSetResult, err error) {
		if m.settings != s {
			return
		}
		s.pending = false
		m.refreshSettings()
		if err != nil {
			s.problem = settingsError(err)
			return
		}
		var themeReport, settingsReport contracts.ReloadReport
		for _, r := range result.Reports {
			if r.Service == "theme" {
				themeReport = r
			}
			if r.Service == "settings" {
				settingsReport = r
			}
		}
		unknown := slices.ContainsFunc(themeReport.Problems, func(p protocol.Problem) bool { return p.Code == "unknown_theme" })
		if themeReport.Applied && !unknown {
			for _, entry := range s.themes {
				if entry.Current && entry.Name == name {
					m.closeSettings()
					return
				}
			}
			s.problem = "set in " + result.Source + " layer, which wins"
			return
		}
		problems := themeReport.Problems
		if len(problems) == 1 && problems[0].Code == "settings_unchanged" {
			problems = settingsReport.Problems
		}
		if problem := settingsFirstProblem(problems); problem != nil {
			s.problem = problem.Code + ": " + problem.Message
		}
		s.written = "written to the user layer; run husk theme check"
	})
}

func settingsError(err error) string {
	code := contracts.CodeOf(err)
	var data contracts.DataError
	if errors.As(err, &data) {
		if reason, ok := data.Data()["reason"].(string); ok && reason != "" {
			code = reason
		}
	}
	return code + ": " + err.Error()
}

func settingsFirstProblem(problems []protocol.Problem) *protocol.Problem {
	for _, severity := range []string{"error", "warning"} {
		for i := range problems {
			if problems[i].Severity == severity {
				return &problems[i]
			}
		}
	}
	return nil
}

func (m *WorkbenchModel) openSettingsProfiles() {
	s := m.settings
	if s.switching {
		return
	}
	m.buildSettingsProfiles(s.active)
	m.refreshSettings()
}

func (m *WorkbenchModel) buildSettingsProfiles(choice string) {
	s := m.settings
	options := []huh.Option[string]{huh.NewOption("(none)", "")}
	s.profileNames, s.profileTop = []string{""}, 0
	for _, p := range s.profiles {
		if p.Name != "" {
			label := ansi.Truncate(plainText(p.Name+"  "+p.Source), m.settingsProfileRect().Dx()-2, "…")
			options = append(options, huh.NewOption(label, p.Name))
			s.profileNames = append(s.profileNames, p.Name)
		}
	}
	s.profileChoice = choice
	field := huh.NewSelect[string]().Title("Profile").Options(options...).Value(&s.profileChoice)
	s.profileForm = huh.NewForm(huh.NewGroup(field)).WithShowHelp(false).WithShowErrors(false).WithTheme(settingsHuhTheme(m.palette)).WithWidth(m.settingsProfileRect().Dx())
	s.styled, s.indices = protocol.Palette(m.served), maps.Clone(m.ansi)
	m.cmds = append(m.cmds, s.profileForm.Init())
}

func (m *WorkbenchModel) useSettingsProfile() {
	s := m.settings
	name := s.profileChoice
	s.profileForm = nil
	if s.switching || m.fe == nil {
		return
	}
	s.switching = true
	m.fe.UseProfile(name, func(result contracts.ProfileUseResult, err error) {
		if m.settings != s {
			return
		}
		s.switching = false
		if err != nil {
			s.profileError = settingsError(err)
			return
		}
		m.refreshSettings()
		s.active = result.Active
	})
}

// settingsMouse consumes all events while the center is open. The pill is
// routed before this function, so it switches to Theme rather than closing.
func (m *WorkbenchModel) settingsMouse(event tea.MouseMsg) (tea.Model, tea.Cmd) {
	s := m.settings
	mouse := event.Mouse()
	point := image.Pt(mouse.X, mouse.Y)
	_, click := event.(tea.MouseClickMsg)
	left := click && mouse.Button == tea.MouseLeft
	if !point.In(m.settingsRect()) {
		if left {
			m.closeSettings()
		}
		return m, nil
	}
	if s.profileForm != nil {
		if _, wheel := event.(tea.MouseWheelMsg); wheel {
			if mouse.Button == tea.MouseWheelUp {
				return m.settingsKey(tea.KeyPressMsg{Code: tea.KeyUp})
			}
			if mouse.Button == tea.MouseWheelDown {
				return m.settingsKey(tea.KeyPressMsg{Code: tea.KeyDown})
			}
		}
		if left && point.In(m.settingsProfileRect()) {
			// The form's header occupies the first row; list rows follow it.
			at := s.profileTop + mouse.Y - m.settingsProfileRect().Min.Y - 1
			if mouse.Y > m.settingsProfileRect().Min.Y && at >= 0 && at < len(s.profileNames) {
				s.profileChoice = s.profileNames[at]
				m.useSettingsProfile()
			}
		} else if left {
			s.profileForm = nil
		}
		return m, nil
	}
	if left && point.In(m.settingsCornerRect()) {
		s.focus = settingsProfileFocus
		m.openSettingsProfiles()
		return m, nil
	}
	r := m.settingsRect()
	if left && mouse.X < r.Min.X+14 && mouse.Y > r.Min.Y && mouse.Y < r.Max.Y-2 {
		row := mouse.Y - r.Min.Y - 1
		tabs := []string{"general", "keys", "theme", "store", "store", "installed"}
		if row < len(tabs) {
			s.tab = tabs[row]
			m.lastSettingsTab = s.tab
			s.focus = settingsTabsFocus
		}
		return m, nil
	}
	content := m.settingsContentRect()
	if left && point.In(content) {
		s.focus = settingsContentFocus
		if s.tab == "theme" && !s.pending {
			at := s.top + mouse.Y - content.Min.Y
			if at < len(s.themes) && mouse.Y < content.Min.Y+max(1, content.Dy()-4) {
				if at == s.cursor {
					m.selectSettingsTheme()
				} else {
					s.cursor = at
				}
			}
		}
	}
	if _, wheel := event.(tea.MouseWheelMsg); wheel {
		if mouse.Button == tea.MouseWheelUp {
			return m.settingsKey(tea.KeyPressMsg{Code: tea.KeyUp})
		}
		if mouse.Button == tea.MouseWheelDown {
			return m.settingsKey(tea.KeyPressMsg{Code: tea.KeyDown})
		}
	}
	return m, nil
}
