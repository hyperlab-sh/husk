package main

import (
	"context"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/services/secrets"
)

// secretPromptState is the confirm dialog the Secrets service asked for. The
// service shows one at a time, so there is at most one; reply carries the
// answer back to the service goroutine that is blocked on it.
type secretPromptState struct {
	id     uint64
	prompt secrets.Prompt
	reply  chan<- secrets.Answer
	// armed is set a moment after the dialog appears. A plugin can open the
	// dialog while the user types, so y and n do nothing until then.
	armed bool
}

// secretPromptArmDelay is how long y and n stay inactive after the dialog
// appears.
const secretPromptArmDelay = time.Second

// secretPromptOpenMsg asks the model to show a dialog; secretPromptCloseMsg
// withdraws it when the service closed it first (a grant, a changed
// declaration, a dropped read or the timeout).
type (
	secretPromptOpenMsg struct {
		id     uint64
		prompt secrets.Prompt
		reply  chan<- secrets.Answer
	}
	secretPromptCloseMsg struct{ id uint64 }
	// secretPromptArmMsg arms the answer keys of dialog id.
	secretPromptArmMsg struct{ id uint64 }
)

// secretPromptFunc is host.WithPrompt's dialog: it runs on a service
// goroutine, posts the dialog to the program and blocks for the answer. A
// program that stopped reading, or a cancelled ctx, is a dismissal.
func secretPromptFunc(prog *programRef) func(context.Context, secrets.Prompt) secrets.Answer {
	var seq atomic.Uint64
	return func(ctx context.Context, p secrets.Prompt) secrets.Answer {
		id := seq.Add(1)
		reply := make(chan secrets.Answer, 1)
		if !prog.post(secretPromptOpenMsg{id: id, prompt: p, reply: reply}) {
			return secrets.Dismissed
		}
		select {
		case a := <-reply:
			return a
		case <-ctx.Done():
			prog.post(secretPromptCloseMsg{id: id})
			return secrets.Dismissed
		}
	}
}

// answerSecretPrompt sends the answer once and takes the dialog down.
func (m *WorkbenchModel) answerSecretPrompt(a secrets.Answer) {
	p := m.secretPrompt
	m.secretPrompt = nil
	if p == nil {
		return
	}
	select {
	case p.reply <- a:
	default:
	}
}

// secretPromptKey handles a key while the dialog is up. It swallows every key:
// nothing reaches the palette, the keymap or a plugin, and only an explicit
// y or n, once the dialog is armed, grants or declines; esc always dismisses. Enter does not answer, so a stray keystroke
// cannot accept.
func (m *WorkbenchModel) secretPromptKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		if m.secretPrompt.armed {
			m.answerSecretPrompt(secrets.Accepted)
		}
	case "n":
		if m.secretPrompt.armed {
			m.answerSecretPrompt(secrets.Declined)
		}
	case "esc", "ctrl+c":
		m.answerSecretPrompt(secrets.Dismissed)
	}
	return m, nil
}

// plainText keeps only graphic runes of text a plugin wrote, so it cannot
// smuggle an escape sequence or a line break into the dialog.
func plainText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsGraphic(r) {
			return r
		}
		return -1
	}, s)
}

// drawSecretPrompt draws the confirm box: the verified plugin id first, the
// plugin's own claim of a display name marked as such, then what it asks for.
func (m *WorkbenchModel) drawSecretPrompt(c *canvas) uv.Rectangle {
	p := m.secretPrompt.prompt
	boxW := min(72, m.width-8)
	iw := boxW - 4
	label := lipgloss.NewStyle().Foreground(m.palette.InkMuted)
	ink := lipgloss.NewStyle().Foreground(m.palette.Ink)
	field := func(name, value string) string {
		return label.Render(name) + " " + ink.Render(value)
	}
	required := "no"
	if p.Required {
		required = "yes"
	}
	rows := []string{
		m.statusMark("warning", "⚠") + " " + ink.Bold(true).Render("SECRET ACCESS REQUEST"),
		"",
		label.Render("Plugin  ") + " " + ink.Bold(true).Render(plainText(p.Plugin)),
	}
	if name := plainText(p.PluginName); name != "" {
		rows = append(rows, field("Claims  ", name+" (the plugin's own claim)"))
	}
	rows = append(rows, field("Secret  ", plainText(p.Name)), field("Required", required), "",
		label.Render("Purpose, in the plugin's words:"))
	for _, line := range strings.Split(ansi.Wrap(plainText(p.Purpose), iw-2, ""), "\n") {
		rows = append(rows, "  "+ink.Render(line))
	}
	rule := lipgloss.NewStyle().Foreground(m.palette.Border).Render(strings.Repeat("─", iw))
	key := ink.Bold(true)
	hint := key.Render("y") + label.Render(" allow   ") + key.Render("n") + label.Render(" decline   ") + key.Render("esc") + label.Render(" ask again later")
	if !m.secretPrompt.armed {
		hint = label.Render("y and n are not active yet   ") + key.Render("esc") + label.Render(" ask again later")
	}
	rows = append(rows, "", rule, hint)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.palette.BorderFocused).
		Background(m.palette.SurfaceElevated).
		Padding(0, 1).
		Width(boxW).
		Render(strings.Join(rows, "\n"))
	h := strings.Count(box, "\n") + 1
	r := c.blitBlock(max(0, (m.width-boxW)/2), max(1, (m.height-h)/2), box)
	c.fillDefaultBg(r, m.palette.SurfaceElevated)
	return r
}
