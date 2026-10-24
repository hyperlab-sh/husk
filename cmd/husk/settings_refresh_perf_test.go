package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

type settingsFieldCounter struct {
	huh.Field
	views int
}

func (f *settingsFieldCounter) View() string { f.views++; return f.Field.View() }

func TestSettingsRefreshIdleAndReload(t *testing.T) {
	for _, tab := range settingsTabs {
		t.Run(tab, func(t *testing.T) {
			m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.value", Type: "string", Default: "old", Owner: "husk"})
			m.settings.tab, m.settings.focus = tab, settingsContentFocus
			counter := &settingsReadCounter{Frontend: fe}
			m.fe = counter
			m.settle()
			schema, values, keys, catalog := fe.SchemaReads, fe.SettingReads, fe.KeyListingReads, fe.CatalogReads
			counter.themes, counter.profiles = 0, 0
			for range 3 {
				m.Update(modifierTickMsg{})
			}
			if counter.themes != 0 || counter.profiles != 0 || fe.SchemaReads != schema || fe.SettingReads != values || fe.KeyListingReads != keys || fe.CatalogReads != catalog {
				t.Fatalf("idle refresh: themes=%d profiles=%d schema=%d values=%d keys=%d catalog=%d", counter.themes, counter.profiles, fe.SchemaReads-schema, fe.SettingReads-values, fe.KeyListingReads-keys, fe.CatalogReads-catalog)
			}
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
