---
type: Design Spec
title: "State: the live slice tree"
description: "The single in-memory tree of owner-registered slices that answers what is true right now in one Husk instance."
status: planned
generated: { by: claude/claude-opus-5-5, at: 2026-10-22T06:00:00Z }
tags: [architecture-v2, substrate]
---

# State: the live slice tree

## Level 0: What and why

State is one in-memory tree per Husk instance, divided into **slices**. Each
slice is a JSON document registered by exactly one owner, with a scope, a
persistence flag and a retention policy. State is the one place to ask "what is
true right now": the Dispatcher asks it whether an action's owner is live, the
Surfaces module keeps focus and layout in it, `husk state` reads it from the
CLI, Store persists from it and hydrates it on start, and a test harness
inspects it instead of reaching into the Bubble Tea model.

The problem it solves: before State, truth was scattered across workbench model
fields and the publication registry in the command host, mixed with config-derived
catalog data and input bookkeeping. Nothing outside the model could read it,
nothing persisted it, and nothing told a plugin it changed. The private
`workbenchModel` now lives in `pkg/workbench` and reads the host's State snapshots.

State is **not**:

- persistence. It never touches a file. [Store](store.md) reads State and
  hydrates it.
- config. The action catalog, the resolved keymap, themes, settings and
  declared publications are loaded and reloaded by [Services](services.md) and
  [Pods](pods.md) manifest conversion, never written into State.
- a schema registry. Values are JSON documents; each owner validates its own.
- an event system. It reports changes through a listener hook; the
  Event bus ([Message kinds](message-kinds.md)) turns them into `state.changed` events.
- a secrets holder. Every slice is readable by every reader.

Who depends on it: Stem modules (Surfaces, the
[Dispatcher](message-kinds.md), the Event bus adapter), the Profile service,
[Pods](pods.md) (liveness and the per-plugin slice), [Store](store.md)
(retention, hydration), the socket [Server](pods.md) (reads for `state.get`,
`state.watch`, `husk state`). State depends on nothing in Husk: it imports only
the Go standard library.

## Level 1: Contract

### Package and dependency direction

The package is `substrate/state`. Directions, all one-way:

- Stem, Services, Pods and Store import State. State imports none of them.
- Store implements `state.Hydrator` and registers a `state.Listener`; State
  never imports Store.
- The Event bus adapter registers a `state.Listener` and publishes
  `state.changed`; State never imports the Event bus.
- State never imports `protocol`. The wire shapes for `state.*` methods live in
  [Protocol](protocol.md); the Server converts between them and State's Go
  types.

### Types

```go
package state

type Scope string

const (
	ScopeUser      Scope = "user"      // shared by every instance of this user
	ScopeInstance  Scope = "instance"  // this instance only; keyed by instance name
	ScopeWorkspace Scope = "workspace" // the named instance's workspace snapshot
)

type Policy string

const (
	KeepLatest Policy = "keep-latest" // Store keeps the newest value only
	KeepLastN  Policy = "keep-last-n" // Store keeps the newest N values
	TTL        Policy = "ttl"         // Store drops the value TTL after its last write
	Manual     Policy = "manual"      // Store keeps the value until explicitly deleted
)

type Retention struct {
	Policy Policy
	N      int           // keep-last-n only: 2..20
	TTL    time.Duration // ttl only: 1m..8760h
}

// Spec is what an owner registers. It is immutable after Register.
type Spec struct {
	Namespace  string          // "<owner>.<name>", see invariants 1-3
	Owner      string          // "husk" for core modules, else a plugin id or the workspace owner
	Scope      Scope
	Persistent bool
	Retention  Retention       // zero value iff !Persistent
	MaxBytes   int             // 0 = class default; may only lower it
	Initial    json.RawMessage // a JSON object; nil means {}
}

// Descriptor is a slice's metadata as readers see it.
type Descriptor struct {
	Spec
	Revision uint64 // see invariant 9
	Bytes    int    // encoded size of Value
	Hydrated bool   // the value at registration came from Store
}

// Entry is one slice in a snapshot. Value is never mutated after publication.
type Entry struct {
	Descriptor
	Value json.RawMessage
}

// Snapshot is an immutable full read of all slices. Safe on any goroutine.
type Snapshot struct {
	Seq    uint64           // increments on every committed write or (un)registration
	Slices map[string]Entry // read-only; never mutated after publication
}

// Change is one slice's net change in one loop tick.
type Change struct {
	Namespace string
	Owner     string
	Revision  uint64
	Removed   bool // the slice was unregistered this tick
}

// Hydrator supplies persisted values at registration. Store implements it.
// Hydrate runs on the loop and must not do I/O: Store preloads before the loop starts.
type Hydrator interface {
	Hydrate(spec Spec) (value json.RawMessage, ok bool)
}

// Listener receives the changes of one tick, on the loop, after Flush.
// It must not block, must not write to State, and must not retain the slice past the call.
type Listener func(changes []Change, snap *Snapshot)

type Options struct {
	Hydrator       Hydrator // nil: no hydration (tests only; an anonymous instance still hydrates user scope)
	WorkspaceOwner string   // from the distribution config; "" means only "husk" may register workspace scope
}

func New(opts Options) *State

// Registration and writes: loop goroutine only.
func (s *State) Register(spec Spec) (*Handle, error)
func (s *State) OnFlush(l Listener)
func (s *State) Flush() // called by the Stem loop at the end of every Update call

// Reads.
func (s *State) Get(namespace string) (Entry, bool) // loop only: sees this tick's writes
func (s *State) Snapshot() *Snapshot                 // any goroutine: atomic load, never blocks

// Handle is the write capability for one slice. Only its holder can write.
type Handle struct{ /* unexported */ }

func (h *Handle) Namespace() string
func (h *Handle) Set(value json.RawMessage) (rev uint64, err error)
func (h *Handle) Patch(mergePatch json.RawMessage, ifRevision uint64) (rev uint64, err error) // RFC 7396; ifRevision 0 = unconditional
func (h *Handle) Unregister()

// Clear resets a registered slice to its Spec.Initial as a committed write
// (revision bump, change event). It is the one write that bypasses the
// Handle: the state.clear handler calls it on the loop, after the
// husk.before-state-clear gate allowed it (see Hooks) and Store deleted the
// persisted copies, on behalf of the user. The owner sees it as a change.
func (s *State) Clear(namespace string) (rev uint64, err error)

// Typed convenience for core owners. T must marshal to a JSON object.
type Slice[T any] struct{ /* *Handle plus the decoded value */ }

func Register[T any](s *State, spec Spec, initial T) (*Slice[T], error)
func (s *Slice[T]) Get() T                  // the current value; callers treat it as read-only
func (s *Slice[T]) Set(v T) error           // State owns v afterwards; caller must not mutate it
func (s *Slice[T]) Update(f func(*T)) error // f mutates a copy; committed if the encoding changed

var (
	ErrDuplicate    = errors.New("state: namespace already registered")
	ErrInvalidSpec  = errors.New("state: invalid slice spec")
	ErrTooMany      = errors.New("state: slice limit reached")
	ErrTooLarge     = errors.New("state: value exceeds slice size cap")
	ErrInvalidValue = errors.New("state: value is not a JSON object")
	ErrConflict     = errors.New("state: revision mismatch")
	ErrUnregistered = errors.New("state: slice was unregistered")
)
```

