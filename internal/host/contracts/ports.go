package contracts

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// All ports are consumer-defined: the host declares what its adapters need and
// the owning module is adapted to it, never imported here. Every method takes
// the verified Caller (never decoded from params) and returns plain Go values.
// Failures are CodedError values; adapters map them to wire errors by code.

// StateReadResult is one slice as state.get and state.watch report it.
type StateReadResult struct {
	Slice    string `json:"slice"`
	Owner    string `json:"owner"`
	Revision uint64 `json:"revision"`
	// Value is the whole slice value, or the value at the requested pointer.
	Value json.RawMessage `json:"value"`
}

// StateRetention is a slice's persistence policy as state.snapshot reports it.
type StateRetention struct {
	Policy string `json:"policy"`
	N      int    `json:"n,omitempty"`
}

// StateSlice is one slice descriptor as the state.snapshot and state.watch
// replies carry it. Value is set only when values were requested.
type StateSlice struct {
	Slice      string          `json:"slice"`
	Owner      string          `json:"owner"`
	Scope      string          `json:"scope"`
	Persistent bool            `json:"persistent"`
	Retention  *StateRetention `json:"retention,omitempty"`
	Revision   uint64          `json:"revision"`
	Bytes      int             `json:"bytes"`
	Hydrated   bool            `json:"hydrated"`
	Value      json.RawMessage `json:"value,omitempty"`
}

// StateSnapshotResult is the answer to state.snapshot and the baseline of
// state.watch.
type StateSnapshotResult struct {
	Seq    uint64       `json:"seq"`
	Slices []StateSlice `json:"slices"`
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
	// Watch serves state.watch: it registers n for slices (all when empty),
	// returns the baseline at once (it becomes the reply), then pushes
	// state.changed notifications on n. The registration is removed when ctx
	// ends (the connection closed).
	Watch(ctx context.Context, c Caller, slices []string, n Notifier) (StateSnapshotResult, error)
	// Clear serves state.clear, user only (plugin callers get denied). force
	// is the request's "force" flag: when true the husk.before-state-clear
	// hook is not fired at all (hooks.md, "Host-fired hooks"); false when
	// absent.
	Clear(ctx context.Context, c Caller, namespace string, force bool) error
}

// ActionInfo is one catalog entry as action.list reports it.
type ActionInfo struct {
	ID     string `json:"id"`
	Owner  string `json:"owner"`
	Public bool   `json:"public"`
	Live   bool   `json:"live"`
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
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Default any      `json:"default"`
	Enum    []string `json:"enum,omitempty"`
	Min     *int64   `json:"min,omitempty"`
	Max     *int64   `json:"max,omitempty"`
	Doc     string   `json:"doc,omitempty"`
	Owner   string   `json:"owner"`
	Apply   string   `json:"apply"`
	// Public marks a host or distribution entry plugins may read.
	Public bool `json:"public,omitempty"`
}

