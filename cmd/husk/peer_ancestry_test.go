package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func skipWithoutParentLookup(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("parent pid lookup is implemented on linux and darwin only")
	}
}

func TestParentPIDOfSelfIsGetppid(t *testing.T) {
	skipWithoutParentLookup(t)
	got, err := parentPID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if got != os.Getppid() {
		t.Fatalf("parentPID(self) = %d, want %d", got, os.Getppid())
	}
}

func TestParentPIDOfMissingProcessFails(t *testing.T) {
	skipWithoutParentLookup(t)
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	// The child has been reaped, so its pid names no process (barring reuse
	// within this instant).
	if ppid, err := parentPID(cmd.Process.Pid); err == nil {
		t.Fatalf("parentPID(reaped %d) = %d, want an error", cmd.Process.Pid, ppid)
	}
}

func TestPeerAncestryStartsAtPeerAndWalksParents(t *testing.T) {
	skipWithoutParentLookup(t)
	chain := peerAncestry(os.Getpid())
	if len(chain) < 2 {
		t.Fatalf("peerAncestry(self) = %v, want self and parent", chain)
	}
	if chain[0] != os.Getpid() || chain[1] != os.Getppid() {
		t.Fatalf("peerAncestry(self) = %v, want [%d %d ...]", chain, os.Getpid(), os.Getppid())
	}
	for _, p := range chain {
		if p <= 1 {
			t.Fatalf("chain %v includes init or an invalid pid", chain)
		}
	}
	if len(chain) > maxAncestryDepth {
		t.Fatalf("chain length %d exceeds %d", len(chain), maxAncestryDepth)
	}
}

func TestPeerAncestryOfInvalidPIDsIsEmpty(t *testing.T) {
	for _, pid := range []int{-5, 0, 1} {
		if got := peerAncestry(pid); len(got) != 0 {
			t.Fatalf("peerAncestry(%d) = %v, want empty", pid, got)
		}
	}
}

func TestDescendantOfLaunchedProcessBindsToItsManifest(t *testing.T) {
	skipWithoutParentLookup(t)
	m := newWorkbenchModel("", nil, t.TempDir())
	// The test runner (our parent) stands in for the launched wrapper; this
	// process is the plugin it started.
	launchedAs(t, m, fileManifest("acme.p", "main-stage"), os.Getppid())
	if got := m.launchedID(os.Getpid()); got != "acme.p" {
		t.Fatalf("launchedID(child of launched) = %q, want acme.p", got)
	}
}

func TestNearestLaunchedAncestorWins(t *testing.T) {
	skipWithoutParentLookup(t)
	m := newWorkbenchModel("", nil, t.TempDir())
	launchedAs(t, m, fileManifest("outer", "main-stage"), os.Getppid())
	launchedAs(t, m, fileManifest("inner", "side-panel"), os.Getpid())
	if got := m.launchedID(os.Getpid()); got != "inner" {
		t.Fatalf("launchedID = %q, want the nearest launched process, inner", got)
	}
	// Once the inner process has exited, the walk continues to the outer one.
	close(m.procs[1].done)
	if got := m.launchedID(os.Getpid()); got != "outer" {
		t.Fatalf("launchedID after inner exit = %q, want outer", got)
	}
}

func TestUnrelatedPeerIsNotBound(t *testing.T) {
	skipWithoutParentLookup(t)
	m := newWorkbenchModel("", nil, t.TempDir())
	// A launched child of this process is not an ancestor of this process.
	child := startWrapper(t, "/bin/sleep 30")
	launchedAs(t, m, fileManifest("acme.p", "main-stage"), child.wrapper)
	if got := m.launchedID(os.Getpid()); got != "" {
		t.Fatalf("launchedID(parent of launched) = %q, want unbound", got)
	}
}

// wrapperTree is a /bin/sh wrapper running a backgrounded grandchild.
type wrapperTree struct {
	wrapper int
	child   int
}

// startWrapper runs `/bin/sh -c "<cmd> & echo $!; wait"`, the shape of a
// wrapper script that starts the real plugin as its child, and returns both
// pids. The tree is killed when the test ends.
func startWrapper(t *testing.T, cmd string) wrapperTree {
	t.Helper()
	sh := exec.Command("/bin/sh", "-c", cmd+" & echo $!; wait")
	out, err := sh.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		_ = sh.Process.Kill()
		t.Fatalf("reading grandchild pid: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		_ = sh.Process.Kill()
		t.Fatalf("grandchild pid %q: %v", line, err)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(child); err == nil {
			_ = p.Kill()
		}
		_ = sh.Process.Kill()
		_ = sh.Wait()
	})
	return wrapperTree{wrapper: sh.Process.Pid, child: child}
}

func TestSocketPluginBehindWrapperScriptCannotClaimForeignID(t *testing.T) {
	skipWithoutParentLookup(t)
	tree := startWrapper(t, "/bin/sleep 30")
	// $! is set after the fork, so the grandchild already has the wrapper as
	// its parent.
	if ppid, err := parentPID(tree.child); err != nil || ppid != tree.wrapper {
		t.Fatalf("parentPID(grandchild %d) = %d, %v; want wrapper %d", tree.child, ppid, err, tree.wrapper)
	}

	m := newWorkbenchModel("", nil, t.TempDir())
	var log bytes.Buffer
	m.logOut = &log
	launchedAs(t, m, fileManifest("acme.p", "main-stage"), tree.wrapper)
	if got := m.launchedID(tree.child); got != "acme.p" {
		t.Fatalf("launchedID(grandchild) = %q, want acme.p", got)
	}

	// Before ancestry binding, a claim of an id with no manifest went
	// unchecked; now it is held to the wrapper's manifest.
	hello := `{"type":"hello","hello":{"plugin_id":"evil","slots":["side-panel"]}}` + "\n"
	env, logText, m := rejectedFrom(t, m, streamOrigin{peerPID: tree.child}, hello, &log)
	want := `hello plugin_id "evil" does not match manifest id "acme.p"`
	if env.Type != "protocol_error" || env.ProtocolError.Message != want {
		t.Fatalf("got %+v, want protocol_error %q", env, want)
	}
	if m.plugins["evil"] != nil {
		t.Fatal("foreign id was registered")
	}
	if got := m.registry.Occupants["side-panel"]; got != "" {
		t.Fatalf("side-panel occupied by %q", got)
	}
	if !strings.Contains(logText, "rejected hello from plugin") {
		t.Fatalf("log = %q", logText)
	}
}

func TestSocketPluginBehindWrapperScriptWithMatchingHelloRegisters(t *testing.T) {
	skipWithoutParentLookup(t)
	tree := startWrapper(t, "/bin/sleep 30")
	m := newWorkbenchModel("", nil, t.TempDir())
	launchedAs(t, m, fileManifest("slow", "main-stage"), tree.wrapper)
	peer, _ := connectPipePluginFrom(t, m, streamOrigin{peerPID: tree.child}, slowHello)
	defer peer.Close()
	go func() { _, _ = io.Copy(io.Discard, peer) }()
	if m.plugins["slow"] == nil {
		t.Fatal("matching hello from the wrapper's child was not registered")
	}
	if got := m.plugins["slow"].PeerPID; got != tree.child {
		t.Fatalf("PeerPID = %d, want %d", got, tree.child)
	}
}
