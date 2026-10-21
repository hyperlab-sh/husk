package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

// readPeer collects every envelope the host writes to a pipe peer.
func readPeer(peer net.Conn) <-chan huskipc.Envelope {
	out := make(chan huskipc.Envelope, 64)
	go func() {
		_ = huskipc.ReadLoop(peer, func(e huskipc.Envelope) { out <- e })
		close(out)
	}()
	return out
}

func nextEnvelope(t *testing.T, ch <-chan huskipc.Envelope) huskipc.Envelope {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("connection closed, want another envelope")
		}
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no envelope from host")
	}
	return huskipc.Envelope{}
}

// nextSub applies subChan messages to the model until one of type T arrives.
func nextSub[T any](t *testing.T, m *WorkbenchModel) T {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-m.subChan:
			m.Update(msg)
			if v, ok := msg.(T); ok {
				return v
			}
		case <-deadline:
			var zero T
			t.Fatalf("no %T on subChan", zero)
			return zero
		}
	}
}

func TestBadLinesAnsweredWithProtocolErrorAndConnectionStaysOpen(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	var log bytes.Buffer
	m.logOut = &log
	peer, done := connectPipePlugin(t, m, slowHello)
	defer peer.Close()
	in := readPeer(peer)
	if w := nextEnvelope(t, in); w.Type != "welcome" {
		t.Fatalf("first envelope = %s, want welcome", w.Type)
	}

	cases := []struct {
		name, line, msgType, message string
	}{
		{"invalid json", `{"type":`, "", "malformed message: unexpected end of JSON input"},
		{"mistyped field", `{"type":"slot_update","slot_update":{"slot":"main-stage","lines":"oops"}}`, "slot_update", "malformed message: json: cannot unmarshal string into Go struct field"},
		{"oversized", strings.Repeat("x", huskipc.MaxLineBytes+1), "", "line exceeds 4194304 bytes"},
	}
	for _, tc := range cases {
		go func() { _, _ = io.WriteString(peer, tc.line+"\n") }()
		e := nextEnvelope(t, in)
		if e.Type != "protocol_error" || e.ProtocolError == nil {
			t.Fatalf("%s: host sent %+v, want protocol_error", tc.name, e)
		}
		if e.ProtocolError.MessageType != tc.msgType || !strings.HasPrefix(e.ProtocolError.Message, tc.message) {
			t.Fatalf("%s: protocol_error = %+v, want type %q message %q...", tc.name, e.ProtocolError, tc.msgType, tc.message)
		}
		rej := nextSub[lineRejectedMsg](t, m)
		if rej.plugin == nil || rej.plugin.Info.PluginID != "slow" {
			t.Fatalf("%s: rejection not attributed to the plugin", tc.name)
		}
		if !strings.Contains(log.String(), "husk: protocol error from plugin slow: "+tc.message) {
			t.Fatalf("%s: log = %q", tc.name, log.String())
		}
	}

	// The connection survived every bad line: a valid update still lands.
	go func() {
		_, _ = io.WriteString(peer, `{"type":"slot_update","slot_update":{"slot":"main-stage","lines":["still here"]}}`+"\n")
	}()
	nextSub[ipcEventMsg](t, m)
	if su := m.slotUpdates[huskipc.SlotMainStage]; su == nil || len(su.Lines) != 1 || su.Lines[0] != "still here" {
		t.Fatalf("slot update after bad lines = %+v", su)
	}
	if m.plugins["slow"] == nil {
		t.Fatal("plugin disconnected by a bad line")
	}
	_ = peer.Close()
	waitDone(t, done)
}

