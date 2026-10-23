package services

import (
	"fmt"
	"testing"

	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

var captureCtx = keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "a", Capture: protocol.CaptureText}

// While Capture is set, a global binding on a text key is removed from every
// layer; modified keys, function keys, esc and surface-scoped entries stay.
func TestCaptureRemovesGlobalTextKeyCandidates(t *testing.T) {
	kb := rankKeys(t, map[store.Layer]string{
		store.LayerDistribution: bind(`{"key":"d","action":"d.letter"}`, `{"key":"f2","action":"d.f2"}`, `{"key":"enter","action":"d.enter"}`),
		store.LayerUser:         bind(`{"key":"u","action":"u.letter"}`, `{"key":"alt+u","action":"u.alt"}`, `{"key":"left","action":"u.left"}`),
		store.LayerProfile:      bind(`{"key":"p","action":"p.letter"}`, `{"key":" ","action":"p.space"}`, `{"key":"up","action":"p.up"}`, `{"key":"s","focus":"stage","action":"p.surface"}`),
	}, bind(`{"key":"l","focus":"stage","action":"a.local"}`))
	for _, tc := range []struct {
		key  string
		want string // with Capture
		free string // without Capture
	}{
		{"q", "<none>", "husk.quit"},
		{"d", "<none>", "d.letter"},
		{"u", "<none>", "u.letter"},
		{"p", "<none>", "p.letter"},
		{" ", "<none>", "p.space"},
		{"enter", "<none>", "d.enter"},
		{"tab", "<none>", "husk.focus.next"},
		{"up", "<none>", "p.up"},
		{"left", "<none>", "u.left"},
		{"?", "<none>", "husk.modifiers.toggle_alt_latch"},
		{"ctrl+c", "husk.quit", "husk.quit"},
		{"alt+1", "husk.home", "husk.home"},
		{"alt+u", "u.alt", "u.alt"},
		{"shift+tab", "husk.focus.next", "husk.focus.next"},
		{"f1", "husk.home", "husk.home"},
		{"f2", "d.f2", "d.f2"},
		{"esc", "husk.home", "husk.home"},
		{"s", "p.surface", "p.surface"},
		{"l", "a.local", "a.local"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			if got := resolved(kb, captureCtx, tc.key); got != tc.want {
				t.Errorf("capturing Resolve(%q) = %s, want %s", tc.key, got, tc.want)
			}
			free := captureCtx
			free.Capture = protocol.CaptureNone
			if got := resolved(kb, free, tc.key); got != tc.free {
				t.Errorf("Resolve(%q) = %s, want %s", tc.key, got, tc.free)
			}
		})
	}
}

func TestCaptureHidesGlobalTextKeySequences(t *testing.T) {
	kb := rankKeys(t, map[store.Layer]string{store.LayerUser: bind(
		`{"keys":["g","h"],"action":"u.gh"}`, `{"keys":["ctrl+k","ctrl+s"],"action":"u.save"}`, `{"keys":["ctrl+k","x"],"action":"u.kx"}`)}, "")
	free := captureCtx
	free.Capture = protocol.CaptureNone
	if got := resolved(kb, free, "g"); got != "<prefix>" {
		t.Fatalf("without capture g = %s, want prefix", got)
	}
	if got := resolved(kb, captureCtx, "g"); got != "<none>" {
		t.Errorf("g while capturing = %s, want none (no prefix)", got)
	}
	// The first key decides: a modified first key still starts a sequence,
	// whatever its continuation is.
	if got := resolved(kb, captureCtx, "ctrl+k"); got != "<prefix>" {
		t.Errorf("ctrl+k while capturing = %s, want prefix", got)
	}
	if got := resolved(kb, captureCtx, "ctrl+k", "x"); got != "u.kx" {
		t.Errorf("ctrl+k x while capturing = %s, want u.kx", got)
	}
}

func TestCaptureModalExemptionNeedsAnOpenModal(t *testing.T) {
	kb := rankKeys(t, map[store.Layer]string{store.LayerUser: bind(
		`{"key":"n","action":"d.open"}`, `{"key":"c","action":"husk.modal.close"}`, `{"key":"x","action":"u.other"}`)}, "")
	modal := keybinding.KeyContext{FocusSurface: "modal-surface", ModalOpen: true, ModalAction: "d.open", Capture: protocol.CaptureText}
	for _, tc := range []struct{ key, want string }{
		{"n", "d.open"}, {"c", "husk.modal.close"}, {"x", "<none>"}, {"q", "<none>"}, {"esc", "husk.modal.close"},
	} {
		if got := resolved(kb, modal, tc.key); got != tc.want {
			t.Errorf("modal+capture Resolve(%q) = %s, want %s", tc.key, got, tc.want)
		}
	}
	// With no modal open there is no exemption and ModalAction is not compared.
	noModal := keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "a", Capture: protocol.CaptureText, ModalAction: "d.open"}
	for _, key := range []string{"n", "c"} {
		if got := resolved(kb, noModal, key); got != "<none>" {
			t.Errorf("no modal, Resolve(%q) = %s, want none", key, got)
		}
	}
}

func TestCaptureKeepsTombstones(t *testing.T) {
	kb := rankKeys(t, map[store.Layer]string{
		store.LayerDistribution: bind(`{"key":"n","action":"d.open"}`),
		store.LayerUser:         bind(`{"key":"n","disable":true}`),
	}, "")
	modal := keybinding.KeyContext{FocusSurface: "modal-surface", ModalOpen: true, ModalAction: "d.open", Capture: protocol.CaptureText}
	// A filtered-out tombstone would let the exempt d.open binding through.
	if got := resolved(kb, modal, "n"); got != "<none>" {
		t.Errorf("tombstone on the modal's own key = %s, want none", got)
	}
	// A user tombstone on n over a distribution n -> husk.modal.close: the
	// close binding is exempt from the filter, so a tombstone the filter
	// dropped would let it match. It survives and vetoes the key.
	tomb := rankKeys(t, map[store.Layer]string{
		store.LayerDistribution: bind(`{"key":"n","action":"husk.modal.close"}`),
		store.LayerUser:         bind(`{"key":"n","disable":true}`),
	}, "")
	if got := resolved(tomb, modal, "n"); got != "<none>" {
		t.Errorf("tombstone over the exempt close binding = %s, want none", got)
	}
}

func TestCaptureHintsInheritTheFilter(t *testing.T) {
	kb := rankKeys(t, map[store.Layer]string{store.LayerUser: bind(
		`{"key":"g","action":"u.g","hint":"G"}`, `{"key":"ctrl+g","action":"u.cg","hint":"CG"}`,
		`{"key":"s","focus":"stage","action":"u.s","hint":"S"}`)}, "")
	texts := func(ctx keybinding.KeyContext) string {
		var out []string
		for _, h := range kb.Hints(ctx) {
			out = append(out, h.Text)
		}
		return fmt.Sprint(out)
	}
	free := captureCtx
	free.Capture = protocol.CaptureNone
	if got, want := texts(free), "[S G CG]"; got != want {
		t.Errorf("Hints without capture = %s, want %s", got, want)
	}
	if got, want := texts(captureCtx), "[S CG]"; got != want {
		t.Errorf("Hints while capturing = %s, want %s", got, want)
	}
}
