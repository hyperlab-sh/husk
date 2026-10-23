package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}
type fakeTimer struct {
	clock   *fakeClock
	at      time.Time
	ch      chan time.Time
	stopped bool
}

func newClock() *fakeClock          { return &fakeClock{now: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	if d <= 0 {
		t.ch <- c.now
		t.stopped = true
	}
	return t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, t := range c.timers {
		if !t.stopped && !c.now.Before(t.at) {
			t.ch <- c.now
			t.stopped = true
		}
	}
}
func (t *fakeTimer) C() <-chan time.Time { return t.ch }
func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	old := t.stopped
	t.stopped = true
	return !old
}

type testEnv struct {
	root  string
	clock *fakeClock
	posts chan func()
}

func fixture(t *testing.T) *testEnv {
	t.Helper()
	return &testEnv{root: t.TempDir(), clock: newClock(), posts: make(chan func(), 128)}
}

// runtime returns the fixture's private runtime directory, created on first
// use: the opener's job, since Store creates none.
func (e *testEnv) runtime() string {
	path := filepath.Join(e.root, "runtime")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if err := privateDir(path); err != nil {
			panic(err)
		}
	}
	return path
}
func (e *testEnv) opts(name string) Options {
	return Options{Instance: InstanceRef{ID: name}, RuntimeDir: e.runtime(), Clock: e.clock, Env: Env{LookupEnv: func(k string) (string, bool) {
		switch k {
		case "XDG_CONFIG_HOME":
			return filepath.Join(e.root, "config"), true
		case "XDG_STATE_HOME":
			return filepath.Join(e.root, "state"), true
		case "XDG_DATA_HOME":
			return filepath.Join(e.root, "data"), true
		}
		return "", false
	}, HomeDir: func() (string, error) { return e.root, nil }}, Post: func(f func()) bool {
		select {
		case e.posts <- f:
			return true
		default:
			return false
		}
	}}
}
func (e *testEnv) open(t *testing.T, name string, change func(*Options)) *Store {
	t.Helper()
	opts := e.opts(name)
	if change != nil {
		change(&opts)
	}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if err = s.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}
func attach(t *testing.T, s *Store) *state.State {
	t.Helper()
	st := state.New(state.Options{Hydrator: s, WorkspaceOwner: "plugin"})
	if err := s.Attach(st); err != nil {
		t.Fatal(err)
	}
	return st
}
func slice(t *testing.T, st *state.State, scope state.Scope, r state.Retention) *state.Handle {
	t.Helper()
	h, err := st.Register(state.Spec{Namespace: "plugin.state", Owner: "plugin", Scope: scope, Persistent: true, Retention: r})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func set(t *testing.T, h *state.Handle, value string) {
	t.Helper()
	if _, err := h.Set([]byte(value)); err != nil {
		t.Fatal(err)
	}
}
func syncDue(t *testing.T, s *Store) {
	t.Helper()
	r := flushRequest{ctx: context.Background(), done: make(chan error, 1)}
	s.barriers <- r
	if err := <-r.done; err != nil {
		t.Fatal(err)
	}
}
func stored(t *testing.T, s *Store, spec state.Spec, file string) envelope {
	t.Helper()
	data, _, err := s.source.Get(context.Background(), s.sliceKey(spec, file))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err = json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	return env
}
func writeConfig(t *testing.T, s *Store, parts []string, data string) {
	t.Helper()
	p, err := pathInside(s.paths.Config, parts, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(context.Background(), p, []byte(data)); err != nil {
		t.Fatal(err)
	}
}
func drainPosts(e *testEnv) {
	for {
		select {
		case f := <-e.posts:
			f()
		default:
			return
		}
	}
}

func TestOpenPathsPermissionsAndLifecycle(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	for _, p := range []string{s.Paths().Config, s.Paths().State, s.Paths().Data, s.RuntimeDir()} {
		info, err := os.Lstat(p)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("%s: %v, %v", p, info, err)
		}
	}
	f, err := s.OpenPluginLog("example.notes")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	name := f.Name()
	f.Close()
	f, err = s.OpenPluginLog("example.notes")
	if err != nil {
		t.Fatal(err)
	}
	if f.Name() == name {
		t.Fatal("log overwritten")
	}
	f.Close()
	if _, err = s.OpenPluginLog("../bad"); err == nil {
		t.Fatal("unsafe plugin log")
	}
	bad := e.opts("Bad_Name")
	bad.RuntimeDir = filepath.Join(e.root, "untouched")
	if _, err = Open(bad); err == nil {
		t.Fatal("invalid id accepted")
	}
	if _, err := os.Lstat(bad.RuntimeDir); !os.IsNotExist(err) {
		t.Fatal("invalid id touched the runtime directory")
	}
	st := attach(t, s)
	if err = s.Attach(st); err == nil {
		t.Fatal("attached twice")
	}
	h := slice(t, st, state.ScopeInstance, state.Retention{Policy: state.KeepLatest})
	set(t, h, `{"n":1}`)
	st.Flush()
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entry := st.Snapshot().Slices[h.Namespace()]
	env := stored(t, s, entry.Spec, "current.json")
	if string(env.Value) != `{"n":1}` {
		t.Fatal(string(env.Value))
	}
}

func TestHealthUsesLoopPostAndBoundsDiagnostics(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	st := attach(t, s)
	before := st.Snapshot()
	for i := 0; i < 60; i++ {
		s.diagnostic(Problem{Code: "test", Message: "problem"}, "")
	}
	if st.Snapshot() != before {
		t.Fatal("diagnostic wrote State without posting")
	}
	drainPosts(e)
	var health health
	if err := json.Unmarshal(st.Snapshot().Slices[state.StoreNamespace].Value, &health); err != nil {
		t.Fatal(err)
	}
	if len(health.Diagnostics) != 50 {
		t.Fatal(len(health.Diagnostics))
	}
}