func TestUnknownOrIncompleteMessagesRejected(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		msgType string
		message string
	}{
		{"unknown type", `{"type":"bogus"}`, "bogus", `unknown message type "bogus"`},
		{"host-to-plugin type", `{"type":"welcome","welcome":{"host_version":"x"}}`, "welcome", `unknown message type "welcome"`},
		{"missing type", `{"slot_update":{"slot":"main-stage","lines":[]}}`, "", "message has no type"},
		{"slot_update without payload", `{"type":"slot_update"}`, "slot_update", "slot_update payload required"},
		{"toast without payload", `{"type":"toast","slot_update":{"slot":"main-stage"}}`, "toast", "toast payload required"},
		{"workspace_context without payload", `{"type":"workspace_context"}`, "workspace_context", "workspace_context payload required"},
		{"unknown type carrying close_modal", `{"type":"bogus","close_modal":true}`, "bogus", `unknown message type "bogus"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel("", nil, t.TempDir())
			var log bytes.Buffer
			m.logOut = &log
			cp, buf := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p", Slots: []huskipc.SlotID{huskipc.SlotMainStage}})
			m.openModal("p", "r")
			buf.Reset()
			var env huskipc.Envelope
			if err := json.Unmarshal([]byte(tc.env), &env); err != nil {
				t.Fatal(err)
			}
			m.Update(ipcEventMsg{pluginID: "p", env: env, connection: cp})

			got := envelopes(t, buf)
			if len(got) != 1 || got[0].Type != "protocol_error" {
				t.Fatalf("host sent %+v, want exactly one protocol_error", got)
			}
			if pe := got[0].ProtocolError; pe.MessageType != tc.msgType || pe.Message != tc.message {
				t.Fatalf("protocol_error = %+v, want %q / %q", pe, tc.msgType, tc.message)
			}
			if !strings.Contains(log.String(), "protocol error from plugin p: "+tc.message) {
				t.Fatalf("log = %q", log.String())
			}
			if m.modalOwner().PluginID != "p" {
				t.Fatal("a rejected message still acted (close_modal)")
			}
			if m.plugins["p"] == nil {
				t.Fatal("rejection dropped the connection")
			}
		})
	}
}

// Known plugin messages are not answered with protocol_error.
func TestKnownMessagesAreNotRejected(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	m.logOut = io.Discard
	cp, buf := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p", Slots: []huskipc.SlotID{huskipc.SlotMainStage}})
	buf.Reset()
	for _, line := range []string{
		`{"type":"slot_update","slot_update":{"slot":"main-stage","lines":["x"]}}`,
		`{"type":"workspace_context","workspace_context":{"workspace_id":"w","workspace_label":"W","cwd":"/"}}`,
		`{"type":"toast","toast":{"level":"info","title":"t","message":"m"}}`,
		`{"type":"close_modal","close_modal":true}`,
		`{"type":"event_declare","event_declare":{"id":"p.changed"}}`,
	} {
		var env huskipc.Envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatal(err)
		}
		m.Update(ipcEventMsg{pluginID: "p", env: env, connection: cp})
	}
	if got := envelopes(t, buf); len(got) != 0 {
		t.Fatalf("host answered valid messages with %+v", got)
	}
}

// A plugin's own protocol_error is logged, never answered: two strict peers
// answering each other's errors would loop forever.
func TestPluginProtocolErrorIsLoggedNotAnswered(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	var log bytes.Buffer
	m.logOut = &log
	cp, buf := addPublicationTestPlugin(m, huskipc.HelloPayload{PluginID: "p"})
	buf.Reset()
	m.Update(ipcEventMsg{pluginID: "p", connection: cp, env: huskipc.Envelope{
		Type:          "protocol_error",
		ProtocolError: &huskipc.ProtocolErrorPayload{MessageType: "slot_resize", Message: "bad size"},
	}})
	m.Update(ipcEventMsg{pluginID: "p", connection: cp, env: huskipc.Envelope{Type: "protocol_error"}})
	if got := envelopes(t, buf); len(got) != 0 {
		t.Fatalf("host answered a protocol_error with %+v", got)
	}
	want := "husk: plugin p rejected a \"slot_resize\" message: bad size\n" +
		"husk: plugin p sent a protocol_error without a payload\n"
	if log.String() != want {
		t.Fatalf("log = %q, want %q", log.String(), want)
	}
}

func TestHelloProtocolVersionNegotiation(t *testing.T) {
	cases := []struct {
		name, version string
		accepted      bool
		message       string
	}{
		{"legacy (absent)", "", true, ""},
		{"same major, newer minor", "1.4", true, ""},
		{"bare major", "1", true, ""},
		{"newer major", "2.0", false, `unsupported protocol_version "2.0": host speaks 1.0 (major 1)`},
		{"older major", "0.3", false, `unsupported protocol_version "0.3": host speaks 1.0 (major 1)`},
		{"garbage", "latest", false, `invalid protocol_version "latest": want MAJOR.MINOR`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel("", nil, t.TempDir())
			var log bytes.Buffer
			m.logOut = &log
			hello := huskipc.HelloPayload{ProtocolVersion: tc.version, PluginID: "v", Slots: []huskipc.SlotID{huskipc.SlotMainStage}}
			line, _ := json.Marshal(huskipc.Envelope{Type: "hello", Hello: &hello})
			peer, done := connectPipePlugin(t, m, string(line)+"\n")
			defer peer.Close()
			_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
			in := readPeer(peer)
			e := nextEnvelope(t, in)

			if tc.accepted {
				if e.Type != "welcome" || e.Welcome.ProtocolVersion != huskipc.ProtocolVersion || e.Welcome.HostVersion != hostVersion {
					t.Fatalf("accepted hello got %+v, want welcome with protocol_version %s", e, huskipc.ProtocolVersion)
				}
				if m.plugins["v"] == nil || m.surfaceOwner(huskipc.SlotMainStage) != "v" {
					t.Fatal("compatible plugin not registered")
				}
				return
			}
			if e.Type != "protocol_error" || e.ProtocolError.MessageType != "hello" || e.ProtocolError.Message != tc.message {
				t.Fatalf("rejected hello got %+v, want protocol_error %q", e, tc.message)
			}
			if _, open := <-in; open {
				t.Fatal("host kept the connection open after rejecting the hello")
			}
			waitDone(t, done)
			nextSub[pluginDisconnectedMsg](t, m)
			if len(m.plugins) != 0 || m.surfaceOwner(huskipc.SlotMainStage) != "" {
				t.Fatal("rejected plugin left registered state behind")
			}
			wantLog := "husk: rejected hello from plugin \"v\": " + tc.message + "\n" +
				"husk: connection from plugin \"v\" (not registered) closed: "
			if !strings.HasPrefix(log.String(), wantLog) {
				t.Fatalf("log = %q, want prefix %q", log.String(), wantLog)
			}
		})
	}
}

// Every way a connection ends is logged, including those that never got as
// far as a registered hello.
func TestUnregisteredConnectionExitIsLogged(t *testing.T) {
	cases := []struct {
		name, stream string
		want         string
	}{
		{"eof before hello", "", "husk: connection closed before hello: connection closed (EOF)\n"},
		{"garbage then eof", "garbage\n",
			"husk: protocol error from unregistered connection: malformed message: invalid character 'g' looking for beginning of value\n" +
				"husk: connection closed before hello: connection closed (EOF)\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkbenchModel("", nil, t.TempDir())
			var log bytes.Buffer
			m.logOut = &log
			m.handleStream(strings.NewReader(tc.stream), io.Discard, io.NopCloser(nil))
			for len(m.subChan) > 0 {
				m.Update(<-m.subChan)
			}
			if log.String() != tc.want {
				t.Fatalf("log = %q, want %q", log.String(), tc.want)
			}
		})
	}
}

// The wire structs mirror the host's frame and theme types field for field;
// the conversions in the host compile only while the fields match, and this
// pins the JSON spelling too.
func TestWireFrameAndPaletteMatchHostTypes(t *testing.T) {
	spec := frame.Spec{Border: true, Label: "L", Close: true}
	a, _ := json.Marshal(spec)
	b, _ := json.Marshal(huskipc.FrameSpec(spec))
	if string(a) != string(b) {
		t.Fatalf("frame JSON %s != wire %s", a, b)
	}
	pal := theme.Default()
	a, _ = json.Marshal(pal)
	b, _ = json.Marshal(huskipc.Palette(pal))
	if string(a) != string(b) {
		t.Fatalf("palette JSON %s != wire %s", a, b)
	}
}
