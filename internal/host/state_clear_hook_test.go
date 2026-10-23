package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/internal/stem/hooks"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// steppedClock fires timers only when Advance moves past their due time, so
// a test controls every deadline without sleeping.
type steppedClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*stepTimer
}

type stepTimer struct {
	c       *steppedClock
	due     time.Time
	fn      func()
	stopped bool
}

func (t *stepTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

func (c *steppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppedClock) AfterFunc(d time.Duration, f func()) protocol.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &stepTimer{c: c, due: c.now.Add(d), fn: f}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves time forward and runs every timer that came due.
func (c *steppedClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*stepTimer
	rest := c.timers[:0]
	for _, t := range c.timers {
		switch {
		case t.stopped:
		case !t.due.After(c.now):
			t.stopped = true
			due = append(due, t)
		default:
			rest = append(rest, t)
		}
	}
	c.timers = rest
	c.mu.Unlock()
	for _, t := range due {
		t.fn()
	}
}

// clearPlugin writes a v2 manifest for a plugin that owns its <id>.state
// slice and subscribes to the given hooks.
func clearPlugin(t *testing.T, id string, subscribe ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	subs := make([]any, len(subscribe))
	for i, s := range subscribe {
		subs[i] = s
	}
	raw, _ := json.Marshal(map[string]any{
		"manifest_version": 2, "protocol": 2, "id": id, "name": id, "version": "1",
		"command":  []string{"./plugin"},
		"surfaces": []any{map[string]any{"id": "sidebar-list"}},
		"slice":    map[string]any{"scope": "instance", "persistent": false},
		"hooks":    map[string]any{"subscribe": subs},
	})
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// hookCalls counts the hook.call requests the fake process was sent.
func (p *fakeProc) hookCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, m := range p.sent {
		if m.Method == protocol.MethodHookCall {
			n++
		}
	}
	return n
}

// clearHost boots a host over the given plugins on clock and waits until all
// of them are live.
func clearHost(t *testing.T, f *fakes, clock protocol.Clock, logger func(level, msg string), dirs ...string) *harness {
	t.Helper()
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) {
		c.Plugins = dirs
		c.Clock = clock
	}, withHook(hook), WithFactory(f.factory), WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	var ids []string
	for range dirs {
		ids = append(ids, <-f.started)
	}
	for _, id := range ids {
		if got := h.podStatus(id); got != "live" {
			t.Fatalf("%s status = %q", id, got)
		}
	}
	return h
}

func setSlice(t *testing.T, h *harness, token, slice string, value map[string]any) {
	t.Helper()
	m := rpc(t, h, token, protocol.MethodStateSet, map[string]any{"slice": slice, "value": value})
	if m.Error != nil {
		t.Fatalf("state.set: %+v", m.Error)
	}
}

func sliceValue(t *testing.T, h *harness, slice string) string {
	t.Helper()
	m := rpc(t, h, "", protocol.MethodStateGet, map[string]any{"slice": slice})
	if m.Error != nil {
		t.Fatalf("state.get: %+v", m.Error)
	}
	var res struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatal(err)
	}
	return string(res.Value)
}

func TestStateClearVetoAndForceOverTheSocket(t *testing.T) {
	f := newFakes()
	responders{"notes": func(lp legParams) any {
		if lp.Hook != "husk.before-state-clear" || lp.Kind != "gate" ||
			lp.Data["slice"] != "notes.state" || lp.Data["owner"] != "notes" || len(lp.Data) != 2 {
			return map[string]any{"veto": true, "reason": "unexpected leg " + lp.Hook}
		}
		return map[string]any{"veto": true, "reason": "unsaved edits"}
	}}.install(f)
	h := clearHost(t, f, frozenClock{time.Unix(1000, 0)}, nil, clearPlugin(t, "notes", "husk.before-state-clear"))
	setSlice(t, h, "tok-notes", "notes.state", map[string]any{"n": 1})

	m := rpc(t, h, "", protocol.MethodStateClear, map[string]any{"namespace": "notes.state"})
	if m.Error == nil || m.Error.Code != protocol.CodeDenied {
		t.Fatalf("clear reply = %+v, want denied", m)
	}
	var data struct {
		Reason string `json:"reason"`
		Vetoes []struct {
			Plugin string `json:"plugin_id"`
			Reason string `json:"reason"`
		} `json:"vetoes"`
	}
	if err := json.Unmarshal(m.Error.Data, &data); err != nil {
		t.Fatalf("data %s: %v", m.Error.Data, err)
	}
	if data.Reason != "vetoed" || len(data.Vetoes) != 1 || data.Vetoes[0].Plugin != "notes" || data.Vetoes[0].Reason != "unsaved edits" {
		t.Fatalf("data = %s", m.Error.Data)
	}
	if got := sliceValue(t, h, "notes.state"); got != `{"n":1}` {
		t.Fatalf("vetoed clear changed the slice: %s", got)
	}
	if n := f.proc("notes").hookCalls(); n != 1 {
		t.Fatalf("plugin was asked %d times, want 1", n)
	}

	m = rpc(t, h, "", protocol.MethodStateClear, map[string]any{"namespace": "notes.state", "force": true})
	if m.Error != nil {
		t.Fatalf("forced clear: %+v", m.Error)
	}
	if got := sliceValue(t, h, "notes.state"); got != `{}` {
		t.Fatalf("forced clear left the slice at %s", got)
	}
	if n := f.proc("notes").hookCalls(); n != 1 {
		t.Fatalf("force asked the plugin again: %d hook.call requests", n)
	}
}

