package pods

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type fakeLoop struct {
	events       chan tea.Msg
	statuses     []LivenessMsg
	logs         []LogMsg
	prepareCalls int
	prepared     []manifest.Spec
	result       Preparation
}

func (l *fakeLoop) Post(m tea.Msg) { l.events <- m }
func (l *fakeLoop) Opening(string) []protocol.Message {
	return []protocol.Message{{Method: protocol.MethodThemeChanged}, {Method: protocol.MethodSurfaceResized}, {Method: protocol.MethodInputKeyState}}
}
func (l *fakeLoop) Prepare(ctx context.Context, specs []manifest.Spec) Preparation {
	l.prepareCalls++
	l.prepared = specs
	return l.result
}
func (l *fakeLoop) Liveness(m LivenessMsg) { l.statuses = append(l.statuses, m) }
func (l *fakeLoop) Log(m LogMsg)           { l.logs = append(l.logs, m) }

type fakeProcess struct {
	mu                                 sync.Mutex
	id                                 string
	info                               wrapper.Info
	opts                               wrapper.Options
	sent                               []protocol.Message
	opening                            []protocol.Message
	done                               chan struct{}
	once                               sync.Once
	ignore                             bool
	marked, closed, terminated, killed bool
	term                               chan struct{}
	startErr                           error
	stubborn, closeEarly               bool
	termStep                           func()
}

func (p *fakeProcess) PluginID() string { return p.id }
func (p *fakeProcess) Token() string    { return "test-token-" + p.id }
func (p *fakeProcess) Start(open []protocol.Message) error {
	p.opening = open
	if p.startErr != nil {
		p.opts.Replies.Closed(p.id, p.startErr)
		p.opts.Post(LaunchFailedMsg{PluginID: p.id, Err: p.startErr})
		p.once.Do(func() { close(p.done) })
		return p.startErr
	}
	p.info.Status = wrapper.Live
	if p.closeEarly {
		p.opts.Replies.Closed(p.id, errors.New("immediate exit"))
	}
	return nil
}
func (p *fakeProcess) Info() wrapper.Info { p.mu.Lock(); defer p.mu.Unlock(); return p.info }
func (p *fakeProcess) Send(m protocol.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, m)
	return nil
}
func (p *fakeProcess) MarkShutdown() { p.mu.Lock(); p.marked = true; p.mu.Unlock() }
func (p *fakeProcess) CloseStdin()   { p.mu.Lock(); p.closed = true; p.mu.Unlock() }
func (p *fakeProcess) Terminate() error {
	p.mu.Lock()
	p.terminated = true
	marked, closed := p.marked, p.closed
	step := p.termStep
	p.mu.Unlock()
	if !marked || !closed {
		panic("SIGTERM before shutdown mark/stdin close")
	}
	if step != nil {
		step()
	}
	select {
	case <-p.term:
	default:
		close(p.term)
	}
	if !p.ignore {
		p.once.Do(func() { close(p.done) })
	}
	return nil
}
func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	if !p.stubborn {
		p.once.Do(func() { close(p.done) })
	}
	return nil
}
func (p *fakeProcess) Done() <-chan struct{} { return p.done }

