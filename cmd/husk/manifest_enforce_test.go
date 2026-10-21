package main

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
	"github.com/hyperlab-sh/husk/pkg/manifest"
)

func fileManifest(id string, slots ...string) *manifest.Plugin {
	return &manifest.Plugin{ID: id, Name: id, Version: "1", Command: []string{"x"}, Slots: slots, Path: "/x/" + manifest.FileName}
}

// rejectedWith connects a plugin whose hello is checked against mf and returns
// the single protocol_error it receives plus the host log.
func rejectedWith(t *testing.T, mf *manifest.Plugin, hello string, stdioID string) (huskipc.Envelope, string, *WorkbenchModel) {
	t.Helper()
	m := newWorkbenchModel("", nil, t.TempDir())
	var log bytes.Buffer
	m.logOut = &log
	m.manifests[mf.ID] = mf
	host, peer := net.Pipe()
	done := make(chan struct{})
	go func() {
		m.serveStreamFor(stdioID, host, host, host, nil)
		close(done)
	}()
	go func() { _, _ = io.WriteString(peer, hello) }()
	m.Update(<-m.subChan)
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	var got []huskipc.Envelope
	if err := huskipc.ReadLoop(peer, func(e huskipc.Envelope) { got = append(got, e) }); err != nil {
		t.Fatalf("connection did not close cleanly: %v", err)
	}
	waitDone(t, done)
	if len(got) != 1 {
		t.Fatalf("got %d envelopes %+v, want one protocol_error", len(got), got)
	}
	return got[0], log.String(), m
}

func TestHelloSlotsDisagreeingWithManifestIsRejected(t *testing.T) {
	hello := `{"type":"hello","hello":{"plugin_id":"acme.p","slots":["main-stage","side-panel"]}}` + "\n"
	env, log, m := rejectedWith(t, fileManifest("acme.p", "main-stage"), hello, "")
	if env.Type != "protocol_error" || env.ProtocolError.MessageType != "hello" {
		t.Fatalf("got %+v", env)
	}
	want := "claims undeclared slots [side-panel]"
	if !strings.Contains(env.ProtocolError.Message, want) {
		t.Fatalf("message %q lacks %q", env.ProtocolError.Message, want)
	}
	if !strings.Contains(log, "rejected hello from plugin") {
		t.Fatalf("log = %q", log)
	}
	if m.plugins["acme.p"] != nil {
		t.Fatal("rejected plugin was registered")
	}
	if got := m.registry.Occupants["side-panel"]; got != "" {
		t.Fatalf("rejected plugin kept surface side-panel owned by %q", got)
	}
}

func TestHelloOmittingDeclaredSlotIsRejected(t *testing.T) {
	hello := `{"type":"hello","hello":{"plugin_id":"acme.p","slots":["main-stage"]}}` + "\n"
	env, _, _ := rejectedWith(t, fileManifest("acme.p", "main-stage", "side-panel"), hello, "")
	if !strings.Contains(env.ProtocolError.Message, "omits declared slots [side-panel]") {
		t.Fatalf("message = %q", env.ProtocolError.Message)
	}
}

func TestStdioHelloWithForeignPluginIDIsRejected(t *testing.T) {
	// The stream belongs to the process launched from acme.p's manifest, so a
	// hello under another id is a mismatch even though "other" has no manifest.
	hello := `{"type":"hello","hello":{"plugin_id":"other","slots":["main-stage"]}}` + "\n"
	env, _, m := rejectedWith(t, fileManifest("acme.p", "main-stage"), hello, "acme.p")
	want := `hello plugin_id "other" does not match manifest id "acme.p"`
	if env.ProtocolError.Message != want {
		t.Fatalf("message = %q, want %q", env.ProtocolError.Message, want)
	}
	if m.plugins["other"] != nil {
		t.Fatal("foreign id was registered")
	}
}

func TestHelloMatchingManifestRegisters(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	m.manifests["slow"] = fileManifest("slow", "main-stage")
	peer, _ := connectPipePlugin(t, m, slowHello)
	defer peer.Close()
	go func() { _, _ = io.Copy(io.Discard, peer) }()
	if m.plugins["slow"] == nil {
		t.Fatal("matching hello was not registered")
	}
}

func TestPluginWithoutManifestFileIsNotChecked(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	implicit := &manifest.Plugin{ID: "slow", Name: "slow", Version: "0.0.0", Command: []string{"x"}}
	m.manifests["slow"] = implicit
	peer, _ := connectPipePlugin(t, m, slowHello)
	defer peer.Close()
	go func() { _, _ = io.Copy(io.Discard, peer) }()
	if m.plugins["slow"] == nil {
		t.Fatal("implicit plugin was rejected")
	}
}