// SettingValue is one settings.list row: a known key or a retained unknown
// key, its value as written (durations in milliseconds) and the layer that
// supplied it, or "default".
type SettingValue struct {
	Key    string `json:"key"`
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// SettingSetResult is the one-key write and its reload reports.
type SettingSetResult struct {
	Key     string         `json:"key"`
	Value   any            `json:"value"`
	Source  string         `json:"source"`
	Reports []ReloadReport `json:"reports"`
}

// SettingsPort serves setting reads, validation and one-key user-layer writes.
// Only get is open to plugins; list, schema, validate and set are
// CLI methods and answer denied to a plugin caller (plugins read their own keys
// with get).
//
// A plugin caller sees exactly the keys its own manifest declared (owner
// "plugin:<id>") plus public husk.* keys, never an arbitrary prefix subtree
// (decisions D34 and D34a). Any other key is not_found, whether it exists or
// not. User callers see everything. Rate limiting (20/s per plugin) is the
// adapter's job, via Limiter, before the port is called.
type SettingsPort interface {
	// Get returns the effective value and the layer it came from.
	Get(ctx context.Context, c Caller, key string) (value any, source string, err error)
	// List returns every known key and every retained unknown key, sorted by
	// key (CLI only).
	List(ctx context.Context, c Caller) ([]SettingValue, error)
	// Schema serves settings.schema. owner "" means every owner.
	Schema(ctx context.Context, c Caller, owner string) ([]SettingEntry, error)
	// Validate serves settings.validate: a dry rebuild of the current
	// documents, reporting every problem.
	Validate(ctx context.Context, c Caller) (ValidationResult, error)
	// Set writes a registered value in the user layer and awaits its reload.
	// CLI and host callers are admitted; plugins and input are denied.
	Set(ctx context.Context, c Caller, key string, value any) (SettingSetResult, error)
}

// SecretInfo is one entry of secrets.names: metadata, never a value. Set is
// nil when the caller may not learn whether a value exists (a plugin that is
// not granted the name).
type SecretInfo struct {
	Name     string   `json:"name"`
	Declared []string `json:"declared"`
	Granted  []string `json:"granted"`
	Set      *bool    `json:"set,omitempty"`
	Required bool     `json:"required"`
}

// SecretNames is the secrets.names result. Storage is "ok" or "unavailable";
// the entries are sorted by name.
type SecretNames struct {
	Storage string       `json:"storage"`
	Secrets []SecretInfo `json:"secrets"`
}

// SecretsPort serves the secrets.* methods. secrets.get and secrets.names are
// open to plugins (get returns only what was granted to c.PluginID); set,
// grant, revoke and reveal are user only and answer denied to a plugin caller.
// The adapter rate-limits secrets.get (20/s per plugin) before calling.
type SecretsPort interface {
	// Get serves secrets.get. A plugin's read of a declared, ungranted name
	// blocks on the confirm dialog until it is answered; ctx ends the wait.
	// Refusals carry data.reason: not_declared, unset, user_denied,
	// dialog_pending or storage. A cli caller takes the audited reveal path.
	Get(ctx context.Context, c Caller, name string) (string, error)
	// Names serves secrets.names.
	Names(ctx context.Context, c Caller) (SecretNames, error)
	// Set serves secrets.set (user only). The value is UTF-8 text.
	Set(ctx context.Context, c Caller, name, value string) error
	// Grant serves secrets.grant (user only); it returns after the write ran.
	Grant(ctx context.Context, c Caller, plugin, name string) error
	// Revoke serves secrets.revoke (user only); it returns after the write ran.
	Revoke(ctx context.Context, c Caller, plugin, name string) error
	// Reveal serves secrets.reveal (user only), the audited path of a value to
	// a human.
	Reveal(ctx context.Context, c Caller, name string) (string, error)
}

// ProfileInfo describes one profile.
type ProfileInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Active bool   `json:"active"`
}

// ProfilePort serves profile.list and profile.use. Both are CLI methods: a
// plugin caller gets denied.
type ProfilePort interface {
	// List serves profile.list.
	List(ctx context.Context, c Caller) ([]ProfileInfo, error)
	// Use serves profile.use (user only): it returns once the reload the
	// change caused has ended, with that flight's reports.
	Use(ctx context.Context, c Caller, name string) (ProfileUseResult, error)
}

// ProfileUseResult is the profile.use reply.
type ProfileUseResult struct {
	Active  string         `json:"active"`
	Reports []ReloadReport `json:"reports"`
}

// ValidationResult is the outcome of a dry build: whether a reload now would
// publish that Service's candidate, and every problem found (all of them, not
// the first).
type ValidationResult struct {
	OK       bool               `json:"ok"`
	Problems []protocol.Problem `json:"problems"`
}

// ThemeCheckResult is what theme.check reports: every violated pair.
type ThemeCheckResult = ValidationResult

