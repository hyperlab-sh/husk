package guide

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Timers here are failure watchdogs only, never behavior synchronization.
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("waiting for plugin message")
	}
	var zero T
	return zero
}

type lockedLog struct {
	sync.Mutex
	text strings.Builder
}

func (l *lockedLog) Write(b []byte) (int, error) { l.Lock(); defer l.Unlock(); return l.text.Write(b) }
func (l *lockedLog) String() string              { l.Lock(); defer l.Unlock(); return l.text.String() }

type peer struct {
	net.Conn
	messages chan protocol.Message
}
type harness struct {
	t    *testing.T
	in   *io.PipeWriter
	out  chan protocol.Message
	dial chan io.ReadWriteCloser
	done chan error
	peer *peer
	logs *lockedLog
}

func start(t *testing.T, l layout, failDials ...int) *harness {
	t.Helper()
	remaining := 0
	if len(failDials) > 0 {
		remaining = failDials[0]
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, in: inW, out: make(chan protocol.Message, 128), dial: make(chan io.ReadWriteCloser), done: make(chan error, 1), logs: &lockedLog{}}
	go func() { _ = protocol.ReadLoop(outR, func(m protocol.Message) { h.out <- m }, nil) }()
	cfg := config{token: "test-token", dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
		if remaining > 0 {
			remaining--
			return nil, fmt.Errorf("injected dial failure")
		}
		select {
		case conn := <-h.dial:
			return conn, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, retry: func(ctx context.Context) error { return ctx.Err() }}
	go func() { err := run(ctx, cfg, inR, outW, h.logs); _ = outW.Close(); h.done <- err }()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		if h.peer != nil {
			_ = h.peer.Close()
		}
		_ = outR.Close()
		receive(t, h.done)
	})
	h.connect(l)
	h.host(protocol.Message{Method: protocol.MethodThemeChanged, Params: raw(protocol.ThemeChangedParams{Palette: testPalette()})})
	return h
}
func (h *harness) connect(l layout) {
	plug, host := net.Pipe()
	p := &peer{host, make(chan protocol.Message, 128)}
	h.peer = p
	go func() { _ = protocol.ReadLoop(host, func(m protocol.Message) { p.messages <- m }, nil) }()
	select {
	case h.dial <- plug:
	case <-time.After(5 * time.Second):
		h.t.Fatal("plugin did not dial")
	}
	watch := h.expect(protocol.MethodStateWatch)
	var params struct {
		Slices []string `json:"slices"`
	}
	if err := json.Unmarshal(watch.Params, &params); err != nil || strings.Join(params.Slices, ",") != "husk.geometry,husk.surfaces,husk.focus,husk.pods" {
		h.t.Fatalf("watch params %s", watch.Params)
	}
	h.respond(watch, map[string]any{"seq": 1, "slices": []sliceValue{{"husk.geometry", raw(l.Geometry)}, {"husk.surfaces", raw(l.Surfaces)}, {"husk.focus", raw(l.Focus)}, {"husk.pods", raw(map[string]any{"plugins": map[string]any{}})}}}, nil)
}
func (h *harness) host(m protocol.Message) {
	h.t.Helper()
	if err := protocol.NewWriter(h.in).Send(m); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) socket(messages ...protocol.Message) {
	h.t.Helper()
	var batch strings.Builder
	for _, m := range messages {
		batch.Write(raw(m))
		batch.WriteByte('\n')
	}
	if _, err := io.WriteString(h.peer, batch.String()); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) expect(method string) protocol.Message {
	h.t.Helper()
	m := receive(h.t, h.peer.messages)
	if m.Method != method || m.Token != "test-token" {
		h.t.Fatalf("expected %s with token, got %+v", method, m)
	}
	return m
}
func (h *harness) respond(req protocol.Message, result any, e *protocol.Error) {
	m := protocol.Message{ID: req.ID, Error: e}
	if e == nil {
		m.Result = raw(result)
	}
	h.socket(m)
}
func (h *harness) toggle(id int) {
	n, _ := protocol.NewIntID(int64(id))
	h.host(protocol.Message{ID: &n, Method: protocol.MethodActionInvoke, Params: raw(map[string]any{"action": toggleAction, "args": map[string]any{"ignored": true}})})
}
func (h *harness) state(shown bool) {
	h.t.Helper()
	m := h.expect(protocol.MethodStateSet)
	expected := fmt.Sprintf(`{"slice":"guide.state","value":{"shown":%t}}`, shown)
	if string(m.Params) != expected {
		h.t.Fatalf("state: %s, want %s", m.Params, expected)
	}
	h.respond(m, map[string]any{"revision": 1}, nil)
}
func (h *harness) frame() []string {
	h.t.Helper()
	m := h.expect(protocol.MethodSurfaceUpdate)
	var p protocol.SurfaceUpdateParams
	if err := json.Unmarshal(m.Params, &p); err != nil || p.Surface != protocol.SurfaceAppOverlay || p.Capture != nil {
		h.t.Fatalf("update: %s", m.Params)
	}
	return p.Lines
}
func (h *harness) on(k keyListing) []string {
	h.t.Helper()
	h.toggle(1)
	keys := h.expect(protocol.MethodKeysList)
	h.respond(keys, k, nil)
	lines := h.frame()
	h.state(true)
	m := receive(h.t, h.out)
	if m.Error != nil || string(m.Result) != `{"shown":true}` {
		h.t.Fatalf("toggle reply %+v", m)
	}
	return lines
}
func changed(slice string, value any) protocol.Message {
	return protocol.Message{Method: protocol.MethodStateChanged, Params: raw(sliceValue{slice, raw(value)})}
}
func testPalette() protocol.Palette {
	return protocol.Palette{Accent: "#112233", BorderFocused: "#223344", Ink: "#334455", InkMuted: "#445566", SurfaceElevated: "#556677"}
}
func fixture() layout {
	l := layout{}
	l.Geometry.Terminal.Width, l.Geometry.Terminal.Height = 144, 40
	l.Geometry.Rects = map[protocol.SurfaceID]rect{
		protocol.SurfaceActivityRail: {0, 1, 7, 38}, protocol.SurfaceSidebarNavigation: {7, 1, 32, 4},
		protocol.SurfaceMainHeader: {39, 1, 64, 4}, protocol.SurfaceSidePanel: {104, 1, 40, 38},
		protocol.SurfaceSidebarList: {7, 5, 32, 30}, protocol.SurfaceMainStage: {39, 5, 64, 34},
		protocol.SurfaceSidebarFooter: {7, 35, 32, 4}, protocol.SurfaceAppOverlay: {0, 0, 144, 40},
	}
	l.Focus.Surface = protocol.SurfaceMainStage
	_ = json.Unmarshal(raw(map[string]any{"occupants": map[string]any{"app-overlay": map[string]string{"plugin": "guide"}, "main-stage": map[string]string{"plugin": "example.editor"}}}), &l.Surfaces)
	return l
}
func binding(action, key string) keyEntry {
	return keyEntry{Action: action, Keys: []string{key}, State: "effective", Source: protocol.Source{Layer: "husk"}}
}
func fixtureKeys() keyListing {
	return keyListing{Entries: []keyEntry{binding("husk.home", "f1"), binding("husk.sidebar.toggle", "["), binding("husk.sidepanel.toggle", "]"), binding("husk.focus.next", "tab"), binding(toggleAction, "f2")}}
}

