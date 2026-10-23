// Package services coordinates bounded prepare/apply reloads. Children depend
// only on contracts; neither the coordinator nor a child imports Stem or Pods.
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/services/profile"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"io/fs"
	"sync"
	"time"
)

// Generation, Reloadable, ReloadInput, Candidate, Report and Problem are shared
// aliases, avoiding parent/child import cycles.
type Generation = c.Generation
type Reloadable = c.Reloadable
type ReloadInput = c.ReloadInput
type Candidate = c.Candidate
type Report = c.Report
type Problem = c.Problem

// Caller carries an authenticated identity from the Server.
type Caller = c.Caller

// Reason identifies the trigger shared by a coalesced group of callers.
type Reason string

const (
	// ReasonStartup denotes the process's first reload, run by the composition
	// root after State hydration and before the first plugin launches.
	ReasonStartup Reason = "startup"
	// ReasonCLI denotes config.reload.
	ReasonCLI Reason = "cli"
	// ReasonProfile denotes a profile State change.
	ReasonProfile Reason = "profile"
	// ReasonPlugin denotes changed plugin declarations.
	ReasonPlugin Reason = "plugin"
	// ReasonSettings denotes a validated user-layer setting write.
	ReasonSettings Reason = "settings"
)

// StoreReader is the complete reload input port; no filesystem API escapes it.
type StoreReader interface {
	Invalidate()
	Resolve(store.DocName, string) (store.Resolved, error)
	Layers(store.DocName, string) ([]store.LayerDoc, error)
	Declared(store.DocName) map[string]store.Doc
	Profiles() []string
}

// Options injects services, Store input, loop-safe publication and deadline time.
type Options struct {
	Store    StoreReader
	Services map[string]Reloadable
	Settings func() c.SettingsSnapshot
	// Profile is step 2's profile choice: whether this is the startup flight,
	// the unmerged config layers read without a profile, and the profile names
	// Store lists. It returns the selected name and its selection problems.
	Profile   func(startup bool, layers []store.LayerDoc, names []string) (name string, problems []Problem, write bool)
	State     c.Tree
	Publisher c.Publisher
	Clock     c.Clock
	Context   context.Context
}
type flight struct {
	done     chan struct{}
	reason   Reason
	captured bool // protected by Coordinator.mu; set once Store.Invalidate returned
	started  bool // protected by Coordinator.mu; the worker was launched
	dry      bool // a dry run: holds the coordinator's turn, never coalesces
	granted  chan struct{}
	input    <-chan inputResult // buffered worker delivery, protected by Coordinator.mu
	reports  []Report
	err      error
}

// Coordinator serializes reloads; overlapping callers wait on the same flight.
// A dry run (Inputs) takes a turn in the same queue and never coalesces.
type Coordinator struct {
	opts   Options
	mu     sync.Mutex
	active *flight
	queue  []*flight // behind active, in start order; at most one regular flight
	ctx    context.Context
	cancel context.CancelFunc

	// selected is the profile the running or last flight chose; the slice
	// listener ignores a change to it. expect is the name a profile.use just
	// wrote and already registered a flight for. last holds the reports of the
	// most recent completed flight. All guarded by mu.
	selected     string
	haveSelected bool
	expect       string
	hasExpect    bool
	last         []Report
}

// NewCoordinator installs a nonblocking loop listener. Call it on the loop.
func NewCoordinator(opts Options) (*Coordinator, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("services: Store is required")
	}
	for _, name := range order() {
		if opts.Services[name] == nil {
			return nil, fmt.Errorf("services: missing %s", name)
		}
	}
	if opts.Clock == nil {
		opts.Clock = c.RealClock{}
	}
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	opts.Services = cloneServices(opts.Services)
	ctx, cancel := context.WithCancel(opts.Context)
	co := &Coordinator{opts: opts, ctx: ctx, cancel: cancel}
	if opts.State != nil {
		seen := opts.State.Snapshot().Slices[state.ProfileNamespace].Revision
		opts.State.OnFlush(func(changes []state.Change, snap *state.Snapshot) {
			for _, change := range changes {
				if change.Namespace == state.ProfileNamespace && change.Revision > seen && co.ctx.Err() == nil {
					seen = change.Revision
					co.profileChanged(snap.Slices[state.ProfileNamespace].Value)
					return
				}
			}
		})
	}
	return co, nil
}
func cloneServices(in map[string]Reloadable) map[string]Reloadable {
	out := map[string]Reloadable{}
	for name, s := range in {
		out[name] = s
	}
	return out
}
func order() []string { return []string{"profile", "settings", "theme", "keybinding", "secrets"} }

