// Package services coordinates bounded prepare/apply reloads. Children depend
// only on contracts; neither the coordinator nor a child imports Stem or Pods.
package services

import (
	"context"
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
	Store     StoreReader
	Services  map[string]Reloadable
	Settings  func() c.SettingsSnapshot
	Profile   func(store.Resolved) string
	State     c.Tree
	Publisher c.Publisher
	Clock     c.Clock
	Context   context.Context
}
type flight struct {
	done     chan struct{}
	start    chan struct{}
	reason   Reason
	captured bool // protected by Coordinator.mu; set before gathering Store inputs
	reports  []Report
	err      error
}

// Coordinator serializes reloads; overlapping callers wait on the same flight.
type Coordinator struct {
	opts   Options
	mu     sync.Mutex
	active *flight
	queued *flight
	joined chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
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
	co := &Coordinator{opts: opts, ctx: ctx, cancel: cancel, joined: make(chan struct{}, 1)}
	if opts.State != nil {
		seen := opts.State.Snapshot().Slices[state.ProfileNamespace].Revision
		opts.State.OnFlush(func(changes []state.Change, _ *state.Snapshot) {
			for _, change := range changes {
				if change.Namespace == state.ProfileNamespace && change.Revision > seen && co.ctx.Err() == nil {
					seen = change.Revision
					go co.Reload(co.ctx, ReasonProfile)
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

// Reload prepares on worker goroutines but owns all candidate application on
// this caller's goroutine. A late worker cannot publish any snapshot.
func (co *Coordinator) Reload(ctx context.Context, reason Reason) ([]Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	co.mu.Lock()
	if co.active == nil {
		f := &flight{done: make(chan struct{}), reason: reason}
		co.active = f
		co.mu.Unlock()
		return co.execute(ctx, f)
	}
	f := co.active
	owner := false
	if f.captured {
		if co.queued == nil {
			co.queued = &flight{done: make(chan struct{}), start: make(chan struct{}), reason: reason}
			owner = true
		}
		f = co.queued
	}
	co.mu.Unlock()
	select {
	case co.joined <- struct{}{}:
	default:
	}
	if owner {
		select {
		case <-f.start:
			return co.execute(ctx, f)
		case <-ctx.Done():
			// Preserve queue ownership even when this waiter leaves early.
			go func() { <-f.start; co.execute(ctx, f) }()
			return nil, ctx.Err()
		}
	}
	select {
	case <-f.done:
		return c.Clone(f.reports), f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-co.ctx.Done():
		return nil, co.ctx.Err()
	}
}
func (co *Coordinator) execute(ctx context.Context, f *flight) ([]Report, error) {
	reports, err := co.reload(ctx, f.reason, f)
	co.mu.Lock()
	f.reports = c.Clone(reports)
	f.err = err
	close(f.done)
	co.active = co.queued
	co.queued = nil
	if co.active != nil {
		close(co.active.start)
	}
	co.mu.Unlock()
	return c.Clone(reports), err
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
	co.mu.Lock()
	f.captured = true
	co.mu.Unlock()
	inputs := make(chan inputResult, 1)
	go func() { inputs <- co.loadInput() }()
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
		message := "Store resolution failed"
		if timeout {
			message = "timed out"
		}
		for _, name := range order() {
			reports = append(reports, Report{Service: name, Gen: co.opts.Services[name].Generation(), Problems: []Problem{{Severity: "error", Message: message}}})
		}
		return reports, errors.New(message)
	}
	failed := false
	settingsFailed := false
	for _, name := range order() {
		service := co.opts.Services[name]
		if timeout || expired() {
			timeout = true
			reports = append(reports, Report{Service: name, Gen: service.Generation(), Problems: []Problem{{Severity: "error", Message: "timed out"}}})
			failed = true
			continue
		}
		if name == "theme" && settingsFailed {
			reports = append(reports, Report{Service: name, Gen: service.Generation(), Problems: []Problem{{Severity: "error", Message: "Settings failed; palette unchanged"}}})
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
			reports = append(reports, Report{Service: name, Gen: service.Generation(), Problems: []Problem{{Severity: "error", Message: "timed out"}}})
			failed = true
			continue
		}
		if p.err == nil && p.candidate != nil {
			p.report = p.candidate.Apply()
		} else {
			failed = true
			p.report.Applied = false
			p.report.Gen = service.Generation()
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

func (co *Coordinator) loadInput() inputResult {
	co.opts.Store.Invalidate()
	base, err := co.opts.Store.Resolve("config", "")
	if err != nil {
		return inputResult{err: err}
	}
	selected := ""
	if co.opts.Profile != nil {
		selected = co.opts.Profile(base)
	}
	if selected != "" {
		valid := false
		for _, name := range co.opts.Store.Profiles() {
			valid = valid || name == selected
		}
		if !valid {
			selected = ""
		}
	}
	resolved := base
	if selected != "" {
		resolved, err = co.opts.Store.Resolve("config", selected)
	}
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
	return inputResult{in: ReloadInput{Doc: resolved, Layers: layers, ConfigLayers: configLayers, Declared: map[store.DocName]map[string]store.Doc{store.DeclaredPlugins: co.opts.Store.Declared(store.DeclaredPlugins), store.DeclaredFocusKeymap: co.opts.Store.Declared(store.DeclaredFocusKeymap)}, Profile: &selected}}
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
	go func() { result <- co.loadInput() }()
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
	Store         StoreReader
	State         c.Tree
	Post          c.Post
	Publisher     c.Publisher
	Clock         c.Clock
	Crypto        store.SecretDoc
	Prompt        func(secrets.Prompt) <-chan bool
	Audit         func(string)
	ProfileFlag   *string
	ProfileSource func(string) string
	Schema        []settings.Entry
	Warn          func(Problem)
	Context       context.Context
}

// New constructs the complete Services set, without loading files on the loop.
func New(opts RuntimeOptions) (*Runtime, error) {
	p, err := profile.New(profile.Options{Store: opts.Store, State: opts.State, Post: opts.Post, Flag: opts.ProfileFlag, Source: opts.ProfileSource})
	if p == nil {
		return nil, err
	}
	set := settings.New(settings.Options{Schema: opts.Schema, Warn: opts.Warn})
	th := theme.New()
	kb := keybinding.New()
	sec, secretErr := secrets.New(secrets.Options{State: opts.State, Post: opts.Post, Crypto: opts.Crypto, Prompt: opts.Prompt, Clock: opts.Clock, Publisher: opts.Publisher, Audit: opts.Audit, Context: opts.Context})
	if sec == nil {
		return nil, secretErr
	}
	co, coErr := NewCoordinator(Options{Store: opts.Store, State: opts.State, Publisher: opts.Publisher, Clock: opts.Clock, Context: opts.Context, Profile: p.ChooseProfile, Settings: set.Snapshot, Services: map[string]Reloadable{"profile": p, "settings": set, "theme": th, "keybinding": kb, "secrets": sec}})
	if coErr != nil {
		sec.Close()
		return nil, coErr
	}
	return &Runtime{co, p, set, th, kb, sec}, errors.Join(err, secretErr)
}

// Close cancels reloads and pending dialogs.
func (r *Runtime) Close() { r.Coordinator.Close(); r.Secrets.Close() }
