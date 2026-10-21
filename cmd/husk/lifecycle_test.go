package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
	"github.com/hyperlab-sh/husk/pkg/manifest"
)

// The test binary doubles as a fake plugin: when HUSK_FAKE_PLUGIN is set it
// runs runFakePlugin instead of the tests.
func TestMain(mm *testing.M) {
	if mode := os.Getenv("HUSK_FAKE_PLUGIN"); mode != "" {
		os.Exit(runFakePlugin(mode))
	}
	os.Exit(mm.Run())
}

// runFakePlugin modes:
//
//	stdio, socket  say hello over that transport; answer welcome with a toast
//	               whose message is the HUSK_SOCKET_PATH the plugin saw; exit 0
//	               on SIGTERM after logging it to stderr
//	crash          write a styled line to stderr and exit 7 before any hello
//	ignore-term    ignore SIGTERM and sleep
func runFakePlugin(mode string) int {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	switch mode {
	case "crash":
		fmt.Fprint(os.Stderr, "fake: starting\n\x1b[31mfatal:\x1b[0m missing config\n")
		return 7
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		fmt.Fprintln(os.Stderr, "fake: ignoring SIGTERM")
		time.Sleep(30 * time.Second)
		return 0
	}
	var (
		r io.Reader
		w io.Writer
	)
	socketPath := os.Getenv("HUSK_SOCKET_PATH")
	switch mode {
	case "stdio":
		r, w = os.Stdin, os.Stdout
	case "socket":
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake: dial:", err)
			return 4
		}
		r, w = conn, conn
	default:
		return 2
	}
	fmt.Fprintf(os.Stderr, "fake: %s up\n", mode)
	var mu sync.Mutex
	send := func(env huskipc.Envelope) {
		mu.Lock()
		defer mu.Unlock()
		_ = huskipc.NewConnWriter(w).Send(env)
	}
	send(huskipc.Envelope{Type: "hello", Hello: &huskipc.HelloPayload{PluginID: "fake"}})
	go func() {
		_ = huskipc.ReadLoop(r, func(env huskipc.Envelope) {
			if env.Type == "welcome" {
				send(huskipc.Envelope{Type: "toast", Toast: &huskipc.ToastPayload{Title: "got welcome", Message: "HUSK_SOCKET_PATH=" + socketPath}})
			}
		})
	}()
	<-sigs
	fmt.Fprintln(os.Stderr, "fake: got SIGTERM")
	return 0
}

var fakeArgv = []string{os.Args[0], "-test.run=^$"}

// awaitMsg returns the next message of type T from the event queue, applying
// any other message through Update first.
func awaitMsg[T tea.Msg](t *testing.T, m *WorkbenchModel) T {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case msg := <-m.subChan:
			if got, ok := msg.(T); ok {
				return got
			}
			m.Update(msg)
		case <-deadline:
			var zero T
			t.Fatalf("no %T arrived", zero)
			return zero
		}
	}
}

