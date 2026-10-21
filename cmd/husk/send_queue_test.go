package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

const slowHello = `{"type":"hello","hello":{"plugin_id":"slow","slots":["main-stage"]}}` + "\n"

// connectPipePlugin runs handleStream on one end of an unbuffered net.Pipe and
// registers the plugin through Update. The returned peer is the plugin side;
// it never reads unless the test does. done closes when handleStream returns.
func connectPipePlugin(t *testing.T, m *WorkbenchModel, hello string) (peer net.Conn, done chan struct{}) {
	t.Helper()
	host, peer := net.Pipe()
	done = make(chan struct{})
	go func() {
		m.handleStream(host, host, host)
		close(done)
	}()
	go func() { _, _ = io.WriteString(peer, hello) }()
	select {
	case msg := <-m.subChan:
		if _, ok := msg.(pluginRegisteredMsg); !ok {
			t.Fatalf("first message = %T, want pluginRegisteredMsg", msg)
		}
		m.Update(msg)
	case <-time.After(3 * time.Second):
		t.Fatal("plugin never registered")
	}
	return peer, done
}

func nextDisconnect(t *testing.T, m *WorkbenchModel) pluginDisconnectedMsg {
	t.Helper()
	select {
	case msg := <-m.subChan:
		d, ok := msg.(pluginDisconnectedMsg)
		if !ok {
			t.Fatalf("got %T, want pluginDisconnectedMsg", msg)
		}
		return d
	case <-time.After(3 * time.Second):
		t.Fatal("no disconnect reported")
		return pluginDisconnectedMsg{}
	}
}

func waitDone(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handleStream did not return")
	}
}

func TestUpdateDoesNotBlockOnNonReadingPlugin(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	m.logOut = io.Discard
	m.writeTimeout = 10 * time.Second
	peer, _ := connectPipePlugin(t, m, slowHello)
	defer peer.Close()

	// The plugin reads nothing. With a synchronous writer the first
	// broadcast below would block Update forever on the unbuffered pipe.
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			m.Update(tea.WindowSizeMsg{Width: 100 + i%40, Height: 30 + i%10})
			m.broadcastKeyState("down", "x", true)
		}
		finished <- struct{}{}
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Update blocked on a plugin that does not read")
	}
	if m.plugins["slow"] == nil {
		t.Fatal("plugin dropped although the queue never overflowed")
	}
	wantW := m.slotRects[huskipc.SlotMainStage].InnerW

	// When the plugin catches up it receives the whole stream in order:
	// welcome, then alternating slot_resize / key_state.
	_ = peer.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var got []huskipc.Envelope
	_ = huskipc.ReadLoop(peer, func(e huskipc.Envelope) { got = append(got, e) })
	if len(got) != 1+400 {
		t.Fatalf("received %d messages, want 401", len(got))
	}
	if got[0].Type != "welcome" || got[1].Type != "slot_resize" || got[2].Type != "key_state" {
		t.Fatalf("stream order = %s, %s, %s", got[0].Type, got[1].Type, got[2].Type)
	}
	last := got[len(got)-2]
	if last.Type != "slot_resize" || last.SlotResize.SlotSizes[huskipc.SlotMainStage].Width != wantW {
		t.Fatalf("last slot_resize main-stage width = %+v, want %d", last.SlotResize, wantW)
	}
}

func TestWriteTimeoutDisconnectsAndSurfacesReason(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	var log bytes.Buffer
	m.logOut = &log
	m.writeTimeout = 50 * time.Millisecond
	peer, done := connectPipePlugin(t, m, slowHello)
	defer peer.Close()
	if m.slotOwners[huskipc.SlotMainStage] != "slow" {
		t.Fatal("plugin did not claim main-stage")
	}

	d := nextDisconnect(t, m)
	m.Update(d)
	if len(m.plugins) != 0 || m.slotOwners[huskipc.SlotMainStage] != "" {
		t.Fatal("timed-out plugin not removed")
	}
	const reason = "stopped reading: write timed out after 50ms"
	if m.activeToast == nil || m.activeToast.Level != "warn" || m.activeToast.Title != "Plugin disconnected" || m.activeToast.Message != "slow: "+reason {
		t.Fatalf("toast = %+v", m.activeToast)
	}
	if log.String() != "husk: plugin slow disconnected: "+reason+"\n" {
		t.Fatalf("log = %q", log.String())
	}
	// The closed connection also ends the read loop; that must not report a
	// second disconnect.
	waitDone(t, done)
	if len(m.subChan) != 0 {
		t.Fatalf("%d extra messages after disconnect", len(m.subChan))
	}
}

