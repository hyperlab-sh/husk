package terminals

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

func TestTerminalLinks(t *testing.T) {
	for _, tc := range []struct {
		name, text, url string
		x, y            int
		mod             uv.KeyMod
		button          uv.MouseButton
		release         bool
	}{
		{name: "OSC8 wins", text: "\x1b]8;id=one;custom://target\x1b\\https://visible\x1b]8;;\x1b\\", url: "custom://target", x: 4, mod: uv.ModCtrl, button: uv.MouseLeft},
		{name: "detected", text: "see https://example.test/a", url: "https://example.test/a", x: 8, mod: uv.ModCtrl, button: uv.MouseLeft},
		{name: "punctuation", text: `(https://example.test/a.,;:!?'"`, url: "https://example.test/a", x: 5, mod: uv.ModCtrl, button: uv.MouseLeft},
		{name: "unbalanced brackets", text: "(https://example.test/a(b))]}>!", url: "https://example.test/a(b)", x: 5, mod: uv.ModCtrl, button: uv.MouseLeft},
		{name: "wide cell prefix", text: "界 https://example.test", url: "https://example.test", x: 5, mod: uv.ModCtrl, button: uv.MouseLeft},
		{name: "OSC wide continuation", text: "\x1b]8;;https://target\x1b\\界\x1b]8;;\x1b\\", url: "https://target", x: 1, mod: uv.ModCtrl, button: uv.MouseLeft},
		{name: "plain text", text: "plain text", x: 2, mod: uv.ModCtrl, button: uv.MouseLeft},
		{name: "no ctrl", text: "https://example.test", x: 2, button: uv.MouseLeft},
		{name: "right button", text: "https://example.test", x: 2, mod: uv.ModCtrl, button: uv.MouseRight},
		{name: "release only", text: "https://example.test", x: 2, mod: uv.ModCtrl, button: uv.MouseLeft, release: true},
		{name: "prefix outside URL", text: "(https://example.test)", x: 0, mod: uv.ModCtrl, button: uv.MouseLeft},
		{name: "trimmed suffix", text: "https://example.test!", x: 20, mod: uv.ModCtrl, button: uv.MouseLeft},
		{name: "no soft wrap join", text: strings.Repeat(" ", 60) + "https://example.test/longpath", x: 2, y: 1, mod: uv.ModCtrl, button: uv.MouseLeft},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := make(chan []byte, 16)
			term := New(80, 3, 10, func(b []byte) { input <- b })
			defer term.Close()
			term.Write([]byte("\x1b[?1000h\x1b[?1006h" + tc.text))
			var urls []string
			term.SetLinkHandler(func(url string, row, col int) {
				urls = append(urls, url)
				if row != tc.y || col != tc.x {
					t.Errorf("coordinates = %d,%d", row, col)
				}
			})
			m := uv.Mouse{X: tc.x, Y: tc.y, Button: tc.button, Mod: tc.mod}
			if !tc.release {
				term.Mouse(uv.MouseClickEvent(m))
			}
			term.Mouse(uv.MouseReleaseEvent(m))
			term.Close() // joins the input drain: absence assertions need no timer
			var bytes strings.Builder
			for len(input) > 0 {
				bytes.Write(<-input)
			}
			if tc.url != "" {
				if len(urls) != 1 || urls[0] != tc.url {
					t.Fatalf("links = %v, want %q", urls, tc.url)
				}
				if bytes.Len() != 0 {
					t.Fatalf("consumed click encoded %q", bytes.String())
				}
			} else {
				if len(urls) != 0 {
					t.Fatalf("unexpected links %v", urls)
				}
				button := 0
				if tc.mod.Contains(uv.ModCtrl) {
					button += 16
				}
				if tc.button == uv.MouseRight {
					button += 2
				}
				want := fmt.Sprintf("\x1b[<%d;%d;%dm", button, tc.x+1, tc.y+1)
				if !tc.release {
					want = fmt.Sprintf("\x1b[<%d;%d;%dM", button, tc.x+1, tc.y+1) + want
				}
				if bytes.String() != want {
					t.Fatalf("passthrough = %q, want %q", bytes.String(), want)
				}
			}
		})
	}
}
