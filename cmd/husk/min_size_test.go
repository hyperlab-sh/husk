package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
	for i, l := range strings.Split(out, "\n") {
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

func TestTooSmallIgnoresMouse(t *testing.T) {
	m := sizedModel(t, 40, 10)
	if _, cmd := m.Update(tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft}); cmd != nil {
		t.Fatal("mouse click produced a command while too small")
	}
}
