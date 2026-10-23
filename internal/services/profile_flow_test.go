package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// recStore records every Resolve and lets a test hold Invalidate or Resolve.
type recStore struct {
	fakeStore
	mu          sync.Mutex
	resolves    []string
	layers      []store.LayerDoc
	onInvalid   func()
	onResolve   func()
	invalidated int
}

func (s *recStore) Invalidate() {
	s.mu.Lock()
	s.invalidated++
	hook := s.onInvalid
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
}
func (s *recStore) Resolve(n store.DocName, profile string) (store.Resolved, error) {
	s.mu.Lock()
	s.resolves = append(s.resolves, profile)
	hook, resolved, err := s.onResolve, s.resolved, s.err
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return resolved, err
}
func (s *recStore) Layers(n store.DocName, _ string) ([]store.LayerDoc, error) {
	if n == "config" {
		return s.layers, nil
	}
	return nil, nil
}
func (s *recStore) Resolved() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.resolves...)
}
func (s *recStore) set(f func()) { s.mu.Lock(); f(); s.mu.Unlock() }

type rig struct {
	rt    *Runtime
	tree  *state.State
	posts chan func()
	st    *recStore
}

func newRig(t *testing.T, st *recStore, flag *string) *rig {
	t.Helper()
	tree := state.New(state.Options{})
	r := &rig{tree: tree, posts: make(chan func(), 16), st: st}
	rt, err := New(RuntimeOptions{Store: st, State: tree, ProfileFlag: flag, Post: func(f func()) bool { r.posts <- f; return true }, Clock: clock(), Crypto: &memoryCrypto{data: map[string][]byte{}}, Publisher: c.PublishFunc(func(string, any) {})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	r.rt = rt
	tree.Flush()
	return r
}

// settle runs every posted loop callback, flushes State and ends the tick, as
// the host loop does.
func (r *rig) settle() {
	for len(r.posts) > 0 {
		(<-r.posts)()
	}
	r.tree.Flush()
	r.rt.Coordinator.Tick()
}

func (r *rig) slice(t *testing.T) string {
	t.Helper()
	var v struct {
		Active string `json:"active"`
	}
	if err := json.Unmarshal(r.tree.Snapshot().Slices[state.ProfileNamespace].Value, &v); err != nil {
		t.Fatal(err)
	}
	return v.Active
}

func (r *rig) idle() bool {
	co := r.rt.Coordinator
	co.mu.Lock()
	defer co.mu.Unlock()
	return co.active == nil && co.queued == nil
}

func userLayer(raw string) store.LayerDoc {
	return store.LayerDoc{Layer: store.LayerUser, File: "/cfg/config.json", Doc: doc(raw)}
}

func problemsOf(reports []Report) []Problem {
	var out []Problem
	for _, r := range reports {
		out = append(out, r.Problems...)
	}
	return out
}

func TestProfileSelectionAtStartup(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name     string
		profiles []string
		layers   []store.LayerDoc
		flag     *string
		want     string
		code     string // a problem the flight must carry, or ""
	}{
		{"user default", []string{"work", "other"}, []store.LayerDoc{userLayer(`{"profile":{"default":"work"}}`)}, nil, "work", ""},
		{"flag beats default", []string{"work", "other"}, []store.LayerDoc{userLayer(`{"profile":{"default":"work"}}`)}, str("other"), "other", ""},
		{"no input", []string{"work"}, nil, nil, "", ""},
		{"unknown default", []string{"work"}, []store.LayerDoc{userLayer(`{"profile":{"default":"missing"}}`)}, nil, "", "unknown_profile"},
		{"unknown flag", []string{"work"}, nil, str("missing"), "", "unknown_profile"},
		{"default in another layer", []string{"work"}, []store.LayerDoc{{Layer: store.LayerProfile, File: "/cfg/p.json", Doc: doc(`{"profile":{"default":"work"}}`)}}, nil, "", "profile_default_ignored"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &recStore{fakeStore: fakeStore{profiles: tc.profiles, resolved: store.Resolved{Doc: doc(`{}`)}}, layers: tc.layers}
			r := newRig(t, st, tc.flag)
			reports, err := r.rt.Coordinator.Reload(context.Background(), ReasonStartup)
			if err != nil {
				t.Fatal(err)
			}
			if got := st.Resolved(); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("Resolve calls = %q, want exactly [%q]", got, tc.want)
			}
			if tc.code != "" {
				if _, ok := findProblem(problemsOf(reports), tc.code); !ok {
					t.Fatalf("missing %s in %+v", tc.code, problemsOf(reports))
				}
			}
			if tc.code == "unknown_profile" {
				p, _ := findProblem(problemsOf(reports), tc.code)
				if !strings.Contains(p.Message, "does not exist; using no profile") {
					t.Fatalf("message = %q", p.Message)
				}
			}
			r.settle()
			if r.slice(t) != tc.want {
				t.Fatalf("slice = %q, want %q", r.slice(t), tc.want)
			}
			// The write-back follows the selection and starts no second flight.
			if !r.idle() || len(st.Resolved()) != 1 {
				t.Fatalf("write-back started a flight: resolves=%q", st.Resolved())
			}
		})
	}
}

