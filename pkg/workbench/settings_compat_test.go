package workbench

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

// settingsCompatModel embeds a form in the real workbench's cell compositor.
// It checks both field kinds without changing the workbench's behaviour.
type settingsCompatModel struct {
	*WorkbenchModel
	form *huh.Form
}

func (m *settingsCompatModel) Init() tea.Cmd {
	return tea.Batch(m.WorkbenchModel.Init(), m.form.Init())
}
func (m *settingsCompatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, base := m.WorkbenchModel.Update(msg)
	_, field := m.form.Update(msg)
	return m, tea.Batch(base, field)
}
func (m *settingsCompatModel) View() tea.View {
	c := newCanvas(goldenW, goldenH)
	c.blitBlock(0, 0, m.WorkbenchModel.View().Content)
	c.blitBlock(20, 5, m.form.View())
	v := tea.NewView(c.render())
	c.release()
	return v
}
func TestSettingsHuhCompatibility(t *testing.T) {
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Choose profile").Options(huh.NewOption("Work profile", "work")),
		huh.NewConfirm().Title("Confirm selection"),
	)).WithShowHelp(false).WithWidth(50).WithHeight(20)
	m := &settingsCompatModel{goldenModel(), form}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(goldenW, goldenH))
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*settingsCompatModel)
	text := ansi.Strip(final.View().Content)
	for _, want := range []string{"Choose profile", "Work profile", "Confirm selection"} {
		if !strings.Contains(text, want) {
			t.Fatalf("form did not render %q", want)
		}
	}
}
