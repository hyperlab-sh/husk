package main

import (
	"strings"
	"testing"
)

func TestSettingsInstalledRemovedConfirmationCannotRetarget(t *testing.T) {
	m, fe := pluginsFixture(t, "installed")
	setSettingsSlice(fe, "husk.pods", `{"plugins":{"alpha":{"status":"live"},"beta":{"status":"live"}}}`)
	m.settle()
	settingsPress(m, "enter")
	settingsPress(m, "y")
	p := m.settings.plugins
	if p.confirmID != "alpha" || !p.confirmed {
		t.Fatal("alpha confirmation not armed")
	}
	setSettingsSlice(fe, "husk.pods", `{"plugins":{"beta":{"status":"live"}}}`)
	m.settle()
	if p.confirm != nil {
		t.Fatal("removed target's confirmation remained open")
	}
	m.width -= 2
	m.settle()
	if p.confirm != nil {
		t.Fatal("resize revived removed target's confirmation")
	}
	settingsPress(m, "enter")
	// Enter may explicitly open a fresh beta confirmation, but it must never
	// carry alpha's previous affirmative choice or remove beta.
	if len(fe.PluginRemovals) != 0 {
		t.Fatal("removed target's approval removed its neighbour")
	}
	if p.confirm != nil && (p.confirmID != "beta" || p.confirmed) {
		t.Fatal("fresh confirmation inherited an old approval")
	}
}

func TestSettingsInstalledConfirmationRebuildKeepsOriginalID(t *testing.T) {
	m, fe := pluginsFixture(t, "installed")
	setSettingsSlice(fe, "husk.pods", `{"plugins":{"alpha":{"status":"live"},"beta":{"status":"live"}}}`)
	m.settle()
	settingsPress(m, "enter")
	settingsPress(m, "y")
	p := m.settings.plugins
	// The table cursor is not the identity of an already-open dialog.
	p.cursor = 1
	m.width -= 2
	m.settle()
	if p.confirm == nil || p.confirmID != "alpha" || !p.confirmed || !strings.Contains(p.confirmView, "Remove alpha") {
		t.Fatal("widget rebuild retargeted an open confirmation")
	}
	settingsPress(m, "enter")
	if len(fe.PluginRemovals) != 1 || fe.PluginRemovals[0].ID != "alpha" {
		t.Fatal("rebuilt confirmation removed the cursor's plugin")
	}
}

func TestSettingsPluginsUnchangedSnapshotAvoidsAllocations(t *testing.T) {
	for _, tab := range []string{"installed", "store"} {
		t.Run(tab, func(t *testing.T) {
			m, fe := pluginsFixture(t, tab)
			if allocations := testing.AllocsPerRun(25, func() { m.refreshSettingsPlugins() }); allocations != 0 {
				t.Fatalf("unchanged snapshot allocated %.0f times per refresh", allocations)
			}
			setSettingsSlice(fe, "husk.pods", `{"plugins":{"beta":{"status":"live"}}}`)
			m.refreshSettingsPlugins()
			if p := m.settings.plugins; len(p.rows) != 1 || p.rows[0].ID != "beta" {
				t.Fatal("new snapshot sequence did not refresh rows")
			}
		})
	}
}

func TestSettingsStoreScrolledFirstRowDoesNotOpenFilter(t *testing.T) {
	m, fe := pluginsFixture(t, "store")
	fe.CatalogRows.Entries[1].Description = strings.Repeat("long detail text ", 200)
	fe.CatalogRev++
	m.settle()
	// Seed a body offset to exercise hit geometry independently of list keys.
	m.settings.plugins.top = 3
	m.settings.plugins.dirty = true
	m.settle()
	if m.settings.plugins.top == 0 {
		t.Fatal("fixture did not scroll past filter")
	}
	r := m.settingsContentRect()
	settingsClick(m, r.Min.X, r.Min.Y)
	if m.settings.plugins.filter != nil {
		t.Fatal("scrolled detail click opened hidden surface filter")
	}
}

func TestSettingsPluginDialogOutsideClickAlsoSwitchesTab(t *testing.T) {
	for _, tab := range []string{"installed", "store"} {
		t.Run(tab, func(t *testing.T) {
			m, fe := pluginsFixture(t, tab)
			settingsPress(m, "enter")
			r := m.settingsRect()
			settingsClick(m, r.Min.X+3, r.Min.Y+3)
			if m.settings.tab != "theme" {
				t.Fatal("outside dialog click swallowed Theme tab switch")
			}
			if p := m.settings.plugins; p.confirm != nil || p.filter != nil {
				t.Fatal("old dialog survived tab click")
			}
			if len(fe.PluginRemovals) != 0 {
				t.Fatal("tab click removed a plugin")
			}
		})
	}
}
