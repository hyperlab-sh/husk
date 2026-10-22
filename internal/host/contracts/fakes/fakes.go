// Package fakes holds no-op, recording fakes for every interface in
// internal/host/contracts. Each fake records its calls in Calls (method name
// first) and returns the configured Result and Err fields. Fakes are safe for
// concurrent use.
package fakes

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Recorder is the shared call log.
type Recorder struct {
	mu    sync.Mutex
	calls []string
}

// Record appends one call line.
func (r *Recorder) Record(method string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := method
	for _, a := range args {
		line += " " + fmt.Sprint(a)
	}
	r.calls = append(r.calls, line)
}

// Calls returns a copy of the recorded calls.
func (r *Recorder) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// Handler echoes an empty successful reply (or Reply when set).
type Handler struct {
	Recorder
	Reply *protocol.Message
}

// Serve implements contracts.Handler.
func (h *Handler) Serve(_ context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	h.Record("Serve", req.Method, c.PluginID)
	if h.Reply != nil {
		return *h.Reply
	}
	return protocol.Message{ID: req.ID, Result: json.RawMessage(`{}`)}
}

// Notifier records notifications.
type Notifier struct {
	Recorder
	Err error
}

// Notify implements contracts.Notifier.
func (n *Notifier) Notify(m protocol.Message) error {
	n.Record("Notify", m.Method)
	return n.Err
}

// State fakes contracts.StatePort.
type State struct {
	Recorder
	Read     contracts.StateReadResult
	Snap     contracts.StateSnapshotResult
	Revision uint64
	Err      error
}

// Get implements contracts.StatePort.
func (f *State) Get(_ context.Context, _ contracts.Caller, slice, path string) (contracts.StateReadResult, error) {
	f.Record("Get", slice, path)
	return f.Read, f.Err
}

// Set implements contracts.StatePort.
func (f *State) Set(_ context.Context, _ contracts.Caller, slice string, _ json.RawMessage) (uint64, error) {
	f.Record("Set", slice)
	return f.Revision, f.Err
}

// Patch implements contracts.StatePort.
func (f *State) Patch(_ context.Context, _ contracts.Caller, slice string, _ json.RawMessage, ifRevision uint64) (uint64, error) {
	f.Record("Patch", slice, ifRevision)
	return f.Revision, f.Err
}

// Snapshot implements contracts.StatePort.
func (f *State) Snapshot(_ context.Context, _ contracts.Caller, values bool) (contracts.StateSnapshotResult, error) {
	f.Record("Snapshot", values)
	return f.Snap, f.Err
}

// Watch implements contracts.StatePort.
func (f *State) Watch(_ context.Context, _ contracts.Caller, slices []string, _ contracts.Notifier) (contracts.StateSnapshotResult, error) {
	f.Record("Watch", slices)
	return f.Snap, f.Err
}

// Clear implements contracts.StatePort.
func (f *State) Clear(_ context.Context, _ contracts.Caller, namespace string) error {
	f.Record("Clear", namespace)
	return f.Err
}

// Calls fakes contracts.CallsPort.
type Calls struct {
	Recorder
	Result  json.RawMessage
	Actions []contracts.ActionInfo
	Err     error
}

// ActionInvoke implements contracts.CallsPort.
func (f *Calls) ActionInvoke(_ context.Context, _ contracts.Caller, action string, _ json.RawMessage) (json.RawMessage, error) {
	f.Record("ActionInvoke", action)
	return f.Result, f.Err
}

// ActionList implements contracts.CallsPort.
func (f *Calls) ActionList(context.Context, contracts.Caller) ([]contracts.ActionInfo, error) {
	f.Record("ActionList")
	return f.Actions, f.Err
}

// Session fakes contracts.SessionPort.
type Session struct {
	Recorder
	Result protocol.SessionPingResult
	Err    error
}

// Ping implements contracts.SessionPort.
func (f *Session) Ping(context.Context, contracts.Caller) (protocol.SessionPingResult, error) {
	f.Record("Ping")
	return f.Result, f.Err
}

// Settings fakes contracts.SettingsPort.
type Settings struct {
	Recorder
	Value  any
	Source string
	Values map[string]any
	Err    error
}

// Get implements contracts.SettingsPort.
func (f *Settings) Get(_ context.Context, _ contracts.Caller, key string) (any, string, error) {
	f.Record("Get", key)
	return f.Value, f.Source, f.Err
}

// List implements contracts.SettingsPort.
func (f *Settings) List(context.Context, contracts.Caller) (map[string]any, error) {
	f.Record("List")
	return f.Values, f.Err
}

// Secrets fakes contracts.SecretsPort.
type Secrets struct {
	Recorder
	Value []byte
	Infos []contracts.SecretInfo
	Err   error
}

// Get implements contracts.SecretsPort.
func (f *Secrets) Get(_ context.Context, _ contracts.Caller, name string) ([]byte, error) {
	f.Record("Get", name)
	return f.Value, f.Err
}

// Names implements contracts.SecretsPort.
func (f *Secrets) Names(context.Context, contracts.Caller) ([]contracts.SecretInfo, error) {
	f.Record("Names")
	return f.Infos, f.Err
}

// Set implements contracts.SecretsPort.
func (f *Secrets) Set(_ context.Context, _ contracts.Caller, name string, _ []byte) error {
	f.Record("Set", name)
	return f.Err
}

