// Package fakes holds no-op, recording fakes for every interface in
// internal/host/contracts. Build each with fakes.NewX(rec) from one shared
// *Recorder so a test can assert call order across fakes. Result and Err
// fields are exported and set after construction. Fakes are safe for
// concurrent use.
package fakes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Call is one recorded call: the method, the attributed caller (zero for
// seams that take none) and the key arguments.
type Call struct {
	Method   string
	Kind     contracts.CallerKind
	PluginID string
	Args     []any
}

// String renders "Method kind/plugin arg arg" for compact assertions.
func (c Call) String() string {
	var b strings.Builder
	b.WriteString(c.Method)
	if c.Kind != 0 {
		fmt.Fprintf(&b, " [%d:%s]", c.Kind, c.PluginID)
	}
	for _, a := range c.Args {
		fmt.Fprintf(&b, " %v", a)
	}
	return b.String()
}

// Recorder is the ordered call log shared by every fake built from it.
type Recorder struct {
	mu    sync.Mutex
	calls []Call
}

// NewRecorder returns an empty shared log.
func NewRecorder() *Recorder { return &Recorder{} }

// Record appends a call that has no caller.
func (r *Recorder) Record(method string, args ...any) {
	r.add(Call{Method: method, Args: args})
}

// RecordCaller appends a call attributed to c.
func (r *Recorder) RecordCaller(method string, c contracts.Caller, args ...any) {
	r.add(Call{Method: method, Kind: c.Kind, PluginID: c.PluginID, Args: args})
}

func (r *Recorder) add(c Call) {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	r.mu.Unlock()
}

// Calls returns a copy of the log in call order.
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Call(nil), r.calls...)
}

// Methods returns just the method names, in order.
func (r *Recorder) Methods() []string {
	var out []string
	for _, c := range r.Calls() {
		out = append(out, c.Method)
	}
	return out
}

// Reset empties the log.
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.calls = nil
	r.mu.Unlock()
}

// watchers holds the notifiers a Watch call registered, until its ctx ends.
type watchers struct {
	mu   sync.Mutex
	next int
	byID map[int]contracts.Notifier
}

func (w *watchers) add(ctx context.Context, n contracts.Notifier) {
	w.mu.Lock()
	if w.byID == nil {
		w.byID = map[int]contracts.Notifier{}
	}
	id := w.next
	w.next++
	w.byID[id] = n
	w.mu.Unlock()
	context.AfterFunc(ctx, func() {
		w.mu.Lock()
		delete(w.byID, id)
		w.mu.Unlock()
	})
}

// push sends m to every live watcher and returns how many got it.
func (w *watchers) push(m protocol.Message) int {
	w.mu.Lock()
	ns := make([]contracts.Notifier, 0, len(w.byID))
	for _, n := range w.byID {
		ns = append(ns, n)
	}
	w.mu.Unlock()
	for _, n := range ns {
		_ = n.Notify(m)
	}
	return len(ns)
}

func (w *watchers) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.byID)
}

func orNew(r *Recorder) *Recorder {
	if r == nil {
		return NewRecorder()
	}
	return r
}

// Handler echoes an empty successful reply (or Reply when set).
type Handler struct {
	*Recorder
	Reply *protocol.Message
}

// NewHandler returns a Handler logging to rec.
func NewHandler(rec *Recorder) *Handler { return &Handler{Recorder: orNew(rec)} }

// Serve implements contracts.Handler.
func (h *Handler) Serve(_ context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	h.RecordCaller("Serve", c, req.Method)
	if h.Reply != nil {
		return *h.Reply
	}
	return protocol.Message{ID: req.ID, Result: json.RawMessage(`{}`)}
}

// Notifier records notifications.
type Notifier struct {
	*Recorder
	Err error
}

// NewNotifier returns a Notifier logging to rec.
func NewNotifier(rec *Recorder) *Notifier { return &Notifier{Recorder: orNew(rec)} }

