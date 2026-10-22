//go:build linux || darwin

package wrapper

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// TestMain reexecutes this binary as a fake plugin. The mode is in argv because
// the real wrapper intentionally strips inherited HUSK_FAKE_PLUGIN with every
// other non-protocol HUSK_* variable.
func TestMain(m *testing.M) {
	for _, arg := range os.Args {
		if mode, ok := strings.CutPrefix(arg, "--husk-fake="); ok {
			os.Exit(fakePlugin(mode))
		}
	}
	os.Exit(m.Run())
}
func fakePlugin(mode string) int {
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGTERM)
	if mode == "crash" {
		fmt.Fprint(os.Stderr, "one\n\x1b[31mtwo\x1b[0m\nthree\nlast\n")
		return 7
	}
	if mode == "legacy" {
		go func() {
			for range sigs {
			}
		}()
		fmt.Fprintln(os.Stdout, `{"type":"hello","hello":{"plugin_id":"fake"}}`)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var head struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(scanner.Bytes(), &head)
			if head.Type == "protocol_error" {
				fmt.Fprintln(os.Stderr, "saw legacy rejection")
				return 0
			}
		}
		return 4
	}
	if mode == "ignore" {
		go func() {
			for range sigs {
			}
		}()
	} else {
		go func() { <-sigs; fmt.Fprintln(os.Stderr, "got SIGTERM"); os.Exit(0) }()
	}
	writer := protocol.NewWriter(os.Stdout)
	send := func(id int64, value any) {
		wire, _ := protocol.NewIntID(id)
		data, _ := json.Marshal(value)
		_ = writer.Send(protocol.Message{ID: &wire, Result: data})
	}
	if mode == "env" {
		pwd, _ := os.Getwd()
		pgid, _ := syscall.Getpgid(0)
		env := make(map[string]string)
		for _, v := range os.Environ() {
			k, val, ok := strings.Cut(v, "=")
			if ok && (strings.HasPrefix(k, "HUSK_") || k == "CUSTOM_KEEP") {
				env[k] = val
			}
		}
		send(9000, map[string]any{"env": env, "cwd": pwd, "pid": os.Getpid(), "pgid": pgid})
	} else {
		send(9000, map[string]bool{"ready": true})
	}
	if mode == "clean" {
		return 0
	}
	if mode == "grandchild" {
		child := exec.Command(os.Args[0], "-test.run=^$", "--husk-fake=ignore")
		child.Env = os.Environ()
		out, err := child.StdoutPipe()
		if err != nil {
			return 8
		}
		if err := child.Start(); err != nil {
			return 9
		}
		if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
			return 10
		}
		_ = out.Close()
		send(9002, map[string]int{"child": child.Process.Pid})
		return 0 // the Wrapper must also clean children without inherited stdio
	}
	if mode == "eof" {
		_ = os.Stdout.Close()
		select {}
	}
	if mode == "ignore" {
		select {}
	}
	if mode == "closed-stdin" {
		_ = os.Stdin.Close()
		select {}
	}
	if mode == "malformed" {
		fmt.Fprintln(os.Stdout, `{"id":41,"result":{},"error":{"code":"failed","message":"x"}}`)
	}
	if mode == "nonreply" || mode == "malformed" {
		fmt.Fprintln(os.Stdout, `{"method":"surface.update","params":{}}`)
	}
	var opening []string
	_ = protocol.ReadLoop(os.Stdin, func(m protocol.Message) {
		if m.ID != nil {
			_ = writer.Send(protocol.Message{ID: m.ID, Result: m.Params})
			return
		}
		if m.Method == protocol.MethodProtocolError {
			send(42, json.RawMessage(m.Params))
			return
		}
		opening = append(opening, m.Method)
		if len(opening) == 3 {
			send(9001, opening)
		}
	}, nil)
	return 0
}

type replyNotice struct {
	id    string
	reply protocol.Message
}
type fixtureSink struct{ events chan tea.Msg }

func (s fixtureSink) Reply(id string, m protocol.Message) { s.events <- replyNotice{id, m} }
func (s fixtureSink) Closed(id string, err error)         { s.events <- ClosedMsg{PluginID: id, Reason: err} }

type fixture struct {
	w       *Wrapper
	clock   *testclock.Clock
	events  chan tea.Msg
	notices []tea.Msg
}

