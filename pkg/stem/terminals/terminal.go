// Package terminals emulates plugin-owned terminal byte streams. It never
// allocates a PTY or starts a process. All methods except the private input
// drain run on the host loop.
package terminals

import (
	"io"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// Metadata is the coalesced, screen-free terminal state.
type Metadata struct {
	Title      string `json:"title"`
	AltScreen  bool   `json:"alt_screen"`
	Mouse      bool   `json:"mouse"`
	Scrollback int    `json:"scrollback"`
}

// Cursor describes the visible cursor, relative to the terminal's content.
type Cursor struct {
	X, Y    int
	Visible bool
	Style   int
	Blink   bool
}

// Terminal owns one emulator and its reply drain. send must be worker-safe and
// nonblocking: it offers bytes to the occupant's existing bounded output queue.
type Terminal struct {
	em      *vt.Emulator
	pipe    *io.PipeWriter
	done    chan struct{}
	title   string
	modes   map[ansi.Mode]bool
	offset  int
	focused bool
	closed  bool
}

// New creates an emulator and starts its input drain before any parser runs.
func New(width, height, scrollback int, send func([]byte)) *Terminal {
	e := vt.NewEmulator(max(1, width), max(1, height))
	e.SetScrollbackSize(scrollback)
	t := &Terminal{em: e, pipe: e.InputPipe().(*io.PipeWriter), done: make(chan struct{}), modes: map[ansi.Mode]bool{}}
	e.SetCallbacks(vt.Callbacks{
		Title: func(s string) { t.title = s },
		EnableMode: func(m ansi.Mode) {
			t.modes[m] = true
			if m == ansi.ModeFocusEvent && t.focused {
				e.Focus()
			}
		},
		DisableMode: func(m ansi.Mode) { delete(t.modes, m) },
	})
	go func() {
		defer close(t.done)
		buf := make([]byte, 65536)
		for {
			n, err := e.Read(buf)
			if n > 0 && send != nil {
				send(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				return
			}
		}
	}()
	return t
}

// Close discards the emulator and synchronously joins its drain. Closing the
// pipe first avoids racing Emulator.Read's closed flag with Emulator.Close.
func (t *Terminal) Close() {
	if t.closed {
		return
	}
	t.closed = true
	_ = t.pipe.Close()
	<-t.done
	_ = t.em.Close()
}

// Write feeds program output and returns scrollback to the live screen.
func (t *Terminal) Write(b []byte) {
	if !t.closed {
		t.offset = 0
		_, _ = t.em.Write(b)
	}
}

// Resize updates the grid before the plugin receives its resize notification.
func (t *Terminal) Resize(w, h int) {
	if !t.closed && w > 0 && h > 0 && (t.em.Width() != w || t.em.Height() != h) {
		t.em.Resize(w, h)
	}
}

// Focus reports focus transitions only when the program requested them.
func (t *Terminal) Focus(on bool) {
	if t.closed || on == t.focused {
		return
	}
	t.focused = on
	if on {
		t.em.Focus()
	} else {
		t.em.Blur()
	}
}

// Metadata returns terminal facts without any screen cells.
func (t *Terminal) Metadata() Metadata {
	return Metadata{Title: t.title, AltScreen: t.em.IsAltScreen(), Mouse: t.mouseMode() != nil, Scrollback: t.em.ScrollbackLen()}
}

func (t *Terminal) mouseMode() ansi.Mode {
	var mode ansi.Mode
	for _, m := range []ansi.DECMode{ansi.ModeMouseX10, ansi.ModeMouseNormal, ansi.ModeMouseHighlight, ansi.ModeMouseButtonEvent, ansi.ModeMouseAnyEvent} {
		if t.modes[m] {
			mode = m
		}
	}
	return mode
}

// Key encodes a legacy key for the application's cursor and keypad modes.
func (t *Terminal) Key(k uv.KeyPressEvent) {
	if t.closed {
		return
	}
	t.offset = 0
	// The legacy encoder matches control keys structurally; associated text
	// and repeat metadata must not turn ctrl+c into a printable c.
	k.IsRepeat = false
	if k.Mod.Contains(uv.ModCtrl) {
		r, n := utf8.DecodeRuneInString(k.Text)
		layout := n == len(k.Text) && n > 0 && unicode.IsPrint(r) && !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
		if k.Mod.Contains(uv.ModAlt) && layout {
			k.Mod &^= uv.ModCtrl | uv.ModAlt
		} else {
			k.Text = ""
		}
	}
	t.em.SendKey(k)
}

// Paste respects the application's bracketed-paste mode.
func (t *Terminal) Paste(text string) {
	if !t.closed {
		t.offset = 0
		t.em.Paste(text)
	}
}

// Mouse encodes only the events enabled by the active reporting mode; without
// reporting a wheel scrolls host history instead.
func (t *Terminal) Mouse(event uv.MouseEvent) {
	if t.closed {
		return
	}
	mode := t.mouseMode()
	if mode == nil {
		if wheel, ok := event.(uv.MouseWheelEvent); ok {
			switch wheel.Button {
			case uv.MouseWheelUp:
				t.offset = min(t.em.ScrollbackLen(), t.offset+3)
			case uv.MouseWheelDown:
				t.offset = max(0, t.offset-3)
			}
		}
		return
	}
	if motion, ok := event.(uv.MouseMotionEvent); ok {
		if mode != ansi.ModeMouseAnyEvent && (mode != ansi.ModeMouseButtonEvent || motion.Button == uv.MouseNone) {
			return
		}
	}
	if _, ok := event.(uv.MouseReleaseEvent); ok && mode == ansi.ModeMouseX10 {
		return
	}
	if release, ok := event.(uv.MouseReleaseEvent); ok && !t.modes[ansi.ModeMouseExtSgr] {
		// Legacy release uses button code 3, not the released button's code.
		release.Button = uv.MouseNone
		event = release
	}
	t.em.SendMouse(event)
}

// Draw paints the grid into area; the caller's screen enforces clipping.
func (t *Terminal) Draw(screen uv.Screen, area uv.Rectangle) {
	if t.closed {
		return
	}
	for y := 0; y < area.Dy(); y++ {
		for x := 0; x < area.Dx(); {
			var cell *uv.Cell
			row := y - t.offset
			if row < 0 {
				cell = t.em.ScrollbackCellAt(x, t.em.ScrollbackLen()+row)
			} else {
				cell = t.em.CellAt(x, row)
			}
			if cell == nil {
				cell = &uv.EmptyCell
			}
			screen.SetCell(area.Min.X+x, area.Min.Y+y, cell)
			x += max(1, cell.Width)
		}
	}
}

// Cursor returns the application's cursor, hidden while viewing history.
func (t *Terminal) Cursor() Cursor {
	p := t.em.CursorPosition()
	style, blink := t.em.CursorStyle()
	return Cursor{X: p.X, Y: p.Y, Visible: !t.closed && t.offset == 0 && !t.em.CursorHidden(), Style: int(style), Blink: blink}
}