// ThemeEntry is one theme resolved by the serving Theme snapshot.
type ThemeEntry struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Current bool   `json:"current"`
}

// ThemePort serves theme.get, theme.list and theme.check.
type ThemePort interface {
	// Get serves theme.get for every authenticated caller, returning the palette
	// and a detached index map from one snapshot read.
	Get(ctx context.Context, c Caller) (protocol.Palette, map[string]int, error)
	// List returns the serving themes, built-in first then sorted by name.
	List(ctx context.Context, c Caller) ([]ThemeEntry, error)
	// Check serves theme.check (user only).
	Check(ctx context.Context, c Caller) (ThemeCheckResult, error)
}

// KeyEntry is one resolved or shadowed binding as keys.list reports it.
type KeyEntry struct {
	Keys       []string         `json:"keys,omitempty"`
	Action     string           `json:"action,omitempty"`
	Args       map[string]any   `json:"args,omitempty"`
	Hint       string           `json:"hint,omitempty"`
	Focus      string           `json:"focus,omitempty"`
	Plugin     string           `json:"plugin,omitempty"`
	Disable    bool             `json:"disable,omitempty"`
	Source     protocol.Source  `json:"source"`
	State      string           `json:"state"`
	ShadowedBy *protocol.Source `json:"shadowed_by,omitempty"`
}

// KeyListing is the keys.list answer.
type KeyListing struct {
	Entries  []KeyEntry         `json:"entries,omitempty"`
	Problems []protocol.Problem `json:"problems,omitempty"`
}

// KeybindingPort serves keys.list. A plugin caller sees its own entries and
// problems plus effective husk.* and declared rail-item bindings. Users see
// the full listing.
type KeybindingPort interface {
	// List serves keys.list.
	List(ctx context.Context, c Caller) (KeyListing, error)
}

// ReloadReport is one service's outcome in a reload.
type ReloadReport struct {
	Service  string             `json:"service"`
	Gen      uint64             `json:"gen"`
	Applied  bool               `json:"applied"`
	Changed  bool               `json:"changed"`
	Problems []protocol.Problem `json:"problems,omitempty"`
}

// ConfigPort serves config.reload, a CLI method: a plugin caller gets denied. The Services coordinator implements it;
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
	// Watch serves event.watch with the same lifecycle as StatePort.Watch: it
	// registers n for events matching pattern (an exact name or "<prefix>.*"),
	// pushes event.deliver notifications on n afterwards, and returns nil at
	// once, so the adapter replies {} before the first delivery and the
	// request does not hold an in-flight slot. The subscription is removed
	// when ctx ends (the connection closed). A refused pattern is
	// invalid_params and registers nothing.
	Watch(ctx context.Context, c Caller, pattern string, n Notifier) error
}

// HookResult is one subscriber's entry in a hook outcome.
type HookResult struct {
	Plugin string
	// Data is a transform stage's replacement as a JSON object; nil on
	// pass-through, "{}" for an empty replacement.
	Data json.RawMessage
	// Items is a collect subscriber's items as a JSON array; nil when none.
	Items json.RawMessage
	// Veto is true when a gate subscriber vetoed; Reason is its reason.
	Veto   bool
	Reason string
	// Err is nil on success; otherwise the protocol Error the leg produced
	// (timeout, cancelled, busy, unavailable, invalid_params or the
	// subscriber's own). A DataError carries the error's data member.
	Err CodedError
}

// HookVeto is one gate objection.
type HookVeto struct {
	Plugin string
	Reason string
}

// HookOutcome is the aggregate an owner's hook.call receives.
type HookOutcome struct {
	// Kind is "transform", "collect" or "gate".
	Kind string
	// Data is a transform's value after the last stage as a JSON object;
	// nil for the other kinds.
	Data json.RawMessage
	// Allowed is false only for a gate with at least one veto.
	Allowed bool
	// Vetoes lists every gate veto in priority order.
	Vetoes []HookVeto
	// Results has one entry per subscriber asked, in priority order.
	Results []HookResult
}