func newFixture(t *testing.T, mode string, alter func(*Options)) *fixture {
	t.Helper()
	clock := testclock.New()
	events := make(chan tea.Msg, 1024)
	dir := t.TempDir()
	opts := Options{Clock: clock, Post: func(m tea.Msg) { events <- m }, Replies: fixtureSink{events}, LogPath: func(string) (string, error) { return filepath.Join(dir, "fake.log"), nil }, BaseEnv: []string{"PATH=" + os.Getenv("PATH"), "GORACE=atexit_sleep_ms=0", "HUSK_FAKE_PLUGIN=drop", "HUSK_DEBUG=drop", "HUSK_PLUGIN_TOKEN=drop", "CUSTOM_KEEP=present"}, SocketPath: filepath.Join(dir, "instance.sock"), InstanceID: "instance", BinPath: os.Args[0]}
	if alter != nil {
		alter(&opts)
	}
	w, err := New(manifest.Spec{ID: "fake", Protocol: protocol.Major, Command: []string{os.Args[0], "-test.run=^$", "--husk-fake=" + mode}, WorkDir: dir}, opts)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{w: w, clock: clock, events: events}
	t.Cleanup(func() {
		w.mu.Lock()
		started := w.started
		w.mu.Unlock()
		if !started {
			w.cancel()
			return
		}
		w.MarkShutdown()
		w.CloseStdin()
		_ = w.Terminate()
		_ = w.Kill()
		<-w.Done()
	})
	return f
}
func openingMessages() []protocol.Message {
	return []protocol.Message{{Method: protocol.MethodThemeChanged, Params: json.RawMessage(`{"palette":{}}`)}, {Method: protocol.MethodSurfaceResized, Params: json.RawMessage(`{"surfaces":[]}`)}, {Method: protocol.MethodInputKeyState, Params: json.RawMessage(`{"phase":"sync","key":"","held":false,"modifiers":{}}`)}}
}
func (f *fixture) start(t *testing.T) {
	t.Helper()
	if err := f.w.Start(openingMessages()); err != nil {
		t.Fatal(err)
	}
	t.Logf("owned fake plugin pid %d", f.w.Info().PID)
}
func awaitNotice[T any](t *testing.T, f *fixture) T {
	t.Helper()
	for {
		m := <-f.events
		f.notices = append(f.notices, m)
		if v, ok := m.(T); ok {
			return v
		}
	}
}
func awaitReply(t *testing.T, f *fixture, id string) protocol.Message {
	t.Helper()
	for {
		v := awaitNotice[replyNotice](t, f)
		if v.reply.ID != nil && v.reply.ID.String() == id {
			return v.reply
		}
	}
}
func closedCount(f *fixture) int {
	n := 0
	for _, m := range f.notices {
		if _, ok := m.(ClosedMsg); ok {
			n++
		}
	}
	return n
}

func TestLaunchEnvironmentProcessGroupAndOpeningOrder(t *testing.T) {
	f := newFixture(t, "env", nil)
	if !errors.Is(f.w.Send(protocol.Message{}), ErrNotLive) {
		t.Fatal("pre-launch send succeeded")
	}
	f.start(t)
	m := awaitReply(t, f, "9000")
	var v struct {
		Env       map[string]string `json:"env"`
		Cwd       string            `json:"cwd"`
		PID, PGID int
	}
	if err := json.Unmarshal(m.Result, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Env) != 7 || v.Env["HUSK_FAKE_PLUGIN"] != "" || v.Env["HUSK_DEBUG"] != "" || v.Env["HUSK_PLUGIN_TOKEN"] != f.w.Token() {
		t.Fatal("inherited Husk variables leaked or token was not replaced")
	}
	if v.Env["HUSK_PROTOCOL_VERSION"] != protocol.Version || v.Env["CUSTOM_KEEP"] != "present" || v.Cwd != f.w.spec.WorkDir || v.PID != v.PGID || v.PID != f.w.Info().PID {
		t.Fatal("environment, workdir or process group mismatch")
	}
	order := awaitReply(t, f, "9001")
	if string(order.Result) != `["theme.changed","surface.resized","input.key_state"]` {
		t.Fatal(string(order.Result))
	}
	id, _ := protocol.NewIntID(8)
	if err := f.w.Send(protocol.Message{ID: &id, Method: "sample.echo", Params: json.RawMessage(`{"value":7}`)}); err != nil {
		t.Fatal(err)
	}
	if got := awaitReply(t, f, "8"); !bytes.Equal(got.Result, []byte(`{"value":7}`)) {
		t.Fatal(string(got.Result))
	}
	st, err := os.Stat(f.w.Info().LogPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("stderr log mode is not 0600", err)
	}
	if f.w.Start(nil) != ErrAlreadyStarted {
		t.Fatal("wrapper could be relaunched")
	}
}

