package terminals

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"go.uber.org/goleak"
)

func newTestTerminal(t *testing.T, w, h, history int) (*Terminal, <-chan []byte) {
	t.Helper()
	input := make(chan []byte, 128)
	term := New(w, h, history, func(b []byte) { input <- b })
	t.Cleanup(term.Close)
	return term, input
}

func grid(term *Terminal) string {
	var rows []string
	for y := 0; y < term.em.Height(); y++ {
		var row strings.Builder
		for x := 0; x < term.em.Width(); {
			cell := term.em.CellAt(x, y)
			if cell == nil {
				row.WriteByte(' ')
				x++
				continue
			}
			row.WriteString(cell.Content)
			x += max(1, cell.Width)
		}
		rows = append(rows, row.String())
	}
	return strings.Join(rows, "\n")
}

func TestScriptedFrames(t *testing.T) {
	for _, tc := range []struct{ name, stream, want string }{
		{"plain", "hello", "hello   \n        \n        "},
		{"wide-combining", "界e\u0301", "界e\u0301     \n        \n        "},
		{"alternate", "base\x1b[?1049hother\x1b[?1049l", "base    \n        \n        "},
		{"scroll-region", "one\r\ntwo\r\nthree\x1b[2;3r\x1b[3;1H\nlast", "one     \nthree   \nlast    "},
		{"cursor-address", "abc\x1b[2;3HX", "abc     \n  X     \n        "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term, _ := newTestTerminal(t, 8, 3, 3)
			term.Write([]byte(tc.stream))
			if got := grid(term); got != tc.want {
				t.Fatalf("grid = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMetadataColorsCursorResizeAndHistory(t *testing.T) {
	term, _ := newTestTerminal(t, 8, 3, 2)
	term.Write([]byte("\x1b]2;Example\a\x1b[31mred\x1b[?25l\x1b[?1003h"))
	if m := term.Metadata(); m.Title != "Example" || !m.Mouse || m.AltScreen {
		t.Fatalf("metadata %+v", m)
	}
	if term.em.CellAt(0, 0).Style.Fg == nil || term.Cursor().Visible {
		t.Fatal("SGR or hidden cursor lost")
	}
	term.Write([]byte("\x1b[?25h\r\n1\r\n2\r\n3\r\n4"))
	if term.Metadata().Scrollback != 2 || !term.Cursor().Visible {
		t.Fatal("history cap or cursor visibility lost")
	}
	term.Resize(10, 4)
	if term.em.Width() != 10 || term.em.Height() != 4 {
		t.Fatal("resize lost")
	}
	term.Mouse(uv.MouseWheelEvent{Button: uv.MouseWheelUp}) // reporting is still on
	term.Write([]byte("\x1b[?1003l"))
	term.Mouse(uv.MouseWheelEvent{Button: uv.MouseWheelUp})
	if term.offset == 0 || term.Cursor().Visible {
		t.Fatal("wheel did not enter history")
	}
	term.Write([]byte("x"))
	if term.offset != 0 {
		t.Fatal("output did not return to live")
	}
	term.Write([]byte("\x1b[?1049h"))
	if !term.Metadata().AltScreen {
		t.Fatal("alternate screen missing")
	}
}

func TestInputEncodingAndQueryReplies(t *testing.T) {
	for _, tc := range []struct {
		name, mode, want string
		send             func(*Terminal)
	}{
		{"up", "", "\x1b[A", func(v *Terminal) { v.Key(uv.KeyPressEvent{Code: uv.KeyUp}) }},
		{"application-up", "\x1b[?1h", "\x1bOA", func(v *Terminal) { v.Key(uv.KeyPressEvent{Code: uv.KeyUp}) }},
		{"interrupt", "", "\x03", func(v *Terminal) { v.Key(uv.KeyPressEvent{Code: 'c', Text: "c", Mod: uv.ModCtrl}) }},
		{"paste", "", "hello", func(v *Terminal) { v.Paste("hello") }},
		{"bracketed-paste", "\x1b[?2004h", "\x1b[200~hello\x1b[201~", func(v *Terminal) { v.Paste("hello") }},
		{"sgr-mouse", "\x1b[?1000h\x1b[?1006h", "\x1b[<0;3;2M", func(v *Terminal) { v.Mouse(uv.MouseClickEvent{X: 2, Y: 1, Button: uv.MouseLeft}) }},
		{"query", "", "\x1b[1;1R", func(v *Terminal) { v.Write([]byte("\x1b[6n")) }},
		{"device-attributes", "", "\x1b[?62;1;6;22c", func(v *Terminal) { v.Write([]byte("\x1b[c")) }},
		{"focus", "\x1b[?1004h", "\x1b[I", func(v *Terminal) { v.Focus(true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term, input := newTestTerminal(t, 8, 3, 2)
			term.Write([]byte(tc.mode))
			tc.send(term)
			var got string
			for len(got) < len(tc.want) {
				got += string(<-input)
			}
			if got != tc.want {
				t.Fatalf("bytes %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCloseJoinsDrain(t *testing.T) {
	// A closed done channel fences the drain's work, but the runtime may still
	// count its returning goroutine. Check actual stacks, as the host tests do.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	for range 32 {
		term := New(8, 3, 2, nil)
		term.Close()
		select {
		case <-term.done:
		default:
			t.Fatal("drain outlived terminal")
		}
		term.Close()
	}
}

func TestMouseReportingModes(t *testing.T) {
	for _, tc := range []struct {
		name, mode, want string
		event            uv.MouseEvent
	}{
		{"normal-click", "1000", "\x1b[<0;3;2M", uv.MouseClickEvent{X: 2, Y: 1, Button: uv.MouseLeft}},
		{"normal-release", "1000", "\x1b[<0;3;2m", uv.MouseReleaseEvent{X: 2, Y: 1, Button: uv.MouseLeft}},
		{"normal-no-motion", "1000", "", uv.MouseMotionEvent{X: 2, Y: 1, Button: uv.MouseLeft}},
		{"button-motion", "1002", "\x1b[<32;3;2M", uv.MouseMotionEvent{X: 2, Y: 1, Button: uv.MouseLeft}},
		{"button-no-hover", "1002", "", uv.MouseMotionEvent{X: 2, Y: 1, Button: uv.MouseNone}},
		{"all-hover", "1003", "\x1b[<35;3;2M", uv.MouseMotionEvent{X: 2, Y: 1, Button: uv.MouseNone}},
		{"wheel", "1000", "\x1b[<64;3;2M", uv.MouseWheelEvent{X: 2, Y: 1, Button: uv.MouseWheelUp}},
		{"modifiers", "1000", "\x1b[<28;3;2M", uv.MouseClickEvent{X: 2, Y: 1, Button: uv.MouseLeft, Mod: uv.ModCtrl | uv.ModAlt | uv.ModShift}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term, input := newTestTerminal(t, 8, 3, 2)
			term.Write([]byte("\x1b[?" + tc.mode + "h\x1b[?1006h"))
			term.Mouse(tc.event)
			// A following key fences the asynchronous drain, including ignored motion.
			term.Key(uv.KeyPressEvent{Code: '!', Text: "!"})
			var got string
			for !strings.HasSuffix(got, "!") {
				got += string(<-input)
			}
			if got != tc.want+"!" {
				t.Fatalf("encoded bytes %q, want %q", got, tc.want+"!")
			}
		})
	}
}

func BenchmarkTerminalRedraw(b *testing.B) {
	term := New(200, 60, 2000, nil)
	defer term.Close()
	term.Write([]byte(strings.Repeat(strings.Repeat("x", 200)+"\r\n", 60)))
	screen := uv.NewScreenBuffer(200, 60)
	b.ResetTimer()
	for b.Loop() {
		term.Draw(screen, uv.Rect(0, 0, 200, 60))
	}
}