// Notify implements contracts.Notifier.
func (n *Notifier) Notify(m protocol.Message) error {
	n.Record("Notify", m.Method)
	return n.Err
}

// State fakes contracts.StatePort. Watch registers the notifier until ctx
// ends and returns Snap at once; Push delivers to live watchers.
type State struct {
	*Recorder
	Read     contracts.StateReadResult
	Snap     contracts.StateSnapshotResult
	Revision uint64
	Err      error
	w        watchers
}

// NewState returns a State logging to rec.
func NewState(rec *Recorder) *State { return &State{Recorder: orNew(rec)} }

// Get implements contracts.StatePort.
func (f *State) Get(_ context.Context, c contracts.Caller, slice, path string) (contracts.StateReadResult, error) {
	f.RecordCaller("Get", c, slice, path)
	return f.Read, f.Err
}

// Set implements contracts.StatePort.
func (f *State) Set(_ context.Context, c contracts.Caller, slice string, _ json.RawMessage) (uint64, error) {
	f.RecordCaller("Set", c, slice)
	return f.Revision, f.Err
}

// Patch implements contracts.StatePort.
func (f *State) Patch(_ context.Context, c contracts.Caller, slice string, _ json.RawMessage, ifRevision uint64) (uint64, error) {
	f.RecordCaller("Patch", c, slice, ifRevision)
	return f.Revision, f.Err
}

// Snapshot implements contracts.StatePort.
func (f *State) Snapshot(_ context.Context, c contracts.Caller, values bool) (contracts.StateSnapshotResult, error) {
	f.RecordCaller("Snapshot", c, values)
	return f.Snap, f.Err
}

// Watch implements contracts.StatePort.
func (f *State) Watch(ctx context.Context, c contracts.Caller, slices []string, n contracts.Notifier) (contracts.StateSnapshotResult, error) {
	f.RecordCaller("Watch", c, slices)
	if f.Err != nil {
		return contracts.StateSnapshotResult{}, f.Err
	}
	f.w.add(ctx, n)
	return f.Snap, nil
}

// Push delivers m to every live watcher and returns how many got it.
func (f *State) Push(m protocol.Message) int { return f.w.push(m) }

// Watching reports the number of live watchers.
func (f *State) Watching() int { return f.w.count() }

// Clear implements contracts.StatePort.
func (f *State) Clear(_ context.Context, c contracts.Caller, namespace string) error {
	f.RecordCaller("Clear", c, namespace)
	return f.Err
}

// Calls fakes contracts.CallsPort.
type Calls struct {
	*Recorder
	Result  json.RawMessage
	Actions []contracts.ActionInfo
	Err     error
}

// NewCalls returns a Calls logging to rec.
func NewCalls(rec *Recorder) *Calls { return &Calls{Recorder: orNew(rec)} }

// ActionInvoke implements contracts.CallsPort.
func (f *Calls) ActionInvoke(_ context.Context, c contracts.Caller, action string, _ json.RawMessage) (json.RawMessage, error) {
	f.RecordCaller("ActionInvoke", c, action)
	return f.Result, f.Err
}

// ActionList implements contracts.CallsPort.
func (f *Calls) ActionList(_ context.Context, c contracts.Caller) ([]contracts.ActionInfo, error) {
	f.RecordCaller("ActionList", c)
	return f.Actions, f.Err
}

// Session fakes contracts.SessionPort.
type Session struct {
	*Recorder
	Result protocol.SessionPingResult
	Err    error
}

// NewSession returns a Session logging to rec.
func NewSession(rec *Recorder) *Session { return &Session{Recorder: orNew(rec)} }

// Ping implements contracts.SessionPort.
func (f *Session) Ping(_ context.Context, c contracts.Caller) (protocol.SessionPingResult, error) {
	f.RecordCaller("Ping", c)
	return f.Result, f.Err
}

// Settings fakes contracts.SettingsPort.
type Settings struct {
	*Recorder
	Value      any
	Source     string
	Values     map[string]any
	Entries    []contracts.SettingEntry
	Validation contracts.ValidationResult
	Err        error
}

