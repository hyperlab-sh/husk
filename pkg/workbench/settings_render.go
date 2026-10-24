package workbench

import (
	"image"
	"slices"
	"strings"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// settingsHuhTheme uses the already resolved palette, retaining ANSI indices.
// It is built only on Update, never from View.
func settingsHuhTheme(p drawPalette) huh.Theme {
	return huh.ThemeFunc(func(bool) *huh.Styles {
		t := huh.ThemeBase(false)
		base := lipgloss.NewStyle().Foreground(p.Ink).Background(p.SurfaceElevated)
		muted := base.Foreground(p.InkMuted)
		t.Form.Base = base
		// A styled empty Group.Base makes huh's hidden footer non-empty,
		// reserving a phantom row and shifting the Select viewport.
		t.Group.Base = lipgloss.NewStyle()
		t.Group.Title, t.Group.Description = base, muted
		for _, f := range []*huh.FieldStyles{&t.Focused, &t.Blurred} {
			f.Base = base
			f.Title, f.Description = base, muted
			f.ErrorIndicator = base.Foreground(p.Danger).SetString("✕")
			f.ErrorMessage = base
			f.SelectSelector = base.SetString("▸ ")
			f.Option = base
			f.SelectedOption = base.Background(p.SurfaceSelected)
			f.UnselectedOption = base
			f.NextIndicator, f.PrevIndicator = muted, muted
			f.FocusedButton = base.Background(p.SurfaceSelected)
			f.BlurredButton = base
			f.TextInput.Text, f.TextInput.Placeholder = base, muted
			f.TextInput.Prompt, f.TextInput.Cursor = base, base
		}
		t.Focused.Title = base.Foreground(p.Accent)
		t.Focused.SelectSelector = base.Foreground(p.Accent).Background(p.SurfaceSelected).SetString("▸ ")
		t.Focused.FocusedButton = base.Foreground(p.Accent).Background(p.SurfaceSelected)
		t.Focused.TextInput.Prompt = base.Foreground(p.Accent)
		return t
	})
}

func (m *WorkbenchModel) settingsRect() image.Rectangle {
	w, h := min(100, m.width-4), min(32, m.height-4)
	x, y := (m.width-w)/2, (m.height-h)/2
	return image.Rect(x, y, x+w, y+h)
}
func (m *WorkbenchModel) settingsContentRect() image.Rectangle {
	r := m.settingsRect()
	return image.Rect(r.Min.X+15, r.Min.Y+1, r.Max.X-2, r.Max.Y-2)
}
func (m *WorkbenchModel) settingsCornerRect() image.Rectangle {
	r := m.settingsRect()
	w := ansi.StringWidth(m.settingsCorner())
	return image.Rect(r.Max.X-2-w, r.Min.Y, r.Max.X-2, r.Min.Y+1)
}
func (m *WorkbenchModel) settingsProfileRect() image.Rectangle {
	r := m.settingsRect()
	return image.Rect(r.Max.X-34, r.Min.Y+1, r.Max.X-2, r.Min.Y+min(r.Dy()-2, len(m.settings.profiles)+5))
}

func (m *WorkbenchModel) settingsCorner() string {
	s := m.settings
	name := s.active
	if name == "" {
		name = "default"
	}
	text := "profile: " + plainText(name) + " ▾"
	if s.switching {
		text = "switching…"
	}
	return ansi.Truncate(text, 24, "…")
}

func (m *WorkbenchModel) drawSettings(c *canvas) uv.Rectangle {
	s := m.settings
	r := m.settingsRect()
	base := lipgloss.NewStyle().Foreground(m.palette.Ink).Background(m.palette.SurfaceElevated)
	muted := base.Foreground(m.palette.InkMuted)
	border := base.Foreground(m.palette.BorderFocused)
	// Draw a completely opaque box, then replace only the cells each fragment owns.
	rows := make([]string, r.Dy())
	rows[0] = "┌" + strings.Repeat("─", r.Dx()-2) + "┐"
	for i := 1; i < r.Dy()-1; i++ {
		rows[i] = "│" + strings.Repeat(" ", r.Dx()-2) + "│"
	}
	rows[len(rows)-1] = "└" + strings.Repeat("─", r.Dx()-2) + "┘"
	c.blitBlock(r.Min.X, r.Min.Y, border.Render(strings.Join(rows, "\n")))
	c.blitBlock(r.Min.X+2, r.Min.Y, base.Bold(true).Render(" Settings "))
	corner := m.settingsCornerRect()
	style := muted
	if s.focus == settingsProfileFocus {
		style = base.Foreground(m.palette.Accent)
	}
	c.blitBlock(corner.Min.X-1, corner.Min.Y, style.Render(" "+m.settingsCorner()+" "))
	labels := []string{"General", "Keys", "Theme", "Plugins", " Store", " Installed"}
	tabs := []string{"general", "keys", "theme", "", "store", "installed"}
	for row, label := range labels {
		st := base
		marker := " "
		if tabs[row] == s.tab {
			st = st.Background(m.palette.SurfaceSelected)
			marker = "▸"
		}
		if s.focus == settingsTabsFocus && tabs[row] != "" {
			st = st.Foreground(m.palette.Accent)
		}
		text := marker + " " + label
		if tabs[row] != "" && s.focus == settingsTabsFocus {
			text = marker + slicesTabDigit(tabs[row]) + " " + strings.TrimSpace(label)
		}
		text = ansi.Truncate(text, 12, "…")
		text += strings.Repeat(" ", max(0, 12-ansi.StringWidth(text)))
		c.blitBlock(r.Min.X+1, r.Min.Y+row+1, st.Render(text))
	}
	for y := r.Min.Y + 1; y < r.Max.Y-2; y++ {
		c.blitBlock(r.Min.X+13, y, border.Render("│"))
	}
	content := m.settingsContentRect()
	if s.tab == "general" && s.general != nil {
		g := s.general
		c.blitLines(content.Min.X, content.Min.Y, content.Dx(), content.Dy(), g.lines[g.top:])
	}
	if s.tab == "keys" {
		c.blitLines(content.Min.X, content.Min.Y, content.Dx(), content.Dy(), m.settingsKeysLines())
	}
	if (s.tab == "store" || s.tab == "installed") && s.plugins != nil {
		p := s.plugins
		lines := p.lines[min(p.top, len(p.lines)):]
		if p.confirm != nil {
			confirm, rect := m.settingsPluginConfirmLayout()
			keep := rect.Min.Y - content.Min.Y - 1
			lines = append(slices.Clone(lines[:keep]), "")
			lines = append(lines, confirm...)
		}
		if p.filter != nil {
			lines = strings.Split(p.filterView, "\n")
		}
		c.blitLines(content.Min.X, content.Min.Y, content.Dx(), content.Dy(), lines)
	}
	if s.tab == "theme" {
		height := max(1, content.Dy()-4)
		top := settingsListTop(s.top, s.cursor, len(s.themes), height)
		for i := top; i < min(len(s.themes), top+height); i++ {
			entry := s.themes[i]
			st := base
			if i == s.cursor {
				st = st.Background(m.palette.SurfaceSelected)
			}
			mark := st.Render(" ")
			if entry.Current {
				mark = st.Foreground(m.palette.Accent).Render("●")
			}
			tail := ""
			if s.pending && i == s.cursor {
				tail = "  pending…"
			}
			text := " " + plainText(entry.Name) + "  " + plainText(entry.Source) + tail
			text = ansi.Truncate(text, content.Dx()-1, "…")
			text += strings.Repeat(" ", max(0, content.Dx()-1-ansi.StringWidth(text)))
			c.blitBlock(content.Min.X, content.Min.Y+i-top, mark+st.Render(text))
		}
		if s.problem != "" {
			c.blitLines(content.Min.X, content.Max.Y-3, content.Dx(), 1, []string{m.statusMark("danger", "✕") + " " + base.Render(plainText(s.problem))})
		}
		if s.written != "" {
			c.blitLines(content.Min.X, content.Max.Y-2, content.Dx(), 1, []string{muted.Render(s.written)})
		}
	}
	if s.profileError != "" {
		c.blitLines(content.Min.X, content.Max.Y-1, content.Dx(), 1, []string{m.statusMark("danger", "✕") + " " + base.Render(plainText(s.profileError))})
	}
	footer := base.Bold(true).Render("tab/shift+tab") + muted.Render(" focus · ")
	switch s.focus {
	case settingsTabsFocus:
		footer += base.Bold(true).Render("1-5") + muted.Render(" tabs · ")
	case settingsContentFocus:
		if s.tab == "theme" {
			footer += base.Bold(true).Render("enter") + muted.Render(" select · ")
		}
		if s.tab == "installed" {
			footer += base.Bold(true).Render("enter/x") + muted.Render(" remove · ")
		}
		if s.tab == "store" {
			footer += base.Bold(true).Render("enter") + muted.Render(" surface · type to filter · ")
		}
		if s.tab == "general" {
			footer += base.Bold(true).Render("ctrl+s") + muted.Render(" save · ")
		}
	case settingsProfileFocus:
		footer += base.Bold(true).Render("enter") + muted.Render(" profiles · ")
	}
	footer += base.Bold(true).Render("esc") + muted.Render(" close")
	if s.tab == "keys" {
		footer = muted.Render("edit keymap.json to change a key · ") + base.Bold(true).Render("esc") + muted.Render(" close")
	}
	if s.dirty() {
		footer += muted.Render(" (discards edits)")
	}
	if s.profileForm != nil {
		footer = muted.Render("applies now; persisting is husk profile use --save")
	}
	c.blitLines(r.Min.X+2, r.Max.Y-2, r.Dx()-4, 1, []string{footer})
	if s.profileForm != nil {
		pr := m.settingsProfileRect()
		for y := pr.Min.Y; y < pr.Max.Y; y++ {
			c.blitBlock(pr.Min.X, y, base.Render(strings.Repeat(" ", pr.Dx())))
		}
		c.blitLines(pr.Min.X, pr.Min.Y, pr.Dx(), pr.Dy(), strings.Split(s.profileView, "\n"))
	}
	area := uv.Rect(r.Min.X, r.Min.Y, r.Dx(), r.Dy())
	c.fillDefaultBg(area, m.palette.SurfaceElevated)
	return area
}

func slicesTabDigit(tab string) string {
	for i, id := range settingsTabs {
		if id == tab {
			return string(rune('1' + i))
		}
	}
	return ""
}
