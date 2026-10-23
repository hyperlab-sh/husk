//go:build linux || darwin

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestTerminalPluginStalledInputKeepsControlResponsive(t *testing.T) {
	if os.Args[len(os.Args)-1] == "input-helper" {
		cmd := exec.Command("stty", "-echo", "-icanon")
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			os.Exit(2)
		}
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM)
		fmt.Print("input-ready")
		<-stop // deliberately never read the PTY input
		os.Exit(0)
	}
	host, plugin := net.Pipe()
	defer host.Close()
	defer plugin.Close()
	stdin, input := io.Pipe()
	defer stdin.Close()
	defer input.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ended := make(chan error, 1)
	go func() {
		ended <- run(ctx, []string{os.Args[0], "-test.run=^TestTerminalPluginStalledInputKeepsControlResponsive$", "--", "input-helper"}, "credential", stdin, plugin)
	}()
	writer := protocol.NewWriter(input)
	raw, _ := json.Marshal(protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: protocol.SurfaceSidebarList, Width: 80, Height: 24}}})
	if err := writer.Send(protocol.Message{Method: protocol.MethodSurfaceResized, Params: raw}); err != nil {
		t.Fatal(err)
	}
	decoder, reply := json.NewDecoder(host), protocol.NewWriter(host)
	var m protocol.Message
	if err := decoder.Decode(&m); err != nil {
		t.Fatal(err)
	}
	if err := reply.Send(protocol.Message{ID: m.ID, Result: json.RawMessage(`{"width":80,"height":24}`)}); err != nil {
		t.Fatal(err)
	}
	var ready string
	for !strings.Contains(ready, "input-ready") {
		if err := decoder.Decode(&m); err != nil {
			t.Fatal(err)
		}
		var p protocol.TerminalWriteParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Fatal(err)
		}
		b, err := p.Decode()
		if err != nil {
			t.Fatal(err)
		}
		ready += string(b)
		if err := reply.Send(protocol.Message{ID: m.ID, Result: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	p := protocol.TerminalInputParams{Surface: protocol.SurfaceSidebarList, Data: base64.StdEncoding.EncodeToString(make([]byte, protocol.MaxTerminalBytes))}
	raw, _ = json.Marshal(p)
	for range 64 {
		if err := writer.Send(protocol.Message{Method: protocol.MethodTerminalInput, Params: raw}); err != nil {
			t.Fatal(err)
		}
	}
	// EOF is a control event, not a PTY write: it must still shut down and
	// join the blocked writer even after input has saturated its queue.
	_ = input.Close()
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
}

func TestTerminalPluginAcknowledgedFlood(t *testing.T) {
	host, plugin := net.Pipe()
	defer host.Close()
	defer plugin.Close()
	stdin, input := io.Pipe()
	defer stdin.Close()
	defer input.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ended := make(chan error, 1)
	go func() {
		ended <- run(ctx, []string{"sh", "-c", "head -c 8388608 /dev/zero"}, "credential", stdin, plugin)
	}()
	raw, _ := json.Marshal(protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: protocol.SurfaceSidebarList, Width: 80, Height: 24}}})
	if err := protocol.NewWriter(input).Send(protocol.Message{Method: protocol.MethodSurfaceResized, Params: raw}); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(host)
	writer := protocol.NewWriter(host)
	var attach protocol.Message
	if err := decoder.Decode(&attach); err != nil {
		t.Fatal(err)
	}
	if err := writer.Send(protocol.Message{ID: attach.ID, Result: json.RawMessage(`{"width":80,"height":24}`)}); err != nil {
		t.Fatal(err)
	}
	chunks, total := 0, 0
	for {
		var m protocol.Message
		if err := decoder.Decode(&m); err != nil {
			t.Fatal(err)
		}
		if m.Method == protocol.MethodTerminalDetach {
			continue
		}
		if m.Method == protocol.MethodSurfaceClose {
			if err := writer.Send(protocol.Message{ID: m.ID, Result: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			break
		}
		if m.Method != protocol.MethodTerminalWrite || m.ID == nil {
			t.Fatal("output bypassed the acknowledged window")
		}
		var p protocol.TerminalWriteParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Fatal(err)
		}
		b, err := p.Decode()
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range b {
			if v != 0 {
				t.Fatal("flood output corrupted")
			}
		}
		total += len(b)
		chunks++
		if err := writer.Send(protocol.Message{ID: m.ID, Result: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if total != 8388608 || chunks <= 64 {
		t.Fatalf("flood: %d bytes in %d chunks", total, chunks)
	}
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
}
