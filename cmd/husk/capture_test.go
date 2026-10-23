package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/stem/surfaces"
)

// keyContext carries husk.surfaces.capture for the focused surface, and for
// the modal surface while a modal is open.
func TestKeyContextCarriesCapture(t *testing.T) {
	m := goldenModel()
	addPlugin(m, "p", map[protocol.SurfaceID][]string{protocol.SurfaceMainStage: {"x"}, protocol.SurfaceSidebarList: {"y"}})
	m.setFocus(protocol.SurfaceMainStage)
	if m.keyContext().Capture != protocol.CaptureNone {
		t.Fatal("capture set with nothing captured")
	}
	m.catalog.Capture[protocol.SurfaceMainStage] = protocol.CaptureText
	if ctx := m.keyContext(); ctx.Capture != protocol.CaptureText || ctx.FocusSurface != "main-stage" {
		t.Fatalf("focused capture not carried: %+v", ctx)
	}
	m.setFocus(protocol.SurfaceSidebarList)
	if m.keyContext().Capture != protocol.CaptureNone {
		t.Fatal("capture of main-stage leaked to a focused sidebar-list")
	}

	m.openModal("p", "p.open")
	if m.keyContext().Capture != protocol.CaptureNone {
		t.Fatal("capture of main-stage leaked to the modal")
	}
	m.catalog.Capture[protocol.SurfaceModal] = protocol.CaptureText
	if ctx := m.keyContext(); ctx.Capture != protocol.CaptureText || !ctx.ModalOpen || ctx.FocusSurface != "modal-surface" {
		t.Fatalf("modal capture not carried: %+v", ctx)
	}
}

// refresh copies the engine's capture map into the catalog and never writes it.
func TestRefreshReadsCaptureFromState(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	fe.pods, fe.occ = map[string]string{"p": "live"}, map[protocol.SurfaceID]string{protocol.SurfaceMainStage: "p"}
	fe.seq++
	res := surfaces.Surfaces{
		Occupants: map[protocol.SurfaceID]contracts.Occupant{protocol.SurfaceMainStage: {Surface: protocol.SurfaceMainStage, Plugin: "p"}},
		Capture:   map[protocol.SurfaceID]protocol.CaptureMode{protocol.SurfaceMainStage: protocol.CaptureText},
	}
	fe.Snap = snapshot(fe.seq, fe.pods, res)
	m.refresh()
	if m.catalog.Capture[protocol.SurfaceMainStage] != protocol.CaptureText {
		t.Fatalf("capture not read: %v", m.catalog.Capture)
	}

	res.Capture = map[protocol.SurfaceID]protocol.CaptureMode{}
	fe.seq++
	fe.Snap = snapshot(fe.seq, fe.pods, res)
	m.refresh()
	if len(m.catalog.Capture) != 0 {
		t.Fatalf("capture not cleared by the next snapshot: %v", m.catalog.Capture)
	}
}

func captureModel(t *testing.T) (*WorkbenchModel, *fakeFE, *pluginWire, *pluginWire) {
	t.Helper()
	m := goldenModel()
	_, side := addPublicationTestPlugin(m, hello{PluginID: "side", Slots: []protocol.SurfaceID{protocol.SurfaceSidebarList}})
	_, stage := addPublicationTestPlugin(m, hello{PluginID: "stage", Slots: []protocol.SurfaceID{protocol.SurfaceMainStage}})
	fe := withFE(m)
	m.setFocus(protocol.SurfaceMainStage)
	m.catalog.Capture[protocol.SurfaceMainStage] = protocol.CaptureText
	resetAll(side, stage)
	return m, fe, side, stage
}