`Register[T]` decodes a hydrated value into `T`. If decoding fails, the slice
starts at `initial`, `Hydrated` is false, and the error is returned wrapped
together with a valid `*Slice[T]` so the owner logs it and continues; the
persisted copy is replaced on the owner's first write. Both typed and untyped
registration also fall back to `Initial` if the hydrated value is invalid,
not an object or exceeds the registered cap after normalization. Untyped
`State.Register` returns a usable `*Handle`, `Hydrated` false and a wrapped
`state: discarded persisted <ns>: <err>` error (`ErrInvalidValue` or
`ErrTooLarge`). Owners log this recoverable error and continue startup; lowering
a plugin's cap must not prevent it from launching.

### Invariants

1. A namespace is `<owner>.<name>`: the final dot separates the owner from one
   non-empty, dot-free name, the same rule `ownID` applies to publications
   today. Characters are `[A-Za-z0-9_.-]`; total length is at most 128 bytes.
2. `Spec.Owner` equals the namespace's owner part. Core modules register with
   owner `husk`; `husk` is reserved and no plugin id can equal it.
3. Registration of a namespace already registered fails with `ErrDuplicate`.
   Nothing changes.
4. Only the `Handle` returned by `Register` writes, with one exception:
   `State.Clear`, which resets a slice to its `Initial` for `state.clear` and
   is reachable only from the user's CLI. Core owners keep their
   handle private to the module that owns the slice. A plugin never holds a
   handle: its Pod holds it and applies the plugin's `state.set` and
   `state.patch` requests (see Plugin requests below).
5. Anyone reads anything: `Get` on the loop, `Snapshot` elsewhere, `state.get`
   over the socket.
6. Every value is a JSON object. `Initial` nil means `{}`. A write whose result
   is not a JSON object fails with `ErrInvalidValue` and changes nothing.
7. Every write is atomic per slice: it commits fully or fails with no effect on
   value, revision or snapshot. `Clear` is an explicit reset: it commits a
   revision bump and change even if the value already equals `Initial`.
8. All registration, writes and `Flush` run on the Bubble Tea loop goroutine.
   Other goroutines read only through `Snapshot`. Violations are programming
   errors caught by the race detector in tests, not runtime errors.
9. Revisions are per slice, start at 1 at registration (with the initial or
   hydrated value), and grow by 1 per committed write. A `Set` or `Patch` whose
   normalized encoding equals the current bytes commits nothing: no revision
   bump, no change. `Unregister` retains the last value revision as its tombstone
   and removal revision; re-registration starts at that revision plus one, so
   revisions never go backwards within one process. Revisions are not persisted.
10. A committed write publishes a new `Snapshot` before the write call returns,
    so a reply sent after a write is never overtaken by a read of the older
    value (read-your-writes for socket callers).
11. `Flush` emits at most one `Change` per slice per tick: the slice's latest
    revision, or `Removed`. Changes in one tick are ordered by namespace. A tick
    ends at the end of one `Update` call: the loop flushes State first, then
    the Event bus. Follow-up messages run in separate ticks.
12. `Retention` is set if and only if `Persistent` is true. Retention is Store's
    policy; State never expires a value in memory.
13. Only owner `husk` and `Options.WorkspaceOwner` may register a `workspace`
    scope slice.
14. State holds no secret. No slice is encrypted or read-restricted.

### Size caps and limits

| Limit | Value | Reason |
|---|---|---|
| Core slice cap (owner `husk`, not workspace) | 64 KiB | Surfaces, focus, layout, liveness and profile are kilobytes; a larger value means a bug, such as frame content leaking in. |
| Plugin slice cap | 256 KiB | Enough for a plugin's view state and cursors; 64 plugins at the cap cost 16 MiB, and a `state.get` reply fits a protocol line many times over. |
| Workspace slice cap | 1 MiB | Workspace content is the largest legitimate document and is written rarely. |
| Hard ceiling on any slice | 1 MiB | Keeps every single-slice reply under a quarter of the 4 MiB protocol line limit (`protocol.MaxLineBytes`), with room for the envelope. |
| Registered slices | 512 | Bounds snapshot copy cost (one map copy per committed write) and `husk state` output; one instance has a handful of core slices plus one per plugin. |
| Namespace length | 128 bytes | Long enough for dotted plugin ids plus a name; short enough for table output. |
| `keep-last-n` N | 2..20 | N=1 is `keep-latest`; 20 generations bound disk use to 20 times the slice cap ([Store](store.md)); longer history belongs in an application store, not Husk. |
| `ttl` duration | 1m..8760h | Below a minute the value would rarely survive a restart; above a year it is `manual`. |

`Spec.MaxBytes` may lower a class cap, never raise it. Every accepted value
(`Initial`, hydrated, `Set` or the result of `Patch`) uses the same normalized
encoding: compact JSON with object keys sorted recursively and number lexemes
preserved without float64 rounding. Size checks and `Descriptor.Bytes` use that
encoding, and equality checks compare it, so padding or key order alone never
causes a write or a no-op patch change. The size check runs after applying a
patch and normalizing its result.

State has no timeouts: every operation is synchronous and in memory. Debounce
of persistent writes belongs to [Store](store.md).

### Change notification

After every `Update`, the Stem loop calls `Flush`. `Flush` collects the slices
that committed a write or were unregistered during the tick and calls each
registered `Listener` once with the ordered changes and the current snapshot.
Three listeners exist:

- the Event bus adapter, which publishes one `state.changed` notification per
  change;
- Store retention, which marks persistent slices dirty for its debounced writer;
- the Server's `state.watch` fan-out, which enqueues one `state.changed`
  notification with the value per changed slice on each watching connection
  (see Plugin requests). It only enqueues on bounded writers and never blocks.

Registration is also a change, so watchers and Store see newly registered
slices. Unregistering and re-registering the same namespace in one tick emits
only the new registration's latest revision, not a removal. A `Flush` with no
changes calls no listeners.

