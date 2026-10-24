package workbench

import (
	"fmt"
	"testing"

	"charm.land/bubbles/v2/cursor"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

type settingsFieldCounter struct {
	huh.Field
	views, updates int
}

func (f *settingsFieldCounter) View() string { f.views++; return f.Field.View() }
func (f *settingsFieldCounter) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	f.updates++
	return f.Field.Update(msg)
}

func TestSettingsRefreshIdleAndReload(t *testing.T) {
	for _, tab := range settingsTabs {
		t.Run(tab, func(t *testing.T) {
			m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.value", Type: "string", Default: "old", Owner: "husk"})
			m.settings.tab, m.settings.focus = tab, settingsContentFocus
			counter := &settingsReadCounter{Frontend: fe}
			m.fe = counter
			m.settle()
			field := m.settings.general.fields["husk.value"]
			widget := &settingsFieldCounter{Field: field.widget}
			field.widget = widget
			schema, values, keys, catalog := fe.SchemaReads, fe.SettingReads, fe.KeyListingReads, fe.CatalogReads
			counter.themes, counter.profiles = 0, 0
			for range 3 {
				m.Update(modifierTickMsg{})
				m.Update(struct{ worker bool }{})
			}
			if widget.views != 0 || widget.updates != 0 {
				t.Fatalf("idle widget work: views=%d updates=%d", widget.views, widget.updates)
			}
			if counter.themes != 0 || counter.profiles != 0 || fe.SchemaReads != schema || fe.SettingReads != values || fe.KeyListingReads != keys || fe.CatalogReads != catalog {
				t.Fatalf("idle refresh: themes=%d profiles=%d schema=%d values=%d keys=%d catalog=%d", counter.themes, counter.profiles, fe.SchemaReads-schema, fe.SettingReads-values, fe.KeyListingReads-keys, fe.CatalogReads-catalog)
			}
			// Restore the concrete field before refresh: syncWidget dispatches
			// on huh's concrete types to rebind the refreshed accessor value.
			field.widget = widget.Field
			fe.SettingValues = map[string]contracts.SettingValue{"husk.value": {Value: "new", Source: "user"}}
			fe.ThemeRows = []contracts.ThemeEntry{{Name: "new", Current: true}}
			fe.ProfileRows = []contracts.ProfileInfo{{Name: "new", Active: true}}
			fe.ServiceRev++
			m.settle()
			if counter.themes != 1 || counter.profiles != 1 || fe.SchemaReads != schema+1 || fe.SettingReads != values+1 || m.settings.active != "new" || m.settings.themes[0].Name != "new" || m.settings.general.fields["husk.value"].text != "new" {
				t.Fatal("reload did not refresh serving sources")
			}
			if tab == "keys" && fe.KeyListingReads != keys+1 {
				t.Fatal("reload missed visible Keys")
			}
			if tab == "store" && fe.CatalogReads != catalog+1 {
				t.Fatal("reload missed visible Store")
			}
		})
	}
}

func TestSettingsProfileSelectSurvivesUnchangedReload(t *testing.T) {
	m, fe := settingsFixture(t, "theme")
	fe.ProfileRows = nil
	for i := range 40 {
		fe.ProfileRows = append(fe.ProfileRows, contracts.ProfileInfo{Name: fmt.Sprintf("profile%02d", i), Source: "user"})
	}
	fe.ServiceRev++
	m.settle()
	m.openSettingsProfiles()
	settingsPress(m, "end")
	m.settle()
	s := m.settings
	form, top, view, commands := s.profileForm, s.profileTop, s.profileView, len(m.cmds)
	if top == 0 {
		t.Fatal("fixture did not scroll the profile Select")
	}
	fe.ServiceRev++
	m.settle()
	if s.profileForm != form || s.profileTop != top || s.profileView != view || len(m.cmds) != commands {
		t.Fatal("unchanged reload rebuilt or moved the profile Select")
	}
}

func TestSettingsIdleProfileSelectRefreshAllocatesNothing(t *testing.T) {
	m, _ := settingsFixture(t, "theme")
	m.openSettingsProfiles()
	m.settle()
	if allocations := testing.AllocsPerRun(10, func() { m.refreshSettings() }); allocations != 0 {
		t.Fatalf("idle refresh with profile Select allocated %g times", allocations)
	}
}

func TestSettingsGeneralReadsEachNewOrRetypedFieldOnce(t *testing.T) {
	for _, kind := range []string{"new", "retyped"} {
		t.Run(kind, func(t *testing.T) {
			m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.value", Type: "string", Default: "old", Owner: "husk"})
			if kind == "retyped" {
				fe.SchemaRows[0].Type, fe.SchemaRows[0].Default = "bool", true
				fe.ServiceRev++
				fe.SettingReads = 0
				m.settle()
				if !m.settings.general.fields["husk.value"].boolean {
					t.Fatal("retyped field did not read its new value")
				}
			}
			if fe.SettingReads != 1 {
				t.Fatalf("%s field read %d times", kind, fe.SettingReads)
			}
		})
	}
}

func TestSettingsGeneralAcceptsWidgetMessages(t *testing.T) {
	for name, msg := range map[string]tea.Msg{"blink": cursor.BlinkMsg{}, "initial blink": cursor.Blink()} {
		t.Run(name, func(t *testing.T) {
			m, _ := generalFixture(t, contracts.SettingEntry{Key: "husk.value", Type: "string", Default: "old", Owner: "husk"})
			m.settings.focus = settingsContentFocus
			m.settle()
			f := m.settings.general.fields["husk.value"]
			counter := &settingsFieldCounter{Field: f.widget}
			f.widget = counter
			m.Update(msg)
			if counter.updates != 1 {
				t.Fatalf("widget message reached the field %d times, want 1", counter.updates)
			}
		})
	}
}

func TestSettingsProfileSelectRoutesWorkerAndWidgetMessages(t *testing.T) {
	m, _ := settingsFixture(t, "theme")
	m.openSettingsProfiles()
	m.settle()
	s := m.settings
	view := s.profileView
	// Inspect the flag before settle consumes it, then check public Update too.
	m.update(struct{ worker bool }{})
	if s.profileDirty || s.profileView != view {
		t.Fatal("unrelated worker message invalidated the profile Select")
	}
	m.Update(struct{ worker bool }{})
	if s.profileDirty || s.profileView != view {
		t.Fatal("unrelated worker message invalidated the profile Select")
	}
	for _, msg := range []tea.Msg{cursor.BlinkMsg{}, cursor.Blink()} {
		// Step the handler before settling so its dirty signal is observable;
		// the public Update consumes that signal in its final settle.
		m.update(msg)
		if !s.profileDirty {
			t.Fatal("widget message did not invalidate the profile Select")
		}
		m.settle()
		if s.profileDirty {
			t.Fatal("settle did not consume the profile view invalidation")
		}
	}
}

func TestSettingsGeneralIgnoresUnrelatedWorkerMessages(t *testing.T) {
	m, _ := generalFixture(t, contracts.SettingEntry{Key: "husk.value", Type: "string", Default: "old", Owner: "husk"})
	m.settings.focus = settingsContentFocus
	m.settle()
	f := m.settings.general.fields["husk.value"]
	counter := &settingsFieldCounter{Field: f.widget}
	f.widget = counter
	m.Update(struct{ unrelated bool }{true})
	if counter.views != 0 {
		t.Fatalf("unrelated message rendered focused field %d times", counter.views)
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if counter.views == 0 {
		t.Fatal("input did not refresh field view")
	}
}
