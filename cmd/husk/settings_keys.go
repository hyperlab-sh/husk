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
		s.keyTop--
	case "down", "ctrl+j":
		s.keyTop++
	case "pgup":
		s.keyTop -= max(1, m.settingsContentRect().Dy()-3)
	case "pgdown":
		s.keyTop += max(1, m.settingsContentRect().Dy()-3)
	case "home":
		s.keyTop = 0
	case "end":
		s.keyTop = m.settingsKeysMaxTop()
	default:
		if paletteTextFor(msg, key) {
			s.keyQuery += key
			s.keyTop = 0
		}
	}
	m.clampSettingsKeysTop()
}
func (m *WorkbenchModel) settingsKeyRows() []contracts.KeyEntry {
	s := m.settings
	query := strings.ToLower(s.keyQuery)
	var rows []contracts.KeyEntry
	for _, entry := range s.keyListing.Entries {
		for _, field := range []string{strings.Join(entry.Keys, " "), entry.Action, entry.Plugin, entry.Source.Plugin} {
			if strings.Contains(strings.ToLower(field), query) {
				rows = append(rows, entry)
				break
			}
		}
	}
	return rows
}

// The filter, blank row and table header are fixed. Both Update and View use
// these exact body/viewport counts, including bounded diagnostics and no-match.
func (m *WorkbenchModel) settingsKeysExtent(rowCount int) (body, height int) {
	problems := len(m.settings.keyListing.Problems)
	body = max(1, rowCount) + min(20, problems)
	if problems > 20 {
		body++
	}
	return body, max(1, m.settingsContentRect().Dy()-3)
}
func (m *WorkbenchModel) settingsKeysMaxTop() int {
	body, height := m.settingsKeysExtent(len(m.settingsKeyRows()))
	return max(0, body-height)
}
func (m *WorkbenchModel) clampSettingsKeysTop() {
	s := m.settings
	s.keyTop = max(0, min(s.keyTop, m.settingsKeysMaxTop()))
}

func settingsKeyWidths(cells [][]string, width int) []int {
	widths := make([]int, len(cells[0]))
	for _, row := range cells {
		for i, cell := range row {
			widths[i] = max(widths[i], min(width, ansi.StringWidth(plainText(cell))))
		}
	}
	total := 2 * (len(widths) - 1)
	for _, w := range widths {
		total += w
	}
	for total > width {
		chosen := -1
		// Preserve scope and source identity until the flexible columns have
		// exhausted their room. Within each tier shrink the widest first.
		for _, tier := range [][]int{{2, 4, 0}, {1, 3}} {
			for _, i := range tier {
				if widths[i] > ansi.StringWidth(cells[0][i]) && (chosen < 0 || widths[i] > widths[chosen]) {
					chosen = i
				}
			}
			if chosen >= 0 {
				break
			}
		}
		if chosen < 0 {
			break
		}
		widths[chosen]--
		total--
	}
	return widths
}

// settingsKeysLines is pure. State is clamped on input, reload and resize;
// local clamping also keeps a standalone View safe without mutating the model.
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
		} else if entry.Plugin != "" {
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
	widths := settingsKeyWidths(cells, width)
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
	_, height := m.settingsKeysExtent(len(rows))
	top := max(0, min(s.keyTop, len(body)-height))
	return append(lines, body[top:min(len(body), top+height)]...)
}
