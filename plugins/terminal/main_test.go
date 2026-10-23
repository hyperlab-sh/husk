//go:build linux || darwin

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestTerminalPluginPTYRoundTripAndExit(t *testing.T) {
	for _, surface := range []protocol.SurfaceID{protocol.SurfaceMainStage, protocol.SurfaceSidebarList} {
		t.Run(string(surface), func(t *testing.T) {
			host, plugin := net.Pipe()
			defer host.Close()
			defer plugin.Close()
			stdin, input := io.Pipe()
			defer stdin.Close()
			defer input.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ended := make(chan error, 1)
			go func() {
				ended <- run(ctx, []string{"sh", "-c", `stty -echo; read line; printf 'child:%s' "$line"`}, "credential", stdin, plugin)
			}()
			writer := protocol.NewWriter(input)
			socket := protocol.NewWriter(host)
			decoder := json.NewDecoder(host)
			send := func(method string, p any) {
				t.Helper()
				raw, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				if err = writer.Send(protocol.Message{Method: method, Params: raw}); err != nil {
					t.Fatal(err)
				}
			}
			next := func() protocol.Message {
				t.Helper()
				var m protocol.Message
				if err := decoder.Decode(&m); err != nil {
					t.Fatal(err)
				}
				return m
			}
			send(protocol.MethodSurfaceResized, protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: surface, Width: 80, Height: 24, Visible: true, Focused: true}}})
			attach := next()
			if attach.Method != protocol.MethodTerminalAttach || attach.ID == nil {
				t.Fatalf("attach = %+v", attach)
			}
			var params protocol.TerminalAttachParams
			if err := json.Unmarshal(attach.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Surface != surface || params.Capture == nil || params.Capture.Mode() != protocol.CaptureAll {
				t.Fatalf("attach params %+v", params)
			}
			if err := socket.Send(protocol.Message{ID: attach.ID, Result: json.RawMessage(`{"width":80,"height":24}`)}); err != nil {
				t.Fatal(err)
			}
			send(protocol.MethodTerminalInput, protocol.TerminalInputParams{Surface: surface, Data: base64.StdEncoding.EncodeToString([]byte("z\n"))})
			var output string
			for {
				m := next()
				if m.Method == protocol.MethodTerminalDetach {
					break
				}
				if m.Method != protocol.MethodTerminalWrite {
					t.Fatalf("unexpected method %s", m.Method)
				}
				var p protocol.TerminalWriteParams
				if err := json.Unmarshal(m.Params, &p); err != nil {
					t.Fatal(err)
				}
				b, err := p.Decode()
				if err != nil {
					t.Fatal(err)
				}
				output += string(b)
				if m.ID == nil {
					t.Fatal("output has no acknowledgement")
				}
				if err := socket.Send(protocol.Message{ID: m.ID, Result: json.RawMessage(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
			if !strings.Contains(output, "child:z") {
				t.Fatalf("PTY input/output %q", output)
			}
			close := next()
			if close.Method != protocol.MethodSurfaceClose || close.ID == nil {
				t.Fatalf("surface close %+v", close)
			}
			if err := socket.Send(protocol.Message{ID: close.ID, Result: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			if err := <-ended; err != nil {
				t.Fatal(err)
			}
		})
	}
}