// Grant implements contracts.SecretsPort.
func (f *Secrets) Grant(_ context.Context, _ contracts.Caller, plugin, name string) error {
	f.Record("Grant", plugin, name)
	return f.Err
}

// Revoke implements contracts.SecretsPort.
func (f *Secrets) Revoke(_ context.Context, _ contracts.Caller, plugin, name string) error {
	f.Record("Revoke", plugin, name)
	return f.Err
}

// Reveal implements contracts.SecretsPort.
func (f *Secrets) Reveal(_ context.Context, _ contracts.Caller, name string) ([]byte, error) {
	f.Record("Reveal", name)
	return f.Value, f.Err
}

// Profile fakes contracts.ProfilePort.
type Profile struct {
	Recorder
	Profiles []contracts.ProfileInfo
	Err      error
}

// List implements contracts.ProfilePort.
func (f *Profile) List(context.Context, contracts.Caller) ([]contracts.ProfileInfo, error) {
	f.Record("List")
	return f.Profiles, f.Err
}

// Use implements contracts.ProfilePort.
func (f *Profile) Use(_ context.Context, _ contracts.Caller, name string) error {
	f.Record("Use", name)
	return f.Err
}

// Theme fakes contracts.ThemePort.
type Theme struct {
	Recorder
	Palette     protocol.Palette
	CheckResult contracts.ThemeCheckResult
	Err         error
}

// Get implements contracts.ThemePort.
func (f *Theme) Get(context.Context, contracts.Caller) (protocol.Palette, error) {
	f.Record("Get")
	return f.Palette, f.Err
}

// Check implements contracts.ThemePort.
func (f *Theme) Check(context.Context, contracts.Caller) (contracts.ThemeCheckResult, error) {
	f.Record("Check")
	return f.CheckResult, f.Err
}

// Keybinding fakes contracts.KeybindingPort.
type Keybinding struct {
	Recorder
	Listing contracts.KeyListing
	Err     error
}

// List implements contracts.KeybindingPort.
func (f *Keybinding) List(context.Context, contracts.Caller) (contracts.KeyListing, error) {
	f.Record("List")
	return f.Listing, f.Err
}

// Pods fakes contracts.PodRegistry. It rejects duplicates like the real one.
type Pods struct {
	Recorder
	mu       sync.Mutex
	Statuses map[string]contracts.PodStatus
}

// Reserve implements contracts.PodRegistry.
func (f *Pods) Reserve(id string) error {
	f.Record("Reserve", id)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Statuses == nil {
		f.Statuses = map[string]contracts.PodStatus{}
	}
	if _, dup := f.Statuses[id]; dup {
		return contracts.NewError(protocol.CodeConflict, "duplicate plugin id", nil)
	}
	f.Statuses[id] = ""
	return nil
}

// SetStatus implements contracts.PodRegistry.
func (f *Pods) SetStatus(id string, s contracts.PodStatus, detail string) error {
	f.Record("SetStatus", id, s, detail)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Statuses[id]; !ok {
		return contracts.NewError(protocol.CodeNotFound, "unknown plugin id", nil)
	}
	f.Statuses[id] = s
	return nil
}

// Release implements contracts.PodRegistry.
func (f *Pods) Release(id string) {
	f.Record("Release", id)
	f.mu.Lock()
	delete(f.Statuses, id)
	f.mu.Unlock()
}

// Catalog fakes contracts.CatalogDeclarer.
type Catalog struct {
	Recorder
	Err error
}

// Declare implements contracts.CatalogDeclarer.
func (f *Catalog) Declare(id string, _, _ json.RawMessage) error {
	f.Record("Declare", id)
	return f.Err
}

// Undeclare implements contracts.CatalogDeclarer.
func (f *Catalog) Undeclare(id string) { f.Record("Undeclare", id) }

// Reloader fakes contracts.Reloader.
type Reloader struct {
	Recorder
	Applied  bool
	Problems []contracts.Problem
}

// Reload implements contracts.Reloader.
func (f *Reloader) Reload(context.Context) (bool, []contracts.Problem) {
	f.Record("Reload")
	return f.Applied, f.Problems
}

// Slice fakes contracts.SliceHandle.
type Slice struct {
	Recorder
	Revision uint64
	Err      error
}

// Set implements contracts.SliceHandle.
func (f *Slice) Set(json.RawMessage) (uint64, error) { f.Record("Set"); return f.Revision, f.Err }

// Patch implements contracts.SliceHandle.
func (f *Slice) Patch(_ json.RawMessage, ifRevision uint64) (uint64, error) {
	f.Record("Patch", ifRevision)
	return f.Revision, f.Err
}

// Unregister implements contracts.SliceHandle.
func (f *Slice) Unregister() { f.Record("Unregister") }

// Slices fakes contracts.SliceRegistrar, handing out one Slice per namespace.
type Slices struct {
	Recorder
	mu      sync.Mutex
	Handles map[string]*Slice
	Err     error
}

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
	h := &Slice{}
	f.Handles[spec.Namespace] = h
	return h, nil
}

// Wrapper fakes contracts.WrapperStarter.
type Wrapper struct {
	Recorder
	Err error
}

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
// 20/s rule that needs no timers.
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
	Recorder
	RunErr, CloseErr error
}

// Run implements contracts.Host.
func (f *Host) Run(context.Context) error { f.Record("Run"); return f.RunErr }

// Close implements contracts.Host.
func (f *Host) Close() error { f.Record("Close"); return f.CloseErr }
