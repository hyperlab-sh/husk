package terminals

import (
	uv "github.com/charmbracelet/ultraviolet"
	"testing"
)

func TestTerminalLegacyMouseAndLayoutText(t *testing.T) {
	for _, tc := range []struct {
		name, mode, want string
		event            uv.MouseEvent
	}{
		{"x10", "9", "\x1b[M #\"", uv.MouseClickEvent{X: 2, Y: 1, Button: uv.MouseLeft}},
		{"normal", "1000", "\x1b[M #\"", uv.MouseClickEvent{X: 2, Y: 1, Button: uv.MouseLeft}},
		{"normal-release", "1000", "\x1b[M##\"", uv.MouseReleaseEvent{X: 2, Y: 1, Button: uv.MouseLeft}},
		{"x10-no-release", "9", "", uv.MouseReleaseEvent{X: 2, Y: 1, Button: uv.MouseLeft}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term, input := newTestTerminal(t, 8, 3, 2)
			term.Write([]byte("\x1b[?" + tc.mode + "h"))
			term.Mouse(tc.event)
			term.Key(uv.KeyPressEvent{Code: '!', Text: "!"})
			var got string
			for len(got) < len(tc.want)+1 {
				got += string(<-input)
			}
			if got != tc.want+"!" {
				t.Fatalf("legacy bytes %q, want %q", got, tc.want+"!")
			}
		})
	}
	for _, tc := range []struct{ text, want string }{{"@", "@"}, {"q", "\x1b\x11"}} {
		term, input := newTestTerminal(t, 8, 3, 2)
		term.Key(uv.KeyPressEvent{Code: 'q', Text: tc.text, Mod: uv.ModCtrl | uv.ModAlt})
		if got := string(<-input); got != tc.want {
			t.Fatalf("layout %q = %q", tc.text, got)
		}
	}
}
