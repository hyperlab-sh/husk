package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	return rejectedFrom(t, m, streamOrigin{expectID: stdioID}, hello, &log)
}

func rejectedFrom(t *testing.T, m *WorkbenchModel, origin streamOrigin, hello string, log *bytes.Buffer) (huskipc.Envelope, string, *WorkbenchModel) {
	t.Helper()
	host, peer := net.Pipe()
	done := make(chan struct{})
	go func() {
		m.serveStreamOrigin(origin, host, host, host, nil)
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

// launchedAs makes m believe a live process launched from manifest mf has the
// given pid.
func launchedAs(t *testing.T, m *WorkbenchModel, mf *manifest.Plugin, pid int) {
	t.Helper()
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	m.manifests[mf.ID] = mf
	m.procs = append(m.procs, &pluginProcess{id: mf.ID, cmd: &exec.Cmd{Process: proc}, done: make(chan struct{})})
}

func TestSocketPeerLaunchedFromManifestCannotClaimForeignID(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	var log bytes.Buffer
	m.logOut = &log
	launchedAs(t, m, fileManifest("acme.p", "main-stage"), os.Getpid())
	hello := `{"type":"hello","hello":{"plugin_id":"evil","slots":["main-stage"]}}` + "\n"
	env, _, m := rejectedFrom(t, m, streamOrigin{peerPID: os.Getpid()}, hello, &log)
	want := `hello plugin_id "evil" does not match manifest id "acme.p"`
	if env.ProtocolError.Message != want {
		t.Fatalf("message = %q, want %q", env.ProtocolError.Message, want)
	}
	if m.plugins["evil"] != nil {
		t.Fatal("foreign id was registered")
	}
}

func TestSocketPeerWithUnknownPIDIsMatchedByClaimedID(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	var log bytes.Buffer
	m.logOut = &log
	launchedAs(t, m, fileManifest("acme.p", "main-stage"), os.Getpid())
	// A peer that is not the launched process (pid 1) and claims an id with no
	// manifest is not bound to anything.
	if got := m.launchedID(1); got != "" {
		t.Fatalf("launchedID(1) = %q", got)
	}
	if got := m.launchedID(0); got != "" {
		t.Fatalf("launchedID(0) = %q", got)
	}
	// Claiming the launched id still gets the slot check.
	hello := `{"type":"hello","hello":{"plugin_id":"acme.p","slots":["side-panel"]}}` + "\n"
	env, _, _ := rejectedFrom(t, m, streamOrigin{peerPID: 1}, hello, &log)
	if !strings.Contains(env.ProtocolError.Message, "claims undeclared slots [side-panel]") {
		t.Fatalf("message = %q", env.ProtocolError.Message)
	}
}

func TestExitedProcessPIDDoesNotBind(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	launchedAs(t, m, fileManifest("acme.p", "main-stage"), os.Getpid())
	close(m.procs[0].done)
	if got := m.launchedID(os.Getpid()); got != "" {
		t.Fatalf("exited process still binds: %q", got)
	}
}

func TestManifestWithoutSlotsRejectionHintsAtFix(t *testing.T) {
	hello := `{"type":"hello","hello":{"plugin_id":"acme.p","slots":["main-stage"]}}` + "\n"
	env, _, _ := rejectedWith(t, fileManifest("acme.p"), hello, "")
	if !strings.Contains(env.ProtocolError.Message, "add them to husk-plugin.json") {
		t.Fatalf("message = %q", env.ProtocolError.Message)
	}
}

func TestLaunchRejectsDuplicatePluginID(t *testing.T) {
	m, _ := lifecycleModel(t, filepath.Join(t.TempDir(), socketName), nil)
	first := fileManifest("dup", "main-stage")
	m.manifests["dup"] = first
	implicit := &manifest.Plugin{ID: "dup", Name: "dup", Version: "0.0.0", Command: fakeArgv}
	err := m.launchPlugin(implicit)
	if err == nil || !strings.Contains(err.Error(), "duplicate plugin id") {
		t.Fatalf("err = %v", err)
	}
	if m.manifests["dup"] != first {
		t.Fatal("duplicate replaced the file manifest")
	}
	if len(m.procs) != 0 {
		t.Fatal("duplicate was spawned")
	}
}