// NewSettings returns a Settings logging to rec.
func NewSettings(rec *Recorder) *Settings { return &Settings{Recorder: orNew(rec)} }

// Get implements contracts.SettingsPort.
func (f *Settings) Get(_ context.Context, c contracts.Caller, key string) (any, string, error) {
	f.RecordCaller("Get", c, key)
	return f.Value, f.Source, f.Err
}

// List implements contracts.SettingsPort.
func (f *Settings) List(_ context.Context, c contracts.Caller) (map[string]any, error) {
	f.RecordCaller("List", c)
	return f.Values, f.Err
}

// Schema implements contracts.SettingsPort.
func (f *Settings) Schema(_ context.Context, c contracts.Caller, owner string) ([]contracts.SettingEntry, error) {
	f.RecordCaller("Schema", c, owner)
	return f.Entries, f.Err
}

// Validate implements contracts.SettingsPort.
func (f *Settings) Validate(_ context.Context, c contracts.Caller) (contracts.ValidationResult, error) {
	f.RecordCaller("Validate", c)
	return f.Validation, f.Err
}

// Secrets fakes contracts.SecretsPort.
type Secrets struct {
	*Recorder
	Value []byte
	Infos []contracts.SecretInfo
	Err   error
}

// NewSecrets returns a Secrets logging to rec.
func NewSecrets(rec *Recorder) *Secrets { return &Secrets{Recorder: orNew(rec)} }

// Get implements contracts.SecretsPort.
func (f *Secrets) Get(_ context.Context, c contracts.Caller, name string) ([]byte, error) {
	f.RecordCaller("Get", c, name)
	return f.Value, f.Err
}

// Names implements contracts.SecretsPort.
func (f *Secrets) Names(_ context.Context, c contracts.Caller) ([]contracts.SecretInfo, error) {
	f.RecordCaller("Names", c)
	return f.Infos, f.Err
}

// Set implements contracts.SecretsPort.
func (f *Secrets) Set(_ context.Context, c contracts.Caller, name string, _ []byte) error {
	f.RecordCaller("Set", c, name)
	return f.Err
}

// Grant implements contracts.SecretsPort.
func (f *Secrets) Grant(_ context.Context, c contracts.Caller, plugin, name string) error {
	f.RecordCaller("Grant", c, plugin, name)
	return f.Err
}

// Revoke implements contracts.SecretsPort.
func (f *Secrets) Revoke(_ context.Context, c contracts.Caller, plugin, name string) error {
	f.RecordCaller("Revoke", c, plugin, name)
	return f.Err
}

// Reveal implements contracts.SecretsPort.
func (f *Secrets) Reveal(_ context.Context, c contracts.Caller, name string) ([]byte, error) {
	f.RecordCaller("Reveal", c, name)
	return f.Value, f.Err
}

// Profile fakes contracts.ProfilePort.
type Profile struct {
	*Recorder
	Profiles []contracts.ProfileInfo
	Err      error
}

// NewProfile returns a Profile logging to rec.
func NewProfile(rec *Recorder) *Profile { return &Profile{Recorder: orNew(rec)} }

// List implements contracts.ProfilePort.
func (f *Profile) List(_ context.Context, c contracts.Caller) ([]contracts.ProfileInfo, error) {
	f.RecordCaller("List", c)
	return f.Profiles, f.Err
}

// Use implements contracts.ProfilePort.
func (f *Profile) Use(_ context.Context, c contracts.Caller, name string) error {
	f.RecordCaller("Use", c, name)
	return f.Err
}

// Theme fakes contracts.ThemePort.
type Theme struct {
	*Recorder
	Palette     protocol.Palette
	CheckResult contracts.ThemeCheckResult
	Err         error
}

// NewTheme returns a Theme logging to rec.
func NewTheme(rec *Recorder) *Theme { return &Theme{Recorder: orNew(rec)} }