// Close cancels the lifetime used by listener-triggered reloads. A flight in
// progress reports its unpublished Services as cancelled and answers its
// waiters cancelled/shutdown. A flight the loop requested but Tick has not yet
// launched is launched here, so its waiters get the same answer.
func (co *Coordinator) Close() {
	co.cancel()
	co.mu.Lock()
	f := co.active
	launch := f != nil && !f.dry && !f.started
	if launch {
		f.started = true
	}
	co.mu.Unlock()
	if launch {
		go co.execute(f)
	}
}

// shutdown is the error a requester sees when the coordinator closed first.
func shutdown() error { return c.Failure("cancelled", "shutdown") }

// ReloadFor implements the user-only config.reload exposure. Internal triggers
// use Reload directly with an explicit reason and never trust a wire reason.
func (co *Coordinator) ReloadFor(ctx context.Context, caller Caller) ([]Report, error) {
	if err := c.RequireUser(caller); err != nil {
		return nil, err
	}
	return co.Reload(ctx, ReasonCLI)
}

// Reload waits for a coordinator-owned flight. The caller's context controls
// only this wait; cancellation cannot poison another requester's shared work.
// It returns an error only when the flight itself failed (unavailable
// store_error, timeout reload_deadline, cancelled shutdown); the reports
// accompany it. A Service that kept its snapshot is a report, not an error.
func (co *Coordinator) Reload(ctx context.Context, reason Reason) ([]Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if co.ctx.Err() != nil {
		return nil, shutdown()
	}
	// ctx.Done() must not be read before the flight is joined: tests take its
	// first call as the signal that this caller is parked on it.
	return co.waiter(co.request(reason, false)).Wait(ctx)
}

// request joins the running flight when it has not yet captured, otherwise the
// single queued regular follow-up, otherwise starts or queues one; a dry run
// holding or awaiting the turn is never joined. A request from the loop
// (onLoop) only creates the flight; its worker launches at Tick, the end of the
// loop tick, so every request posted in that tick joins the same flight.
func (co *Coordinator) request(reason Reason, onLoop bool) *flight {
	co.mu.Lock()
	defer co.mu.Unlock()
	switch {
	case co.active == nil:
		f := &flight{done: make(chan struct{}), reason: reason}
		co.active = f
		if !onLoop {
			f.started = true
			go co.execute(f)
		}
		return f
	case !co.active.dry && !co.active.captured:
		return co.active
	}
	for _, q := range co.queue {
		if !q.dry {
			return q
		}
	}
	f := &flight{done: make(chan struct{}), reason: reason}
	co.queue = append(co.queue, f)
	return f
}

// Tick launches the worker of a flight the loop requested in this tick. The
// host calls it at the end of every Update, after State flushed.
func (co *Coordinator) Tick() {
	co.mu.Lock()
	// A profile.use mark is only for the flush of its own tick; a Set that
	// changed nothing produces no change to consume it.
	co.hasExpect = false
	f := co.active
	if f == nil || f.dry || f.started {
		co.mu.Unlock()
		return
	}
	f.started = true
	co.mu.Unlock()
	go co.execute(f)
}

// profileChanged is the slice listener's body, on the loop. It ignores the
// write a flight made to follow its own selection and the one a profile.use
// already registered a flight for; any other value starts a profile reload.
func (co *Coordinator) profileChanged(raw json.RawMessage) {
	var v struct {
		Active *string `json:"active"`
	}
	json.Unmarshal(raw, &v)
	co.mu.Lock()
	// An absent slice (a state clear) is a change to follow the default again:
	// it never equals a selection.
	skip := v.Active != nil && (co.haveSelected && *v.Active == co.selected || co.hasExpect && *v.Active == co.expect)
	co.hasExpect = false
	co.mu.Unlock()
	if !skip {
		co.request(ReasonProfile, true)
	}
}