func TestMalformedReplySalvagedAndNonReplyRejected(t *testing.T) {
	f := newFixture(t, "malformed", nil)
	f.start(t)
	bad := awaitReply(t, f, "41")
	if bad.Result != nil || bad.Error != nil || bad.Method != "" {
		t.Fatal("malformed reply was not a bare-id salvage")
	}
	notice := awaitReply(t, f, "42")
	var p protocol.ProtocolErrorParams
	if err := json.Unmarshal(notice.Result, &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != protocol.CodeInvalidMessage || p.Message != "stdout carries replies only" || p.Method != protocol.MethodSurfaceUpdate {
		t.Fatal(p)
	}
	if f.w.Info().Status != Live {
		t.Fatal("non-reply killed the plugin")
	}
}

func TestLegacyHelloGetsOneLegacyLineThenFails(t *testing.T) {
	f := newFixture(t, "legacy", nil)
	f.start(t)
	exit := awaitNotice[ExitMsg](t, f)
	if exit.Info.Status != Failed || !strings.Contains(exit.Info.LastError, "unsupported protocol major 1") || closedCount(f) != 1 {
		t.Fatalf("exit=%+v closed=%d", exit, closedCount(f))
	}
	data, err := os.ReadFile(exit.Info.LogPath)
	if err != nil || strings.Count(string(data), "saw legacy rejection") != 1 {
		t.Fatal("legacy rejection was not delivered exactly once", err)
	}
}

func TestStdoutEOFFailsOnlyAfterInjectedReapGrace(t *testing.T) {
	f := newFixture(t, "eof", nil)
	f.start(t)
	awaitReply(t, f, "9000")
	f.clock.Await(EOFReapGrace)
	f.clock.Advance(EOFReapGrace - 1)
	select {
	case <-f.w.Done():
		t.Fatal("EOF killed before reap grace")
	default:
	}
	f.clock.Advance(1)
	exit := awaitNotice[ExitMsg](t, f)
	if exit.Info.Status != Failed || !strings.Contains(exit.Info.LastError, ErrStdoutClosed.Error()) || closedCount(f) != 1 {
		t.Fatalf("%+v", exit)
	}
}

func TestCleanAndCrashExitsRouteClosedBeforeLiveness(t *testing.T) {
	for _, mode := range []string{"clean", "crash"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, mode, nil)
			f.start(t)
			exit := awaitNotice[ExitMsg](t, f)
			if closedCount(f) != 1 {
				t.Fatal("Closed was not posted exactly once before Exit")
			}
			if mode == "clean" && exit.Info.Status != Exited {
				t.Fatal(exit)
			}
			if mode == "crash" && (exit.Info.Status != Failed || strings.Join(exit.Info.StderrTail, ",") != "two,three,last") {
				t.Fatal(exit)
			}
			if !errors.Is(f.w.Send(protocol.Message{}), ErrNotLive) {
				t.Fatal("post-exit send accepted")
			}
		})
	}
}

func TestStdinOverflowAndTimeoutAreFatal(t *testing.T) {
	for _, mode := range []string{"overflow", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, "ignore", func(o *Options) { o.QueueCapacity = 1 })
			if err := f.w.Start(nil); err != nil {
				t.Fatal(err)
			}
			t.Logf("owned fake plugin pid %d", f.w.Info().PID)
			awaitReply(t, f, "9000")
			big := protocol.Message{Method: "sample.frame", Params: json.RawMessage(`{"padding":"` + strings.Repeat("x", 2<<20) + `"}`)}
			if mode == "overflow" {
				var err error
				for i := 0; i < 5; i++ {
					if err = f.w.Send(big); err != nil {
						break
					}
				}
				if !errors.Is(err, protocol.ErrSendQueueFull) {
					t.Fatal("expected queue overflow", err)
				}
			} else {
				if err := f.w.Send(big); err != nil {
					t.Fatal(err)
				}
				f.clock.Await(WriteTimeout)
				f.clock.Advance(WriteTimeout)
			}
			awaitNotice[ClosedMsg](t, f)
			f.clock.Await(StopGrace)
			f.clock.Advance(StopGrace)
			exit := awaitNotice[ExitMsg](t, f)
			if exit.Info.Status != Failed {
				t.Fatal(exit)
			}
			want := "send queue full"
			if mode == "timeout" {
				want = "write timed out"
			}
			if !strings.Contains(exit.Info.LastError, want) {
				t.Fatal(exit.Info.LastError)
			}
		})
	}
}