func hostFixture(t *testing.T) (*Host, *fakeLoop, *testclock.Clock, map[string]*fakeProcess) {
	t.Helper()
	loop := &fakeLoop{events: make(chan tea.Msg, 128)}
	clock := testclock.New()
	processes := make(map[string]*fakeProcess)
	h, err := New(loop, Options{Wrapper: wrapper.Options{Clock: clock}, Factory: func(s manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		p := &fakeProcess{id: s.ID, info: wrapper.Info{PluginID: s.ID, Status: wrapper.Starting}, opts: o, done: make(chan struct{}), term: make(chan struct{})}
		processes[s.ID] = p
		return p, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.cancel)
	return h, loop, clock, processes
}
func addPod(t *testing.T, h *Host, id string) *Pod {
	t.Helper()
	p, err := h.Add(manifest.Spec{ID: id, Protocol: protocol.Major, Command: []string{"plugin"}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func prepareHost(t *testing.T, h *Host) {
	t.Helper()
	cmd := h.Prepare()
	if cmd == nil {
		t.Fatal("missing preparation command")
	}
	if !h.Handle(cmd()) {
		t.Fatal("unhandled preparation")
	}
}

func TestHostBatchGateOpeningOutboxAndSinks(t *testing.T) {
	h, loop, _, procs := hostFixture(t)
	b := addPod(t, h, "b")
	a := addPod(t, h, "a")
	if _, err := h.Add(a.Spec()); err == nil {
		t.Fatal("duplicate id replaced first pod")
	}
	if len(procs) != 0 || a.Info().Status != wrapper.Starting || h.Send("a", protocol.Message{}) != wrapper.ErrNotLive {
		t.Fatal("launched before gate")
	}
	cmd := h.Prepare()
	if cmd == nil || h.Prepare() != nil {
		t.Fatal("duplicate preparation")
	}
	msg := cmd()
	if loop.prepareCalls != 1 || len(loop.prepared) != 2 || loop.prepared[0].ID != "a" || len(procs) != 0 {
		t.Fatal("gate did not precede launch")
	}
	h.Handle(msg)
	h.Handle(msg)
	if len(procs) != 2 || len(procs["a"].opening) != 3 || a.Info().Status != wrapper.Live {
		t.Fatal("batch not started exactly once")
	}
	if err := h.Send(calls.PluginID("a"), protocol.Message{Method: "sample.notice"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Notify("sample.target", map[string]any{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if err := h.Notify("sample.all", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if len(procs["a"].sent) != 3 || len(procs["b"].sent) != 1 || b.Info().Status != wrapper.Live {
		t.Fatal("Sink routing mismatch")
	}
	if id, ok := h.Resolve(procs["a"].Token()); !ok || id != "a" {
		t.Fatal("token not registered")
	}
	if _, ok := h.Resolve("unknown"); ok {
		t.Fatal("unknown token accepted")
	}
	copy := a.Spec()
	copy.Command[0] = "mutated"
	if a.Spec().Command[0] != "plugin" {
		t.Fatal("spec alias leaked")
	}
}

func TestReloadErrorOpensGateButFatalPluginFailureDoesNot(t *testing.T) {
	h, loop, _, procs := hostFixture(t)
	addPod(t, h, "bad")
	addPod(t, h, "good")
	loop.result = Preparation{ReloadError: errors.New("reload timed out"), Failed: map[string]error{"bad": errors.New("slice declaration failed")}}
	prepareHost(t, h)
	bad, _ := h.Pod("bad")
	good, _ := h.Pod("good")
	if procs["bad"] != nil || bad.Info().Status != wrapper.Failed || good.Info().Status != wrapper.Live || len(loop.logs) < 2 {
		t.Fatal("preparation failure policy mismatch")
	}
}

func TestClosedBridgeRevokesTokenBeforeExitLiveness(t *testing.T) {
	h, loop, _, procs := hostFixture(t)
	p := addPod(t, h, "p")
	prepareHost(t, h)
	procs["p"].opts.Replies.Closed("p", errors.New("pipe closed"))
	if _, ok := h.Resolve(procs["p"].Token()); ok {
		t.Fatal("token survived channel close")
	}
	msg := <-loop.events
	if _, ok := msg.(ClosedMsg); !ok || h.Handle(msg) {
		t.Fatal("Host consumed Calls cleanup message")
	}
	if p.Info().Status != wrapper.Live {
		t.Fatal("liveness changed before loop cleanup")
	}
	h.Handle(ExitMsg{Info: wrapper.Info{PluginID: "p", Status: wrapper.Failed, ExitStatus: "exit status 7"}})
	if p.Info().Status != wrapper.Failed {
		t.Fatal("exit liveness not applied")
	}
	procs["p"].opts.Replies.Reply("p", protocol.Message{})
	if m, ok := (<-loop.events).(calls.ReplyMsg); !ok || m.Plugin != "p" {
		t.Fatal("reply bridge did not post Calls input")
	}
}

func TestSharedShutdownDeadlineKillsAllSurvivors(t *testing.T) {
	h, _, clock, procs := hostFixture(t)
	addPod(t, h, "a")
	addPod(t, h, "b")
	prepareHost(t, h)
	procs["a"].ignore = true
	procs["b"].ignore = true
	cmd := h.Stop()
	if cmd == nil || h.Stop() != nil {
		t.Fatal("Stop not idempotent")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	<-procs["a"].term
	<-procs["b"].term
	clock.Await(wrapper.StopGrace)
	clock.Advance(wrapper.StopGrace - 1)
	for _, p := range procs {
		p.mu.Lock()
		killed := p.killed
		p.mu.Unlock()
		if killed {
			t.Fatal("killed before shared grace")
		}
	}
	clock.Advance(1)
	msg := <-result
	if m, ok := msg.(StoppedMsg); !ok || m.Err != nil {
		t.Fatal(msg)
	}
	for _, p := range procs {
		p.mu.Lock()
		all := p.marked && p.closed && p.terminated && p.killed
		p.mu.Unlock()
		if !all {
			t.Fatal("shutdown sequence incomplete")
		}
	}
}

func TestShutdownPreventsLatePreparationFromLaunching(t *testing.T) {
	h, _, _, procs := hostFixture(t)
	addPod(t, h, "p")
	cmd := h.Prepare()
	stop := h.Stop()
	h.Handle(cmd())
	stop()
	if len(procs) != 0 || h.Prepare() != nil {
		t.Fatal("launched after shutdown")
	}
	if _, err := h.Add(manifest.Spec{}); err == nil {
		t.Fatal("Add accepted after shutdown")
	}
}

func TestHostNoticesOnlyMutateLoopThroughHandle(t *testing.T) {
	h, loop, _, _ := hostFixture(t)
	addPod(t, h, "p")
	before := len(loop.statuses)
	m := LogMsg{PluginID: "p", Level: "warn", Message: "test notice"}
	loop.Post(m)
	if len(loop.logs) != 0 || len(loop.statuses) != before {
		t.Fatal("Post wrote host state/log")
	}
	h.Handle(<-loop.events)
	if !reflect.DeepEqual(loop.logs, []LogMsg{m}) {
		t.Fatal(loop.logs)
	}
}