// With capture on, a globally bound text key goes to the occupant as
// input.key and invokes nothing; modified keys and shift+tab still resolve,
// and tab, a text key, reaches the occupant.
func TestCaptureKeepsPlainTypingForTheOccupant(t *testing.T) {
	m, fe, _, stage := captureModel(t)
	for _, k := range []string{"q", "v", "h", "[", ":", "?", "/"} {
		press(m, k)
	}
	press(m, "tab")
	r := routed(t, stage)
	if got, want := len(r.slotKeys), 8; got != want || len(r.invokes) != 0 {
		t.Fatalf("routing = %+v, want %d keys and no invokes", r, want)
	}
	if len(fe.Invoked) != 0 || m.showPalette || m.focusedSlot != protocol.SurfaceMainStage || !m.showSidebar || m.stageMode != "plugin" {
		t.Fatalf("a global key fired while capturing: invoked=%v palette=%v focus=%s sidebar=%v stage=%s", fe.Invoked, m.showPalette, m.focusedSlot, m.showSidebar, m.stageMode)
	}
	for i, want := range []string{"q", "v", "h", "[", ":", "?", "/", "tab"} {
		if r.slotKeys[i].Key != want || r.slotKeys[i].Slot != protocol.SurfaceMainStage {
			t.Errorf("key %d = %+v, want %q on main-stage", i, r.slotKeys[i], want)
		}
	}

	// shift+tab cycles focus whatever the occupant is doing.
	resetAll(stage)
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.focusedSlot == protocol.SurfaceMainStage {
		t.Fatal("shift+tab did not move focus while capturing")
	}
	if r := routed(t, stage); len(r.slotKeys) != 0 {
		t.Fatalf("shift+tab reached the occupant: %+v", r)
	}

	// ctrl+p still opens the palette while capturing.
	m.setFocus(protocol.SurfaceMainStage)
	press(m, "ctrl+p")
	if !m.showPalette {
		t.Fatal("ctrl+p did not open the palette while capturing")
	}
}

func TestWithoutCaptureTextKeysStillFire(t *testing.T) {
	m, _, _, stage := captureModel(t)
	delete(m.catalog.Capture, protocol.SurfaceMainStage)
	press(m, "q")
	if r := routed(t, stage); len(r.slotKeys) != 0 {
		t.Fatalf("q reached the occupant with capture off: %+v", r)
	}
}

// The no-host fallback mirrors the Capture filter, modal exemption included.
func TestDefaultKeysMirrorTheCaptureFilter(t *testing.T) {
	d := defaultKeys{Extra: []defaultBinding{{"n", "p.open", ""}, {"z", "p.surface", "main-stage"}}}
	resolve := func(ctx contracts.KeyContext, key string) string {
		if res := d.Resolve(ctx, []string{key}); res.Outcome == contracts.KeyMatch {
			return res.Binding.Action
		}
		return ""
	}
	cap := contracts.KeyContext{FocusSurface: "main-stage", Capture: protocol.CaptureText}
	for _, tc := range []struct {
		key  string
		want string
	}{
		{"q", ""}, {"tab", ""}, {"?", ""}, {"n", ""}, {"ctrl+c", "husk.quit"}, {"alt+1", "husk.home"},
		{"shift+tab", "husk.focus.next"}, {"f1", "husk.home"}, {"esc", "husk.home"}, {"z", "p.surface"},
	} {
		if got := resolve(cap, tc.key); got != tc.want {
			t.Errorf("capturing %q = %q, want %q", tc.key, got, tc.want)
		}
	}
	modal := contracts.KeyContext{FocusSurface: "modal-surface", ModalOpen: true, ModalAction: "p.open", Capture: protocol.CaptureText}
	for key, want := range map[string]string{"n": "p.open", "esc": "husk.modal.close", "q": ""} {
		if got := resolve(modal, key); got != want {
			t.Errorf("modal+capture %q = %q, want %q", key, got, want)
		}
	}
	// No modal open: the modal's action key has no exemption.
	if got := resolve(contracts.KeyContext{FocusSurface: "main-stage", Capture: protocol.CaptureText, ModalAction: "p.open"}, "n"); got != "" {
		t.Errorf("n with no modal open = %q, want none", got)
	}
	if got := resolve(contracts.KeyContext{FocusSurface: "main-stage"}, "q"); got != "husk.quit" {
		t.Errorf("q without capture = %q", got)
	}
}