// The flag is an input of the startup flight only.
func TestProfileFlagIgnoredAfterStartup(t *testing.T) {
	flag := "other"
	st := &recStore{fakeStore: fakeStore{profiles: []string{"work", "other"}, resolved: store.Resolved{Doc: doc(`{}`)}}, layers: []store.LayerDoc{userLayer(`{"profile":{"default":"work"}}`)}}
	r := newRig(t, st, &flag)
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonStartup); err != nil {
		t.Fatal(err)
	}
	r.settle()
	// The user moves to work; a later CLI flight must follow the slice, never
	// the start flag again.
	got := r.use("work")
	(<-r.posts)()
	r.tree.Flush()
	r.rt.Coordinator.Tick()
	if res := <-got; res.err != nil {
		t.Fatal(res.err)
	}
	r.settle()
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonCLI); err != nil {
		t.Fatal(err)
	}
	if got := st.Resolved(); len(got) != 3 || got[0] != "other" || got[1] != "work" || got[2] != "work" {
		t.Fatalf("Resolve calls = %q, want [other work work]", got)
	}
}

func startedRig(t *testing.T) (*rig, *recStore) {
	t.Helper()
	st := &recStore{fakeStore: fakeStore{profiles: []string{"work"}, resolved: store.Resolved{Doc: doc(`{}`)}}, layers: []store.LayerDoc{userLayer(`{"profile":{"default":"work"}}`)}}
	r := newRig(t, st, nil)
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonStartup); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if r.rt.Profile.Active() != "work" {
		t.Fatal("startup did not activate work", r.rt.Profile.Active())
	}
	return r, st
}

type useResult struct {
	active  string
	reports []Report
	err     error
}

func (r *rig) use(name string) <-chan useResult {
	out := make(chan useResult, 1)
	go func() {
		active, reports, err := r.rt.Profile.Use(context.Background(), Caller{Kind: "cli"}, name)
		out <- useResult{active, reports, err}
	}()
	return out
}

func TestProfileUseSameNameRepliesAtOnceAndWritesNothing(t *testing.T) {
	r, st := startedRig(t)
	before := st.Resolved()
	active, reports, err := r.rt.Profile.Use(context.Background(), Caller{Kind: "cli"}, "work")
	if err != nil || active != "work" || len(reports) != 5 {
		t.Fatalf("Use = %q %d %v", active, len(reports), err)
	}
	if len(r.posts) != 0 || !r.idle() || len(st.Resolved()) != len(before) {
		t.Fatal("same-name Use wrote or started a flight")
	}
}

func TestProfileUseUnknownName(t *testing.T) {
	r, _ := startedRig(t)
	_, _, err := r.rt.Profile.Use(context.Background(), Caller{Kind: "cli"}, "nope")
	if reason(t, err) != "not_found/unknown_profile" || len(r.posts) != 0 {
		t.Fatalf("err = %v posts=%d", err, len(r.posts))
	}
}

