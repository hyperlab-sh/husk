package contracts

import (
	"context"
	"encoding/json"

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

// SettingsPort serves settings.get and settings.list.
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

// ThemeCheckResult is what theme.check reports: every violated pair.
type ThemeCheckResult struct {
	Applied  bool
	Problems []Problem
}

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
