package main

import (
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func settingsModalFixture(t *testing.T) (*WorkbenchModel, *fakeFE) {
	t.Helper()
	m, fe := generalFixture(t, contracts.SettingEntry{Key: "husk.value", Type: "string", Default: "saved", Owner: "husk"})
	addPublicationTestPlugin(m, hello{PluginID: "p", Slots: []protocol.SurfaceID{protocol.SurfaceModal}, RailItems: []contracts.RailItem{{ID: "p.dialog", Placement: contracts.PlacementModal}}})
	m.declared = fe.Rails
	m.registerActions()
	m.attachSlices()
	m.settle()
	fe.OnInvoke = func(action string, args map[string]any) error {
		h := fe.Handlers[action]
		if h == nil {
			t.Fatalf("action %s was not registered", action)
		}
		_, err := h(args)
		return err
	}
	return m, fe
}

func TestSettingsReplacedByInvokedModal(t *testing.T) {
	for _, caller := range []protocol.CallerKind{protocol.CallerCLI, protocol.CallerPlugin, protocol.CallerHost, protocol.CallerInput} {
		t.Run(string(caller), func(t *testing.T) {
			m, fe := settingsModalFixture(t)
			editGeneral(m, "husk.value", "unsaved")
			if !m.settings.dirty() {
				t.Fatal("fixture has no unsaved edit")
			}
			if m.invoke("p.dialog", nil, caller, nil) {
				t.Fatal("modal action declined")
			}
			m.settle()
			if m.settings != nil || m.modalOwner() != (modalState{PluginID: "p", RailID: "p.dialog"}) {
				t.Fatalf("overlays stacked: settings=%v modal=%+v", m.settingsTab(), m.modalOwner())
			}
			want := contracts.Overlays{Modal: contracts.ModalOverlay{Plugin: "p", RailItem: "p.dialog"}}
			if got := fe.Views[len(fe.Views)-1].Overlays; got != want {
				t.Fatalf("committed overlays = %+v, want %+v", got, want)
			}
			settingsPress(m, "x")
			if len(fe.Keys) != 1 || fe.Keys[0].Plugin != "p" || fe.Keys[0].P.Surface != protocol.SurfaceModal || fe.Keys[0].P.Key != "x" {
				t.Fatalf("new modal did not receive key: %+v", fe.Keys)
			}
			m.invoke("husk.settings.open", nil, caller, nil)
			m.settle()
			if m.settings == nil || m.settings.dirty() || m.settings.general.fields["husk.value"].text != "saved" || len(fe.SettingWrites) != 0 {
				t.Fatal("closing Settings did not discard edits without writing")
			}
		})
	}
}

func TestSettingsInvocationReplacesModal(t *testing.T) {
	for _, action := range []string{"husk.settings.open", "husk.theme.select"} {
		t.Run(action, func(t *testing.T) {
			m, fe := settingsModalFixture(t)
			m.closeSettings()
			m.invoke("p.dialog", nil, protocol.CallerCLI, nil)
			m.settle()
			if !m.modalOwner().Open() {
				t.Fatal("fixture modal did not open")
			}
			if m.invoke(action, nil, protocol.CallerCLI, nil) {
				t.Fatal("Settings action declined instead of replacing modal")
			}
			m.settle()
			wantTab := "general"
			if action == "husk.theme.select" {
				wantTab = "theme"
			}
			if m.modalOwner().Open() || m.settingsTab() != wantTab {
				t.Fatalf("replacement failed: settings=%q modal=%+v", m.settingsTab(), m.modalOwner())
			}
			if got, want := fe.Views[len(fe.Views)-1].Overlays, (contracts.Overlays{SettingsCenter: wantTab}); got != want {
				t.Fatalf("committed overlays = %+v, want %+v", got, want)
			}
			settingsPress(m, "tab")
			settingsPress(m, "x")
			if len(fe.Keys) != 0 || m.settings == nil {
				t.Fatalf("Settings input leaked to replaced modal: %+v", fe.Keys)
			}
		})
	}
}