func TestProfileUseNewNameWaitsForItsFlight(t *testing.T) {
	r, st := startedRig(t)
	before := len(st.Resolved())
	got := r.use("")
	post := <-r.posts
	post()
	r.tree.Flush()
	r.rt.Coordinator.Tick()
	res := <-got
	if res.err != nil || res.active != "" || len(res.reports) != 5 {
		t.Fatalf("Use = %+v", res)
	}
	if resolves := st.Resolved(); len(resolves) != before+1 || resolves[before] != "" {
		t.Fatalf("Resolve calls = %q, want exactly one more, for no profile", resolves)
	}
	r.settle()
	if r.rt.Profile.Active() != "" || r.slice(t) != "" {
		t.Fatalf("active=%q slice=%q after profile.use \"\"", r.rt.Profile.Active(), r.slice(t))
	}
	if !r.idle() || len(st.Resolved()) != before+1 {
		t.Fatal("the flight's own write-back started another")
	}
}

// Two calls in one tick join one flight.
func TestProfileUseTwoCallsOneTickShareAFlight(t *testing.T) {
	r, st := startedRig(t)
	before := len(st.Resolved())
	a, b := r.use(""), r.use("")
	first, second := <-r.posts, <-r.posts
	first()
	second()
	r.tree.Flush()
	r.rt.Coordinator.Tick()
	ra, rb := <-a, <-b
	if ra.err != nil || rb.err != nil || len(ra.reports) != 5 || len(rb.reports) != 5 || ra.reports[0].Gen != rb.reports[0].Gen {
		t.Fatalf("results = %+v %+v", ra, rb)
	}
	if got := len(st.Resolved()); got != before+1 {
		t.Fatalf("%d flights for two calls in one tick, want 1", got-before)
	}
}

// A flight error reaches the requester unchanged.
func TestProfileUseFlightErrorPassesThrough(t *testing.T) {
	r, st := startedRig(t)
	boom := errors.New("boom")
	st.set(func() { st.err = boom })
	got := r.use("")
	(<-r.posts)()
	r.tree.Flush()
	r.rt.Coordinator.Tick()
	res := <-got
	if res.err == nil || !strings.Contains(res.err.Error(), "Store") && !errors.Is(res.err, boom) && !strings.Contains(res.err.Error(), "boom") {
		t.Fatalf("err = %v", res.err)
	}
	if _, synthesized := map[string]bool{"reload_deadline": true, "shutdown": true}[reasonOf(res.err)]; synthesized {
		t.Fatalf("flight error was rewritten: %v", res.err)
	}
}

func reasonOf(err error) string {
	var coded *c.Error
	if errors.As(err, &coded) {
		return coded.Reason
	}
	return ""
}

// Capture is the Store.Invalidate call: a request while it blocks joins the
// flight, one after it returns goes to the follow-up and sees the new value.
func TestCoordinatorCaptureIsInvalidate(t *testing.T) {
	st := &recStore{fakeStore: fakeStore{resolved: store.Resolved{Doc: doc(`{"theme":{"name":"One"}}`)}}}
	r := newRig(t, st, nil)
	co := r.rt.Coordinator

	entered, release := make(chan struct{}), make(chan struct{})
	st.set(func() { st.onInvalid = func() { entered <- struct{}{}; <-release } })
	first := make(chan error, 1)
	go func() { _, err := co.Reload(context.Background(), ReasonCLI); first <- err }()
	<-entered
	// Invalidate has not returned: the request joins.
	co.mu.Lock()
	active := co.active
	co.mu.Unlock()
	if joined := co.request(ReasonCLI, false); joined != active || joined.captured {
		t.Fatal("a request while Invalidate blocks did not join the flight")
	}
	st.set(func() { st.onInvalid = nil })

	// Hold the flight in Resolve, which runs after Invalidate returned.
	resolving, free := make(chan struct{}), make(chan struct{})
	st.set(func() {
		st.onResolve = func() { st.set(func() { st.onResolve = nil }); resolving <- struct{}{}; <-free }
	})
	close(release)
	<-resolving
	st.set(func() { st.resolved = store.Resolved{Doc: doc(`{"theme":{"name":"Two"}}`)} })
	follow := co.request(ReasonCLI, false)
	if follow == active {
		t.Fatal("a request after capture joined the captured flight")
	}
	close(free)
	<-active.done
	<-follow.done
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if got := r.rt.Settings.Snapshot().String("theme.name"); got != "Two" {
		t.Fatalf("follow-up saw theme.name = %q, want the changed value", got)
	}
}