func TestSendQueueOverflowDisconnects(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	var log bytes.Buffer
	m.logOut = &log
	m.writeTimeout = 10 * time.Second
	m.sendQueueCapacity = 2
	peer, done := connectPipePlugin(t, m, slowHello)
	defer peer.Close()

	for i := 0; i < 10; i++ {
		m.Update(tea.WindowSizeMsg{Width: 100 + i, Height: 30})
	}
	m.Update(nextDisconnect(t, m))
	if m.plugins["slow"] != nil {
		t.Fatal("overflowing plugin still connected")
	}
	const reason = "stopped reading: send queue full (2 messages)"
	if m.activeToast == nil || m.activeToast.Message != "slow: "+reason {
		t.Fatalf("toast = %+v", m.activeToast)
	}
	if !strings.Contains(log.String(), "plugin slow disconnected: "+reason) {
		t.Fatalf("log = %q", log.String())
	}
	waitDone(t, done)
	if len(m.subChan) != 0 {
		t.Fatalf("%d extra messages after disconnect", len(m.subChan))
	}
}

func TestDisconnectReasonEOFAndReadError(t *testing.T) {
	cases := []struct {
		name   string
		stream string
		reason string
	}{
		{"eof", slowHello, "connection closed (EOF)"},
		{"oversized line", slowHello + strings.Repeat("x", 5*1024*1024) + "\n", "read error: bufio.Scanner: token too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel("", nil, t.TempDir())
			var log bytes.Buffer
			m.logOut = &log
			m.handleStream(strings.NewReader(tc.stream), io.Discard, io.NopCloser(nil))
			if len(m.subChan) != 2 {
				t.Fatalf("queued %d messages, want register + disconnect", len(m.subChan))
			}
			m.Update(<-m.subChan)
			m.Update(<-m.subChan)
			if len(m.plugins) != 0 {
				t.Fatal("plugin not removed")
			}
			if m.activeToast == nil || m.activeToast.Message != "slow: "+tc.reason {
				t.Fatalf("toast = %+v", m.activeToast)
			}
			if log.String() != "husk: plugin slow disconnected: "+tc.reason+"\n" {
				t.Fatalf("log = %q", log.String())
			}
		})
	}
}

func TestDisconnectReasonText(t *testing.T) {
	cases := map[string]error{
		"connection closed (EOF)":                    nil,
		"write error: io: read/write on closed pipe": fmt.Errorf("huskipc: write: %w", io.ErrClosedPipe),
		"closed by host":                             huskipc.ErrWriterClosed,
		"read error: boom":                           fmt.Errorf("boom"),
	}
	for want, err := range cases {
		if got := disconnectReason(err); got != want {
			t.Errorf("disconnectReason(%v) = %q, want %q", err, got, want)
		}
	}
}

// A hello rejected after the plugin's queued writer exists must still reach
// the plugin: the protocol_error is flushed before the connection closes.
func TestRejectedHelloFlushesProtocolError(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	var log bytes.Buffer
	m.logOut = &log
	first, _ := connectPipePlugin(t, m, slowHello)
	defer first.Close()
	go func() { _, _ = io.Copy(io.Discard, first) }()

	second, done := connectPipePlugin(t, m, slowHello)
	_ = second.SetReadDeadline(time.Now().Add(3 * time.Second))
	var got []huskipc.Envelope
	if err := huskipc.ReadLoop(second, func(e huskipc.Envelope) { got = append(got, e) }); err != nil {
		t.Fatalf("rejected connection did not close cleanly: %v", err)
	}
	if len(got) != 1 || got[0].Type != "protocol_error" || got[0].ProtocolError.MessageType != "hello" || got[0].ProtocolError.Message != "plugin_id is already connected" {
		t.Fatalf("rejected plugin received %+v", got)
	}
	if !strings.Contains(log.String(), `rejected hello from plugin "slow": plugin_id is already connected`) {
		t.Fatalf("log = %q", log.String())
	}
	waitDone(t, done)
	// The rejected connection's disconnect must not remove the live plugin.
	for len(m.subChan) > 0 {
		m.Update(<-m.subChan)
	}
	if m.plugins["slow"] == nil {
		t.Fatal("rejected duplicate removed the live plugin")
	}
}
