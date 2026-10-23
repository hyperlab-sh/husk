package surfaces

import (
	"encoding/base64"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"testing"
)

func TestTerminalAttachCaptureFocusAndIdle(t *testing.T) {
	st, e, pods := newEngine(t, Options{})
	t.Cleanup(e.CloseTerminals)
	const id = protocol.SurfaceMainStage
	pods.launch("p")
	pods.claim("p", string(id))
	settle(st, e)
	if n := testing.AllocsPerRun(10, e.flushTerminals); n != 0 {
		t.Fatalf("empty terminal flush allocated %g", n)
	}
	e.Resized([]protocol.SurfaceSize{{Surface: id, Width: 20, Height: 5, Focused: true, Visible: true}})
	for _, tc := range []struct {
		capture *protocol.CaptureValue
		mode    protocol.CaptureMode
	}{{nil, protocol.CaptureAll}, {protocol.CaptureBool(true), protocol.CaptureText}, {protocol.CaptureBool(false), protocol.CaptureNone}, {protocol.CaptureValueText.Ptr(), protocol.CaptureText}, {protocol.CaptureValueAll.Ptr(), protocol.CaptureAll}} {
		input := make(chan []byte, 2)
		if _, err := e.TerminalAttach("p", protocol.TerminalAttachParams{Surface: id, Capture: tc.capture}, protocol.TerminalAttachResult{Width: 20, Height: 5}, func(b []byte) { input <- b }); err != nil {
			t.Fatal(err)
		}
		if resolution(t, st).Capture[id] != tc.mode {
			t.Fatalf("capture not normalized to %q", tc.mode)
		}
		if err := e.TerminalWrite("p", protocol.TerminalWriteParams{Surface: id, Data: base64.StdEncoding.EncodeToString([]byte("\x1b[?1004h"))}); err != nil {
			t.Fatal(err)
		}
		if got := string(<-input); got != "\x1b[I" {
			t.Fatalf("initial focus %q", got)
		}
		settle(st, e)
		if n := testing.AllocsPerRun(10, e.flushTerminals); n != 0 {
			t.Fatalf("clean terminal flush allocated %g", n)
		}
		if err := e.TerminalDetach("p", protocol.TerminalDetachParams{Surface: id}); err != nil {
			t.Fatal(err)
		}
	}
}
