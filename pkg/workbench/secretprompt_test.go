package workbench

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/services/secrets"
)

func openPrompt(m *WorkbenchModel, id uint64, p secrets.Prompt) chan secrets.Answer {
	reply := make(chan secrets.Answer, 1)
	m.Update(secretPromptOpenMsg{id: id, prompt: p, reply: reply})
	return reply
}

var askPrompt = secrets.Prompt{Plugin: "git.tools", PluginName: "Totally Trusted", Name: "git.token", Purpose: "Push your branches", Required: true}

// Only y, n and esc answer; each answers once and takes the dialog down.
func TestSecretPromptKeysAnswerOnce(t *testing.T) {
	for key, want := range map[string]secrets.Answer{"y": secrets.Accepted, "n": secrets.Declined, "esc": secrets.Dismissed, "ctrl+c": secrets.Dismissed} {
		t.Run(key, func(t *testing.T) {
			m := goldenModel()
			reply := openPrompt(m, 1, askPrompt)
			if m.secretPrompt == nil {
				t.Fatal("dialog not shown")
			}
			m.Update(secretPromptArmMsg{id: 1})
			press(m, key)
			if m.secretPrompt != nil {
				t.Fatal("dialog still up after an answer")
			}
			if got := <-reply; got != want {
				t.Fatalf("answer = %v, want %v", got, want)
			}
			press(m, key)
			if len(reply) != 0 {
				t.Fatal("a second key answered again")
			}
		})
	}
}

// y and n do nothing until the dialog is armed, so a keystroke meant for a
// plugin cannot answer it; esc dismisses at any time. A stale arm message for
// another dialog arms nothing.
func TestSecretPromptKeysAreInactiveUntilArmed(t *testing.T) {
	m := goldenModel()
	reply := openPrompt(m, 2, askPrompt)
	press(m, "y")
	press(m, "n")
	m.Update(secretPromptArmMsg{id: 1})
	press(m, "y")
	if m.secretPrompt == nil || len(reply) != 0 {
		t.Fatalf("answered before arming: prompt=%v answers=%d", m.secretPrompt != nil, len(reply))
	}
	if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, "not active yet") {
		t.Fatalf("footer does not say the keys are inactive:\n%s", frame)
	}
	m.Update(secretPromptArmMsg{id: 2})
	if frame := ansi.Strip(m.View().Content); strings.Contains(frame, "not active yet") || !strings.Contains(frame, "allow") {
		t.Fatalf("footer after arming:\n%s", frame)
	}
	press(m, "y")
	if got := <-reply; got != secrets.Accepted {
		t.Fatalf("answer = %v", got)
	}
	// esc works before arming.
	m = goldenModel()
	reply = openPrompt(m, 3, askPrompt)
	press(m, "esc")
	if got := <-reply; got != secrets.Dismissed {
		t.Fatalf("esc before arming = %v", got)
	}
}

// The open message schedules the arm message.
func TestSecretPromptOpenSchedulesArm(t *testing.T) {
	m := goldenModel()
	reply := make(chan secrets.Answer, 1)
	_, cmd := m.Update(secretPromptOpenMsg{id: 9, prompt: askPrompt, reply: reply})
	if cmd == nil {
		t.Fatal("open returned no arm command")
	}
}

// The dialog is modal for paste too, even with the palette open.
func TestSecretPromptSwallowsPaste(t *testing.T) {
	m := goldenModel()
	m.showPalette = true
	openPrompt(m, 1, askPrompt)
	before := m.paletteQuery
	m.Update(tea.PasteMsg{Content: "hello"})
	if m.paletteQuery != before {
		t.Fatalf("paste reached the palette: %q", m.paletteQuery)
	}
}

// Enter and every other key are swallowed: nothing accepts by accident and
// nothing reaches the palette or the keymap.
func TestSecretPromptSwallowsOtherKeys(t *testing.T) {
	m := goldenModel()
	reply := openPrompt(m, 1, askPrompt)
	m.Update(secretPromptArmMsg{id: 1})
	for _, key := range []string{"enter", "tab", "x", "Y", "ctrl+p", "f1", " "} {
		press(m, key)
	}
	if m.secretPrompt == nil || len(reply) != 0 || m.showPalette {
		t.Fatalf("prompt=%v answered=%d palette=%v", m.secretPrompt != nil, len(reply), m.showPalette)
	}
}

// The service withdrawing its own dialog closes it; a stale id does not.
func TestSecretPromptCloseMessage(t *testing.T) {
	m := goldenModel()
	openPrompt(m, 4, askPrompt)
	m.Update(secretPromptCloseMsg{id: 3})
	if m.secretPrompt == nil {
		t.Fatal("a stale close withdrew the dialog")
	}
	m.Update(secretPromptCloseMsg{id: 4})
	if m.secretPrompt != nil {
		t.Fatal("close did not withdraw the dialog")
	}
}

// A newer dialog replaces an older one, which is dismissed.
func TestSecretPromptReplacedIsDismissed(t *testing.T) {
	m := goldenModel()
	old := openPrompt(m, 1, askPrompt)
	openPrompt(m, 2, askPrompt)
	if got := <-old; got != secrets.Dismissed {
		t.Fatalf("replaced answer = %v", got)
	}
}

// The verified plugin id comes before the plugin's own display-name claim,
// which is marked as a claim; the purpose is labelled as the plugin's words
// and cannot inject control sequences.
func TestSecretPromptContent(t *testing.T) {
	m := goldenModel()
	p := askPrompt
	p.Purpose = "Push\x1b[2J branches\nand\x07 more"
	openPrompt(m, 1, p)
	m.Update(secretPromptArmMsg{id: 1})
	frame := ansi.Strip(m.View().Content)
	order := []string{"git.tools", "Totally Trusted (the plugin's own claim)", "git.token", "Required yes", "Purpose, in the plugin's words", "Push[2J branchesand more"}
	at := 0
	for _, want := range order {
		i := strings.Index(frame[at:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", want, frame)
		}
		at += i + len(want)
	}
	if strings.Contains(m.View().Content, "\x1b[2J") || strings.Contains(frame, "\x07") {
		t.Fatal("control sequence from the purpose reached the frame")
	}
	for _, want := range []string{"allow", "decline", "esc"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("hint %q missing", want)
		}
	}
}

// Rendering the dialog reads the model and never writes it.
func TestSecretPromptViewIsPure(t *testing.T) {
	m := goldenModel()
	openPrompt(m, 1, askPrompt)
	a := m.View().Content
	b := m.View().Content
	if a != b || m.secretPrompt == nil {
		t.Fatal("View changed state or output")
	}
}

// A program that stopped reading dismisses the dialog instead of blocking the
// Secrets service.
func TestSecretPromptFuncDismissesWhenProgramGone(t *testing.T) {
	ref := newProgramRef()
	ref.closeGate()
	got := secretPromptFunc(ref)(context.Background(), askPrompt)
	if got != secrets.Dismissed {
		t.Fatalf("answer = %v", got)
	}
}
