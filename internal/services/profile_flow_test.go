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
	return newRigWith(t, st, flag, state.Options{})
}

func newRigWith(t *testing.T, st *recStore, flag *string, opts state.Options) *rig {
	t.Helper()
	tree := state.New(opts)
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

// present reports whether the slice holds an active key at all.
func (r *rig) present() bool {
	var v map[string]json.RawMessage
	json.Unmarshal(r.tree.Snapshot().Slices[state.ProfileNamespace].Value, &v)
	_, ok := v["active"]
	return ok
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
		written  bool   // the flight records a choice in the slice
	}{
		{"user default", []string{"work", "other"}, []store.LayerDoc{userLayer(`{"profile":{"default":"work"}}`)}, nil, "work", "", false},
		{"flag beats default", []string{"work", "other"}, []store.LayerDoc{userLayer(`{"profile":{"default":"work"}}`)}, str("other"), "other", "", true},
		{"no input", []string{"work"}, nil, nil, "", "", false},
		{"unknown default", []string{"work"}, []store.LayerDoc{userLayer(`{"profile":{"default":"missing"}}`)}, nil, "", "unknown_profile", false},
		{"unknown flag persists none", []string{"work"}, nil, str("missing"), "", "unknown_profile", true},
		{"default in another layer", []string{"work"}, []store.LayerDoc{{Layer: store.LayerProfile, File: "/cfg/p.json", Doc: doc(`{"profile":{"default":"work"}}`)}}, nil, "", "profile_default_ignored", false},
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
			if (tc.written && r.slice(t) != tc.want) || r.present() != tc.written {
				t.Fatalf("slice = %q present=%v, want %q present=%v", r.slice(t), r.present(), tc.want, tc.written)
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
	prior := r.rt.Coordinator.LastReports()
	got := r.use("")
	post := <-r.posts
	post()
	r.tree.Flush()
	r.rt.Coordinator.Tick()
	res := <-got
	if res.err != nil || res.active != "" || len(res.reports) != 5 {
		t.Fatalf("Use = %+v", res)
	}
	// The reply carries the reports of the flight this write caused, not the
	// previous flight's.
	if res.reports[0].Service != "profile" || res.reports[0].Gen != prior[0].Gen+1 {
		t.Fatalf("reply gen %d, previous flight gen %d", res.reports[0].Gen, prior[0].Gen)
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

// A flight error reaches the requester unchanged: profile.use fails exactly as
// config.reload does under the same Store.
func TestProfileUseFlightErrorPassesThrough(t *testing.T) {
	r, st := startedRig(t)
	st.set(func() { st.err = errors.New("boom") })
	_, reloadErr := r.rt.Coordinator.Reload(context.Background(), ReasonCLI)
	if reloadErr == nil {
		t.Fatal("reload under a failing Store succeeded")
	}
	got := r.use("")
	(<-r.posts)()
	r.tree.Flush()
	r.rt.Coordinator.Tick()
	res := <-got
	if res.err == nil || res.err.Error() != reloadErr.Error() || reasonOf(res.err) != reasonOf(reloadErr) {
		t.Fatalf("profile.use error %v, config.reload error %v", res.err, reloadErr)
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

// A name in the slice beats profile.default.
func TestProfileSliceBeatsDefault(t *testing.T) {
	st := &recStore{fakeStore: fakeStore{profiles: []string{"work", "other"}, resolved: store.Resolved{Doc: doc(`{}`)}}, layers: []store.LayerDoc{userLayer(`{"profile":{"default":"work"}}`)}}
	r := newRigWith(t, st, nil, state.Options{Hydrator: profileHydratorOf("other")})
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonStartup); err != nil {
		t.Fatal(err)
	}
	if got := st.Resolved(); len(got) != 1 || got[0] != "other" {
		t.Fatalf("Resolve calls = %q, want [other]", got)
	}
}

type profileHydratorOf string

func (h profileHydratorOf) Hydrate(state.Spec) (json.RawMessage, bool) {
	return json.RawMessage(`{"active":"` + string(h) + `"}`), true
}

// The active profile's directory disappears: the next flight selects none,
// reports unknown_profile and the slice follows without a second flight.
func TestProfileDirectoryGoneOnLaterFlight(t *testing.T) {
	r, st := startedRig(t)
	before := len(st.Resolved())
	st.set(func() { st.profiles = nil })
	reports, err := r.rt.Coordinator.Reload(context.Background(), ReasonCLI)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findProblem(problemsOf(reports), "unknown_profile"); !ok {
		t.Fatalf("no unknown_profile in %+v", problemsOf(reports))
	}
	r.settle()
	if got := st.Resolved(); len(got) != before+1 || got[before] != "" {
		t.Fatalf("Resolve calls = %q", got)
	}
	if r.slice(t) != "" || !r.idle() || len(st.Resolved()) != before+1 {
		t.Fatalf("slice=%q idle=%v resolves=%d", r.slice(t), r.idle(), len(st.Resolved()))
	}
}

// The reply names what the flight selected, not what was asked for, when the
// profile directory vanished between validation and step 2.
func TestProfileUseReplyNamesTheSelection(t *testing.T) {
	st := &recStore{fakeStore: fakeStore{profiles: []string{"work", "other"}, resolved: store.Resolved{Doc: doc(`{}`)}}, layers: []store.LayerDoc{userLayer(`{"profile":{"default":"work"}}`)}}
	r := newRig(t, st, nil)
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonStartup); err != nil {
		t.Fatal(err)
	}
	r.settle()
	got := r.use("other")
	(<-r.posts)()
	st.set(func() { st.profiles = []string{"work"} })
	r.tree.Flush()
	r.rt.Coordinator.Tick()
	res := <-got
	if res.err != nil || res.active != "" {
		t.Fatalf("Use = %+v, want active \"\"", res)
	}
	if _, ok := findProblem(problemsOf(res.reports), "unknown_profile"); !ok {
		t.Fatalf("reply lacks unknown_profile: %+v", problemsOf(res.reports))
	}
}

// A profile.use mark whose Set changed nothing must not outlive its tick.
func TestAwaitProfileMarkDoesNotOutliveTheTick(t *testing.T) {
	st := &recStore{fakeStore: fakeStore{resolved: store.Resolved{Doc: doc(`{}`)}}}
	r := newRig(t, st, nil)
	co := r.rt.Coordinator
	co.AwaitProfile("x")
	co.Tick()
	co.mu.Lock()
	left := co.hasExpect
	co.mu.Unlock()
	if left {
		t.Fatal("expect mark survived the tick")
	}
}

func defaultRig(t *testing.T, opts state.Options, def string) (*rig, *recStore) {
	t.Helper()
	st := &recStore{fakeStore: fakeStore{profiles: []string{"work", "other"}, resolved: store.Resolved{Doc: doc(`{}`)}}}
	if def != "" {
		st.layers = []store.LayerDoc{userLayer(`{"profile":{"default":"` + def + `"}}`)}
	}
	return newRigWith(t, st, nil, opts), st
}

// An absent slice follows profile.default, writes nothing, and picks up a
// changed default on the next flight.
func TestProfileAbsentSliceFollowsDefaultLive(t *testing.T) {
	r, st := defaultRig(t, state.Options{}, "work")
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonStartup); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if r.present() || r.rt.Profile.Active() != "work" {
		t.Fatalf("present=%v active=%q after a default selection", r.present(), r.rt.Profile.Active())
	}
	st.set(func() { st.layers = []store.LayerDoc{userLayer(`{"profile":{"default":"other"}}`)} })
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonCLI); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := st.Resolved(); len(got) != 2 || got[1] != "other" || r.present() || r.rt.Profile.Active() != "other" {
		t.Fatalf("Resolve calls = %q present=%v active=%q", got, r.present(), r.rt.Profile.Active())
	}
}

// {"active":""} is an explicit no profile: a default never overrides it,
// including after a restart of a named instance that persisted it.
func TestProfileExplicitNoneBeatsDefault(t *testing.T) {
	r, st := defaultRig(t, state.Options{Hydrator: profileHydratorOf("")}, "work")
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonStartup); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if got := st.Resolved(); len(got) != 1 || got[0] != "" || !r.present() || r.rt.Profile.Active() != "" {
		t.Fatalf("Resolve calls = %q present=%v active=%q", got, r.present(), r.rt.Profile.Active())
	}
}

// profile.use "" and then a default appearing still gives no profile.
func TestProfileUseNoneThenDefaultStaysNone(t *testing.T) {
	r, st := defaultRig(t, state.Options{}, "")
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonStartup); err != nil {
		t.Fatal(err)
	}
	r.settle()
	got := r.use("other")
	(<-r.posts)()
	r.tree.Flush()
	r.rt.Coordinator.Tick()
	<-got
	r.settle()
	got = r.use("")
	(<-r.posts)()
	r.tree.Flush()
	r.rt.Coordinator.Tick()
	if res := <-got; res.err != nil || res.active != "" {
		t.Fatalf("use \"\" = %+v", res)
	}
	r.settle()
	st.set(func() { st.layers = []store.LayerDoc{userLayer(`{"profile":{"default":"work"}}`)} })
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonCLI); err != nil {
		t.Fatal(err)
	}
	if last := st.Resolved(); last[len(last)-1] != "" || r.rt.Profile.Active() != "" {
		t.Fatalf("Resolve calls = %q active=%q", last, r.rt.Profile.Active())
	}
}

// A state clear returns the slice to absent, and the next flight follows the
// default again.
func TestProfileStateClearFollowsDefaultAgain(t *testing.T) {
	r, st := defaultRig(t, state.Options{Hydrator: profileHydratorOf("")}, "work")
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonStartup); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if _, err := r.tree.Clear(state.ProfileNamespace); err != nil {
		t.Fatal(err)
	}
	r.tree.Flush()
	r.rt.Coordinator.Tick()
	// The clear itself starts the profile flight; wait for it through a joiner.
	if _, err := r.rt.Coordinator.Reload(context.Background(), ReasonCLI); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if last := st.Resolved(); last[len(last)-1] != "work" || r.present() || r.rt.Profile.Active() != "work" {
		t.Fatalf("Resolve calls = %q present=%v active=%q", last, r.present(), r.rt.Profile.Active())
	}
}
