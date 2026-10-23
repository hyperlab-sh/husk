package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	stheme "github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func apply(t *testing.T, s Reloadable, in ReloadInput) Report {
	t.Helper()
	candidate, r, err := s.Rebuild(context.Background(), in)
	if err != nil {
		t.Fatalf("prepare %s: %v %+v", r.Service, err, r.Problems)
	}
	if candidate == nil {
		t.Fatal("nil candidate")
	}
	r = candidate.Apply()
	if !r.Applied {
		t.Fatalf("not applied: %+v", r)
	}
	return r
}
func doc(raw string) store.Doc {
	var out store.Doc
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		panic(err)
	}
	return out
}
func input(raw string) ReloadInput {
	return ReloadInput{Doc: store.Resolved{Doc: doc(raw), Origins: map[string]store.Layer{}}, Declared: map[store.DocName]map[string]store.Doc{}}
}
func catalog(plugin string) ReloadInput {
	in := input(`{}`)
	in.Declared[store.DeclaredPlugins] = map[string]store.Doc{plugin: doc(fmt.Sprintf(`{"id":%q,"name":"Display","actions":[{"id":%q}],"surfaces":[{"id":"stage"}],"secret_requests":[{"name":"git.token","purpose":"Push","required":true}],"settings_declared":[{"key":%q,"type":"bool","default":false}]}`, plugin, plugin+".run", "plugins."+plugin+".enabled"))}
	return in
}
func TestSettingsAtomicValidationAndAccess(t *testing.T) {
	s := newSet(settings.Options{Schema: []settings.Entry{{Key: "dist.count", Type: "int", Default: int64(1), Owner: "dist", Apply: "live"}, {Key: "dist.wait", Type: "duration", Default: json.Number("250"), Owner: "dist", Apply: "live"}}})
	in := catalog("Upper.ID")
	in.Doc.Doc = doc(`{"plugins":{"Upper":{"ID":{"enabled":true}}},"dist":{"count":2}}`)
	apply(t, s, in)
	if !s.Snapshot().Bool("plugins.Upper.ID.enabled") || s.Snapshot().Int("dist.count") != 2 || s.Snapshot().Duration("dist.wait") != 250*time.Millisecond {
		t.Fatal("typed/default/exact-id getters")
	}
	for _, caller := range []Caller{{Kind: "plugin", PluginID: "other"}, {Kind: "plugin"}} {
		if _, _, err := s.Get(caller, "plugins.Upper.ID.enabled"); err == nil {
			t.Fatal("foreign settings allowed")
		}
	}
	if _, _, err := s.Get(Caller{Kind: "plugin", PluginID: "Upper.ID"}, "plugins.Upper.ID.enabled"); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	bad := catalog("Upper.ID")
	bad.Doc.Doc = doc(`{"dist":{"count":"bad","wait":false},"unknown":7}`)
	_, r, err := s.Rebuild(context.Background(), bad)
	if err == nil || len(r.Problems) != 3 || s.Snapshot() != before {
		t.Fatalf("atomic/every-problem failed %+v %v", r, err)
	}
	raw, _ := before.Raw("dist.count")
	if fmt.Sprint(raw) != "2" {
		t.Fatal(raw)
	}
	if _, err := s.List(Caller{PluginID: "other"}); err == nil {
		t.Fatal("plugin list")
	}
}
func TestSettingsRestartSchemaRawAndSecretRejection(t *testing.T) {
	s := newSet(settings.Options{})
	in := catalog("files")
	in.Doc.Doc = doc(`{"husk":{"plugins":{"paths":["one"]}}}`)
	apply(t, s, in)
	in.Doc.Doc = doc(`{"husk":{"plugins":{"paths":["two"]}}}`)
	r := apply(t, s, in)
	if fmt.Sprint(s.Snapshot().Strings("husk.plugins.paths")) != "[one]" || len(r.Problems) != 1 {
		t.Fatal("restart not held")
	}
	raw, _ := s.Snapshot().Raw("husk.plugins.paths")
	if fmt.Sprint(raw) != "[one]" {
		t.Fatal("raw follows the in-force value on a held restart key")
	}
	in.Doc.Doc = doc(`{"git":{"token":"DO_NOT_REPORT_VALUE"}}`)
	_, r, err := s.Rebuild(context.Background(), in)
	b, _ := json.Marshal(r)
	if err != nil || !hasProblem(r.Problems, "unknown_key", "warning") || strings.Contains(string(b), "DO_NOT_REPORT_VALUE") {
		t.Fatal("secret-shaped key not an unknown_key warning, or its value reported", r, err)
	}
	s = newSet(settings.Options{})
	in = catalog("raw")
	delete(in.Declared[store.DeclaredPlugins]["raw"], "settings_declared")
	in.Doc.Doc = doc(`{"plugins":{"raw":{"anything":17}}}`)
	r = apply(t, s, in)
	if len(r.Problems) != 1 {
		t.Fatal(r)
	}
}
func TestThemeContrastAllPairsAndHex(t *testing.T) {
	if got := palette.CheckContrast(palette.Default()); len(got) != 0 {
		t.Fatal(got)
	}
	if len(palette.ContrastPairs()) != 75 {
		t.Fatalf("contrast table changed: %d", len(palette.ContrastPairs()))
	}
	for _, value := range []string{"#123", "17", "#xyzxyz"} {
		t.Run(value, func(t *testing.T) {
			s := newSet(settings.Options{})
			in := input(fmt.Sprintf(`{"theme":{"palette":{"ink":%q}}}`, value))
			apply(t, s, in)
			in.Settings = s.Snapshot()
			th := stheme.New()
			_, _, err := th.Rebuild(context.Background(), in)
			if err == nil || th.Generation() != 0 {
				t.Fatal("invalid hex applied")
			}
		})
	}
	s := newSet(settings.Options{})
	in := input(`{"theme":{"palette":{"ink":"#000000","border":"#000000"}}}`)
	apply(t, s, in)
	in.Settings = s.Snapshot()
	th := stheme.New()
	_, r, err := th.Rebuild(context.Background(), in)
	if err == nil || len(r.Problems) < 10 {
		t.Fatal("not all contrast pairs reported", len(r.Problems))
	}
	in.Doc.Doc = doc(`{"theme":{"palette":{"ink":"#000000"},"allow_low_contrast":true}}`)
	in.Doc.Origins = map[string]store.Layer{"theme.allow_low_contrast": store.LayerUser}
	apply(t, s, in)
	in.Settings = s.Snapshot()
	apply(t, th, in)
	if th.Palette().Ink != "#000000" {
		t.Fatal("user escape hatch")
	}
	in.Doc.Origins["theme.allow_low_contrast"] = store.LayerDistribution
	apply(t, s, in)
	in.Settings = s.Snapshot()
	_, _, err = th.Rebuild(context.Background(), in)
	if err == nil {
		t.Fatal("distribution optout accepted")
	}
}
func TestKeybindingLayersFirstWinsDisableAndFocus(t *testing.T) {
	kb := keybinding.New()
	in := catalog("files")
	in.Declared[store.DeclaredKeymap] = map[string]store.Doc{"files": doc(`{"version":1,"bind":[{"key":"n","focus":"stage","action":"files.run"},{"key":"ctrl+badkey","focus":"stage","action":"files.run"}],"problems":[{"index":3,"code":"foreign_action","message":"conversion rejected action"},{"index":4,"code":"global_not_allowed","message":"conversion rejected scope"}]}`)}
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1,"bind":[{"key":"n","action":"husk.home","hint":"Home"},{"key":"n","action":"files.run"},{"key":"m","action":"husk.home","hint":"Home"},{"key":"q","disable":true},{"keys":["ctrl+k","ctrl+s"],"action":"files.run"}]}`)}}
	apply(t, kb, in)
	ctx := keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "files"}
	// The focused surface's own binding beats the user's global `n`.
	r := kb.Resolve(ctx, []string{"n"})
	if r.Outcome != keybinding.Match || r.Binding.Action != "files.run" {
		t.Fatal(r)
	}
	if r := kb.Resolve(keybinding.KeyContext{}, []string{"n"}); r.Outcome != keybinding.Match || r.Binding.Action != "husk.home" {
		t.Fatal("global n", r)
	}
	if kb.Resolve(ctx, []string{"q"}).Outcome != keybinding.NoMatch {
		t.Fatal("disable did not fall through")
	}
	if kb.Resolve(ctx, []string{"ctrl+k"}).Outcome != keybinding.Prefix {
		t.Fatal("no prefix")
	}
	if kb.Resolve(ctx, []string{"ctrl+k", "ctrl+s"}).Outcome != keybinding.Match {
		t.Fatal("no sequence")
	}
	codes := map[string]bool{}
	for _, p := range kb.Listing().Problems {
		codes[p.Code] = true
	}
	for _, code := range []string{"duplicate", "unparsable_key", "global_not_allowed", "foreign_action"} {
		if !codes[code] {
			t.Fatal("missing problem", code, codes)
		}
	}
	if len(kb.Hints(ctx)) != 1 {
		t.Fatal("effective hints")
	}
	ctx.Frozen = true
	if kb.Resolve(ctx, []string{"n"}).Outcome != keybinding.NoMatch {
		t.Fatal("frozen action")
	}
	ctx = keybinding.KeyContext{FocusSurface: "modal-surface", ModalOpen: true, ModalAction: "husk.home"}
	if kb.Resolve(ctx, []string{"n"}).Outcome != keybinding.Match {
		t.Fatal("modal rail action not permitted")
	}
	if kb.Resolve(ctx, []string{"ctrl+k"}).Outcome != keybinding.NoMatch {
		t.Fatal("modal grab")
	}
	listing := kb.Listing()
	listing.Entries[0].Keys[0] = "mutated"
	if kb.Listing().Entries[0].Keys[0] == "mutated" {
		t.Fatal("mutable keymap")
	}
}

type manualClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*manualTimer
	created chan *manualTimer
	gate    <-chan struct{}
}
type manualTimer struct {
	clock   *manualClock
	at      time.Time
	ch      chan time.Time
	stopped chan struct{}
	once    sync.Once
	off     bool
}

func clock() *manualClock {
	return &manualClock{now: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), created: make(chan *manualTimer, 100)}
}
func (c *manualClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (mc *manualClock) NewTimer(d time.Duration) c.Timer {
	mc.mu.Lock()
	t := &manualTimer{clock: mc, at: mc.now.Add(d), ch: make(chan time.Time, 1), stopped: make(chan struct{})}
	mc.timers = append(mc.timers, t)
	mc.mu.Unlock()
	mc.created <- t
	if mc.gate != nil {
		<-mc.gate
	}
	return t
}
func (t *manualTimer) C() <-chan time.Time { return t.ch }
func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	old := !t.off
	t.off = true
	t.clock.mu.Unlock()
	t.once.Do(func() { close(t.stopped) })
	return old
}
func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	for _, t := range c.timers {
		if !t.off && !c.now.Before(t.at) {
			t.off = true
			t.ch <- c.now
		}
	}
	c.mu.Unlock()
}

type memoryCrypto struct {
	data  map[string][]byte
	err   error
	onGet func() // runs before Get answers, to interleave with a read
}

func (m *memoryCrypto) Get(k string) ([]byte, bool, error) {
	if m.onGet != nil {
		m.onGet()
	}
	v, ok := m.data[k]
	return append([]byte(nil), v...), ok, m.err
}
func (m *memoryCrypto) Put(k string, v []byte) error {
	if m.err != nil {
		return m.err
	}
	m.data[k] = append([]byte(nil), v...)
	return nil
}
func (m *memoryCrypto) Delete(k string) error { delete(m.data, k); return m.err }
func (m *memoryCrypto) Keys() ([]string, error) {
	var out []string
	for k := range m.data {
		out = append(out, k)
	}
	return out, m.err
}

type fakeStore struct {
	profiles []string
	resolved store.Resolved
	err      error
	block    <-chan struct{}
	mu       sync.Mutex
	log      []string // "invalidate" per capture, plus whatever note records
}

func (s *fakeStore) note(what string) { s.mu.Lock(); s.log = append(s.log, what); s.mu.Unlock() }
func (s *fakeStore) trace() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}
func (s *fakeStore) Invalidate() { s.note("invalidate") }
func (s *fakeStore) Resolve(store.DocName, string) (store.Resolved, error) {
	if s.block != nil {
		<-s.block
	}
	return s.resolved, s.err
}
func (s *fakeStore) Layers(store.DocName, string) ([]store.LayerDoc, error) { return nil, nil }
func (s *fakeStore) Declared(store.DocName) map[string]store.Doc            { return nil }
func (s *fakeStore) Profiles() []string                                     { return append([]string(nil), s.profiles...) }

type fakeService struct {
	name  string
	gen   atomic.Uint64
	run   func(context.Context)
	calls atomic.Int64
}

func (s *fakeService) Generation() Generation { return Generation(s.gen.Load()) }
func (s *fakeService) Rebuild(ctx context.Context, in ReloadInput) (Candidate, Report, error) {
	s.calls.Add(1)
	if s.run != nil {
		s.run(ctx)
	}
	r := Report{Service: s.name, Gen: s.Generation()}
	old := s.gen.Load()
	return c.Prepare(ctx, r, true, func() bool { return s.gen.CompareAndSwap(old, old+1) }, s.Generation), r, nil
}
func fakeServices() map[string]Reloadable {
	out := map[string]Reloadable{}
	for _, n := range order() {
		out[n] = &fakeService{name: n}
	}
	return out
}
func TestCoordinatorOrderSharedResultAndEvent(t *testing.T) {
	services := fakeServices()
	var mu sync.Mutex
	var observed []string
	gate := make(chan struct{})
	clk := clock()
	clk.gate = gate
	for _, n := range order() {
		name := n
		services[n].(*fakeService).run = func(context.Context) {
			mu.Lock()
			observed = append(observed, name)
			mu.Unlock()
		}
	}
	events := make(chan any, 4)
	co, err := NewCoordinator(Options{Store: &fakeStore{}, Services: services, Publisher: c.PublishFunc(func(n string, v any) {
		if n != "services.reloaded" {
			t.Error(n)
		}
		events <- v
	}), Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	defer co.Close()
	done := make(chan []Report, 2)
	go func() {
		r, err := co.Reload(context.Background(), ReasonCLI)
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	<-clk.created // the running flight has not begun input capture
	watched, parked := parkedCtx(context.Background())
	go func() {
		r, err := co.Reload(watched, ReasonPlugin)
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	<-parked
	close(gate)
	a, b := <-done, <-done
	if fmt.Sprint(a) != fmt.Sprint(b) || len(a) != 5 {
		t.Fatal("results not shared")
	}
	if strings.Join(observed, ",") != "profile,settings,theme,keybinding,secrets" {
		t.Fatal(observed)
	}
	if len(events) != 1 {
		t.Fatal("not one event", len(events))
	}
	for _, s := range services {
		if s.(*fakeService).calls.Load() != 1 {
			t.Fatal("duplicate rebuild")
		}
	}
}
func TestCoordinatorTimeoutPartialApplyNeverPublishesLate(t *testing.T) {
	services := fakeServices()
	entered := make(chan struct{})
	gate := make(chan struct{})
	probe := &lateProbeService{entered: entered, gate: gate, prepared: make(chan Candidate, 1)}
	services["theme"] = probe
	clock := clock()
	co, _ := NewCoordinator(Options{Store: &fakeStore{}, Services: services, Clock: clock})
	defer co.Close()
	done := make(chan []Report, 1)
	go func() {
		r, err := co.Reload(context.Background(), ReasonCLI)
		var coded *c.Error
		if !errors.As(err, &coded) || coded.Code != "timeout" || coded.Reason != "reload_deadline" {
			t.Errorf("timeout error = %v", err)
		}
		done <- r
	}()
	<-entered
	clock.advance(2 * time.Second)
	reports := <-done
	if !reports[0].Applied || !reports[1].Applied || reports[2].Applied || reports[3].Applied {
		t.Fatal(reports)
	}
	close(gate)
	lateCandidate := <-probe.prepared
	if probe.Generation() != 0 {
		t.Fatal("coordinator applied a late candidate")
	}
	// Positive control: unlike a context-guarded fake, this candidate really
	// can publish after cancellation. The coordinator must be what discards it.
	lateCandidate.Apply()
	if probe.Generation() != 1 {
		t.Fatal("late-publication probe cannot fail the assertion")
	}

}
func TestCoordinatorStoreFailureDoesNotPrepareAnyService(t *testing.T) {
	for _, tc := range []struct {
		name    string
		problem store.Problem
		err     error
	}{
		{name: "resolve error", err: errors.New("bad source")},
		{name: "invalid path error", err: fmt.Errorf("resolve /secret/dir: %w", store.ErrInvalidPath)},
		{name: "user parse problem", problem: store.Problem{Code: "invalid_document", Layer: store.LayerUser, File: "config.json", Line: 3, Column: 14, Message: `hujson: line 3, column 14: invalid character '"hunter2"'`}},
		{name: "line without column", problem: store.Problem{Code: "invalid_document", Layer: store.LayerUser, File: "config.json", Line: 7, Message: "invalid JSON"}},
		{name: "profile read problem", problem: store.Problem{Code: "unreadable", Layer: store.LayerProfile, File: "profiles/work/config.json", Message: "permission denied"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			services := fakeServices()
			source := &fakeStore{err: tc.err}
			if tc.problem.Code != "" {
				source.resolved.Problems = []store.Problem{tc.problem}
			}
			co, err := NewCoordinator(Options{Store: source, Services: services, Clock: clock()})
			if err != nil {
				t.Fatal(err)
			}
			defer co.Close()
			r, err := co.Reload(context.Background(), ReasonCLI)
			if err == nil || len(r) != 5 {
				t.Fatal(r, err)
			}
			for _, rep := range r {
				if len(rep.Problems) != 1 || rep.Problems[0].Code != "store_error" || rep.Problems[0].Severity != "error" {
					t.Fatalf("%s report = %+v", rep.Service, rep.Problems)
				}
				problem := rep.Problems[0]
				// The message says where the failure is (code, file, layer,
				// line, column) but never Store's own text, which may quote a
				// value; an error without a problem gets a coded reason.
				var want, absent []string
				var wantSource *c.Source
				switch {
				case tc.problem.Code != "":
					want = []string{tc.problem.Code + ": configuration file " + tc.problem.File, "(layer " + string(tc.problem.Layer) + ")"}
					if tc.problem.Line > 0 {
						want = append(want, fmt.Sprintf("line %d", tc.problem.Line))
					} else {
						absent = append(absent, "line ")
					}
					if tc.problem.Column > 0 {
						want = append(want, fmt.Sprintf("line %d, column %d", tc.problem.Line, tc.problem.Column))
					} else {
						absent = append(absent, "column")
					}
					absent = append(absent, tc.problem.Message, "hunter2")
					wantSource = &c.Source{Layer: string(tc.problem.Layer), File: tc.problem.File, Line: tc.problem.Line}
				case errors.Is(tc.err, store.ErrInvalidPath):
					want = []string{"invalid_path: configuration could not be loaded"}
					absent = []string{"/secret/dir", "store: invalid path"}
				default:
					want = []string{"store_failed: configuration could not be loaded"}
					absent = []string{"bad source"}
				}
				for _, text := range want {
					if !strings.Contains(problem.Message, text) {
						t.Fatalf("%s message=%q missing %q", rep.Service, problem.Message, text)
					}
				}
				for _, text := range absent {
					if strings.Contains(problem.Message, text) {
						t.Fatalf("%s message=%q has %q", rep.Service, problem.Message, text)
					}
				}
				if (problem.Source == nil) != (wantSource == nil) || (wantSource != nil && *problem.Source != *wantSource) {
					t.Fatalf("%s source=%+v want %+v", rep.Service, problem.Source, wantSource)
				}
			}
			if r[0].Problems[0].Source != nil && r[0].Problems[0].Source == r[1].Problems[0].Source {
				t.Fatal("reports share one Source")
			}
			var coded *c.Error
			if !errors.As(err, &coded) || coded.Code != "unavailable" || coded.Reason != "store_error" || coded.Message != r[0].Problems[0].Message {
				t.Fatalf("flight error = %v", err)
			}
			for _, s := range services {
				if s.(*fakeService).calls.Load() != 0 {
					t.Fatal("prepared after source failure")
				}
			}
		})
	}
}

// rebuildErrService returns a Rebuild error that raised no problem of its own.
type rebuildErrService struct {
	fakeService
	err error
}

func (s *rebuildErrService) Rebuild(ctx context.Context, in ReloadInput) (Candidate, Report, error) {
	return nil, Report{Service: s.name}, s.err
}

// A Rebuild that fails without a problem still yields a coded error problem:
// the Service's own failure code, or cancelled/timed_out for a context end.
func TestCoordinatorCodesARebuildErrorWithoutProblems(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code string
	}{
		"service code": {c.Failure("unavailable", "settings_missing"), "settings_missing"},
		"code only":    {&c.Error{Code: "unavailable", Message: "x"}, "unavailable"},
		"cancelled":    {fmt.Errorf("service preparation cancelled: %w", context.Canceled), "cancelled"},
		"deadline":     {fmt.Errorf("service preparation cancelled: %w", context.DeadlineExceeded), "timed_out"},
		"plain":        {errors.New("boom"), "internal"},
	} {
		t.Run(name, func(t *testing.T) {
			services := fakeServices()
			services["keybinding"] = &rebuildErrService{fakeService{name: "keybinding"}, tc.err}
			co, _ := NewCoordinator(Options{Store: &fakeStore{}, Services: services, Clock: clock()})
			defer co.Close()
			reports, err := co.Reload(context.Background(), ReasonCLI)
			if err != nil {
				t.Fatalf("a Service that kept its snapshot is a success reply: %v", err)
			}
			for _, r := range reports {
				if r.Service != "keybinding" {
					continue
				}
				if r.Applied || len(r.Problems) != 1 || r.Problems[0].Code != tc.code || r.Problems[0].Severity != "error" {
					t.Fatalf("keybinding report = %+v, want one error %q", r, tc.code)
				}
				return
			}
			t.Fatal("no keybinding report")
		})
	}
}

// genCountService counts Generation reads: every flight that runs reads each
// Service's generation at least once, so the count tells how many ran.
type genCountService struct {
	fakeService
	reads atomic.Int64
}

func (s *genCountService) Generation() Generation {
	s.reads.Add(1)
	return s.fakeService.Generation()
}

// waitIdle spins until the coordinator has no active or queued flight.
func waitIdle(t *testing.T, co *Coordinator) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		co.mu.Lock()
		idle := co.active == nil && len(co.queue) == 0
		co.mu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("coordinator never went idle")
		}
		runtime.Gosched()
	}
}

func assertShutdown(t *testing.T, err error) {
	t.Helper()
	var coded *c.Error
	if !errors.As(err, &coded) || coded.Code != "cancelled" || coded.Reason != "shutdown" {
		t.Fatalf("err = %v, want cancelled/shutdown", err)
	}
}

// A profile.use flight is requested on the loop and launched by Tick. A Close
// that comes before that Tick launches it itself, so its waiter is answered
// cancelled/shutdown; the flight touches neither Store nor the Publisher, and
// a later Tick runs nothing.
func TestCloseLaunchesALoopRequestedFlight(t *testing.T) {
	services := fakeServices()
	counter := &genCountService{fakeService: fakeService{name: "secrets"}}
	services["secrets"] = counter
	source := &fakeStore{}
	var events atomic.Int64
	co, err := NewCoordinator(Options{Store: source, Services: services, Clock: clock(), Publisher: c.PublishFunc(func(string, any) { events.Add(1) })})
	if err != nil {
		t.Fatal(err)
	}
	waiter := co.AwaitProfile("x")
	co.mu.Lock()
	started := co.active != nil && co.active.started
	co.mu.Unlock()
	if started {
		t.Fatal("AwaitProfile on the loop launched its flight before Tick")
	}
	co.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reports, err := waiter.Wait(ctx)
	assertShutdown(t, err)
	if len(reports) != 5 {
		t.Fatalf("reports = %+v, want five", reports)
	}
	for i, rep := range reports {
		if rep.Service != order()[i] || rep.Applied || len(rep.Problems) != 1 || rep.Problems[0].Code != "cancelled" {
			t.Fatalf("report %d = %+v, want %s cancelled", i, rep, order()[i])
		}
	}
	waitIdle(t, co)
	ran := counter.reads.Load()
	co.Tick()
	waitIdle(t, co)
	if got := counter.reads.Load(); got != ran {
		t.Fatalf("Tick after Close ran another flight (generation reads %d then %d)", ran, got)
	}
	if got := source.trace(); len(got) != 0 {
		t.Fatalf("store trace = %v, want no invalidate", got)
	}
	if n := events.Load(); n != 0 {
		t.Fatalf("published %d services.reloaded events after Close", n)
	}
	for _, s := range services {
		if calls := s.(interface{ rebuilds() int64 }).rebuilds(); calls != 0 {
			t.Fatalf("a Service rebuilt after Close (%d)", calls)
		}
	}
}

func (s *fakeService) rebuilds() int64 { return s.calls.Load() }