// AwaitProfile implements profile.Reloader. It runs on the loop, in the
// callback that wrote the slice: it registers the requester on the flight that
// write causes and tells the slice listener not to request a second one.
//
// After Close it answers like Reload: a waiter on a flight that has already
// ended with cancelled/shutdown, which no Tick or Close will ever launch.
func (co *Coordinator) AwaitProfile(name string) profile.Waiter {
	if co.ctx.Err() != nil {
		f := &flight{done: make(chan struct{}), reason: ReasonProfile, started: true, err: shutdown()}
		close(f.done)
		return co.waiter(f)
	}
	co.mu.Lock()
	co.expect, co.hasExpect = name, true
	co.mu.Unlock()
	return co.waiter(co.request(ReasonProfile, true))
}

// AwaitSettings registers a setting write on the loop before Tick captures
// inputs. Requests after capture join the single queued follow-up flight.
func (co *Coordinator) AwaitSettings() settings.Waiter {
	if co.ctx.Err() != nil {
		f := &flight{done: make(chan struct{}), reason: ReasonSettings, started: true, err: shutdown()}
		close(f.done)
		return co.waiter(f)
	}
	return co.waiter(co.request(ReasonSettings, true))
}

// Settled implements profile.Reloader. An applied name alone is insufficient:
// a captured flight may be switching away, with another selection queued.
func (co *Coordinator) Settled(name string) bool {
	co.mu.Lock()
	defer co.mu.Unlock()
	return co.active == nil && len(co.queue) == 0 && co.haveSelected && co.selected == name
}

// LastReports implements profile.Reloader.
func (co *Coordinator) LastReports() []Report {
	co.mu.Lock()
	defer co.mu.Unlock()
	return c.Clone(co.last)
}

type flightWait struct {
	co *Coordinator
	f  *flight
}

func (co *Coordinator) waiter(f *flight) flightWait { return flightWait{co, f} }