func TestStateClearSilentSubscribersCostOneLegTimeout(t *testing.T) {
	f := newFakes()
	sent := make(chan string, 4)
	f.onSend = func(p *fakeProc, m protocol.Message) {
		if m.Method == protocol.MethodHookCall {
			sent <- p.id
		}
	}
	clock := &steppedClock{now: time.Unix(1000, 0)}
	var mu sync.Mutex
	var lines []string
	logger := func(level, msg string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, level+" "+msg)
	}
	h := clearHost(t, f, clock, logger,
		clearPlugin(t, "notes", "husk.before-state-clear"),
		clearPlugin(t, "other", "husk.before-state-clear"))
	setSlice(t, h, "tok-notes", "notes.state", map[string]any{"n": 1})

	reply := make(chan protocol.Message, 1)
	go func() {
		reply <- rpc(t, h, "", protocol.MethodStateClear, map[string]any{"namespace": "notes.state"})
	}()
	<-sent
	<-sent
	h.loop(func() {}) // the Update that sent the legs has armed the sweep
	// The Calls table sweeps every SweepInterval, so walk the clock in sweep
	// steps: seven of them reach 1.75 s, short of the 2 s leg timeout.
	step := func() {
		clock.Advance(calls.SweepInterval)
		h.loop(func() {}) // the sweep message has run and the next sweep is armed
	}
	for range 7 {
		step()
	}
	select {
	case m := <-reply:
		t.Fatalf("clear answered before the leg timeout: %+v", m)
	default:
	}
	if got := sliceValue(t, h, "notes.state"); got != `{"n":1}` {
		t.Fatalf("slice changed before the timeout: %s", got)
	}
	step() // 2 s: both legs expire together
	m := <-reply
	if m.Error != nil {
		t.Fatalf("clear after the timeout: %+v", m.Error)
	}
	if got := sliceValue(t, h, "notes.state"); got != `{}` {
		t.Fatalf("slice = %s after the clear", got)
	}
	mu.Lock()
	defer mu.Unlock()
	var logged int
	for _, l := range lines {
		if strings.Contains(l, "husk.before-state-clear") && strings.Contains(l, "timeout") {
			logged++
		}
	}
	if logged != 2 {
		t.Fatalf("%d timeout lines for 2 silent subscribers; log:\n%s", logged, strings.Join(lines, "\n"))
	}
}

func TestHookFirerReturns(t *testing.T) {
	f := newFakes()
	responders{"sub": func(legParams) any { return map[string]any{} }}.install(f)
	h := clearHost(t, f, frozenClock{time.Unix(1000, 0)}, nil,
		clearPlugin(t, "sub", "husk.before-state-clear"))
	fire := hookFirer{h.root}
	data := map[string]any{"slice": "sub.state", "owner": "sub"}

	t.Run("the outcome", func(t *testing.T) {
		o, err := fire.Fire(t.Context(), "husk.before-state-clear", data)
		if err != nil || !o.Allowed || len(o.Results) != 1 || o.Results[0].Plugin != "sub" {
			t.Fatalf("outcome = %+v, err = %v", o, err)
		}
	})
	t.Run("a refusal of the runner comes back as a HookError", func(t *testing.T) {
		for hook, code := range map[string]protocol.Code{
			"husk.unknown": protocol.CodeNotFound,
			"sub.nothing":  protocol.CodeNotFound,
			"not a hook":   protocol.CodeInvalidParams,
		} {
			_, err := fire.Fire(t.Context(), hook, data)
			var he *hooks.HookError
			if !errors.As(err, &he) || he.Code != code {
				t.Fatalf("%q: err = %v, want a HookError %s", hook, err, code)
			}
		}
	})
	t.Run("busy once the in-flight cap is reached", func(t *testing.T) {
		silent := newFakes()
		sent := make(chan struct{}, 64)
		silent.onSend = func(_ *fakeProc, m protocol.Message) {
			if m.Method == protocol.MethodHookCall {
				sent <- struct{}{}
			}
		}
		hs := clearHost(t, silent, frozenClock{time.Unix(1000, 0)}, nil, clearPlugin(t, "quiet", "husk.before-state-clear"))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var refusal error
	fill:
		for range 64 {
			errc := make(chan error, 1)
			go func() {
				_, err := hookFirer{hs.root}.Fire(ctx, "husk.before-state-clear", data)
				errc <- err
			}()
			select {
			case <-sent: // in flight, silent
			case refusal = <-errc:
				break fill
			}
		}
		var he *hooks.HookError
		if !errors.As(refusal, &he) || he.Code != protocol.CodeBusy {
			t.Fatalf("refusal = %v, want busy before 64 calls", refusal)
		}
	})
	t.Run("cancelled wraps the context error when it ends first", func(t *testing.T) {
		silent := newFakes()
		sent := make(chan struct{}, 1)
		silent.onSend = func(_ *fakeProc, m protocol.Message) {
			if m.Method == protocol.MethodHookCall {
				sent <- struct{}{}
			}
		}
		hs := clearHost(t, silent, frozenClock{time.Unix(1000, 0)}, nil, clearPlugin(t, "quiet", "husk.before-state-clear"))
		ctx, cancel := context.WithCancel(t.Context())
		errc := make(chan error, 1)
		go func() {
			_, err := hookFirer{hs.root}.Fire(ctx, "husk.before-state-clear", data)
			errc <- err
		}()
		<-sent
		cancel()
		err := <-errc
		var he *hooks.HookError
		if !errors.As(err, &he) || he.Code != protocol.CodeCancelled || !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want cancelled wrapping context.Canceled", err)
		}
	})
	t.Run("unavailable when the loop is gone", func(t *testing.T) {
		h.shutdown()
		_, err := fire.Fire(t.Context(), "husk.before-state-clear", data)
		var he *hooks.HookError
		if !errors.As(err, &he) || he.Code != protocol.CodeUnavailable {
			t.Fatalf("err = %v, want unavailable", err)
		}
	})
}