// Every host action the built-in keymap names has a palette row, and every
// row names a registered host action.
func TestBuiltinCommandsMatchTheKeymap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "services", "keybinding", "keymap.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Bind []struct{ Action string }
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, b := range doc.Bind {
		named[b.Action] = true
	}
	named["husk.settings.open"] = true
	named["husk.theme.select"] = true
	m := goldenModel()
	rows := map[string]paletteCommand{}
	for _, c := range m.builtinCommands() {
		if _, dup := rows[c.ID]; dup {
			t.Errorf("duplicate palette row %s", c.ID)
		}
		rows[c.ID] = c
		if c.Title == "" {
			t.Errorf("row %s has no title", c.ID)
		}
		if m.actions[c.ID] == nil {
			t.Errorf("row %s is not a registered host action", c.ID)
		}
	}
	for action := range named {
		if _, ok := rows[action]; !ok {
			t.Errorf("keymap action %s has no palette row", action)
		}
	}
	if len(named) != 17 || len(rows) != len(named) {
		t.Errorf("keymap plus unbound actions names %d actions and the palette lists %d, want 17 each", len(named), len(rows))
	}
	if r := rows["husk.focus.next"]; !strings.Contains(r.Shortcut, "tab") {
		t.Errorf("shortcut column of husk.focus.next = %q", r.Shortcut)
	}
}

// reboundKeys is a key source whose palette opener is one chosen key.
type reboundKeys struct {
	defaultKeys
	open string
	two  bool // the opener is a two-key sequence
}

func (r reboundKeys) Resolve(ctx contracts.KeyContext, keys []string) contracts.KeyResolution {
	switch {
	case r.two && len(keys) == 1 && keys[0] == r.open:
		return contracts.KeyResolution{Outcome: contracts.KeyPrefix}
	case r.two && len(keys) == 2 && keys[0] == r.open && keys[1] == "p":
		return contracts.KeyResolution{Outcome: contracts.KeyMatch, Binding: &contracts.KeyEntry{Keys: keys, Action: "husk.palette.open"}}
	case !r.two && len(keys) == 1 && keys[0] == r.open && ctx.FocusSurface == "":
		return contracts.KeyResolution{Outcome: contracts.KeyMatch, Binding: &contracts.KeyEntry{Keys: keys, Action: "husk.palette.open"}}
	}
	return contracts.KeyResolution{}
}

func TestPaletteClosesOnItsOpenersAndEsc(t *testing.T) {
	for _, tc := range []struct {
		name  string
		keys  keySource
		press string
		want  bool // closes
	}{
		{"esc", defaultKeys{}, "esc", true},
		{"ctrl+c closes rather than quits", defaultKeys{}, "ctrl+c", true},
		{"ctrl+p", defaultKeys{}, "ctrl+p", true},
		{"alt+p", defaultKeys{}, "alt+p", true},
		{": is typed", defaultKeys{}, ":", false},
		{"a is typed", defaultKeys{}, "a", false},
		{"an unrelated modified key", defaultKeys{}, "ctrl+g", false},
		{"a surface-scoped opener", defaultKeys{Extra: []defaultBinding{{"ctrl+o", "husk.palette.open", "main-stage"}}}, "ctrl+o", false},
		{"a two-key opener", reboundKeys{open: "ctrl+k", two: true}, "ctrl+k", false},
		{"a reboundKeys modified opener closes", reboundKeys{open: "alt+x"}, "alt+x", true},
		{"the old opener stops closing after a rebind", reboundKeys{open: "alt+x"}, "ctrl+p", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := goldenModel()
			m.keys = tc.keys
			m.setFocus(protocol.SurfaceMainStage) // a scoped opener matches under this focus
			m.openPalette()
			m.paletteQuery = ""
			m.Update(keyMsg(tc.press))
			if closed := !m.showPalette; closed != tc.want {
				t.Fatalf("palette closed = %v after %q, want %v", closed, tc.press, tc.want)
			}
			if !tc.want && utf8.RuneCountInString(tc.press) == 1 && m.paletteQuery != tc.press {
				t.Errorf("query = %q, want the typed %q", m.paletteQuery, tc.press)
			}
		})
	}
}

// keyMsg builds the key press whose wire spelling is spelling.
func keyMsg(spelling string) tea.KeyPressMsg {
	switch spelling {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	if after, ok := strings.CutPrefix(spelling, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: rune(after[0]), Mod: tea.ModCtrl}
	}
	if after, ok := strings.CutPrefix(spelling, "alt+"); ok {
		return tea.KeyPressMsg{Code: rune(after[0]), Mod: tea.ModAlt}
	}
	r := []rune(spelling)
	return tea.KeyPressMsg{Code: r[0], Text: spelling}
}