func lifecycleModel(t *testing.T, socketPath string, listener net.Listener) (*WorkbenchModel, *bytes.Buffer) {
	t.Helper()
	m := newWorkbenchModel(socketPath, listener, t.TempDir())
	var log bytes.Buffer
	m.logOut = &log
	t.Cleanup(m.cleanup)
	return m, &log
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertCleanExit(t *testing.T, p *pluginProcess) {
	t.Helper()
	ps := p.cmd.ProcessState
	if ps == nil {
		t.Fatal("cleanup returned before the plugin was waited for")
	}
	if !ps.Exited() || ps.ExitCode() != 0 {
		t.Fatalf("plugin state = %s, want a clean exit after SIGTERM", ps)
	}
}

func TestStdioPluginRunsOverStdioWithoutSocketEnv(t *testing.T) {
	// An inherited value must not leak through either.
	t.Setenv("HUSK_SOCKET_PATH", "/inherited/husk.sock")
	t.Setenv("HUSK_FAKE_PLUGIN", "stdio")
	m, log := lifecycleModel(t, filepath.Join(t.TempDir(), socketName), nil)
	if err := m.spawnPlugin("fake", manifest.TransportStdio, "", fakeArgv); err != nil {
		t.Fatal(err)
	}
	m.Update(awaitMsg[pluginRegisteredMsg](t, m))
	// The welcome reached the plugin on stdin and its reply came back on stdout.
	ev := awaitMsg[ipcEventMsg](t, m)
	if ev.pluginID != "fake" || ev.env.Type != "toast" || ev.env.Toast.Title != "got welcome" {
		t.Fatalf("event = %+v", ev.env)
	}
	if ev.env.Toast.Message != "HUSK_SOCKET_PATH=" {
		t.Fatalf("stdio plugin saw %q, want no socket path", ev.env.Toast.Message)
	}

	p := m.procs[0]
	m.cleanup()
	assertCleanExit(t, p)
	if got := readFile(t, p.logPath); got != "fake: stdio up\nfake: got SIGTERM\n" {
		t.Fatalf("stderr log = %q", got)
	}
	if !strings.Contains(log.String(), "husk: plugin fake stderr kept at "+p.logPath+"\n") {
		t.Fatalf("log = %q", log.String())
	}
	for len(m.subChan) > 0 {
		if msg, ok := (<-m.subChan).(pluginExitedMsg); ok {
			t.Fatalf("shutdown reported as an unexpected exit: %+v", msg)
		}
	}
}

func TestSocketPluginGetsSocketPath(t *testing.T) {
	t.Setenv("HUSK_FAKE_PLUGIN", "socket")
	l, path, dir, err := listenPrivate(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := lifecycleModel(t, path, l)
	m.startAcceptLoop()
	if err := m.spawnPlugin("fake", manifest.TransportSocket, "", fakeArgv); err != nil {
		t.Fatal(err)
	}
	m.Update(awaitMsg[pluginRegisteredMsg](t, m))
	ev := awaitMsg[ipcEventMsg](t, m)
	if ev.env.Type != "toast" || ev.env.Toast.Message != "HUSK_SOCKET_PATH="+path {
		t.Fatalf("event = %+v", ev.env)
	}

	p := m.procs[0]
	if want := filepath.Join(dir, pluginLogDir, "fake.log"); p.logPath != want {
		t.Fatalf("log path = %q, want %q", p.logPath, want)
	}
	if st, err := os.Stat(p.logPath); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("log file stat = %v, %v; want mode 600", st, err)
	}
	m.cleanup()
	assertCleanExit(t, p)
	if got := readFile(t, p.logPath); got != "fake: socket up\nfake: got SIGTERM\n" {
		t.Fatalf("stderr log = %q", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket not removed: %v", err)
	}
	// Closing the listener during shutdown is not an accept failure.
	time.Sleep(50 * time.Millisecond)
	for len(m.subChan) > 0 {
		if msg, ok := (<-m.subChan).(hostNoticeMsg); ok {
			t.Fatalf("shutdown reported %+v", msg)
		}
	}
}

func TestPluginExitIsReportedWithStatusAndStderr(t *testing.T) {
	t.Setenv("HUSK_FAKE_PLUGIN", "crash")
	cases := []struct {
		transport string
		toast     string
		logLine   string
	}{
		{manifest.TransportStdio, "fake: exit status 7 before hello: fatal: missing config", "husk: plugin fake exited before hello (exit status 7)"},
		{manifest.TransportSocket, "fake: exit status 7: fatal: missing config", "husk: plugin fake exited (exit status 7)"},
	}
	for _, tc := range cases {
		t.Run(tc.transport, func(t *testing.T) {
			m, log := lifecycleModel(t, filepath.Join(t.TempDir(), socketName), nil)
			if err := m.spawnPlugin("fake", tc.transport, "", fakeArgv); err != nil {
				t.Fatal(err)
			}
			msg := awaitMsg[pluginExitedMsg](t, m)
			if msg.status != "exit status 7" || msg.beforeHello != (tc.transport == manifest.TransportStdio) {
				t.Fatalf("exit = %+v", msg)
			}
			if want := []string{"fake: starting", "fatal: missing config"}; !reflect.DeepEqual(msg.stderrTail, want) {
				t.Fatalf("stderr tail = %q, want %q", msg.stderrTail, want)
			}
			// The log file keeps the raw bytes, styling included.
			if got := readFile(t, msg.logPath); got != "fake: starting\n\x1b[31mfatal:\x1b[0m missing config\n" {
				t.Fatalf("stderr log = %q", got)
			}
			m.Update(msg)
			if m.activeToast == nil || m.activeToast.Level != "warn" || m.activeToast.Title != "Plugin exited" || m.activeToast.Message != tc.toast {
				t.Fatalf("toast = %+v", m.activeToast)
			}
			want := tc.logLine + "; stderr log: " + msg.logPath + "\n" +
				"husk: plugin fake stderr: fake: starting\n" +
				"husk: plugin fake stderr: fatal: missing config\n"
			if log.String() != want {
				t.Fatalf("log = %q, want %q", log.String(), want)
			}
		})
	}
}

func TestCleanupKillsPluginThatIgnoresSIGTERM(t *testing.T) {
	t.Setenv("HUSK_FAKE_PLUGIN", "ignore-term")
	m, log := lifecycleModel(t, filepath.Join(t.TempDir(), socketName), nil)
	m.stopGrace = 300 * time.Millisecond
	if err := m.spawnPlugin("fake", manifest.TransportSocket, "", fakeArgv); err != nil {
		t.Fatal(err)
	}
	p := m.procs[0]
	for deadline := time.Now().Add(10 * time.Second); ; {
		if data, _ := os.ReadFile(p.logPath); strings.Contains(string(data), "ignoring") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake plugin never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	m.cleanup()
	elapsed := time.Since(start)
	if elapsed < m.stopGrace || elapsed > 3*time.Second {
		t.Fatalf("cleanup took %s, want the %s grace and then a prompt kill", elapsed, m.stopGrace)
	}
	ps := p.cmd.ProcessState
	if ps == nil {
		t.Fatal("killed plugin was not waited for")
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("plugin state = %s, want killed", ps)
	}
	if !strings.Contains(log.String(), "husk: plugin fake did not exit within 300ms of SIGTERM; killing it\n") {
		t.Fatalf("log = %q", log.String())
	}
}

func TestLaunchFailureIsLoggedAndToasted(t *testing.T) {
	dir := t.TempDir()
	body := `{"id":"ghost","name":"Ghost","version":"1","command":["./missing-binary"]}`
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := manifest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sockDir := t.TempDir()
	m, log := lifecycleModel(t, filepath.Join(sockDir, socketName), nil)
	if err := m.launchPlugin(p); err == nil {
		t.Fatal("launching a missing binary succeeded")
	}
	if len(m.procs) != 0 {
		t.Fatalf("failed launch tracked %d processes", len(m.procs))
	}
	if entries, _ := os.ReadDir(filepath.Join(sockDir, pluginLogDir)); len(entries) != 0 {
		t.Fatalf("failed launch left log files %v", entries)
	}
	m.Update(awaitMsg[pluginLaunchFailedMsg](t, m))
	if m.activeToast == nil || m.activeToast.Level != "error" || m.activeToast.Title != "Plugin failed to launch" ||
		!strings.HasPrefix(m.activeToast.Message, "ghost: ") || !strings.Contains(m.activeToast.Message, "missing-binary") {
		t.Fatalf("toast = %+v", m.activeToast)
	}
	if !strings.HasPrefix(log.String(), "husk: failed to launch plugin ghost: ") || !strings.Contains(log.String(), "missing-binary") {
		t.Fatalf("log = %q", log.String())
	}
}

func TestSpawnRejectsBadInput(t *testing.T) {
	m, _ := lifecycleModel(t, filepath.Join(t.TempDir(), socketName), nil)
	if err := m.spawnPlugin("x", manifest.TransportSocket, "", nil); err == nil || err.Error() != "empty command" {
		t.Fatalf("empty argv: %v", err)
	}
	if err := m.spawnPlugin("x", "carrier-pigeon", "", fakeArgv); err == nil || err.Error() != `unknown transport "carrier-pigeon"` {
		t.Fatalf("bad transport: %v", err)
	}
	noDir, _ := lifecycleModel(t, "", nil)
	if err := noDir.spawnPlugin("x", manifest.TransportSocket, "", fakeArgv); err == nil || !strings.Contains(err.Error(), "no runtime directory") {
		t.Fatalf("no socket dir: %v", err)
	}
}

func TestPluginEnv(t *testing.T) {
	base := []string{"PATH=/bin", "HUSK_SOCKET_PATH=/old.sock", "HOME=/h"}
	if got, want := pluginEnv(base, manifest.TransportSocket, "/new.sock"), []string{"PATH=/bin", "HOME=/h", "HUSK_SOCKET_PATH=/new.sock"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("socket env = %q, want %q", got, want)
	}
	if got, want := pluginEnv(base, manifest.TransportStdio, "/new.sock"), []string{"PATH=/bin", "HOME=/h"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stdio env = %q, want %q", got, want)
	}
	if base[1] != "HUSK_SOCKET_PATH=/old.sock" {
		t.Fatal("pluginEnv modified its input")
	}
}

func TestOpenPluginLogNamesAreUniqueAndSafe(t *testing.T) {
	m, _ := lifecycleModel(t, filepath.Join(t.TempDir(), socketName), nil)
	var names []string
	for _, id := range []string{"a/b", "a/b", "..", ""} {
		f, err := m.openPluginLog(id)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		names = append(names, filepath.Base(f.Name()))
	}
	if want := []string{"a_b.log", "a_b-2.log", "plugin.log", "plugin-2.log"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %q, want %q", names, want)
	}
}

func TestTailLinesKeepsLastLinesOfLargeLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	b.WriteString("\n\x1b[1mlast\x07 one\x1b[0m\r\n\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := tailLines(path, 3), []string{"line 1998", "line 1999", "last one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tail = %q, want %q", got, want)
	}
	if got := tailLines(filepath.Join(t.TempDir(), "missing"), 3); got != nil {
		t.Fatalf("missing file tail = %q", got)
	}
}

func TestPreHelloTrafficGetsProtocolError(t *testing.T) {
	m := newWorkbenchModel("", nil, t.TempDir())
	m.logOut = io.Discard
	host, peer := net.Pipe()
	defer peer.Close()
	go m.handleStream(host, host, host)
	go func() {
		_, _ = io.WriteString(peer, `{"type":"slot_update"}`+"\n"+
			`{"type":"hello"}`+"\n"+slowHello)
	}()
	var got []huskipc.ProtocolErrorPayload
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	_ = huskipc.ReadLoop(io.LimitReader(peer, 1<<20), func(e huskipc.Envelope) {
		if e.Type == "protocol_error" {
			got = append(got, *e.ProtocolError)
		}
		if len(got) == 2 {
			_ = peer.SetReadDeadline(time.Now())
		}
	})
	want := []huskipc.ProtocolErrorPayload{
		{MessageType: "slot_update", Message: "hello required before slot_update"},
		{MessageType: "hello", Message: "hello payload required"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("protocol errors = %+v, want %+v", got, want)
	}
	// The pre-hello slot_update was not delivered; the real hello still
	// registers the plugin.
	select {
	case msg := <-m.subChan:
		if reg, ok := msg.(pluginRegisteredMsg); !ok || reg.plugin.Info.PluginID != "slow" {
			t.Fatalf("first queued message = %#v, want slow's registration", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hello after the protocol errors did not register")
	}
}

// failingListener returns err from Accept.
type failingListener struct {
	net.Listener
	err error
}

func (l failingListener) Accept() (net.Conn, error) { return nil, l.err }
func (l failingListener) Close() error              { return nil }

func TestAcceptFailureIsReported(t *testing.T) {
	m, log := lifecycleModel(t, "", failingListener{err: errors.New("too many open files")})
	m.startAcceptLoop()
	m.Update(awaitMsg[hostNoticeMsg](t, m))
	const text = "accept failed: too many open files; socket plugins can no longer connect"
	if m.activeToast == nil || m.activeToast.Level != "warn" || m.activeToast.Title != "IPC listener stopped" || m.activeToast.Message != text {
		t.Fatalf("toast = %+v", m.activeToast)
	}
	if log.String() != "husk: "+text+"\n" {
		t.Fatalf("log = %q", log.String())
	}
}

func TestAcceptErrorDuringShutdownIsSilent(t *testing.T) {
	m, _ := lifecycleModel(t, "", failingListener{err: net.ErrClosed})
	m.cleanup()
	m.startAcceptLoop()
	time.Sleep(50 * time.Millisecond)
	if len(m.subChan) != 0 {
		t.Fatalf("shutdown queued %#v", <-m.subChan)
	}
}

func TestCleanupIsIdempotent(t *testing.T) {
	m, _ := lifecycleModel(t, "", nil)
	m.cleanup()
	m.cleanup()
	if !m.shuttingDown() {
		t.Fatal("quit not closed")
	}
	if m.post(toastExpireMsg{}) {
		t.Fatal("post delivered after shutdown")
	}
}
