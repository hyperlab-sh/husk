package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
)

func (m *WorkbenchModel) settingsKeysKey(msg tea.KeyPressMsg) {
	s := m.settings
	key := legacyKeyString(msg)
	switch key {
	case "backspace":
		if len(s.keyQuery) > 0 {
			_, size := utf8.DecodeLastRuneInString(s.keyQuery)
			s.keyQuery = s.keyQuery[:len(s.keyQuery)-size]
		}
		s.keyTop = 0
	case "up", "ctrl+k":
		s.keyTop = max(0, s.keyTop-1)
	case "down", "ctrl+j":
		s.keyTop++
	case "pgup":
		s.keyTop = max(0, s.keyTop-m.settingsContentRect().Dy()+3)
	case "pgdown":
		s.keyTop += max(1, m.settingsContentRect().Dy()-3)
	case "home":
		s.keyTop = 0
	case "end":
		s.keyTop = len(m.settingsKeyRows())
	default:
		if paletteTextFor(msg, key) {
			s.keyQuery += key
			s.keyTop = 0
		}
	}
}
func (m *WorkbenchModel) settingsKeyRows() []contracts.KeyEntry {
	s := m.settings
	query := strings.ToLower(s.keyQuery)
	var rows []contracts.KeyEntry
	for _, entry := range s.keyListing.Entries {
		if strings.Contains(strings.ToLower(strings.Join(entry.Keys, " ")+" "+entry.Action+" "+entry.Plugin+" "+entry.Source.Plugin), query) {
			rows = append(rows, entry)
		}
	}
	return rows
}

// settingsKeysLines is pure: widths and clipping are derived locally, including
// the clamped scroll offset when a reload removes rows under the old viewport.
func (m *WorkbenchModel) settingsKeysLines() []string {
	s := m.settings
	width := m.settingsContentRect().Dx()
	base := lipgloss.NewStyle().Foreground(m.palette.Ink).Background(m.palette.SurfaceElevated)
	muted := base.Foreground(m.palette.InkMuted)
	query := base.Render("❯ " + plainText(s.keyQuery))
	if s.focus == settingsContentFocus {
		query += base.Render("█")
	}
	lines := []string{query, ""}
	rows := m.settingsKeyRows()
	cells := [][]string{{"KEY", "SCOPE", "ACTION", "LAYER", "STATE"}}
	for _, entry := range rows {
		scope := entry.Focus
		if scope == "" {
			scope = "global"
		}
		if entry.Plugin != "" {
			scope += " (" + entry.Plugin + ")"
		}
		action := entry.Action
		if entry.Disable {
			action = "(disabled)"
		}
		layer := entry.Source.Layer
		if entry.Source.Plugin != "" {
			layer += " (" + entry.Source.Plugin + ")"
		}
		state := entry.State
		if entry.ShadowedBy != nil {
			state = "shadowed by " + entry.ShadowedBy.Layer
			if entry.ShadowedBy.File != "" {
				state += " " + entry.ShadowedBy.File
			}
		}
		cells = append(cells, []string{strings.Join(entry.Keys, " "), scope, action, layer, state})
	}
	// Reserve space for every column even at the minimum terminal size.
	widths := []int{max(8, width/6), max(8, width/6), max(12, width/4), max(7, width/8), 0}
	widths[4] = max(5, width-8-widths[0]-widths[1]-widths[2]-widths[3])
	render := func(values []string, style lipgloss.Style) string {
		parts := make([]string, len(values))
		for i, value := range values {
			text := ansi.Truncate(plainText(value), widths[i], "…")
			parts[i] = text + strings.Repeat(" ", max(0, widths[i]-ansi.StringWidth(text)))
		}
		return style.Render(strings.Join(parts, "  "))
	}
	lines = append(lines, render(cells[0], base.Bold(true)))
	var body []string
	for i, entry := range rows {
		style := base
		if entry.State == "shadowed" {
			style = muted
		}
		body = append(body, render(cells[i+1], style))
	}
	if len(rows) == 0 {
		body = append(body, muted.Render("no key matches"))
	}
	for _, problem := range s.keyListing.Problems[:min(20, len(s.keyListing.Problems))] {
		body = append(body, base.Render(plainText(problem.Severity+": "+problem.Code+": "+problem.Message)))
	}
	if n := len(s.keyListing.Problems) - 20; n > 0 {
		body = append(body, muted.Render(fmt.Sprintf("and %d more", n)))
	}
	height := max(1, m.settingsContentRect().Dy()-len(lines))
	top := max(0, min(s.keyTop, len(body)-height))
	return append(lines, body[top:min(len(body), top+height)]...)
}