The event carries no value. Subscribers that need the value read it, which
keeps fan-out cheap when a large slice changes. On the wire it is an
`event.deliver` notification ([Protocol](protocol.md)) named `state.changed`:

```json
{"method":"event.deliver","params":{"name":"state.changed","source":"husk","data":{"slice":"husk.focus","owner":"husk","revision":42}}}
{"method":"event.deliver","params":{"name":"state.changed","source":"husk","data":{"slice":"example.notes.state","owner":"example.notes","revision":7,"removed":true}}}
```

Because each event names the latest revision, coalescing several
`state.changed` events for the same slice into the last one loses nothing. The
Event bus may coalesce by slice.

### Plugin requests

A plugin reaches State over the instance socket, directly or through the
`husk` CLI. The Server identifies the caller (see Cross-module assumptions),
answers reads from `Snapshot` on its own goroutine, and enqueues writes to the
loop, where the caller's Pod applies them with its handle. A plugin writes only its own slice, `<plugin_id>.state`, plus
`<plugin_id>.workspace` when it is the workspace owner the distribution
names (invariant 13). It never writes `<plugin_id>.occupants` or
`<plugin_id>.contributions`: those are its Pod's declarations, and the
Server's check compares the requested namespace against them, not an owner
string.

`state.get`: any caller, any slice. `path` is an optional RFC 6901 JSON Pointer
into the value.

```json
{"id":"1","method":"state.get","params":{"slice":"husk.focus"}}
{"id":"1","result":{"slice":"husk.focus","owner":"husk","revision":42,"value":{"surface":"main-stage","fallback":null,"terminal_focused":true}}}
{"id":"2","method":"state.get","params":{"slice":"husk.pods","path":"/plugins/example.notes/status"}}
{"id":"2","result":{"slice":"husk.pods","owner":"husk","revision":19,"value":"live"}}
```

`state.set`: replaces the caller's own slice.

```json
{"id":"3","method":"state.set","params":{"slice":"example.notes.state","value":{"open":["a.md"],"cursor":12}}}
{"id":"3","result":{"slice":"example.notes.state","revision":8}}
```

`state.patch`: RFC 7396 JSON Merge Patch on the caller's own slice. With
`if_revision`, the write commits only when the slice is at that revision.

```json
{"id":"4","method":"state.patch","params":{"slice":"example.notes.state","patch":{"cursor":13},"if_revision":8}}
{"id":"4","result":{"slice":"example.notes.state","revision":9}}
{"id":"5","method":"state.patch","params":{"slice":"example.notes.state","patch":{"cursor":14},"if_revision":8}}
{"id":"5","error":{"code":"conflict","message":"slice example.notes.state is at revision 9, not 8","data":{"revision":9}}}
```

`state.snapshot`: descriptors of every slice; values only when asked, and only
when the reply fits the protocol line limit.

```json
{"id":"6","method":"state.snapshot","params":{"values":false}}
{"id":"6","result":{"seq":311,"slices":[{"slice":"husk.focus","owner":"husk","scope":"instance","persistent":false,"revision":42,"bytes":61,"hydrated":false},{"slice":"husk.layout","owner":"husk","scope":"instance","persistent":true,"retention":{"policy":"keep-latest"},"revision":5,"bytes":188,"hydrated":true}]}}
```

`state.watch`: a long-lived request on one socket connection. The reply gives
the current entries of the requested slices (all when `slices` is empty) as a
baseline; afterwards the Server sends one `state.changed` notification per
changed slice per tick on that connection, with the value at the snapshot the
listener received. A watcher sees the latest value, not every intermediate one.
The watch ends when the connection closes.

```json
{"id":"7","method":"state.watch","params":{"slices":["husk.focus"]}}
{"id":"7","result":{"seq":311,"slices":[{"slice":"husk.focus","revision":42,"value":{"surface":"main-stage","fallback":null,"terminal_focused":true}}]}}
{"method":"state.changed","params":{"slice":"husk.focus","owner":"husk","revision":43,"value":{"surface":"sidebar-list","fallback":null,"terminal_focused":true}}}
```

Error codes come from the [Protocol](protocol.md) registry:

| Code | When |
|---|---|
| `not_found` | The slice is not registered, or the JSON Pointer does not resolve. |
| `denied` | A write targets a slice other than the caller's own, or the caller is the bare CLI. |
| `invalid_params` with `data.limit` | The value exceeds the slice cap, or a snapshot with values exceeds the line limit. |
| `invalid_params` | The value or the patched result is not a JSON object, or the patch is not valid JSON. |
| `conflict` | `if_revision` does not match. `data.revision` gives the current one. |

### CLI exposure

| Command | Method | Notes |
|---|---|---|
| `husk state` | `state.snapshot` without values | Table: slice, owner, scope, persistent, retention, revision, bytes. |
| `husk state get <slice> [<pointer>]` | `state.get` | Prints the value as JSON. |
| `husk state dump` | `state.snapshot` with values | Fails with `invalid_params` and points to `husk state get` when the dump exceeds one line. |
| `husk state watch [<slice>...]` | `state.watch` | One JSON line per change until interrupted. |
| `husk state set <json>` / `husk state patch <json> [--if-revision N]` | `state.set` / `state.patch` | `<json>` is the request's params object (`{"slice", "value"}` / `{"slice", "patch"}`); `--if-revision` adds `if_revision`. Only from inside a plugin process; the slice is the caller's own. From a bare shell they fail with `denied`. |
| `husk state clear <namespace> [--force]` | `state.clear` | User only. The handler fires the `husk.before-state-clear` gate ([Hooks](hooks.md)) unless `force` is set; any veto answers `denied` (reason `vetoed`, with the `vetoes`) and nothing changes. The handler waits for the outcome under the request context, at most one 2 s leg timeout since every subscriber is asked at once; until the Store delete starts, an ended context answers `unavailable` if caused by host shutdown, `cancelled` otherwise, and deletes nothing, including after an allowed outcome or with `force`. A `busy` refusal from the hook runner (either cap) is logged and the clear proceeds; a shutdown (the hook cannot be fired, a leg ended `cancelled` with reason `shutdown`, or the request context ended before deletion because the host is closing) answers `unavailable` and deletes nothing; any other refusal of the fire is a host bug and answers `internal`, deleting nothing. The hook's data is `{"slice", "owner"}`; a slice that is not registered has no owner to name, so the hook is not fired for it. A subscriber that timed out or failed counts as allow and is logged by the handler. A nil hook firer means no hooks are wired and the gate passes. Otherwise Store deletes the persisted copies ([Store](store.md)) without request cancellation interrupting it; a failed delete answers `internal`. Once deletion succeeds, the handler completes the reset to `Initial` through claimed loop execution and answers its real outcome regardless of caller cancellation. Shutdown abandons a queued reset but waits for a running one. A reset skipped because the host is closing still answers success with no result body and one warning: the persisted copies are deleted, Store's epoch prevents a final flush from resurrecting them, and the live value dies with the host. |

