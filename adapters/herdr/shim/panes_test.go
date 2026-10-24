package shim

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/proxy"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type paneProcess struct {
	*testProcess
	reader *io.PipeReader
	output *io.PipeWriter
	input  chan []byte
	sizes  chan [2]int
	once   sync.Once
	write  func([]byte) (int, error)
	closed chan struct{}
}

func (p *paneProcess) Read(b []byte) (int, error) { return p.reader.Read(b) }
func (p *paneProcess) Write(b []byte) (int, error) {
	if p.write != nil {
		return p.write(b)
	}
	p.input <- append([]byte(nil), b...)
	return len(b), nil
}
func (p *paneProcess) Resize(w, h int) error { p.sizes <- [2]int{w, h}; return nil }
func (p *paneProcess) Close() error {
	p.once.Do(func() { close(p.closed); _ = p.reader.Close(); _ = p.output.Close() })
	return nil
}

type paneSpawn struct {
	command Command
	process *paneProcess
	size    [2]int
}

func paneRig(t *testing.T) (*rig, chan paneSpawn) {
	t.Helper()
	r := newRig(t, "never", false, "")
	spawns := make(chan paneSpawn, 16)
	r.o.SpawnTerminal = func(c Command, w, h int) (TerminalProcess, error) {
		rd, wr := io.Pipe()
		p := &paneProcess{testProcess: &testProcess{pid: 100, done: make(chan int, 1), signals: make(chan syscall.Signal, 8), termStops: true}, reader: rd, output: wr, input: make(chan []byte, 4), sizes: make(chan [2]int, 4), closed: make(chan struct{})}
		spawns <- paneSpawn{c, p, [2]int{w, h}}
		return p, nil
	}
	counter := 0
	r.host.call = func(_ context.Context, m string, p any) (json.RawMessage, error) {
		switch m {
		case protocol.MethodSurfaceOpen:
			counter++
			return json.Marshal(protocol.SurfaceOpenResult{Surface: protocol.SurfaceID("demo.pane-" + string(rune('a'+counter)))})
		case protocol.MethodTerminalAttach:
			return json.RawMessage(`{"width":42,"height":12}`), nil
		case protocol.MethodSessionPing:
			return json.RawMessage(`{"host_version":"v-test"}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	return r, spawns
}
func paneInvoke(t *testing.T, r *rig, args any) protocol.Message {
	t.Helper()
	id := protocol.NewStringID("pane")
	raw, _ := json.Marshal(map[string]any{"action": "demo.pane-panel", "args": args})
	if err := protocol.NewWriter(r.in).Send(protocol.Message{ID: &id, Method: protocol.MethodActionInvoke, Params: raw}); err != nil {
		t.Fatal(err)
	}
	var reply protocol.Message
	if err := r.reply.Decode(&reply); err != nil {
		t.Fatal(err)
	}
	return reply
}
func paneNotice(t *testing.T, r *rig, m string, p any) {
	t.Helper()
	raw, _ := json.Marshal(p)
	if err := protocol.NewWriter(r.in).Send(protocol.Message{Method: m, Params: raw}); err != nil {
		t.Fatal(err)
	}
}

func TestPanePlacementsAndOpaqueTraffic(t *testing.T) {
	for _, placement := range []string{"popup", "overlay", "zoomed", "tab", "split"} {
		t.Run(placement, func(t *testing.T) {
			r, spawns := paneRig(t)
			r.start(t)
			r.host.until(t, protocol.MethodStateSet, "attached")
			reply := paneInvoke(t, r, map[string]any{"placement": placement, "width": "60%", "height": 15, "env": map[string]string{"EXTRA": "set"}})
			if reply.Error != nil {
				t.Fatal(reply.Error)
			}
			spawned := take(t, spawns)
			p := spawned.process
			var opened protocol.SurfaceOpenParams
			_ = json.Unmarshal(r.host.until(t, protocol.MethodSurfaceOpen, ""), &opened)
			expected := protocol.SurfacePlacementCanvas
			if placement == "popup" {
				expected = protocol.SurfacePlacementModal
			}
			if opened.Placement != expected || string(opened.Width) != `"60%"` || string(opened.Height) != "15" || opened.Frame == nil || !opened.Frame.Close {
				t.Fatalf("open=%+v", opened)
			}
			var attached protocol.TerminalAttachParams
			_ = json.Unmarshal(r.host.until(t, protocol.MethodTerminalAttach, ""), &attached)
			if attached.Capture == nil || *attached.Capture != protocol.CaptureValueAll || spawned.size != [2]int{42, 12} {
				t.Fatal("capture or inner PTY size lost")
			}
			env := envMap(spawned.command.Env)
			if env["HERDR_PLUGIN_ENTRYPOINT_ID"] != "panel" || env["HERDR_PANE_ID"] != "" || env["TERM"] != "xterm-256color" || env["COLORTERM"] != "truecolor" || env["TERM_PROGRAM"] != "husk" || env["TERM_PROGRAM_VERSION"] != "v-test" || env["HUSK_PLUGIN_TOKEN"] != "" || env["EXTRA"] != "set" || spawned.command.Dir != r.o.Dir {
				t.Fatal("pane environment table differs")
			}
			var context InvocationContext
			_ = json.Unmarshal([]byte(env["HERDR_PLUGIN_CONTEXT_JSON"]), &context)
			if context.FocusedPaneID != "p" {
				t.Fatal("underlying focus lost from context")
			}
			// The host, not the shim, interprets terminal escapes and keyboard modes.
			bytes := "\x1b[31mopaque\x1b[0m"
			if _, err := p.output.Write([]byte(bytes)); err != nil {
				t.Fatal(err)
			}
			var write protocol.TerminalWriteParams
			_ = json.Unmarshal(r.host.until(t, protocol.MethodTerminalWrite, ""), &write)
			decoded, err := write.Decode()
			if err != nil || string(decoded) != bytes {
				t.Fatal("PTY bytes changed")
			}
			paneNotice(t, r, protocol.MethodTerminalInput, protocol.TerminalInputParams{Surface: attached.Surface, Data: base64.StdEncoding.EncodeToString([]byte("\x1b[A"))})
			if string(take(t, p.input)) != "\x1b[A" {
				t.Fatal("encoded input changed")
			}
			paneNotice(t, r, protocol.MethodSurfaceResized, protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: attached.Surface, Width: 61, Height: 17, Visible: false}}})
			if take(t, p.sizes) != [2]int{61, 17} {
				t.Fatal("resize missing")
			}
			select {
			case <-p.signals:
				t.Fatal("hidden canvas was killed")
			default:
			}
			// A duplicate focuses the original, preserving the pane record and process.
			again := paneInvoke(t, r, map[string]any{"placement": placement})
			if again.Error != nil || string(again.Result) != string(reply.Result) {
				t.Fatalf("duplicate changed lifetime: %s %s", reply.Result, again.Result)
			}
			r.host.until(t, protocol.MethodSurfaceFocus, "")
			select {
			case <-spawns:
				t.Fatal("duplicate spawned another process")
			default:
			}
			path := env["HERDR_SOCKET_PATH"]
			if placement != "popup" {
				focused := proxyCall(t, path, `{"id":"f","method":"plugin.pane.focus","params":{"pane_id":"husk:`+string(attached.Surface)+`"}}`+"\n")
				if err := shape(t, "success_response").Validate(focused); err != nil {
					t.Fatal(err)
				}
			}
			method := "plugin.pane.close"
			params := map[string]any{"pane_id": "husk:" + string(attached.Surface)}
			if placement == "popup" {
				method = "popup.close"
				params = map[string]any{}
			}
			raw, _ := json.Marshal(map[string]any{"id": "close", "method": method, "params": params})
			closed := proxyCall(t, path, string(raw)+"\n")
			if err := shape(t, "success_response").Validate(closed); err != nil {
				t.Fatal(err)
			}
			if take(t, p.signals) != syscall.SIGHUP || take(t, p.signals) != syscall.SIGTERM {
				t.Fatal("close did not hang up and terminate group")
			}
		})
	}
}

func TestPaneBusyRefusalStartsNoProcess(t *testing.T) {
	r, spawns := paneRig(t)
	original := r.host.call
	r.host.call = func(ctx context.Context, m string, p any) (json.RawMessage, error) {
		if m == protocol.MethodSurfaceOpen {
			return nil, &protocol.Error{Code: protocol.CodeBusy, Message: "a modal is open", Data: json.RawMessage(`{"reason":"modal_open"}`)}
		}
		return original(ctx, m, p)
	}
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	reply := paneInvoke(t, r, map[string]any{"placement": "popup"})
	if reply.Error == nil || reply.Error.Code != protocol.CodeBusy {
		t.Fatalf("Husk busy=%+v", reply)
	}
	// Obtain the lifetime proxy path through an ordinary action's environment.
	if response := r.invoke(t, "demo.go"); response.Error != nil {
		t.Fatal(response.Error)
	}
	command := take(t, r.spawns)
	result := proxyCall(t, envMap(command.command.Env)["HERDR_SOCKET_PATH"], "{\"id\":\"busy\",\"method\":\"plugin.pane.open\",\"params\":{\"plugin_id\":\"demo\",\"entrypoint\":\"panel\",\"placement\":\"popup\"}}\n")
	if err := shape(t, "error_response").Validate(result); err != nil {
		t.Fatal(err)
	}
	if result["error"].(map[string]any)["code"] != "ui_busy" {
		t.Fatal(result)
	}
	select {
	case <-spawns:
		t.Fatal("refused modal spawned PTY")
	default:
	}
	command.process.done <- 0
}

func TestPaneExitHostCloseAndGrace(t *testing.T) {
	for _, mode := range []string{"exit", "host_close", "write_refused"} {
		t.Run(mode, func(t *testing.T) {
			r, spawns := paneRig(t)
			checking := make(chan context.Context, 1)
			original := r.host.call
			r.host.call = func(ctx context.Context, m string, p any) (json.RawMessage, error) {
				if mode == "host_close" && m == protocol.MethodStateGet {
					checking <- ctx
				}
				if mode == "write_refused" && m == protocol.MethodTerminalWrite {
					return nil, errors.New("surface gone")
				}
				return original(ctx, m, p)
			}
			r.start(t)
			r.host.until(t, protocol.MethodStateSet, "attached")
			if reply := r.invoke(t, "demo.pane-panel"); reply.Error != nil {
				t.Fatal(reply.Error)
			}
			p := take(t, spawns).process
			var attached protocol.TerminalAttachParams
			_ = json.Unmarshal(r.host.until(t, protocol.MethodTerminalAttach, ""), &attached)
			switch mode {
			case "exit":
				_ = p.output.Close()
				p.done <- 0
			case "host_close":
				p.termStops = false
				paneNotice(t, r, protocol.MethodSurfaceResized, protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: attached.Surface, Visible: false}}})
			case "write_refused":
				_, _ = p.output.Write([]byte("bytes"))
			}
			if mode != "host_close" {
				r.host.until(t, protocol.MethodSurfaceClose, "")
			}
			if take(t, p.signals) != syscall.SIGHUP || take(t, p.signals) != syscall.SIGTERM {
				t.Fatal("group not stopped")
			}
			if mode == "host_close" {
				// Finish the lifetime check first: its deadline and the kill grace
				// share a duration, but only the kill timer should still be active.
				checkCtx := take(t, checking)
				take(t, checkCtx.Done())
				r.clock.fire(t, 2*time.Second)
				if take(t, p.signals) != syscall.SIGKILL {
					t.Fatal("grace did not kill group")
				}
			}
		})
	}
}

func TestPaneInitialGeometryAndSpawnFailure(t *testing.T) {
	for _, mode := range []string{"initial_geometry", "spawn_failure"} {
		t.Run(mode, func(t *testing.T) {
			r, spawns := paneRig(t)
			original := r.host.call
			var ready atomic.Bool
			if mode == "spawn_failure" {
				r.o.SpawnTerminal = func(Command, int, int) (TerminalProcess, error) { return nil, os.ErrPermission }
			}
			r.host.call = func(ctx context.Context, m string, p any) (json.RawMessage, error) {
				if mode == "initial_geometry" {
					if m == protocol.MethodTerminalAttach && !ready.Load() {
						return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "geometry pending"}
					}
					if m == protocol.MethodStateGet {
						return json.RawMessage(`{"value":{"runtime":{"demo.pane-b":{}}}}`), nil
					}
				}
				return original(ctx, m, p)
			}
			r.start(t)
			r.host.until(t, protocol.MethodStateSet, "attached")
			reply := make(chan protocol.Message, 1)
			go func() { reply <- r.invoke(t, "demo.pane-panel") }()
			if mode == "initial_geometry" {
				var attached protocol.TerminalAttachParams
				_ = json.Unmarshal(r.host.until(t, protocol.MethodTerminalAttach, ""), &attached)
				paneNotice(t, r, protocol.MethodSurfaceResized, protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: attached.Surface, Visible: false}}})
				r.host.until(t, protocol.MethodStateGet, "")
				ready.Store(true)
				paneNotice(t, r, protocol.MethodSurfaceResized, protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: attached.Surface, Visible: true, Width: 42, Height: 12}}})
				if response := take(t, reply); response.Error != nil {
					t.Fatal(response.Error)
				}
				take(t, spawns)
			} else {
				if response := take(t, reply); response.Error == nil || response.Error.Code != protocol.CodeFailed {
					t.Fatalf("spawn failure=%+v", response)
				}
				r.host.until(t, protocol.MethodSurfaceClose, "")
			}
		})
	}
}

func TestPaneLimits(t *testing.T) {
	for _, limit := range []string{"surfaces", "commands"} {
		t.Run(limit, func(t *testing.T) {
			r, spawns := paneRig(t)
			if limit == "surfaces" {
				path := filepath.Join(r.o.Dir, "herdr-plugin.toml")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				for i := range 9 {
					data = append(data, []byte(fmt.Sprintf("\n[[panes]]\nid='p%d'\ntitle='Pane'\ncommand=['./pane']\n", i))...)
				}
				if err = os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			r.start(t)
			r.host.until(t, protocol.MethodStateSet, "attached")
			if limit == "surfaces" {
				for i := range 8 {
					if reply := r.invoke(t, fmt.Sprintf("demo.pane-p%d", i)); reply.Error != nil {
						t.Fatal(reply.Error)
					}
					take(t, spawns)
				}
				if reply := r.invoke(t, "demo.pane-p8"); reply.Error == nil || reply.Error.Code != protocol.CodeBusy {
					t.Fatalf("ninth pane accepted: %+v", reply)
				}
			} else {
				for range 32 {
					if reply := r.invoke(t, "demo.go"); reply.Error != nil {
						t.Fatal(reply.Error)
					}
					take(t, r.spawns)
				}
				if reply := r.invoke(t, "demo.pane-panel"); reply.Error == nil || reply.Error.Code != protocol.CodeBusy {
					t.Fatalf("33rd command accepted: %+v", reply)
				}
			}
			select {
			case <-spawns:
				t.Fatal("limit spawned another PTY")
			default:
			}
		})
	}
}

func TestPaneChunkBackpressure(t *testing.T) {
	r, spawns := paneRig(t)
	original := r.host.call
	writes := make(chan protocol.TerminalWriteParams, 4)
	ack := make(chan struct{})
	r.host.call = func(ctx context.Context, m string, p any) (json.RawMessage, error) {
		if m == protocol.MethodTerminalWrite {
			writes <- p.(protocol.TerminalWriteParams)
			select {
			case <-ack:
				return json.RawMessage(`{}`), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return original(ctx, m, p)
	}
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	if reply := r.invoke(t, "demo.pane-panel"); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	p := take(t, spawns).process
	done := make(chan error, 1)
	go func() {
		_, err := p.output.Write([]byte(strings.Repeat("x", protocol.MaxTerminalBytes*2)))
		done <- err
	}()
	first := take(t, writes)
	b, err := first.Decode()
	if err != nil || len(b) != protocol.MaxTerminalBytes {
		t.Fatal("wrong raw chunk cap")
	}
	select {
	case <-writes:
		t.Fatal("second output request before acknowledgement")
	default:
	}
	ack <- struct{}{}
	second := take(t, writes)
	b, err = second.Decode()
	if err != nil || len(b) != protocol.MaxTerminalBytes {
		t.Fatal("second chunk lost")
	}
	ack <- struct{}{}
	if err := take(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestPaneProxyRoutingAndFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.json")
	if err := os.WriteFile(path, []byte(`[{"plugin_id":"other","enabled":true}]`), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	h := &testHost{events: make(chan hostRecord, 8), invoke: func(_ context.Context, p any) (json.RawMessage, error) {
		called = true
		raw, _ := json.Marshal(p)
		if !strings.Contains(string(raw), `other.pane-panel`) || !strings.Contains(string(raw), `60%`) {
			t.Fatal(string(raw))
		}
		return json.RawMessage(`{"type":"ok"}`), nil
	}}
	r := &runner{m: manifest.Manifest{ID: "demo"}, o: Options{Host: h, RegistryPath: path, Platform: "linux"}}
	req := proxy.Request{Method: "plugin.pane.open", Params: json.RawMessage(`{"plugin_id":"other","entrypoint":"panel","placement":"popup","width":"60%"}`)}
	_, failure, handled := r.intercept(context.Background(), req)
	if !called || !handled || failure != nil {
		t.Fatalf("cross pane: %v %v", handled, failure)
	}
	h.invoke = func(context.Context, any) (json.RawMessage, error) {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "not loaded"}
	}
	if _, _, handled = r.intercept(context.Background(), req); handled {
		t.Fatal("not_found did not fall back")
	}
	for _, req := range []proxy.Request{{Method: "popup.close", Params: json.RawMessage(`{}`)}, {Method: "plugin.pane.focus", Params: json.RawMessage(`{"pane_id":"real"}`)}, {Method: "plugin.pane.close", Params: json.RawMessage(`{"pane_id":"real"}`)}} {
		if _, _, handled := r.intercept(context.Background(), req); handled {
			t.Fatalf("%s should forward", req.Method)
		}
	}
	_, failure, handled = r.intercept(context.Background(), proxy.Request{Method: "plugin.pane.focus", Params: json.RawMessage(`{"pane_id":"husk:demo.closed"}`)})
	if !handled || failure == nil || failure.Code != "not_found" {
		t.Fatal("closed synthetic id reached upstream")
	}
}