// Get implements contracts.ThemePort.
func (f *Theme) Get(_ context.Context, c contracts.Caller) (protocol.Palette, error) {
	f.RecordCaller("Get", c)
	return f.Palette, f.Err
}

// Check implements contracts.ThemePort.
func (f *Theme) Check(_ context.Context, c contracts.Caller) (contracts.ThemeCheckResult, error) {
	f.RecordCaller("Check", c)
	return f.CheckResult, f.Err
}

// Keybinding fakes contracts.KeybindingPort.
type Keybinding struct {
	*Recorder
	Listing contracts.KeyListing
	Err     error
}

// NewKeybinding returns a Keybinding logging to rec.
func NewKeybinding(rec *Recorder) *Keybinding { return &Keybinding{Recorder: orNew(rec)} }

// List implements contracts.KeybindingPort.
func (f *Keybinding) List(_ context.Context, c contracts.Caller) (contracts.KeyListing, error) {
	f.RecordCaller("List", c)
	return f.Listing, f.Err
}

// Pods fakes contracts.PodRegistry. It rejects duplicates like the real one
// and keeps the merged status per id in Entries.
type Pods struct {
	*Recorder
	mu      sync.Mutex
	entries map[string]contracts.PodStatusUpdate
}

// NewPods returns a Pods logging to rec.
func NewPods(rec *Recorder) *Pods { return &Pods{Recorder: orNew(rec)} }

// Entry returns the merged update for id.
func (f *Pods) Entry(id string) (contracts.PodStatusUpdate, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[id]
	return e, ok
}

// Reserve implements contracts.PodRegistry.
func (f *Pods) Reserve(_ context.Context, id string) error {
	f.Record("Reserve", id)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		f.entries = map[string]contracts.PodStatusUpdate{}
	}
	if _, dup := f.entries[id]; dup {
		return contracts.NewError(protocol.CodeConflict, "duplicate plugin id", nil)
	}
	f.entries[id] = contracts.PodStatusUpdate{}
	return nil
}

// SetStatus implements contracts.PodRegistry.
func (f *Pods) SetStatus(_ context.Context, id string, u contracts.PodStatusUpdate) error {
	f.Record("SetStatus", id, u.Status)
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[id]
	if !ok {
		return contracts.NewError(protocol.CodeNotFound, "unknown plugin id", nil)
	}
	if u.Status != "" {
		e.Status = u.Status
	}
	if u.Detail != nil {
		e.Detail = u.Detail
	}
	if u.PID != nil {
		e.PID = u.PID
	}
	if u.StartedAt != nil {
		e.StartedAt = u.StartedAt
	}
	if u.LogPath != nil {
		e.LogPath = u.LogPath
	}
	f.entries[id] = e
	return nil
}

// Release implements contracts.PodRegistry.
func (f *Pods) Release(_ context.Context, id string) {
	f.Record("Release", id)
	f.mu.Lock()
	delete(f.entries, id)
	f.mu.Unlock()
}

// Catalog fakes contracts.CatalogDeclarer.
type Catalog struct {
	*Recorder
	Err error
}

// NewCatalog returns a Catalog logging to rec.
func NewCatalog(rec *Recorder) *Catalog { return &Catalog{Recorder: orNew(rec)} }

// Declare implements contracts.CatalogDeclarer.
func (f *Catalog) Declare(id string, _, _ json.RawMessage) error {
	f.Record("Declare", id)
	return f.Err
}

// Undeclare implements contracts.CatalogDeclarer.
func (f *Catalog) Undeclare(id string) { f.Record("Undeclare", id) }

// Reloader fakes contracts.Reloader.
type Reloader struct {
	*Recorder
	Applied  bool
	Problems []contracts.Problem
}

// NewReloader returns a Reloader logging to rec.
func NewReloader(rec *Recorder) *Reloader { return &Reloader{Recorder: orNew(rec)} }

// Reload implements contracts.Reloader.
func (f *Reloader) Reload(context.Context) (bool, []contracts.Problem) {
	f.Record("Reload")
	return f.Applied, f.Problems
}