// Decode the small SGR vocabulary emitted by the plugin independently from the
// renderer; retain styling on spaces to test overlay transparency, not just text.
func decode(t *testing.T, lines []string) [][]cell {
	t.Helper()
	out := make([][]cell, len(lines))
	for y, line := range lines {
		fg, bg := "", ""
		for len(line) > 0 {
			if strings.HasPrefix(line, "\x1b[") {
				end := strings.IndexByte(line, 'm')
				if end < 0 {
					t.Fatal("unterminated SGR")
				}
				sgr := line[2:end]
				switch {
				case sgr == "0":
					fg, bg = "", ""
				case strings.HasPrefix(sgr, "38;2;"):
					fg = sgr
				case strings.HasPrefix(sgr, "48;2;"):
					bg = sgr
				default:
					t.Fatalf("unexpected SGR %s", sgr)
				}
				line = line[end+1:]
				continue
			}
			runes := []rune(line)
			r := runes[0]
			line = string(runes[1:])
			out[y] = append(out[y], cell{text: string(r), fg: fg, bg: bg, width: 1})
		}
	}
	return out
}
func plain(lines []string) string {
	var out strings.Builder
	for _, line := range lines {
		for len(line) > 0 {
			if strings.HasPrefix(line, "\x1b[") {
				line = line[strings.IndexByte(line, 'm')+1:]
				continue
			}
			out.WriteByte(line[0])
			line = line[1:]
		}
		out.WriteByte('\n')
	}
	return out.String()
}