// HookSubscriber is one registration in a hook.list entry.
type HookSubscriber struct {
	Plugin   string
	Priority int
	Live     bool
}

// HookInfo is one declared hook as hook.list reports it.
type HookInfo struct {
	ID          string
	Owner       string
	Kind        string
	TimeoutMS   int64
	Title       string
	Description string
	Subscribers []HookSubscriber
}

// HookPort serves hook.call and hook.list. Hook subscription and publication
// are manifest declarations (no wire method), so there is no registration
// seam here.
type HookPort interface {
	// HookCall serves hook.call from the hook's owner (the adapter admits
	// plugin callers only) and returns the outcome once every subscriber
	// asked is accounted for. A refusal before any subscriber is asked is a
	// CodedError (denied, invalid_params, not_found, busy).
	HookCall(ctx context.Context, c Caller, hook string, data json.RawMessage) (HookOutcome, error)
	// HookList serves hook.list: every declared hook with its subscribers
	// and their liveness. No permission filter.
	HookList(ctx context.Context, c Caller) ([]HookInfo, error)
}

// PluginInfo is a plugin's spec summary plus liveness (plugin.list).
type PluginInfo struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Description string    `json:"description"`
	Status      PodStatus `json:"status"`
	PID         int       `json:"pid"`
	StartedAt   time.Time `json:"started_at"`
	LastError   string    `json:"last_error"`
	Log         string    `json:"log"`
	Dir         string    `json:"dir"`
}

// PluginRemoval is the plugin.remove answer: the plugin's last husk.pods
// status (never live), the log path plugin.log would have answered ("" when
// no log was opened) and the one reload's reports, the same type
// config.reload returns.
type PluginRemoval struct {
	Plugin  string         `json:"plugin"`
	Status  PodStatus      `json:"status"`
	Log     string         `json:"log"`
	Reports []ReloadReport `json:"reports"`
}

// PluginSource selects exactly one absolute plugin path or directory.
type PluginSource struct {
	Path string `json:"path,omitempty"`
	Dir  string `json:"dir,omitempty"`
}

// PluginWarning is a conversion diagnostic returned by plugin.add.
type PluginWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PluginAdded is the final launch outcome; removed is a reply-only status.
type PluginAdded struct {
	Plugin   string          `json:"plugin"`
	Status   string          `json:"status"`
	Log      string          `json:"log"`
	Warnings []PluginWarning `json:"warnings"`
}

// PluginAddRefusal describes a directory entry that received no Pod.
type PluginAddRefusal struct {
	Path    string `json:"path"`
	Plugin  string `json:"plugin"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PluginAddition is one batch's final outcomes and shared reload reports.
type PluginAddition struct {
	Added   []PluginAdded      `json:"added"`
	Refused []PluginAddRefusal `json:"refused"`
	Reports []ReloadReport     `json:"reports"`
}

// PluginPort serves the CLI-only plugin lifecycle methods.
type PluginPort interface {
	// Add loads a batch without persisting configuration. ctx only detaches
	// the caller; an admitted batch completes under the Host's context.
	Add(ctx context.Context, c Caller, source PluginSource) (PluginAddition, error)
	// List serves plugin.list.
	List(ctx context.Context, c Caller) ([]PluginInfo, error)
	// Log serves plugin.log: the log file path for a plugin id, or not_found.
	Log(ctx context.Context, c Caller, pluginID string) (string, error)
	// Remove serves plugin.remove: it unloads one plugin from this instance
	// and answers once the Services reload that no longer sees it has
	// completed. Unknown ids are not_found, a closing host is unavailable
	// (data.reason shutdown), and a shutdown during the removal is cancelled
	// (data.reason shutdown). ctx ending only detaches the caller: the
	// removal runs to completion.
	Remove(ctx context.Context, c Caller, pluginID string) (PluginRemoval, error)
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