// Slice fakes contracts.SliceHandle.
type Slice struct {
	*Recorder
	ns       string
	Revision uint64
	Err      error
}

// Set implements contracts.SliceHandle.
func (f *Slice) Set(json.RawMessage) (uint64, error) {
	f.Record("SliceSet", f.ns)
	return f.Revision, f.Err
}

// Patch implements contracts.SliceHandle.
func (f *Slice) Patch(_ json.RawMessage, ifRevision uint64) (uint64, error) {
	f.Record("SlicePatch", f.ns, ifRevision)
	return f.Revision, f.Err
}

// Unregister implements contracts.SliceHandle.
func (f *Slice) Unregister() { f.Record("SliceUnregister", f.ns) }

// Slices fakes contracts.SliceRegistrar, handing out one Slice per namespace.
type Slices struct {
	*Recorder
	mu      sync.Mutex
	Handles map[string]*Slice
	Err     error
}

// NewSlices returns a Slices logging to rec.
func NewSlices(rec *Recorder) *Slices { return &Slices{Recorder: orNew(rec)} }

// RegisterSlice implements contracts.SliceRegistrar.
func (f *Slices) RegisterSlice(spec state.Spec) (contracts.SliceHandle, error) {
	f.Record("RegisterSlice", spec.Namespace)
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Handles == nil {
		f.Handles = map[string]*Slice{}
	}
	if _, dup := f.Handles[spec.Namespace]; dup {
		return nil, state.ErrDuplicate
	}
	h := &Slice{Recorder: f.Recorder, ns: spec.Namespace}
	f.Handles[spec.Namespace] = h
	return h, nil
}

// Wrapper fakes contracts.WrapperStarter.
type Wrapper struct {
	*Recorder
	Err error
}

// NewWrapper returns a Wrapper logging to rec.
func NewWrapper(rec *Recorder) *Wrapper { return &Wrapper{Recorder: orNew(rec)} }

// Start implements contracts.WrapperStarter.
func (f *Wrapper) Start(id string, opening []protocol.Message) error {
	f.Record("Start", id, len(opening))
	return f.Err
}

// Clock is a manual clock: time moves only through Advance.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a Clock at start.
func NewClock(start time.Time) *Clock { return &Clock{now: start} }

// Now implements contracts.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Limiter fakes contracts.Limiter: it allows the first Burst calls per key
// within each one-second window of its Clock, a faithful stand-in for the
// 20/s rule that needs no timers. It is not logged.
type Limiter struct {
	Clock contracts.Clock
	Burst int
	mu    sync.Mutex
	start map[string]time.Time
	count map[string]int
}

// Allow implements contracts.Limiter.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.start == nil {
		l.start, l.count = map[string]time.Time{}, map[string]int{}
	}
	now := l.Clock.Now()
	if s, ok := l.start[key]; !ok || now.Sub(s) >= time.Second {
		l.start[key], l.count[key] = now, 0
	}
	if l.count[key] >= l.Burst {
		return false
	}
	l.count[key]++
	return true
}

// Host fakes contracts.Host.
type Host struct {
	*Recorder
	RunErr, CloseErr error
}

// NewHost returns a Host logging to rec.
func NewHost(rec *Recorder) *Host { return &Host{Recorder: orNew(rec)} }

// Run implements contracts.Host.
func (f *Host) Run(context.Context) error { f.Record("Run"); return f.RunErr }

// Close implements contracts.Host.
func (f *Host) Close() error { f.Record("Close"); return f.CloseErr }

// Config fakes contracts.ConfigPort.
type Config struct {
	*Recorder
	Reports []contracts.ReloadReport
	Err     error
}

// NewConfig returns a Config logging to rec.
func NewConfig(rec *Recorder) *Config { return &Config{Recorder: orNew(rec)} }

// Reload implements contracts.ConfigPort.
func (f *Config) Reload(_ context.Context, c contracts.Caller) ([]contracts.ReloadReport, error) {
	f.RecordCaller("ConfigReload", c)
	return f.Reports, f.Err
}

