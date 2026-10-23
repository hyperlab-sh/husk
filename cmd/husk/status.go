package main

import "charm.land/lipgloss/v2"

// statusMark draws a status glyph or short badge, never a whole message.
// Callers use success, warning, danger or info; unknown levels use info.
// Message text beside the mark keeps its ordinary ink styling.
func (m *WorkbenchModel) statusMark(level, text string) string {
	col := m.palette.Info
	switch level {
	case "success":
		col = m.palette.Success
	case "warning":
		col = m.palette.Warning
	case "danger":
		col = m.palette.Danger
	}
	return lipgloss.NewStyle().Foreground(col).Render(text)
}
