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
	Profile   func(startup bool, layers []store.LayerDoc, names []string) (string, []Problem)
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
	reports  []Report
	err      error
}

// Coordinator serializes reloads; overlapping callers wait on the same flight.
type Coordinator struct {
	opts   Options
	mu     sync.Mutex
	active *flight
	queued *flight
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

// Close cancels the lifetime used by listener-triggered reloads.
func (co *Coordinator) Close() { co.cancel() }

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
func (co *Coordinator) Reload(ctx context.Context, reason Reason) ([]Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// ctx.Done() must not be read before the flight is joined: tests take its
	// first call as the signal that this caller is parked on it.
	return co.waiter(co.request(reason, false)).Wait(ctx)
}

// request joins the running flight when it has not yet captured, otherwise the
// single queued follow-up, otherwise starts one. A request from the loop
// (onLoop) only creates the flight; its worker launches at Tick, the end of the
// loop tick, so every request posted in that tick joins the same flight.
func (co *Coordinator) request(reason Reason, onLoop bool) *flight {
	co.mu.Lock()
	defer co.mu.Unlock()
	f := co.active
	switch {
	case f == nil:
		f = &flight{done: make(chan struct{}), reason: reason}
		co.active = f
		if !onLoop {
			f.started = true
			go co.execute(f)
		}
	case f.captured:
		if co.queued == nil {
			co.queued = &flight{done: make(chan struct{}), reason: reason}
		}
		f = co.queued
	}
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
	if f == nil || f.started {
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
		Active string `json:"active"`
	}
	json.Unmarshal(raw, &v)
	co.mu.Lock()
	skip := co.haveSelected && v.Active == co.selected || co.hasExpect && v.Active == co.expect
	co.hasExpect = false
	co.mu.Unlock()
	if !skip {
		co.request(ReasonProfile, true)
	}
}

// AwaitProfile implements profile.Reloader. It runs on the loop, in the
// callback that wrote the slice: it registers the requester on the flight that
// write causes and tells the slice listener not to request a second one.
func (co *Coordinator) AwaitProfile(name string) profile.Waiter {
	co.mu.Lock()
	co.expect, co.hasExpect = name, true
	co.mu.Unlock()
	return co.waiter(co.request(ReasonProfile, true))
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

// Wait implements profile.Waiter.
func (w flightWait) Wait(ctx context.Context) ([]Report, error) {
	select {
	case <-w.f.done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return c.Clone(w.f.reports), w.f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.co.ctx.Done():
		return nil, w.co.ctx.Err()
	}
}
func (co *Coordinator) execute(f *flight) {
	reports, err := co.reload(co.ctx, f.reason, f)
	co.mu.Lock()
	f.reports = c.Clone(reports)
	f.err = err
	co.last = c.Clone(reports)
	close(f.done)
	co.active = co.queued
	co.queued = nil
	next := co.active
	if next != nil {
		next.started = true
	}
	co.mu.Unlock()
	if next != nil {
		go co.execute(next)
	}
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

func (co *Coordinator) reload(parent context.Context, reason Reason, f *flight) ([]Report, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	timer := co.opts.Clock.NewTimer(2 * time.Second)
	defer timer.Stop()
	deadline := co.opts.Clock.Now().Add(2 * time.Second)
	expired := func() bool {
		if ctx.Err() != nil || co.ctx.Err() != nil || !co.opts.Clock.Now().Before(deadline) {
			cancel()
			return true
		}
		return false
	}
	reports := []Report{}
	publish := func() {
		if co.opts.Publisher != nil {
			co.opts.Publisher.Publish("services.reloaded", map[string]any{"reason": reason, "reports": c.Clone(reports)})
		}
	}
	defer publish()
	inputs := make(chan inputResult, 1)
	go func() { inputs <- co.loadInput(f) }()
	var input inputResult
	timeout := false
	select {
	case input = <-inputs:
	case <-timer.C():
		cancel()
		timeout = true
	case <-ctx.Done():
		timeout = true
	case <-co.ctx.Done():
		cancel()
		timeout = true
	}
	if expired() {
		timeout = true
	}
	if timeout || input.err != nil {
		message, code := "Store resolution failed", "store_error"
		if timeout {
			message, code = "timed out", "timed_out"
		}
		for _, name := range order() {
			reports = append(reports, Report{Service: name, Gen: co.opts.Services[name].Generation(), Problems: []Problem{{Severity: "error", Code: code, Message: message}}})
		}
		return reports, errors.New(message)
	}
	failed := false
	settingsFailed := false
	for _, name := range order() {
		service := co.opts.Services[name]
		if timeout || expired() {
			timeout = true
			reports = append(reports, Report{Service: name, Gen: service.Generation(), Problems: []Problem{{Severity: "error", Code: "timed_out", Message: "timed out"}}})
			failed = true
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
		go func() { candidate, report, err := service.Rebuild(ctx, in); result <- prepared{candidate, report, err} }()
		var p prepared
		select {
		case p = <-result:
		case <-timer.C():
			cancel()
			timeout = true
		case <-ctx.Done():
			timeout = true
		case <-co.ctx.Done():
			cancel()
			timeout = true
		}
		if timeout || expired() {
			timeout = true
			reports = append(reports, Report{Service: name, Gen: service.Generation(), Problems: []Problem{{Severity: "error", Code: "timed_out", Message: "timed out"}}})
			failed = true
			continue
		}
		if p.err == nil && p.candidate != nil {
			p.report = p.candidate.Apply()
		} else {
			failed = true
			p.report.Applied = false
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
		if !p.report.Applied {
			failed = true
		}
	}
	if failed {
		return reports, errors.New("service reload incomplete")
	}
	return reports, nil
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
func (co *Coordinator) loadInput(f *flight) inputResult {
	co.opts.Store.Invalidate()
	if f != nil {
		co.mu.Lock()
		f.captured = true
		co.mu.Unlock()
	}
	selected := ""
	var selectionProblems []Problem
	if co.opts.Profile != nil {
		layers, err := co.opts.Store.Layers("config", "")
		if err != nil {
			return inputResult{err: err}
		}
		selected, selectionProblems = co.opts.Profile(f != nil && f.reason == ReasonStartup, layers, co.opts.Store.Profiles())
	}
	if f != nil {
		co.mu.Lock()
		co.selected, co.haveSelected = selected, true
		co.mu.Unlock()
	}
	resolved, err := co.opts.Store.Resolve("config", selected)
	if err != nil {
		return inputResult{err: err}
	}
	for _, problem := range resolved.Problems {
		if problem.Code != "type_clash" {
			return inputResult{err: errors.New("Store resolution failed")}
		}
	}
	layers, err := co.opts.Store.Layers("keymap", selected)
	if err != nil {
		return inputResult{err: err}
	}
	configLayers, err := co.opts.Store.Layers("config", selected)
	if err != nil {
		return inputResult{err: err}
	}
	return inputResult{in: ReloadInput{Doc: resolved, Layers: layers, ConfigLayers: configLayers, Declared: map[store.DocName]map[string]store.Doc{store.DeclaredPlugins: co.opts.Store.Declared(store.DeclaredPlugins), store.DeclaredKeymap: co.opts.Store.Declared(store.DeclaredKeymap)}, Profile: &selected, ProfileProblems: selectionProblems}}
}

// Inputs collects fresh user-authorized disk input for dry validation/checks.
// It never applies a candidate or publishes services.reloaded.
func (co *Coordinator) Inputs(ctx context.Context, caller Caller) (ReloadInput, error) {
	if err := c.RequireUser(caller); err != nil {
		return ReloadInput{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timer := co.opts.Clock.NewTimer(2 * time.Second)
	defer timer.Stop()
	result := make(chan inputResult, 1)
	go func() { result <- co.loadInput(nil) }()
	select {
	case loaded := <-result:
		if loaded.err != nil {
			return ReloadInput{}, c.SafeError("unavailable")
		}
		return loaded.in, nil
	case <-timer.C():
		return ReloadInput{}, c.Failure("timeout", "input_deadline")
	case <-ctx.Done():
		return ReloadInput{}, ctx.Err()
	case <-co.ctx.Done():
		return ReloadInput{}, co.ctx.Err()
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
	Store     StoreReader
	State     c.Tree
	Post      c.Post
	Publisher c.Publisher
	Clock     c.Clock
	Crypto    store.SecretDoc
	Prompt    func(secrets.Prompt) <-chan bool
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
	set, setErr := settings.New(settings.Options{Schema: opts.Schema, Warn: opts.Warn})
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
	return &Runtime{co, p, set, th, kb, sec}, errors.Join(err, secretErr)
}

// Close cancels reloads and pending dialogs.
func (r *Runtime) Close() { r.Coordinator.Close(); r.Secrets.Close() }