func TestShortCleanExitWithPendingOpeningWriteRemainsExited(t *testing.T) {
	f := newFixture(t, "clean", nil)
	opening := openingMessages()
	opening[0].Params = json.RawMessage(`{"padding":"` + strings.Repeat("x", 2<<20) + `"}`)
	if err := f.w.Start(opening); err != nil {
		t.Fatal(err)
	}
	t.Logf("owned fake plugin pid %d", f.w.Info().PID)
	exit := awaitNotice[ExitMsg](t, f)
	if exit.Info.Status != Exited || exit.Info.LastError != "" {
		t.Fatal("pipe-close race misclassified a clean exit", exit.Info.Status, exit.Info.LastError)
	}
}

func TestClosedStdinReapsBeforeTreatingSurvivorAsFatal(t *testing.T) {
	f := newFixture(t, "closed-stdin", nil)
	f.start(t)
	awaitReply(t, f, "9000")
	large := protocol.Message{Method: "sample.frame", Params: json.RawMessage(`{"padding":"` + strings.Repeat("x", 2<<20) + `"}`)}
	if err := f.w.Send(large); err != nil {
		t.Fatal(err)
	}
	awaitNotice[ClosedMsg](t, f) // immediate cancellation before the reap timer
	f.clock.Await(EOFReapGrace)
	f.clock.Advance(EOFReapGrace)
	exit := awaitNotice[ExitMsg](t, f)
	if exit.Info.Status != Failed || !errors.Is(exit.Err, syscall.EPIPE) {
		t.Fatal(exit)
	}
}

func TestCleanExitDrainsFinalReplyBeforeClosed(t *testing.T) {
	f := newFixture(t, "clean", nil)
	if err := f.w.Start(nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("owned fake plugin pid %d", f.w.Info().PID)
	awaitNotice[ExitMsg](t, f)
	sawReply := false
	for _, notice := range f.notices {
		if _, ok := notice.(replyNotice); ok {
			sawReply = true
		}
		if _, ok := notice.(ClosedMsg); ok && !sawReply {
			t.Fatal("Closed overtook a buffered final reply")
		}
	}
	if !sawReply {
		t.Fatal("final reply dropped")
	}
}

func TestShutdownSIGTERMThenSIGKILLIsNormalExit(t *testing.T) {
	f := newFixture(t, "ignore", nil)
	f.start(t)
	awaitReply(t, f, "9000")
	f.w.MarkShutdown()
	f.w.CloseStdin()
	if err := f.w.Terminate(); err != nil {
		t.Fatal(err)
	}
	// The shared Host deadline calls Kill; no real timer or sleep is needed.
	if err := f.w.Kill(); err != nil {
		t.Fatal(err)
	}
	exit := awaitNotice[ExitMsg](t, f)
	if exit.Info.Status != Exited || !exit.Info.Shutdown || exit.Info.LastError != "" {
		t.Fatal(exit)
	}
}

func TestExitedLeaderDoesNotAbandonSIGTERMResistantChild(t *testing.T) {
	f := newFixture(t, "grandchild", nil)
	f.start(t)
	m := awaitReply(t, f, "9002")
	var payload struct {
		Child int `json:"child"`
	}
	if err := json.Unmarshal(m.Result, &payload); err != nil {
		t.Fatal(err)
	}
	group, err := syscall.Getpgid(payload.Child)
	if err != nil || group != f.w.Info().PID {
		t.Fatal("child escaped the managed group", err)
	}
	f.clock.Await(StopGrace)
	select {
	case <-f.w.Done():
		t.Fatal("leader exit abandoned a live child")
	default:
	}
	f.clock.Advance(StopGrace)
	exit := awaitNotice[ExitMsg](t, f)
	if exit.Info.Status != Exited {
		t.Fatal("clean leader exit misclassified", exit.Info.Status)
	}
}

func TestLaunchFailurePostsClosedOnceAndKeepsStoreOwnedLog(t *testing.T) {
	f := newFixture(t, "echo", nil)
	f.w.spec.Command = []string{filepath.Join(t.TempDir(), "missing")}
	if err := f.w.Start(nil); err == nil {
		t.Fatal("missing executable launched")
	}
	msg := awaitNotice[LaunchFailedMsg](t, f)
	if closedCount(f) != 1 || f.w.Info().Status != Failed {
		t.Fatal("launch failure ordering/liveness")
	}
	if _, err := os.Stat(msg.LogPath); err != nil {
		t.Fatal("wrapper deleted Store-owned log", err)
	}
}

var _ io.Reader = (*firstLineReader)(nil)
