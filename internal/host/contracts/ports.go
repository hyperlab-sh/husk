package contracts

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// All ports are consumer-defined: the host declares what its adapters need and
// the owning module is adapted to it, never imported here. Every method takes
// the verified Caller (never decoded from params) and returns plain Go values.
// Failures are CodedError values; adapters map them to wire errors by code.

// StateReadResult is one slice as state.get and state.watch report it.
type StateReadResult struct {
	Slice    string
	Owner    string
	Revision uint64
	// Value is the whole slice value, or the value at the requested pointer.
	Value json.RawMessage
}

// StateSnapshotResult is the answer to state.snapshot and the baseline of
// state.watch. Entry.Value is empty when values were not requested.
type StateSnapshotResult struct {
	Seq     uint64
	Entries []state.Entry
}

// StatePort serves the state.* methods.
type StatePort interface {
	// Get serves state.get for any caller. path is an optional RFC 6901 pointer.
	// Unknown slice or unresolved pointer is not_found.
	Get(ctx context.Context, c Caller, slice, path string) (StateReadResult, error)
	// Set serves state.set: replace the caller plugin's own slice. Returns the
	// new revision. Writing another owner's slice, or a CLI caller, is denied.
	Set(ctx context.Context, c Caller, slice string, value json.RawMessage) (uint64, error)
	// Patch serves state.patch (RFC 7396). ifRevision 0 means unconditional; a
	// stale revision is conflict with data {"revision": current}.
	Patch(ctx context.Context, c Caller, slice string, patch json.RawMessage, ifRevision uint64) (uint64, error)
	// Snapshot serves state.snapshot. Values only when values is true.
	Snapshot(ctx context.Context, c Caller, values bool) (StateSnapshotResult, error)
	// Watch serves state.watch: it returns the baseline for slices (all when
	// empty) and afterwards pushes state.changed notifications on n until ctx
	// ends. Zero or more slices names.
	Watch(ctx context.Context, c Caller, slices []string, n Notifier) (StateSnapshotResult, error)
	// Clear serves state.clear, user only (plugin callers get denied).
	Clear(ctx context.Context, c Caller, namespace string) error
}

// ActionInfo is one catalog entry as action.list reports it.
type ActionInfo struct {
	ID     string
	Owner  string
	Public bool
	Live   bool
}

// CallsPort serves the socket-originated methods that resolve through the
// Dispatcher and Calls table. The protocol has no call.* prefix: Calls is
// reached by action.invoke, so this port carries the action.* methods.
type CallsPort interface {
	// ActionInvoke serves action.invoke and blocks until the owner's reply, a
	// timeout (timeout), a refusal (denied, not_found, unavailable) or ctx ends.
	// args is a JSON object or nil. The result is the owner's result object.
	ActionInvoke(ctx context.Context, c Caller, action string, args json.RawMessage) (json.RawMessage, error)
	// ActionList serves action.list.
	ActionList(ctx context.Context, c Caller) ([]ActionInfo, error)
}

// SessionPort serves the session.* methods.
type SessionPort interface {
	// Ping serves session.ping. It has no caller restriction.
	Ping(ctx context.Context, c Caller) (protocol.SessionPingResult, error)
}

// SettingEntry is one settings schema entry.
type SettingEntry struct {
	Key     string
	Type    string
	Default any
	Enum    []string
	Min     *int64
	Max     *int64
	Doc     string
	Owner   string
	Apply   string
}

// SettingsPort serves settings.get, settings.list, settings.schema and
// settings.validate. Only get is open to plugins; the others are CLI methods
// and answer denied to a plugin caller.
//
// A plugin caller sees exactly the keys its own manifest declared (owner
// "plugin:<id>") plus public husk.* keys, never an arbitrary prefix subtree
// (decisions D34 and D34a). Any other key is not_found, whether it exists or
// not. User callers see everything. Rate limiting (20/s per plugin) is the
// adapter's job, via Limiter, before the port is called.
type SettingsPort interface {
	// Get returns the effective value and the layer it came from.
	Get(ctx context.Context, c Caller, key string) (value any, source string, err error)
	// List returns the visible keys with their effective values.
	List(ctx context.Context, c Caller) (map[string]any, error)
	// Schema serves settings.schema. owner "" means every owner.
	Schema(ctx context.Context, c Caller, owner string) ([]SettingEntry, error)
	// Validate serves settings.validate: a dry rebuild of the current
	// documents, reporting every problem.
	Validate(ctx context.Context, c Caller) (ValidationResult, error)
}

// SecretInfo is secret metadata. It never carries a value.
type SecretInfo struct {
	Name     string
	Declared []string
	Granted  []string
	Set      bool
	Broken   bool
}

// SecretsPort serves the secrets.* methods. secrets.get and secrets.names are
// open to plugins (get returns only what was granted to c.PluginID); set,
// grant, revoke and reveal are user only and answer denied to a plugin caller.
// The adapter rate-limits secrets.get (20/s per plugin) before calling.
type SecretsPort interface {
	// Get serves secrets.get. Refusals carry data.reason: not_declared,
	// needs_grant or user_denied.
	Get(ctx context.Context, c Caller, name string) ([]byte, error)
	// Names serves secrets.names.
	Names(ctx context.Context, c Caller) ([]SecretInfo, error)
	// Set serves secrets.set (user only).
	Set(ctx context.Context, c Caller, name string, value []byte) error
	// Grant serves secrets.grant (user only).
	Grant(ctx context.Context, c Caller, plugin, name string) error
	// Revoke serves secrets.revoke (user only).
	Revoke(ctx context.Context, c Caller, plugin, name string) error
	// Reveal serves secrets.reveal (user only).
	Reveal(ctx context.Context, c Caller, name string) ([]byte, error)
}

