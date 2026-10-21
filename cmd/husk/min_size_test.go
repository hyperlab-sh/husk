package main

import (
	"bytes"
	"maps"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

func sizedModel(t *testing.T, w, h int) *WorkbenchModel {
	t.Helper()
	m := newWorkbenchModel("", nil, t.TempDir())
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func TestTooSmallFrameFitsTerminalExactly(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
		want string
	}{
		{"40x10", 40, 10, "too small (need 84x24, have 40x10)"},
		{"200x5 extreme wide-short", 200, 5, "terminal too small (need 84x24, have 200x5)"},
		{"83x24 one column short", 83, 24, "terminal too small (need 84x24, have 83x24)"},
		{"84x23 one row short", 84, 23, "terminal too small (need 84x24, have 84x23)"},
		{"10x100 extreme tall-narrow", 10, 100, "10x100"},
		{"1x1", 1, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sizedModel(t, tc.w, tc.h)
			if !m.tooSmall() {
				t.Fatal("expected tooSmall")
			}
			out := ansi.Strip(m.View().Content)
			lines := strings.Split(out, "\n")
			if len(lines) != tc.h {
				t.Fatalf("got %d lines, want %d", len(lines), tc.h)
			}
			for i, l := range lines {
				if got := ansi.StringWidth(l); got != tc.w {
					t.Fatalf("line %d width %d, want %d: %q", i, got, tc.w, l)
				}
			}
			if tc.want != "" && !strings.Contains(out, tc.want) {
				t.Fatalf("missing %q in\n%s", tc.want, out)
			}
		})
	}
}

func TestMinimumBoundaryRendersNormalFrame(t *testing.T) {
	m := sizedModel(t, 84, 24)
	if m.tooSmall() {
		t.Fatal("84x24 must be supported")
	}
	if m.width != 84 || m.height != 24 {
		t.Fatalf("size clamped/changed: %dx%d", m.width, m.height)
	}
	out := ansi.Strip(m.View().Content)
	if strings.Contains(out, "too small") {
		t.Fatal("boundary size showed too-small screen")
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 24 {
		t.Fatalf("got %d lines, want 24", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 84 {
			t.Fatalf("line %d is %d wide, exceeds 84", i, w)
		}
	}
}

func TestResizeBelowMinimumKeepsLayoutAndRecovers(t *testing.T) {
	m := sizedModel(t, 120, 40)
	rects := len(m.slotRects)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if m.width != 40 || m.height != 10 {
		t.Fatalf("real size not tracked: %dx%d", m.width, m.height)
	}
	if len(m.slotRects) != rects {
		t.Fatal("layout discarded while too small")
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.tooSmall() || strings.Contains(ansi.Strip(m.View().Content), "too small") {
		t.Fatal("did not recover after growing")
	}
}

func dividerPoint(t *testing.T, m *WorkbenchModel) (int, int) {
	t.Helper()
	sb, ok := m.slotRects[huskipc.SlotSidebarList]
	if !ok {
		t.Fatal("no sidebar rect")
	}
	x, y := sb.X+sb.Width-1, 6
	if m.detectDividerAt(x, y) == "" {
		t.Fatalf("no divider at %d,%d", x, y)
	}
	return x, y
}

// borderedSidebarModel is a sized model whose sidebar list is occupied by a
// plugin that declared a border, so its right edge is a real divider.
func borderedSidebarModel(t *testing.T, w, h int) *WorkbenchModel {
	t.Helper()
	m := sizedModel(t, w, h)
	addTestPlugin(m, "p", map[huskipc.SlotID]frame.Spec{huskipc.SlotSidebarList: {Border: true}})
	m.registry.Occupants[huskipc.SlotSidebarList] = "p"
	m.recomputeLayout()
	return m
}

func TestTooSmallIgnoresMouse(t *testing.T) {
	// Control: at a valid size a press on the divider starts a drag.
	ok := borderedSidebarModel(t, 120, 40)
	x, y := dividerPoint(t, ok)
	ok.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if ok.activeDrag == "" {
		t.Fatal("control: divider press did not start a drag")
	}

	m := borderedSidebarModel(t, 120, 40)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.activeDrag != "" {
		t.Fatalf("mouse acted while too small: activeDrag=%q", m.activeDrag)
	}
}

func TestTooSmallFreezesLayoutAndSlotResize(t *testing.T) {
	m := sizedModel(t, 120, 40)
	_, buf := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "a", Slots: []huskipc.SlotID{huskipc.SlotSidebarList}})
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	rects := maps.Clone(m.slotRects)
	sidebarW := m.sidebarWidth
	buf.Reset()

	for _, k := range []string{"=", "<", ">", "tab", "]", "ctrl+p", "?"} {
		m.handleKey(keyFor(k))
	}
	m.Update(pluginRegisteredMsg{plugin: &ConnectedPlugin{
		Info:   huskipc.HelloPayload{PluginID: "b", Slots: []huskipc.SlotID{huskipc.SlotMainStage}},
		Writer: huskipc.NewConnWriter(&bytes.Buffer{}),
	}})

	for _, e := range envelopes(t, buf) {
		if e.Type == "slot_resize" || e.Type == "key_state" {
			t.Fatalf("plugin got %q while too small", e.Type)
		}
	}
	if !maps.Equal(rects, m.slotRects) {
		t.Fatal("slotRects changed while too small")
	}
	if m.sidebarWidth != sidebarW || m.showPalette {
		t.Fatal("key input changed UI state while too small")
	}

	// Regrow: layout recomputes and slot_resize is broadcast again.
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	got := false
	for _, e := range envelopes(t, buf) {
		got = got || e.Type == "slot_resize"
	}
	if !got {
		t.Fatal("no slot_resize after regrow")
	}
}

func TestTooSmallStillQuits(t *testing.T) {
	m := sizedModel(t, 40, 10)
	if _, cmd := m.handleKey(keyFor("ctrl+c")); cmd == nil {
		t.Fatal("ctrl+c must quit while too small")
	}
}

func keyFor(k string) tea.KeyPressMsg {
	switch k {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+p":
		return tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}