// Wait implements profile.Waiter. A Close needs no arm of its own: the flight
// observes it and ends with cancelled/shutdown (Close launches a flight Tick
// had not yet started).
func (w flightWait) Wait(ctx context.Context) ([]Report, error) {
	select {
	case <-w.f.done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return c.Clone(w.f.reports), w.f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (co *Coordinator) execute(f *flight) {
	reports, err := co.reload(co.ctx, f.reason, f)
	co.mu.Lock()
	f.reports = c.Clone(reports)
	f.err = err
	co.last = c.Clone(reports)
	close(f.done)
	// reload cancelled the flight context before returning. Late gathers and
	// pure Rebuilds are abandoned; neither may hold up the next turn.
	co.advanceLocked()
	co.mu.Unlock()
}

// advanceLocked ends the active turn and starts the next one, if any.
func (co *Coordinator) advanceLocked() {
	co.active = nil
	if len(co.queue) == 0 {
		return
	}
	next := co.queue[0]
	co.queue = co.queue[1:]
	co.active = next
	if next.dry {
		close(next.granted)
		return
	}
	next.started = true
	go co.execute(next)
}

type inputResult struct {
	in  ReloadInput
	err error
}
type prepared struct {
	candidate Candidate
	report    Report
	err       error
}

// storeFailure is a flight-level Store failure: the message names the code and
// where the failure is (config-relative file, layer, line and column, when
// Store reported them), never a value or Store's own text, which may quote one.
type storeFailure struct {
	message string
	source  *c.Source
}

func (e *storeFailure) Error() string { return e.message }

func storeProblemFailure(problems []store.Problem) error {
	for _, problem := range problems {
		if problem.Code == "type_clash" {
			continue
		}
		if problem.File == "" {
			return &storeFailure{message: problem.Code + ": configuration could not be loaded"}
		}
		where := problem.File
		if problem.Layer != "" {
			where += " (layer " + string(problem.Layer) + ")"
		}
		if problem.Line > 0 {
			where += fmt.Sprintf(" line %d", problem.Line)
			if problem.Column > 0 {
				where += fmt.Sprintf(", column %d", problem.Column)
			}
		}
		return &storeFailure{
			message: problem.Code + ": configuration file " + where + " could not be loaded",
			source:  &c.Source{Layer: string(problem.Layer), File: problem.File, Line: problem.Line},
		}
	}
	return nil
}

// storeErrorFailure codes an error Store returned instead of a problem. Its
// text is never shown: it may quote a path or a value.
func storeErrorFailure(err error) *storeFailure {
	var failure *storeFailure
	if errors.As(err, &failure) {
		return failure
	}
	reason := "store_failed"
	switch {
	case errors.Is(err, store.ErrInvalidPath):
		reason = "invalid_path"
	case errors.Is(err, store.ErrNotFound):
		reason = "not_found"
	case errors.Is(err, store.ErrClosed):
		reason = "closed"
	case errors.Is(err, store.ErrUnavailable):
		reason = "unavailable"
	case errors.Is(err, fs.ErrPermission):
		reason = "permission_denied"
	}
	return &storeFailure{message: reason + ": configuration could not be loaded"}
}

func (co *Coordinator) reload(parent context.Context, reason Reason, f *flight) ([]Report, error) {
	ctx, cancel := context.WithCancel(parent)
	// This defer orders cancellation before execute advances the queue, even
	// when stopped observes the deadline without selecting the timer arm.
	defer cancel()
	timer := co.opts.Clock.NewTimer(2 * time.Second)
	defer timer.Stop()
	deadline := co.opts.Clock.Now().Add(2 * time.Second)
	ctx = c.WithDeadline(ctx, co.opts.Clock, deadline)
	timerFired := false
	// stopped says why the flight can no longer publish: the coordinator
	// closed ("cancelled") or the 2 s deadline passed ("timed_out").
	stopped := func() string {
		switch {
		case co.ctx.Err() != nil:
			return "cancelled"
		case timerFired || !co.opts.Clock.Now().Before(deadline):
			return "timed_out"
		}
		return ""
	}
	reports := []Report{}
	if co.ctx.Err() != nil {
		// A flight queued behind the one that was running when Close came:
		// it touches nothing and publishes nothing.
		for _, name := range order() {
			reports = append(reports, stoppedReport(name, co.opts.Services[name].Generation(), "cancelled"))
		}
		return reports, stoppedError("cancelled")
	}
	publish := func() {
		if co.opts.Publisher != nil {
			co.opts.Publisher.Publish("services.reloaded", map[string]any{"reason": reason, "reports": c.Clone(reports)})
		}
	}
	defer publish()
	inputs := make(chan inputResult, 1)
	co.mu.Lock()
	f.input = inputs
	co.mu.Unlock()
	go func() { inputs <- co.loadInput(ctx, f) }()
	var input inputResult
	select {
	case input = <-inputs:
	case <-timer.C():
		timerFired = true
		cancel()
	case <-co.ctx.Done():
	}
	if why := stopped(); why != "" {
		for _, name := range order() {
			reports = append(reports, stoppedReport(name, co.opts.Services[name].Generation(), why))
		}
		return reports, stoppedError(why)
	}
	if input.err != nil {
		failure := storeErrorFailure(input.err)
		message := failure.message
		for _, name := range order() {
			var source *c.Source
			if failure.source != nil {
				copied := *failure.source
				source = &copied
			}
			reports = append(reports, Report{Service: name, Gen: co.opts.Services[name].Generation(), Problems: []Problem{{Severity: "error", Code: "store_error", Message: message, Source: source}}})
		}
		return reports, &c.Error{Code: "unavailable", Reason: "store_error", Message: message}
	}
	final := ""
	settingsFailed := false
	for _, name := range order() {
		service := co.opts.Services[name]
		if final == "" {
			final = stopped()
		}
		if final != "" {
			reports = append(reports, stoppedReport(name, service.Generation(), final))
			continue
		}
		if name == "theme" && settingsFailed {
			reports = append(reports, Report{Service: name, Gen: service.Generation(), Problems: []Problem{{Severity: "warning", Code: "settings_unchanged", Message: "settings unchanged; palette unchanged"}}})
			continue
		}
		in := input.in
		if name == "theme" && co.opts.Settings != nil {
			in.Settings = co.opts.Settings()
		}
		result := make(chan prepared, 1)
		go func() {
			if initial, ok := service.(interface {
				RebuildStartup(context.Context, ReloadInput) (Candidate, Report, error)
			}); ok && name == "settings" && f.reason == ReasonStartup {
				candidate, report, err := initial.RebuildStartup(ctx, in)
				result <- prepared{candidate, report, err}
				return
			}
			candidate, report, err := service.Rebuild(ctx, in)
			result <- prepared{candidate, report, err}
		}()
		var p prepared
		select {
		case p = <-result:
		case <-timer.C():
			timerFired = true
			cancel()
		case <-co.ctx.Done():
		}
		if final = stopped(); final != "" {
			reports = append(reports, stoppedReport(name, service.Generation(), final))
			continue
		}
		if p.err == nil && p.candidate != nil {
			p.report = p.candidate.Apply()
		} else {
			// Failed startup validation can still initialize Settings' per-key
			// generation-zero baseline. Apply only here, under the flight bound,
			// never on the rebuild worker; the failed report still skips Theme.
			if name == "settings" && f.reason == ReasonStartup && errors.Is(p.err, c.ErrInvalid) && p.candidate != nil {
				p.candidate.Apply()
			}
			p.report.Applied = false
			p.report.Changed = false
			p.report.Gen = service.Generation()
			if p.err != nil && len(p.report.Problems) == 0 {
				p.report.Problems = []Problem{rebuildProblem(p.err)}
			}
		}
		p.report.Service = name
		reports = append(reports, p.report)
		if name == "settings" {
			settingsFailed = !p.report.Applied
		}
	}
	return reports, stoppedError(final)
}

// stoppedReport is the report of a Service the flight could not publish.
func stoppedReport(name string, gen Generation, why string) Report {
	message := "timed out"
	if why == "cancelled" {
		message = "cancelled"
	}
	return Report{Service: name, Gen: gen, Problems: []Problem{{Severity: "error", Code: why, Message: message}}}
}

// stoppedError is the flight's error for the reason it stopped: nil when it
// did not.
func stoppedError(why string) error {
	switch why {
	case "cancelled":
		return shutdown()
	case "timed_out":
		return c.Failure("timeout", "reload_deadline")
	}
	return nil
}

// rebuildProblem names a failed Rebuild that raised no problem of its own, so
// every failing report carries a coded problem: the context end for a
// cancelled or timed-out preparation, otherwise the Service's own failure code.
func rebuildProblem(err error) Problem {
	code := "internal"
	var coded *c.Error
	switch {
	case errors.Is(err, context.Canceled):
		code = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		code = "timed_out"
	case errors.As(err, &coded):
		code = coded.Reason
		if code == "" {
			code = coded.Code
		}
	}
	return Problem{Severity: "error", Code: code, Message: err.Error()}
}

// loadInput is step 2. Capture is the Store.Invalidate call: it is the first
// thing a flight does, a request arriving before it joins the flight, and one
// arriving after it goes to the queued follow-up. f is nil for a dry run.
func (co *Coordinator) loadInput(ctx context.Context, f *flight) inputResult {
	if err := ctx.Err(); err != nil {
		return inputResult{err: err}
	}
	co.opts.Store.Invalidate()
	if err := ctx.Err(); err != nil {
		return inputResult{err: err}
	}
	if f != nil {
		co.mu.Lock()
		if ctx.Err() == nil && co.active == f {
			f.captured = true
		}
		co.mu.Unlock()
	}
	selected := ""
	var selectionProblems []Problem
	var profileWrite bool
	profileNames := co.opts.Store.Profiles()
	if err := ctx.Err(); err != nil {
		return inputResult{err: err}
	}
	if co.opts.Profile != nil {
		layers, err := co.opts.Store.Layers("config", "")
		if ended := ctx.Err(); ended != nil {
			return inputResult{err: ended}
		}
		if err != nil {
			return inputResult{err: err}
		}
		selected, selectionProblems, profileWrite = co.opts.Profile(f != nil && f.reason == ReasonStartup, layers, profileNames)
	}
	if f != nil {
		co.mu.Lock()
		if ctx.Err() == nil && co.active == f {
			co.selected, co.haveSelected = selected, true
		}
		co.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return inputResult{err: err}
	}
	resolved, err := co.opts.Store.Resolve("config", selected)
	if ended := ctx.Err(); ended != nil {
		return inputResult{err: ended}
	}
	if err != nil {
		return inputResult{err: err}
	}
	if err := storeProblemFailure(resolved.Problems); err != nil {
		return inputResult{err: err}
	}
	themesResolved, err := co.opts.Store.Resolve("themes", selected)
	if ended := ctx.Err(); ended != nil {
		return inputResult{err: ended}
	}
	if err != nil {
		return inputResult{err: err}
	}
	if err := storeProblemFailure(themesResolved.Problems); err != nil {
		return inputResult{err: err}
	}
	themes, err := co.opts.Store.Layers("themes", selected)
	if ended := ctx.Err(); ended != nil {
		return inputResult{err: ended}
	}
	if err != nil {
		return inputResult{err: err}
	}
	layers, err := co.opts.Store.Layers("keymap", selected)
	if ended := ctx.Err(); ended != nil {
		return inputResult{err: ended}
	}
	if err != nil {
		return inputResult{err: err}
	}
	configLayers, err := co.opts.Store.Layers("config", selected)
	if ended := ctx.Err(); ended != nil {
		return inputResult{err: ended}
	}
	if err != nil {
		return inputResult{err: err}
	}
	declared := map[store.DocName]map[string]store.Doc{}
	for _, name := range []store.DocName{store.DeclaredPlugins, store.DeclaredKeymap} {
		declared[name] = co.opts.Store.Declared(name)
		if err := ctx.Err(); err != nil {
			return inputResult{err: err}
		}
	}
	return inputResult{in: ReloadInput{Doc: resolved, Layers: layers, ConfigLayers: configLayers, Themes: themes, Declared: declared, Profile: &selected, ProfileNames: profileNames, ProfileProblems: selectionProblems, ProfileWrite: profileWrite}}
}

// Inputs collects fresh user-authorized disk input for a dry run
// (settings.validate, theme.check). It takes the coordinator's turn: it waits
// for a running flight to end, a flight requested meanwhile waits for it, and
// it never coalesces. Step 2 (capture included) runs under the same 2 s
// deadline as a flight, counted from when the turn is granted. The turn ends
// when the gather returns, the deadline passes, the caller gives up or the
// coordinator closes. An unfinished gather is abandoned. The caller's Rebuild
// runs after the turn and publishes nothing, so it needs no exclusion. Inputs
// never applies a candidate or publishes services.reloaded.
func (co *Coordinator) Inputs(ctx context.Context, caller Caller) (ReloadInput, error) {
	if err := c.RequireUser(caller); err != nil {
		return ReloadInput{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if co.ctx.Err() != nil {
		return ReloadInput{}, shutdown()
	}
	f := &flight{dry: true, granted: make(chan struct{})}
	co.mu.Lock()
	if co.active == nil {
		co.active = f
		close(f.granted)
	} else {
		co.queue = append(co.queue, f)
	}
	co.mu.Unlock()
	select {
	case <-f.granted:
	case <-ctx.Done():
		co.abandon(f)
		return ReloadInput{}, ctx.Err()
	case <-co.ctx.Done():
		co.abandon(f)
		return ReloadInput{}, shutdown()
	}
	timer := co.opts.Clock.NewTimer(2 * time.Second)
	defer timer.Stop()
	gatherCtx, cancel := context.WithCancel(co.ctx)
	// input_deadline is enforced by the timer below; returning cancels the
	// gather before releasing the turn, rather than a context-owned timer.
	defer func() { cancel(); co.release(f) }()
	result := make(chan inputResult, 1)
	co.mu.Lock()
	f.input = result
	co.mu.Unlock()
	go func() {
		loaded := co.loadInput(gatherCtx, nil)
		cancel()
		result <- loaded
		co.release(f) // a late release is a no-op after another turn started
	}()
	select {
	case loaded := <-result:
		// Close can make both the gather result and shutdown ready together.
		// Preserve shutdown's wire error whichever select arm wins.
		if co.ctx.Err() != nil {
			return ReloadInput{}, shutdown()
		}
		if loaded.err != nil {
			return ReloadInput{}, c.SafeError("unavailable")
		}
		return loaded.in, nil
	case <-timer.C():
		return ReloadInput{}, c.Failure("timeout", "input_deadline")
	case <-ctx.Done():
		return ReloadInput{}, ctx.Err()
	case <-co.ctx.Done():
		return ReloadInput{}, shutdown()
	}
}

// release ends a dry run's turn.
func (co *Coordinator) release(f *flight) {
	co.mu.Lock()
	if co.active == f {
		co.advanceLocked()
	}
	co.mu.Unlock()
}

// abandon withdraws a dry run that stopped waiting: out of the queue if it
// has not been granted, else it ends its turn.
func (co *Coordinator) abandon(f *flight) {
	co.mu.Lock()
	defer co.mu.Unlock()
	if co.active == f {
		co.advanceLocked()
		return
	}
	for i, q := range co.queue {
		if q == f {
			co.queue = append(co.queue[:i:i], co.queue[i+1:]...)
			break
		}
	}
}

// Runtime is a convenient composed set; Server adapters can also construct each
// child independently. Construction and its two State registrations are loop-only.
type Runtime struct {
	Coordinator *Coordinator
	Profile     *profile.Service
	Settings    *settings.Service
	Theme       *theme.Service
	Keybinding  *keybinding.Service
	Secrets     *secrets.Service
}

// RuntimeOptions injects the same ports without importing a Pod implementation.
type RuntimeOptions struct {
	// Writer is handed only to Settings, not to the coordinator.
	Writer    settings.StoreWriter
	Store     StoreReader
	State     c.Tree
	Post      c.Post
	Publisher c.Publisher
	Clock     c.Clock
	Crypto    store.SecretDoc
	Prompt    func(context.Context, secrets.Prompt) secrets.Answer
	Audit     func(string)
	// ProfileFlag is the --profile start flag: a selection input of the
	// startup flight only.
	ProfileFlag   *string
	ProfileSource func(string) string
	Schema        []settings.Entry
	// Warn is the warning sink for unregistered settings keys.
	Warn    func(msg string)
	Context context.Context
}

// New constructs the complete Services set, without loading files on the loop.
func New(opts RuntimeOptions) (*Runtime, error) {
	p, err := profile.New(profile.Options{Store: opts.Store, State: opts.State, Post: opts.Post, Flag: opts.ProfileFlag, Source: opts.ProfileSource})
	if p == nil {
		return nil, err
	}
	set, setErr := settings.New(settings.Options{Schema: opts.Schema, Warn: opts.Warn, Store: opts.Writer, Post: opts.Post})
	if setErr != nil {
		return nil, setErr
	}
	th := theme.New()
	kb := keybinding.New()
	sec, secretErr := secrets.New(secrets.Options{State: opts.State, Post: opts.Post, Crypto: opts.Crypto, Prompt: opts.Prompt, Clock: opts.Clock, Publisher: opts.Publisher, Audit: opts.Audit, Context: opts.Context})
	if sec == nil {
		return nil, secretErr
	}
	co, coErr := NewCoordinator(Options{Store: opts.Store, State: opts.State, Publisher: opts.Publisher, Clock: opts.Clock, Context: opts.Context, Profile: p.Select, Settings: set.Snapshot, Services: map[string]Reloadable{"profile": p, "settings": set, "theme": th, "keybinding": kb, "secrets": sec}})
	if coErr != nil {
		sec.Close()
		return nil, coErr
	}
	p.Bind(co)
	set.Bind(co)
	return &Runtime{co, p, set, th, kb, sec}, errors.Join(err, secretErr)
}

// Close cancels reloads and pending dialogs.
func (r *Runtime) Close() { r.Coordinator.Close(); r.Secrets.Close() }