### Initial slices

| Namespace | Owner module | Scope | Persistent | Retention | Contents |
|---|---|---|---|---|---|
| `husk.surfaces` | Stem: Surfaces (occupants, contributions, capture) and the frontend (visible, placement, overlays) | instance | no | | The occupancy resolution (one occupant per surface), the combined per-slot contributions, which surfaces capture text, visibility, the active placement, overlays. The Surfaces engine holds the only handle and commits every write: it resolves occupants and contributions itself, and applies the frontend's half through `Engine.UpdateView`. This is the only slice the host renders from. |
| `husk.focus` | the frontend | instance | no | | Focused surface, its fallback, terminal focus. |
| `husk.layout` | the frontend | instance | yes | keep-latest | Dock preferences the user set: shown flags and sizes. |
| `husk.geometry` | the frontend | instance | no | | Terminal size, too-small flag, computed surface rects. |
| `husk.pods` | Pods | instance | no | | Per plugin: status, pid, start time, last error, log path, and the landed `dir` field (additive, listed with protocol minor 2.3), the plugin's directory as the converter resolved it, `""` for an implicit command line ([Settings center](settings-center.md), "Changes to other specs"). |
| `husk.profile` | Services: Profile | instance | yes | keep-latest | The chosen profile; `{"active":""}` is an explicit no profile; absent until a choice is made ([Services](services.md), Profile). |
| `husk.grants` | Services: Secrets | user | yes | manual | Secret grants: `(plugin, name, digest, at)`; never a value ([Services](services.md)). |
| `husk.store` | Store | instance | no | | Store's own health: last 50 diagnostics, quarantined files, keyring fallback flag ([Store](store.md)). |
| `<plugin_id>.state` | that plugin's Pod | instance, or user if the manifest asks | per manifest | per manifest | Plugin-defined. Registered at launch, before the plugin starts; unregistered by `husk plugin remove` ([Pods](pods.md)), which keeps its persisted copies. |
| `<plugin_id>.occupants` | that plugin's Pod | instance | no | | The plugin's occupancy declarations, one entry per claimed surface. The plugin cannot write it. Registered at launch with `<plugin_id>.state`, unregistered with it. |
| `<plugin_id>.contributions` | that plugin's Pod | instance | no | | The plugin's contribution declarations grouped by target slot. The value carries no sequence; the Surfaces engine records the cross-plugin order. The plugin cannot write it. Registered and unregistered with `<plugin_id>.state`. |
| `<owner>.workspace` | the workspace owner the distribution names | workspace | yes, named instances only | keep-latest | Application-defined workspace content. |

Value shapes:

```json
{"slice":"husk.surfaces","value":{
  "occupants":{"main-stage":{"surface":"main-stage","plugin":"example.editor"}},
  "contributions":{"example.editor.toolbar":[{"slot":"example.editor.toolbar","id":"example.git.branch","plugin":"example.git","label":"main"}]},
  "capture":{"main-stage":"text"},
  "runtime":{},
  "terminals":{},
  "visible":["activity-rail","main-header","main-stage","side-panel","sidebar-footer","sidebar-list","sidebar-navigation"],
  "placement":{"rail_item":"","kind":""},
  "overlays":{"modal":{"plugin":"","rail_item":"","surface":""},"palette":false,"slot_explorer":false,"settings_center":""}
}}
{"slice":"example.editor.occupants","value":{"main-stage":{"surface":"main-stage","plugin":"example.editor"}}}
{"slice":"example.git.contributions","value":{
  "example.editor.toolbar":[{"slot":"example.editor.toolbar","id":"example.git.branch","plugin":"example.git","label":"main"}]
}}
{"slice":"husk.focus","value":{"surface":"main-stage","fallback":null,"terminal_focused":true}}
{"slice":"husk.layout","value":{"sidebar":{"shown":true,"width":32,"nav_height":3,"footer_height":2},"side_panel":{"shown":false,"width":40},"main_header_height":3}}
{"slice":"husk.geometry","value":{"terminal":{"width":160,"height":48},"too_small":false,
  "rects":{"main-stage":{"x":34,"y":4,"width":125,"height":43,"inner":{"x":35,"y":5,"width":123,"height":41}}}}}
{"slice":"husk.pods","value":{"plugins":{"example.files":{"status":"live","pid":4242,"started_at":"2026-10-22T06:01:02Z","last_error":null,"log":"/home/u/.local/state/husk/logs/4242/example.files.log","dir":"/home/u/plugins/files"}}}}
{"slice":"husk.profile","value":{"active":"default"}}
```

Occupancy and contributions are State twice over, as declarations and as
resolution.

The declarations live in the Pod-owned slices: `<plugin_id>.occupants` and
`<plugin_id>.contributions` hold one plugin's own claims and entries, their
Pod is the owner, and their value types are the frozen
`pkg/host/contracts` shapes. Every write is owner-checked — an occupant
names its own plugin and a defined host layout surface; a contribution's id
and `on_click` are the owning plugin's own `<plugin_id>.<name>` with a
non-empty dot-free name — so nothing outside the plugin's authority commits.
The remaining checks of today's registry are Pod responsibilities, applied
before a request reaches the slice: a claim targets a defined host layout
surface; a contribution targets a known published slot that is not the
plugin's own; `on_click` names the plugin's own action.
The declarations carry no sequence: they define no combined order across
plugins. A repeated contribution id replaces the earlier entry of its slot
in place, and an identical value commits nothing; an entry's position in
the value is only a tiebreaker for the engine, below.

