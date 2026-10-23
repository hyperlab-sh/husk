//go:build linux || darwin

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/creack/pty"
	"github.com/hyperlab-sh/husk/internal/stem/terminals"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// TestTerminalResizeChild acknowledges readiness only after installing WINCH.
// Re-executing the binary keeps the test independent of shell trap semantics.
func TestTerminalResizeChild(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "--resize-child" {
		return
	}
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	printSize := func() {
		rows, cols, err := pty.Getsize(os.Stdin)
		if err != nil {
			os.Exit(2)
		}
		fmt.Printf("\x1b[2J\x1b[HSIZE:%dx%d\x1b[1;%dHZ", cols, rows, cols)
	}
	printSize()
	<-winch
	printSize()
	os.Exit(0)
}

func TestTerminalResizeSignalsAndRedraws(t *testing.T) {
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
		ended <- run(ctx, []string{os.Args[0], "-test.run=^TestTerminalResizeChild$", "--", "--resize-child"}, "credential", stdin, plugin)
	}()
	writer := protocol.NewWriter(input)
	socket := protocol.NewWriter(host)
	decoder := json.NewDecoder(host)
	resize := func(w, h int) {
		t.Helper()
		raw, _ := json.Marshal(protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: protocol.SurfaceMainStage, Width: w, Height: h, Visible: true, Focused: true}}})
		if err := writer.Send(protocol.Message{Method: protocol.MethodSurfaceResized, Params: raw}); err != nil {
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
	term := terminals.New(80, 24, 10, nil)
	defer term.Close()
	var output strings.Builder
	feed := func(m protocol.Message) {
		t.Helper()
		if m.Method != protocol.MethodTerminalWrite {
			t.Fatalf("expected terminal.write, got %s", m.Method)
		}
		var p protocol.TerminalWriteParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Fatal(err)
		}
		b, err := p.Decode()
		if err != nil {
			t.Fatal(err)
		}
		output.Write(b)
		term.Write(b)
		if m.ID == nil {
			t.Fatal("unacknowledged output")
		}
		if err := socket.Send(protocol.Message{ID: m.ID, Result: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	resize(80, 24)
	attach := next()
	if attach.Method != protocol.MethodTerminalAttach {
		t.Fatalf("attach %s", attach.Method)
	}
	if err := socket.Send(protocol.Message{ID: attach.ID, Result: json.RawMessage(`{"width":80,"height":24}`)}); err != nil {
		t.Fatal(err)
	}
	for !strings.Contains(output.String(), "SIZE:80x24") {
		feed(next())
	}
	// The host resizes the grid before publishing geometry to the plugin.
	term.Resize(100, 30)
	resize(100, 30)
	for {
		m := next()
		if m.Method == protocol.MethodTerminalDetach {
			break
		}
		feed(m)
	}
	if !strings.Contains(output.String(), "SIZE:100x30") {
		t.Fatalf("no redraw from child's SIGWINCH handler: %q", output.String())
	}
	screen := uv.NewScreenBuffer(100, 30)
	term.Draw(screen, uv.Rect(0, 0, 100, 30))
	var title strings.Builder
	for x := range len("SIZE:100x30") {
		title.WriteString(screen.CellAt(x, 0).Content)
	}
	if title.String() != "SIZE:100x30" || screen.CellAt(99, 0).Content != "Z" {
		t.Fatal("program's resized output did not reach the enlarged grid")
	}
	close := next()
	if close.Method != protocol.MethodSurfaceClose {
		t.Fatalf("close %s", close.Method)
	}
	if err := socket.Send(protocol.Message{ID: close.ID, Result: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
}