// ProfileInfo describes one profile.
type ProfileInfo struct {
	Name   string
	Source string
	Active bool
}

// ProfilePort serves profile.list and profile.use.
type ProfilePort interface {
	// List serves profile.list.
	List(ctx context.Context, c Caller) ([]ProfileInfo, error)
	// Use serves profile.use (user only).
	Use(ctx context.Context, c Caller, name string) error
}

// Problem is one validation issue. Messages never contain secret values.
type Problem struct {
	Severity string
	Layer    string
	Key      string
	Message  string
}

// ValidationResult is the outcome of a dry build: whether the candidate would
// apply and every problem found (all of them, not the first).
type ValidationResult struct {
	Applied  bool
	Problems []Problem
}

// ThemeCheckResult is what theme.check reports: every violated pair.
type ThemeCheckResult = ValidationResult

// ThemePort serves theme.get and theme.check.
type ThemePort interface {
	// Get serves theme.get for every authenticated caller.
	Get(ctx context.Context, c Caller) (protocol.Palette, error)
	// Check serves theme.check (user only).
	Check(ctx context.Context, c Caller) (ThemeCheckResult, error)
}

// KeyEntry is one resolved or shadowed binding as keys.list reports it.
type KeyEntry struct {
	Keys       []string
	Action     string
	Hint       string
	Focus      string
	Plugin     string
	Disable    bool
	Layer      string
	State      string
	ShadowedBy string
}

// KeyProblem is one loader or conversion problem.
type KeyProblem struct {
	Code    string
	Layer   string
	Key     string
	Message string
}

// KeyListing is the keys.list answer.
type KeyListing struct {
	Entries  []KeyEntry
	Problems []KeyProblem
}

// KeybindingPort serves keys.list. A plugin caller sees only its own
// resolved bindings and their layer.
type KeybindingPort interface {
	// List serves keys.list.
	List(ctx context.Context, c Caller) (KeyListing, error)
}

// ReloadReport is one service's outcome in a reload.
type ReloadReport struct {
	Service  string
	Gen      uint64
	Applied  bool
	Problems []Problem
}

// ConfigPort serves config.reload. The Services coordinator implements it;
// the reload reason is "cli" and the coordinator, not the wire, picks it.
type ConfigPort interface {
	// Reload serves config.reload (user only) and returns one report per
	// service once the coalesced reload has completed.
	Reload(ctx context.Context, c Caller) ([]ReloadReport, error)
}

// EventPort serves the event.* requests. event.deliver is outbound only
// (Pod.Notify or the Notifier) and has no port.
type EventPort interface {
	// Emit serves event.emit. The Server-set source is c.PluginID. A plugin may
	// emit only under "<plugin_id>."; the bare CLI only under "user.", else
	// denied. Returning nil means the event is on the bus, not delivered.
	Emit(ctx context.Context, c Caller, name string, data json.RawMessage) error
	// Watch serves event.watch: it subscribes the connection to events
	// matching pattern (an exact name or "<prefix>.*"), pushes event.deliver
	// notifications on n, and returns when ctx ends or the subscription is
	// refused. The subscription is cancelled on return.
	Watch(ctx context.Context, c Caller, pattern string, n Notifier) error
}

// HookResult is one subscriber's answer to a hook call.
type HookResult struct {
	Plugin string
	Data   json.RawMessage
	// Err is nil on success; otherwise its code is timeout, cancelled or
	// the subscriber's own (failed).
	Err CodedError
}

// HookPort serves hook.call. Hook subscription and publication are manifest
// declarations (no wire method), so there is no registration seam here.
type HookPort interface {
	// HookCall serves hook.call from a socket caller and returns one result
	// per live subscriber once all are accounted for (empty when none).
	HookCall(ctx context.Context, c Caller, hook string, data json.RawMessage) ([]HookResult, error)
}

// PluginInfo is a plugin's spec summary plus liveness (plugin.list).
type PluginInfo struct {
	ID          string
	Name        string
	Version     string
	Description string
	Status      PodStatus
	PID         int
	StartedAt   time.Time
	LastError   string
	Log         string
}

// PluginPort serves plugin.list and plugin.log. Pods implements it.
type PluginPort interface {
	// List serves plugin.list.
	List(ctx context.Context, c Caller) ([]PluginInfo, error)
	// Log serves plugin.log: the log file path for a plugin id, or not_found.
	Log(ctx context.Context, c Caller, pluginID string) (string, error)
}

// SurfacePort serves the plugin-originated notifications and requests that
// Stem Surfaces owns, which write husk.surfaces. The surface and toast
// methods are notifications: the adapter discards the error code after
// logging it, but a refusal (a surface the plugin does not occupy is denied,
// invalid fields are invalid_params) still comes back as a CodedError so a
// request-shaped method (slot.contribute) can reply with it.
type SurfacePort interface {
	// SurfaceUpdate serves surface.update. The plugin is c.PluginID; any
	// PluginID in p is ignored.
	SurfaceUpdate(ctx context.Context, c Caller, p protocol.SurfaceUpdateParams) error
	// SurfaceClose serves surface.close.
	SurfaceClose(ctx context.Context, c Caller, p protocol.SurfaceCloseParams) error
	// SlotContribute serves slot.contribute (a request: refusals are replies).
	SlotContribute(ctx context.Context, c Caller, p protocol.SlotContributeParams) error
	// ToastShow serves toast.show from a plugin or the CLI.
	ToastShow(ctx context.Context, c Caller, p protocol.ToastShowParams) error
}