`husk.surfaces` is the resolution the declarations feed. The Surfaces engine
holds its only handle, and it is the only slice the host renders from. The
engine keeps the registry's invariants: one occupant per surface, first
live claim wins, conflicting later claims refused, known host surfaces only.
A claim counts only while its Pod is `live` in `husk.pods`: a claim from a
Pod that is starting, failed or exited holds nothing, stays in its
declaration, and resolves if the Pod is live at a later pass. When a Pod
leaves `live` the engine vacates its surfaces and hands each to the first
live claimant in namespace order; a live occupant is never preempted by a
claimant that goes live later. From protocol 2.6, an occupant may retain its
claim as **yielded** through `surface.close` with `yield: true`: yielded live
claims rank after every ordinary live claim, and among yielded claims the
same namespace order applies. A yielded claim becomes ordinary again when
it regains occupancy. Yield rank is engine-local, scoped to the plugin's
claim on that surface; withdrawing the declaration or removing the plugin
drops it. `husk.surfaces` lists resolved occupants, not claimants, so this
adds no field or slice. The host marks the engine on every
`husk.pods` write (`Engine.LivenessChanged`), so the hand-over lands in the
same tick, and the new occupant is told its size like any new occupant. The
engine also keeps `capture`, a map from surface id to `"text"` or `"all"`
for each capturing surface. A `surface.update` from the occupant with
`capture: true` or `"text"` sets text capture, `"all"` sets full capture,
`false` removes it, and an absent field changes nothing
([Protocol](protocol.md) 2.5); `terminal.attach` sets it too (see "Runtime
and terminal surfaces"). The engine
removes the entry whenever the surface's occupant changes, releases or closes
the surface, or its Pod leaves `live`, so a dead or departed plugin never
leaves a surface capturing (D93). An unoccupied surface has no entry, and the
map is always present in the slice, `{}` when empty. It also records the
cross-plugin contribution order itself, since the declarations carry no
sequence: each slot's combined list follows a per-slot sequence of
`(plugin, id)` pairs in the order the engine first observes each entry across
ticks; within one tick, new entries are ordered by namespace (State's
deterministic change order), then by position in the plugin's entries array.
A replaced entry keeps its position; a removed entry — including every entry
of a removed slot owner, as `removePlugin` filters contributions today —
leaves the sequence. The engine itself is the stem-surfaces unit's to build;
the slice and its shape are fixed here.

Release travels the same narrow paths for every surface: the occupant's
`surface.close`, the host's force-release through the same path, or the
owner's declarations going away. By default each of the first two also
retracts the claim declaration at its source — the engine asks the plugin's
Pod to remove the surface from `<plugin_id>.occupants` — so a freed surface
cannot be re-occupied by its own stale declaration. The occupant's
`surface.close` with `yield: true` instead keeps the declaration and marks
that claim yielded; the engine does not ask the Pod to release it. The
freed surface resolves ordinary live claims first, then yielded live
claims, in namespace order within each rank. If there is no other live
claimant, the yielder immediately re-occupies the surface and receives the
normal `surface.resized`, even if its dimensions have not changed; this is
not an error. Release still clears capture and detaches a terminal, even
on immediate reoccupation. Yield requires occupancy through a layout
claim; it is not available to an unclaimed or runtime surface
(`invalid_params` naming `yield`, [Protocol](protocol.md) "Surface close").
An absent or false `yield` keeps the retracting close, and a host
force-release always retracts. A refused claim stays
recorded in the declaration, the occupant is unchanged, and the refusal is
counted; a returning plugin's re-registered declarations resolve again,
first live claim wins among the claims that arrive after the re-registration.

#### Runtime and terminal surfaces

**Terminal metadata and capture modes land in protocol minor 2.5** ([Protocol](protocol.md), "Runtime and terminal surfaces"). **Runtime surfaces land in protocol minor 2.8.** The following facts are written by the Surfaces engine like the rest of its half:

- `runtime`, a map from dynamic surface id (`<plugin_id>.<name>`) to `{"plugin", "placement", "title", "width", "height"}` for every surface a `surface.open` created and nothing has closed. Such a surface also appears in `occupants` (the opener is its occupant from the reply on) and in the frontend's `visible` while shown, and its rect in `husk.geometry.rects`, so a consumer that only knows the layout surfaces still sees it as occupied space. `capture`, `visible` and `rects` are keyed by the same dynamic id. The engine removes the entry on `surface.close`, on the host's close, and when the opener's Pod leaves `live`; a runtime surface is never handed to another claimant (there is no declaration to resolve).
- `terminals`, a map from surface id to `{"title", "alt_screen", "mouse", "scrollback"}` for every attached surface: the title the application set (OSC 0/2), whether it is on the alternate screen, whether it enabled mouse reporting (so Input knows whether a wheel scrolls the application or the host's scrollback) and the scrollback line count. The engine updates it from the emulator's callbacks, coalesced per tick; the screen contents themselves are not State (see "What is not State"), exactly as `surface.update` lines are not.
- `capture` values become `"text"` or `"all"` instead of `true` (`true` on the wire is recorded as `"text"`); `terminal.attach` records `"all"` unless the request said `true` or `"text"` (recorded as `"text"`) or `false` (no entry). A 2.1 reader that tests the entry for presence is unchanged.

Runtime canvases form one instance-wide stack across plugins, ordered by open time, newest first. Opening a canvas shows and focuses the new top. Older runtime canvases stay open and occupied but hidden; the limit remains eight runtime surfaces per plugin. Closing a hidden canvas removes only that entry and does not change visibility or focus. Closing the top through any close path shows the next-newest still-open runtime canvas. When the last canvas closes, the frontend restores the underlying rail selection or Home placement and the focus captured when the stack was most recently shown. Showing a hidden stack, either by a new open or by restore, captures the current underlying focus again.

Activating Home or any rail item hides the runtime canvas stack without closing any entry: occupancy, `runtime` and plugin lifetimes are unchanged. The host action `husk.surfaces.restore` re-shows the hidden stack, newest first, and focuses its top. It has no default binding, appears in the palette only while a hidden stack exists, and is a no-op otherwise. Opening a new runtime canvas shows and focuses the new top even when the existing stack was hidden.

`placement` continues to describe the underlying rail/Home selection while a runtime canvas is shown: `kind` is `stage` or `full-canvas` with that rail item's `rail_item`, or both fields are empty for Home. There is no `placement.kind: "canvas"`. The shown runtime canvas is the dynamic id in `visible` whose entry in `runtime` has `placement: "canvas"`; hidden canvases remain in `runtime` and occupancy but not `visible`.

With runtime surfaces (landed in minor 2.8), `overlays.modal` gains `surface`: the id of the modal's surface, `modal-surface` for a rail item and the dynamic id for a runtime modal, so the frontend's `FocusSurface` while a modal is open is whichever of the two it is. The example value above becomes `"overlays":{"modal":{"plugin":"","rail_item":"","surface":""},...}` and `"runtime":{}` is always present, `{}` when empty, like `capture` and the landed `terminals`.

`overlays` carries the landed `settings_center` (string, with the Settings center of [Settings center](settings-center.md), whose Theme tab is the theme selector of [Services](services.md), "The theme selector"): the id of the active tab (`general`, `keys`, `theme`, `store`, `installed`) while the frontend's Settings center modal is open, `""` while it is closed. It is written by the frontend through `UpdateSurfaceView` like `palette` and `slot_explorer`, rewritten on every tab switch, read by Input (the center consumes every key while open, [Keybindings](keybindings.md) context filters) and by `husk state get husk.surfaces`, and is `""` whenever the center is closed by any path. There is no separate `theme_selector` flag: the selector is a tab, as shown in the `husk.surfaces` example above.

#### Frontend-owned slices

`husk.layout`, `husk.geometry`, `husk.focus` and the view half of
`husk.surfaces` (`visible`, `placement`, `overlays`) describe what the
frontend shows, so the frontend writes them; no Stem module does. This
resolves an earlier reading in which the Surfaces module owned the three
namespaces while "Migration from today" had the TUI's `recomputeLayout` and
`dropVanishedSides` write them.

- **Owner handles.** The host root registers none of the three. The frontend
  calls `Frontend.OwnSlices` once, before the loop runs or on it, and gets
  untyped owner handles holding JSON objects: owner `husk`, instance scope;
  `husk.layout` is persistent with `keep-latest` and is hydrated by Store
  inside `Register`, from the values Store preloaded before the loop
  started, so registering after `Run` hydrates the same as registering
  before it (`TestLayoutSliceHydratesAndPersistsAcrossHosts`); `husk.geometry` and `husk.focus` are not persistent.
  Their value shapes are the examples above; the frontend defines them.
  A second call fails. Handle writes are loop-only like every State write.
- **The view half of `husk.surfaces`.** A namespace has one registered
  owner, so the frontend gets no second handle on it. The engine keeps the
  handle and exposes `Engine.UpdateView`, reached through
  `Frontend.UpdateSurfaceView(fn)`: `fn` edits a copy of `visible`,
  `placement` and `overlays`, and the engine commits it, leaving occupants
  and contributions as they are. An entry of `visible` that is neither a host
  layout surface nor an open runtime surface refuses the whole update and nothing commits. The view
  write takes the same foreign-write guard as a surface release, so a
  `state.clear` of `husk.surfaces` in the same Update is still re-resolved
  from the declarations at the next pass. A resolution pass never touches the
  view half.
- **`app-overlay` is placed.** The frontend gives `app-overlay` a rect in
  `husk.geometry.rects` equal to the whole frame (`inner` equal to the rect:
  it has no chrome) and lists it in `visible` in every placement, occupied or
  not; while the terminal is too small it keeps its last valid rect like
  every surface. So an occupant of the overlay layer is told its size like every
  occupant and a reader of the two slices sees every drawable surface. It is
  never `husk.focus.surface`. Drawing rules: [Cell-buffer compositor](cell-buffer-compositor.md),
  "Overlay layer".
- **Input reads `capture`.** On every key press the frontend copies
  `husk.surfaces.capture[focused surface]` into `KeyContext.Capture` before
  calling `Resolve` ([Keybindings](keybindings.md)); it never writes it.
- **`surface.resized` is driven by the frontend's writes.** The host
  listens to State's flush for `husk.geometry`, `husk.focus` and
  `husk.surfaces`. At the end of the tick, after the State flush and the
  engine's resolution pass, it reads the committed snapshot, computes each
  occupied surface's `SurfaceSize` (inner width and height from
  `husk.geometry`, `focused` from `husk.focus`, `visible` from
  `husk.surfaces`) and calls `Engine.Resized` with only the sizes that
  changed since the last push, keyed by surface and occupant: a new
  occupant of an unchanged surface is told too, and a resolution the engine
  commits during the tick (a claim that resolves, a release) is pushed in
  that tick, not at the next Update. `Engine.Resized` then sends each plugin the
  sizes of its own surfaces. A tick that wrote none of the three namespaces
  and resolved nothing does nothing, and an identical write pushes nothing. Geometry reaches
  occupants, never the whole workbench.

#### Pod-owned slices

`husk.pods` entries carry the converter-resolved `dir` from their initial
`starting` write. The field is always present, including `""` for implicit
command lines, and later liveness writes leave it unchanged.

`husk.pods` status is one of `starting` (launch begun, process not yet
started), `live` (process started), `exited` (clean exit, or stopped by Husk
at shutdown) and `failed` (launch error, crash, foreign signal or stdin
failure; `last_error` says which). Transitions are owned by the Wrapper
([Pods](pods.md)). The Dispatcher treats only `live` as able to receive an
action. A Pod starts the process only after the plugin's declared documents
are in Store and the Services reload that read them has completed (the launch
gate in [Pods](pods.md)), so `live` also means the plugin's actions,
focus-local keys, settings schema and secret declarations exist in every
consumer; `exited` and `failed` keep the catalog, so the action is
`unavailable`, never `not_found`.

The plugin slice's registration comes from the manifest. Without a `state`
block the Pod registers `<plugin_id>.state` as instance scope, not persistent.
With one:

```json
{"id":"example.notes","state":{"scope":"user","persistent":true,"retention":{"policy":"keep-last-n","n":5},"max_bytes":65536}}
```

### What is not State

- **Config-derived**, loaded and reloaded, never written: the action catalog,
  declared publications (slot, hook and event declarations with title and
  description), rail item declarations, the resolved keymap and focus-local
  bindings, theme and palette, settings, the manifests themselves. Whether an
  action is *available* is a read of `husk.pods`; what the action *is* comes
  from the catalog.
- **Rendering buffers**: the lines each plugin sent for its surface
  (`slotUpdates` today) and the composed frame. They change at frame rate and
  belong to the Renderer.
- **Input bookkeeping**: modifier state, held keys and their aliases, the
  Alt latch, pointer hover and drag, the palette query and cursor, the slot
  explorer cursor. They change at input rate, have no reader outside Stem, and
  modifier state already reaches plugins as `key_state`.
- **Delivery bookkeeping**: event and hook subscriber lists (Event bus, Hook
  runner), pending hook calls and their deadlines ([Calls](calls.md)),
  per-connection queues and counters (Server, Wrapper).
- **Transient notices**: the active toast. The engine bounds the queue at
  32 — the oldest is dropped and the drop counted — and expires each toast
  after 3200 ms.
- **Secrets**: they go through the Secrets service and Store's encrypted
  namespace.

### Failure modes

| Condition | Who detects | What happens | What the user or plugin sees |
|---|---|---|---|
| Namespace already registered | State, in `Register` | `ErrDuplicate`; nothing changes | Core: startup aborts with the namespace named (a programming error). Plugin: the Pod refuses to launch the plugin and logs `slice <ns> already registered`; an error toast. |
| Invalid spec (namespace, owner mismatch, scope, retention, cap above class) | State, in `Register` | `ErrInvalidSpec` naming the field | Plugin: manifest error at load, plugin not launched, error toast. |
| Slice limit reached | State | `ErrTooMany` | Plugin not launched, error toast naming the limit. |
| Workspace scope from an owner the distribution did not name | State | `ErrInvalidSpec` | Error toast; the slice does not exist. |
| Hydrated value is invalid, not an object or over the normalized size cap (including a newly lowered cap) | State, in typed or untyped `Register` | Slice starts at `Initial`, `Hydrated` false; wrapped `ErrInvalidValue` or `ErrTooLarge` returned with a usable handle/slice | Log line `state: discarded persisted <ns>: <err>`; owner continues startup and the persisted copy is replaced on the first write. |
| Hydrated value does not decode into the owner's type | `Register[T]` | Slice starts at `initial`; wrapped error returned with a usable slice | Log line `state: discarded persisted <ns>: <err>`; the persisted copy is replaced on the first write. |
| Plugin writes a slice it does not own | Pod | Request refused | `denied` error reply. |
| Value over the cap | State, after encoding or patching | `ErrTooLarge`; value, revision and snapshot unchanged | `invalid_params` with `data.limit`. |
| Value or patch result not a JSON object | State | `ErrInvalidValue`; no change | `invalid_params`. |
| `if_revision` stale | State | `ErrConflict`; no change | `conflict` with the current revision; the plugin re-reads and retries. |
| Read of an unknown slice or unresolved pointer | Server, from the snapshot | Request refused | `not_found`. |
| Plugin process exits | Wrapper | `husk.pods` entry becomes `exited` or `failed`; the plugin's slice keeps its value | Dispatcher refuses actions for it; watchers of `husk.pods` see the change. |
| Pod torn down (`husk plugin remove`, [Pods](pods.md)) | Pods, on the loop, in one tick | `Unregister` of `<id>.state`, `<id>.occupants` and `<id>.contributions`; tombstone revisions kept; the `husk.pods` entry removed; persisted copies of `<id>.state` kept ([Store](store.md)) | One flush with three `state.changed` carrying `removed: true` and one for `husk.pods`; later `state.get` on the three gets `not_found`; the Surfaces engine drops the owner's claims and slot entries in that tick. |
| Write through a handle after `Unregister` | State | `ErrUnregistered`; no change | Programming error in the owner; logged. |
| Clear of an unknown or unregistered namespace | State, in `Clear` | `ErrUnregistered` wrapped with the namespace; no change | Server maps it to `not_found`. |
| Snapshot with values exceeds one protocol line | Server | Request refused | `invalid_params`; `husk state dump` suggests `husk state get <slice>`. |
| A `state.watch` connection stops reading | Server outbound queue | Connection dropped by the Server's disconnect policy | The CLI exits with the disconnect reason. |
| Mutation from a goroutine other than the loop | Race detector in tests | Undefined at runtime | Never shipped: the tests fail. |

## Level 2: Migration from today

- `cmd/husk/registry.go`: split, per the plan to move "catalog out, liveness
  in".
  - Catalog, out to [Pods](pods.md) manifest conversion: `publishedSlot.Spec`,
    `namedPublication` title, description and owner, `FromManifest`,
    `declareFromManifest`, `validateSlot`, `describe` and `PublicationInfo`,
    `declareSlotFromHello` and `declareNamedFromHello`. `FromManifest` and
    `rollbackHello` disappear: declarations no longer live in runtime state.
  - State, as Pod-owned declaration slices: occupancy into
    `<plugin_id>.occupants` (the Pod writes its plugin's claims where
    `claimSurface` wrote `Occupants`) and contributions into
    `<plugin_id>.contributions` (where `contribute` wrote
    `publishedSlot.Contributions`), which the Surfaces engine resolves into
    `husk.surfaces` (occupancy per surface, combined per-slot contribution
    order). A Pod teardown unregisters both, replacing `removePlugin`'s
    cleanup; the engine drops a removed owner's slot entries, as
    `removePlugin` filters contributions today.
  - Not State: `namedPublication.Subscribers` moves to the Event bus and Hook
    runner; `Pending`, `pendingHook`, `hookTimeout`, `maxPendingHookCalls`,
    `maxPendingPerSubscriber`, `expireHooks` and `pendingFor` move to
    [Calls](calls.md).
  - `ownID` and `validPluginID`: the rule is restated in `substrate/state` for
    namespaces, without importing the wire package; a shared test asserts both
    accept the same inputs.
- `pkg/workbench/model.go`, `workbenchModel` fields:
  - To `husk.focus`: `focusedSlot`, `focusFallback`, `blurred` (inverted as
    `terminal_focused`).
  - To `husk.surfaces` (view half, written through `Frontend.UpdateSurfaceView`):
    `modal` (`modalState` becomes `overlays.modal`, and `modalOwner()` reads
    the slice), `activeRailID` and `activePlacement`, `showPalette`,
    `showHotspots` and `stageMode`.
  - To `husk.pods`: `plugins` reduced to liveness (`Connected`, `PeerPID`) plus
    process status from `lifecycle.go` (`pluginProcess`, `pluginExitedMsg`).
    `Writer`, `Closer` and `FramesRecv` stay with the Wrapper and Server.
    The frontend has no traffic counter; frame-dump settling fingerprints
    published content and Pod status.
  - To the workspace slice: `wsContext`. The `workspace_context` message
    becomes a write by the workspace owner; `welcome.context` and the
    broadcast become reads of the slice and its `state.changed`.
  - Stay out: `manifests`, `palette` (config); `railItems` (config too: the
    manifest's `rail_items[]`, declared in the `plugins` document and read
    through `Frontend.RailItems`, see [Pods](pods.md)); `slotUpdates`
    (Renderer); `modifiers`, `altLatched`, `heldKeys`, `heldAliases`,
    `keyReleases`, `termMods`, `lastInput`, `activeDrag`, `hoverDivider`,
    `hoverRailIdx`, `paletteQuery`, `paletteCursor`, `hotspotCursor`,
    `activeToast`, `toastSeq` (Stem-local); `socketPath`, `listener`,
    `subChan`, `procs`, `quit`, `stopGrace`, `handoffGrace`,
    `handoffPending`, `pluginCtx`, `pluginCancel`, `sendQueueCapacity`,
    `writeTimeout`, `logOut` (plumbing).
- `pkg/workbench/layout.go`: `showSidebar`, `sidebarWidth`, `sidebarNavH`,
  `sidebarFootH`, `mainHeaderH`, `showSidePane` and `sidePaneW` move to
  `husk.layout`; `width`, `height`, `slotRects`, `modalRect` and the result of
  `tooSmall()` move to `husk.geometry`, written by `recomputeLayout` through
  the handle `pkg/workbench` obtains from `Frontend.OwnSlices`.
  `dropVanishedSides` writes `husk.focus` through its handle. `pkg/workbench` owns
  these three namespaces and the view half of `husk.surfaces` (see "Frontend-owned
  slices"); the Surfaces engine owns none of them, and its `Resized` is
  driven by the geometry writes. `minTermWidth`, `minTermHeight` and
  `surfaceGutter` stay as Surfaces constants. `leftSideShown` and
  `rightSideShown` become reads of `husk.layout` and `husk.surfaces`.
- `pkg/huskipc`: deleted in the retirement. `WorkspaceContextPayload` is gone with it, and the `state.*` request and reply shapes live in `pkg/protocol`.
- `pkg/manifest` (deleted; now `pkg/pods/manifest`): `Plugin` gains an optional `State` block (`scope`,
  `persistent`, `retention`, `max_bytes`). `Slots` stays: which surfaces a
  plugin claims is declared config; which surface it actually occupies is
  resolved in `husk.surfaces`.

## Decisions made here

1. Slices are addressed by namespace plus an optional RFC 6901 JSON Pointer,
   passed as separate arguments, so a namespace that is a dotted prefix of
   another (`a.state` and `a.b.state`) is never ambiguous.
2. The namespace rule is `<owner>.<name>` with the final-dot split already used
   for publications; core owner is `husk`; the plugin slice is
   `<plugin_id>.state`.
3. Write authority is a Go capability (`Handle`), not a string check: only the
   holder writes. Owner strings exist for display; whether a plugin may write
   a declaration is decided by comparing namespaces, never owners.
4. Every value is a JSON object, so merge patches and pointers are uniform.
5. Plugin patches use RFC 7396 JSON Merge Patch, with optional `if_revision`
   compare-and-set.
6. Size caps: 64 KiB core, 256 KiB plugin, 1 MiB workspace, 1 MiB hard
   ceiling; at most 512 slices.
7. A tick ends at the end of one `Update` call; the loop calls State's `Flush`
   first, then the Event bus's `Flush`. Follow-up messages are separate ticks.
   Snapshots are published on every committed write (read-your-writes); events
   are emitted once per slice per tick.
8. `state.changed` on the Event bus carries no value; `state.watch` on the
   socket does.
9. No-op `Set` and `Patch` writes (identical normalized encoding) commit nothing; explicit
   `Clear` resets always commit. Revisions are per slice,
   survive unregistration as tombstones, and are not persisted.
10. State notifies through in-process `Listener`s; the Event bus adapter,
    Store retention and the Server's `state.watch` fan-out are the three
    listeners (see Change notification). This keeps State free of imports
    of Stem, Store and Pods.
11. Hydration happens inside `Register` through the `Hydrator` Store
    implements; Store preloads before the loop starts, so hydration is a map
    lookup.
12. Retention applies only to persisted copies; State never expires values in
    memory. `keep-last-n` N is 2..20; `ttl` is 1m..8760h.
13. Layout splits into `husk.layout` (persistent dock preferences) and
    `husk.geometry` (terminal size and rects, not persistent), so a restored
    layout never restores a stale terminal size.
14. Published-slot contributions and surface occupancy are State twice over:
    per-plugin declarations (`<plugin_id>.occupants`,
    `<plugin_id>.contributions`, Pod-written, never plugin-writable) and the
    `husk.surfaces` resolution, which only the Surfaces engine writes and
    the host renders from. Subscriber lists and pending hook calls are not
    State.
15. Input bookkeeping, render buffers, the palette query and toasts stay out of
    State.
16. Every plugin gets `<plugin_id>.state`, non-persistent instance scope by
    default; the manifest `state` block opts into persistence or user scope.
17. Workspace scope is reserved for owner `husk` and the one workspace owner
    the distribution config names.
18. Instance and workspace scope differ in how Store writes them: instance
    slices are separate records keyed by instance name; workspace slices of a
    named instance are written and restored together as one overwritten
    snapshot. Store owns the details.
19. The frontend owns `husk.layout`, `husk.geometry`, `husk.focus` and the
    view half of `husk.surfaces`. It gets the first three as owner handles
    from `Frontend.OwnSlices`; the view half goes through
    `Engine.UpdateView`, because a namespace has one handle and the engine
    holds it. `surface.resized` follows the frontend's geometry, focus and
    visibility writes: the host diffs the computed sizes at the end of each
    tick and pushes only the changes.

20. **Runtime canvas stack and restoration (operator-confirmed C2 decision).** Runtime canvases use one global newest-first opening-order stack, not one stack per plugin and not replace-and-close. Home and rail activation hide it without changing runtime lifetime. Selecting the underlying rail item while the stack is shown reveals that item rather than toggling Home. `husk.surfaces.restore` explicitly re-shows the hidden top, with no default key and a palette entry only while hidden. Focus is captured each time the stack becomes shown, and restored when its last canvas closes. This is the canvas lifecycle decision recorded for C2; the future runtime stage/panel stacks must follow the same rule.

## Open questions

1. **Secret slices.** The substrate note lets Store's crypto encrypt "slices
   marked secret", but "anyone reads anything" would expose a secret slice to
   every plugin through `state.get`. Recommendation: no secret slices; secrets
   stay in the Secrets service and Store's encrypted namespace, and State
   carries invariant 14.
2. **Who is the workspace owner.** The notes say "the application" without
   naming how. Recommendation: a `husk.workspace.owner` settings key in the
   distribution layer, naming a plugin id; Husk passes it as
   `Options.WorkspaceOwner`, and an application embedding Husk as a Go library
   may pass its own id.
3. **More than one slice per plugin.** The notes say one. Recommendation:
   `<plugin_id>.state` stays the one plugin-defined slice; the registry split
   adds `<plugin_id>.occupants` and `<plugin_id>.contributions`, fixed-shape
   slices the Pod registers, which are not part of the plugin's own document
   space.
4. **Anonymous instances and instance scope.** Resolved: [Store](store.md)
   persists instance and workspace scope only for named instances; user scope
   always. A pid instance keeps both in memory only.
5. **Test harness access to render buffers.** A harness may want surface lines
   from `husk state`. Recommendation: no; it uses the existing frame dump path,
   keeping frame-rate data out of State.
6. **Runtime `stage` and `panel` placements.** `surface.open` (landed in minor 2.8) offers
   `modal` and `canvas`; a plugin that wants to sit in the main stage or the
   side panel beside the layout occupant has no placement yet. Recommendation:
   add both in a later minor with a stack rule matching runtime canvases:
   newest-first, with hidden entries kept open and the previous placement
   restored when the stack empties. The newest runtime surface in a slot
   shows, the layout occupant and older runtime surfaces are hidden
   (told `visible: false`) and return when it closes, and `husk.surfaces.runtime`
   gains a `slot` field. A split that resizes the occupant is not offered: the
   frontend's layout is fixed slots, not a tree.
