package surfaces

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	"go.uber.org/goleak"
)

func TestTerminalLifecycleAndLimits(t *testing.T) {
	dead := map[string]bool{}
	st, e, pods := newEngine(t, Options{Liveness: liveness{dead: dead}})
	t.Cleanup(e.CloseTerminals)
	const id = protocol.SurfaceMainStage
	pods.launch("p")
	pods.claim("p", string(id))
	settle(st, e)
	size := protocol.TerminalAttachResult{Width: 20, Height: 5}
	attach := protocol.TerminalAttachParams{Surface: id}
	badLimit := 20001
	for _, tc := range []struct {
		name, plugin string
		p            protocol.TerminalAttachParams
		size         protocol.TerminalAttachResult
		code         protocol.Code
	}{
		{"foreign", "other", attach, size, protocol.CodeDenied},
		{"limit", "p", protocol.TerminalAttachParams{Surface: id, Scrollback: &badLimit}, size, protocol.CodeInvalidParams},
		{"geometry", "p", attach, protocol.TerminalAttachResult{}, protocol.CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.TerminalAttach(tc.plugin, tc.p, tc.size, nil)
			if got := codeOf(t, err); got != tc.code {
				t.Fatalf("code %s", got)
			}
		})
	}
	got, err := e.TerminalAttach("p", attach, size, nil)
	if err != nil || got != size {
		t.Fatalf("attach %+v %v", got, err)
	}
	if _, err = e.TerminalAttach("p", attach, size, nil); codeOf(t, err) != protocol.CodeConflict {
		t.Fatal("double attach allowed")
	}
	if err = e.SurfaceUpdate("p", protocol.SurfaceUpdateParams{Surface: id}); codeOf(t, err) != protocol.CodeInvalidParams {
		t.Fatal("line update accepted while attached")
	}
	write := func(plugin, data string) error {
		return e.TerminalWrite(plugin, protocol.TerminalWriteParams{Surface: id, Data: base64.StdEncoding.EncodeToString([]byte(data))})
	}
	if err = write("other", "ignored"); err != nil {
		t.Fatal(err)
	}
	if e.Terminal(id).Cursor().X != 0 {
		t.Fatal("foreign write reached emulator")
	}
	if err = write("p", strings.Repeat("x", protocol.MaxTerminalBytes+1)); codeOf(t, err) != protocol.CodeInvalidParams {
		t.Fatal("oversize write accepted")
	}
	if err = write("p", "\x1b]2;Title\a\x1b[?1000h\x1b[?1049htext"); err != nil {
		t.Fatal(err)
	}
	if resolution(t, st).Terminals[id].Title != "" {
		t.Fatal("metadata escaped tick")
	}
	settle(st, e)
	r := resolution(t, st)
	if r.Capture[id] != protocol.CaptureAll || r.Terminals[id].Title != "Title" || !r.Terminals[id].Mouse || !r.Terminals[id].AltScreen {
		t.Fatalf("terminal state %+v", r)
	}
	if err = e.TerminalDetach("other", protocol.TerminalDetachParams{Surface: id}); codeOf(t, err) != protocol.CodeDenied {
		t.Fatal("foreign detach accepted")
	}
	if err = e.TerminalDetach("p", protocol.TerminalDetachParams{Surface: id}); err != nil {
		t.Fatal(err)
	}
	settle(st, e)
	r = resolution(t, st)
	if e.Terminal(id) != nil || len(r.Terminals) != 0 || len(r.Capture) != 0 {
		t.Fatal("detach retained terminal state")
	}
	if err = e.SurfaceUpdate("p", protocol.SurfaceUpdateParams{Surface: id}); err != nil {
		t.Fatal("line update not restored", err)
	}
	for _, end := range []string{"close", "exit", "shutdown"} {
		t.Run(end, func(t *testing.T) {
			defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
			input := make(chan []byte, 2)
			if end != "close" {
				pods.claim("p", string(id))
				dead["p"] = false
				e.LivenessChanged()
				settle(st, e)
			}
			if _, err := e.TerminalAttach("p", attach, size, func(b []byte) { input <- b }); err != nil {
				t.Fatal(err)
			}
			if err := e.TerminalWrite("p", protocol.TerminalWriteParams{Surface: id, Data: base64.StdEncoding.EncodeToString([]byte("\x1b[6n"))}); err != nil {
				t.Fatal(err)
			}
			if string(<-input) != "\x1b[1;1R" {
				t.Fatal("live drain query lost")
			}
			switch end {
			case "close":
				if err := e.SurfaceClose("p", protocol.SurfaceCloseParams{Surface: id}); err != nil {
					t.Fatal(err)
				}
			case "exit":
				dead["p"] = true
				e.LivenessChanged()
			case "shutdown":
				e.CloseTerminals()
			}
			settle(st, e)
			if e.Terminal(id) != nil || len(resolution(t, st).Terminals) != 0 {
				t.Fatal("terminal survived tenure")
			}
		})
	}
}