// Events fakes contracts.EventPort. Watch registers the notifier until ctx
// ends and returns at once, like the real port; Push delivers to watchers.
type Events struct {
	*Recorder
	Err error
	w   watchers
}

// NewEvents returns an Events logging to rec.
func NewEvents(rec *Recorder) *Events { return &Events{Recorder: orNew(rec)} }

// Emit implements contracts.EventPort.
func (f *Events) Emit(_ context.Context, c contracts.Caller, name string, _ json.RawMessage) error {
	f.RecordCaller("Emit", c, name)
	return f.Err
}

// Watch implements contracts.EventPort.
func (f *Events) Watch(ctx context.Context, c contracts.Caller, pattern string, n contracts.Notifier) error {
	f.RecordCaller("EventWatch", c, pattern)
	if f.Err != nil {
		return f.Err
	}
	f.w.add(ctx, n)
	return nil
}

// Push delivers m to every live watcher and returns how many got it.
func (f *Events) Push(m protocol.Message) int { return f.w.push(m) }

// Watching reports the number of live watchers.
func (f *Events) Watching() int { return f.w.count() }

// Hooks fakes contracts.HookPort.
type Hooks struct {
	*Recorder
	Results []contracts.HookResult
	Err     error
}

// NewHooks returns a Hooks logging to rec.
func NewHooks(rec *Recorder) *Hooks { return &Hooks{Recorder: orNew(rec)} }

// HookCall implements contracts.HookPort.
func (f *Hooks) HookCall(_ context.Context, c contracts.Caller, hook string, _ json.RawMessage) ([]contracts.HookResult, error) {
	f.RecordCaller("HookCall", c, hook)
	return f.Results, f.Err
}

// Plugins fakes contracts.PluginPort.
type Plugins struct {
	*Recorder
	Infos []contracts.PluginInfo
	Path  string
	Err   error
}

// NewPlugins returns a Plugins logging to rec.
func NewPlugins(rec *Recorder) *Plugins { return &Plugins{Recorder: orNew(rec)} }

// List implements contracts.PluginPort.
func (f *Plugins) List(_ context.Context, c contracts.Caller) ([]contracts.PluginInfo, error) {
	f.RecordCaller("PluginList", c)
	return f.Infos, f.Err
}

// Log implements contracts.PluginPort.
func (f *Plugins) Log(_ context.Context, c contracts.Caller, id string) (string, error) {
	f.RecordCaller("PluginLog", c, id)
	return f.Path, f.Err
}

// Surfaces fakes contracts.SurfacePort.
type Surfaces struct {
	*Recorder
	Err error
}

// NewSurfaces returns a Surfaces logging to rec.
func NewSurfaces(rec *Recorder) *Surfaces { return &Surfaces{Recorder: orNew(rec)} }

// SurfaceUpdate implements contracts.SurfacePort.
func (f *Surfaces) SurfaceUpdate(_ context.Context, c contracts.Caller, p protocol.SurfaceUpdateParams) error {
	f.RecordCaller("SurfaceUpdate", c, p.Surface)
	return f.Err
}

// SurfaceClose implements contracts.SurfacePort.
func (f *Surfaces) SurfaceClose(_ context.Context, c contracts.Caller, p protocol.SurfaceCloseParams) error {
	f.RecordCaller("SurfaceClose", c, p.Surface)
	return f.Err
}

// SlotContribute implements contracts.SurfacePort.
func (f *Surfaces) SlotContribute(_ context.Context, c contracts.Caller, p protocol.SlotContributeParams) error {
	f.RecordCaller("SlotContribute", c, p.SlotID)
	return f.Err
}

// ToastShow implements contracts.SurfacePort.
func (f *Surfaces) ToastShow(_ context.Context, c contracts.Caller, p protocol.ToastShowParams) error {
	f.RecordCaller("ToastShow", c, p.Title)
	return f.Err
}
