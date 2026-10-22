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

The problem it solves: today the truth is scattered across `WorkbenchModel`
fields and the publication registry in `cmd/husk`, mixed with config-derived
catalog data and with input bookkeeping. Nothing outside the model can read it,
nothing persists it, and nothing tells a plugin it changed.

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
// Handle: the state.clear handler calls it on the loop, after Store deleted
// the persisted copies, on behalf of the user. The owner sees it as a change.
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
persisted copy is replaced on the owner's first write. The same typed fallback
applies if a hydrated document is not an object or exceeds the registered cap.
Untyped `State.Register` instead rejects invalid hydration atomically with
`ErrInvalidValue` or `ErrTooLarge`, without registering a slice.

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
   encoding equals the current bytes commits nothing: no revision bump, no change. After
   `Unregister` the namespace keeps a tombstone revision, so a later
   re-registration continues above it and no reader ever sees a revision go
   backwards within one process. Unregistration retains the last value revision
   as its tombstone and removal revision; re-registration starts at that revision
   plus one. Revisions are not persisted.
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

`Spec.MaxBytes` may lower a class cap, never raise it. The size check runs on
the encoded value after a patch is applied.

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
loop, where the caller's Pod applies them with its handle. A plugin writes only
its own slice, `<plugin_id>.state`, plus `<plugin_id>.workspace` when it is the
workspace owner the distribution names (invariant 13).

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
| `husk state set <json>` / `husk state patch <json> [--if-revision N]` | `state.set` / `state.patch` | Only from inside a plugin process; the slice is the caller's own. From a bare shell they fail with `denied`. |
| `husk state clear <namespace>` | `state.clear` | User only. Store deletes the persisted copies ([Store](store.md)), then the handler calls `State.Clear` on the loop and the slice resets to its `Initial` value. |

### Initial slices

| Namespace | Owner module | Scope | Persistent | Retention | Contents |
|---|---|---|---|---|---|
| `husk.surfaces` | Stem: Surfaces | instance | no | | Occupancy, visibility, the active placement, overlays, published-slot contributions. |
| `husk.focus` | Stem: Surfaces | instance | no | | Focused surface, its fallback, terminal focus. |
| `husk.layout` | Stem: Surfaces | instance | yes | keep-latest | Dock preferences the user set: shown flags and sizes. |
| `husk.geometry` | Stem: Surfaces | instance | no | | Terminal size, too-small flag, computed surface rects. |
| `husk.pods` | Pods | instance | no | | Per plugin: status, pid, start time, last error, log path. |
| `husk.profile` | Services: Profile | instance | yes | keep-latest | The active profile name. |
| `husk.grants` | Services: Secrets | user | yes | manual | Secret grants: `(plugin, name, digest, at)`; never a value ([Services](services.md)). |
| `husk.store` | Store | instance | no | | Store's own health: last 50 diagnostics, quarantined files, keyring fallback flag ([Store](store.md)). |
| `<plugin_id>.state` | that plugin's Pod | instance, or user if the manifest asks | per manifest | per manifest | Plugin-defined. Registered at launch, before the plugin starts. |
| `<owner>.workspace` | the workspace owner the distribution names | workspace | yes, named instances only | keep-latest | Application-defined workspace content. |

Value shapes:

```json
{"slice":"husk.surfaces","value":{
  "occupants":{"sidebar-list":"example.files","main-stage":"example.editor"},
  "visible":["activity-rail","sidebar-list","main-header","main-stage"],
  "placement":{"rail_item":"","kind":""},
  "overlays":{"modal":{"plugin":"","rail_item":""},"palette":false,"slot_explorer":false},
  "contributions":{"example.editor.toolbar":[{"id":"example.git.branch","plugin":"example.git","label":"main"}]}
}}
{"slice":"husk.focus","value":{"surface":"main-stage","fallback":null,"terminal_focused":true}}
{"slice":"husk.layout","value":{"sidebar":{"shown":true,"width":32,"nav_height":3,"footer_height":2},"side_panel":{"shown":false,"width":40},"main_header_height":3}}
{"slice":"husk.geometry","value":{"terminal":{"width":160,"height":48},"too_small":false,
  "rects":{"main-stage":{"x":34,"y":4,"width":125,"height":43,"inner":{"x":35,"y":5,"width":123,"height":41}}}}}
{"slice":"husk.pods","value":{"plugins":{"example.files":{"status":"live","pid":4242,"started_at":"2026-10-22T06:01:02Z","last_error":null,"log":"/home/u/.local/state/husk/logs/4242/example.files.log"}}}}
{"slice":"husk.profile","value":{"active":"default"}}
```

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
- **Transient notices**: the active toast.
- **Secrets**: they go through the Secrets service and Store's encrypted
  namespace.

### Failure modes

| Condition | Who detects | What happens | What the user or plugin sees |
|---|---|---|---|
| Namespace already registered | State, in `Register` | `ErrDuplicate`; nothing changes | Core: startup aborts with the namespace named (a programming error). Plugin: the Pod refuses to launch the plugin and logs `slice <ns> already registered`; an error toast. |
| Invalid spec (namespace, owner mismatch, scope, retention, cap above class) | State, in `Register` | `ErrInvalidSpec` naming the field | Plugin: manifest error at load, plugin not launched, error toast. |
| Slice limit reached | State | `ErrTooMany` | Plugin not launched, error toast naming the limit. |
| Workspace scope from an owner the distribution did not name | State | `ErrInvalidSpec` | Error toast; the slice does not exist. |
| Hydrated value does not decode into the owner's type | `Register[T]` | Slice starts at `initial`; wrapped error returned with a usable slice | Log line `state: discarded persisted <ns>: <err>`; the persisted copy is replaced on the first write. |
| Plugin writes a slice it does not own | Pod | Request refused | `denied` error reply. |
| Value over the cap | State, after encoding or patching | `ErrTooLarge`; value, revision and snapshot unchanged | `invalid_params` with `data.limit`. |
| Value or patch result not a JSON object | State | `ErrInvalidValue`; no change | `invalid_params`. |
| `if_revision` stale | State | `ErrConflict`; no change | `conflict` with the current revision; the plugin re-reads and retries. |
| Read of an unknown slice or unresolved pointer | Server, from the snapshot | Request refused | `not_found`. |
| Plugin process exits | Wrapper | `husk.pods` entry becomes `exited` or `failed`; the plugin's slice keeps its value | Dispatcher refuses actions for it; watchers of `husk.pods` see the change. |
| Pod torn down (plugin removed from the instance) | Pods | `Unregister`; tombstone revision kept | One `state.changed` with `removed: true`; later `state.get` gets `not_found`. |
| Write through a handle after `Unregister` | State | `ErrUnregistered`; no change | Programming error in the owner; logged. |
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
  - State, into `husk.surfaces` through Surfaces: `Occupants` (written by
    `claimSurface`) and `publishedSlot.Contributions` (written by
    `contribute`, cleaned by `removePlugin`).
  - Not State: `namedPublication.Subscribers` moves to the Event bus and Hook
    runner; `Pending`, `pendingHook`, `hookTimeout`, `maxPendingHookCalls`,
    `maxPendingPerSubscriber`, `expireHooks` and `pendingFor` move to
    [Calls](calls.md).
  - `ownID` and `validPluginID`: the rule is restated in `substrate/state` for
    namespaces, without importing the wire package; a shared test asserts both
    accept the same inputs.
- `cmd/husk/model.go`, `WorkbenchModel` fields:
  - To `husk.focus`: `focusedSlot`, `focusFallback`, `blurred` (inverted as
    `terminal_focused`).
  - To `husk.surfaces`: `modal` (`modalState` becomes `overlays.modal`, and
    `modalOwner()` reads the slice), `activeRailID` and `activePlacement`,
    `showPalette`, `showHotspots` and `stageMode`.
  - To `husk.pods`: `plugins` reduced to liveness (`Connected`, `PeerPID`) plus
    process status from `lifecycle.go` (`pluginProcess`, `pluginExitedMsg`).
    `Writer`, `Closer`, `FramesRecv` and `totalIPCEvents` stay with the
    Wrapper and Server.
  - To the workspace slice: `wsContext`. The `workspace_context` message
    becomes a write by the workspace owner; `welcome.context` and the
    broadcast become reads of the slice and its `state.changed`.
  - Stay out: `manifests`, `railItems`, `palette` (config); `slotUpdates`
    (Renderer); `modifiers`, `altLatched`, `heldKeys`, `heldAliases`,
    `keyReleases`, `termMods`, `lastInput`, `activeDrag`, `hoverDivider`,
    `hoverRailIdx`, `paletteQuery`, `paletteCursor`, `hotspotCursor`,
    `activeToast`, `toastSeq` (Stem-local); `socketPath`, `listener`,
    `subChan`, `procs`, `quit`, `stopGrace`, `handoffGrace`,
    `handoffPending`, `pluginCtx`, `pluginCancel`, `sendQueueCapacity`,
    `writeTimeout`, `logOut` (plumbing).
- `cmd/husk/layout.go`: `showSidebar`, `sidebarWidth`, `sidebarNavH`,
  `sidebarFootH`, `mainHeaderH`, `showSidePane` and `sidePaneW` move to
  `husk.layout`; `width`, `height`, `slotRects`, `modalRect` and the result of
  `tooSmall()` move to `husk.geometry`, written by `recomputeLayout`.
  `dropVanishedSides` writes `husk.focus`. `minTermWidth`, `minTermHeight` and
  `surfaceGutter` stay as Surfaces constants. `leftSideShown` and
  `rightSideShown` become reads of `husk.layout` and `husk.surfaces`.
- `pkg/huskipc`: keeps the existing messages during the transition.
  `WorkspaceContextPayload` is retired once the workspace slice lands. The
  `state.*` request and reply shapes are added to the protocol package that
  replaces it.
- `pkg/manifest`: `Plugin` gains an optional `State` block (`scope`,
  `persistent`, `retention`, `max_bytes`). `Slots` stays: which surfaces a
  plugin claims is declared config; which surface it actually occupies is
  `husk.surfaces`.

## Decisions made here

1. Slices are addressed by namespace plus an optional RFC 6901 JSON Pointer,
   passed as separate arguments, so a namespace that is a dotted prefix of
   another (`a.state` and `a.b.state`) is never ambiguous.
2. The namespace rule is `<owner>.<name>` with the final-dot split already used
   for publications; core owner is `husk`; the plugin slice is
   `<plugin_id>.state`.
3. Write authority is a Go capability (`Handle`), not a string check: only the
   holder writes. Owner strings exist for display and for the Pod's check of
   plugin requests.
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
9. No-op `Set` and `Patch` writes (identical encoding) commit nothing; explicit
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
14. Published-slot contributions and surface occupancy are State in
    `husk.surfaces`; subscriber lists and pending hook calls are not.
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
3. **More than one slice per plugin.** The notes say one. Recommendation: keep
   one for v2; a plugin structures its document internally.
4. **Anonymous instances and instance scope.** Resolved: [Store](store.md)
   persists instance and workspace scope only for named instances; user scope
   always. A pid instance keeps both in memory only.
5. **Test harness access to render buffers.** A harness may want surface lines
   from `husk state`. Recommendation: no; it uses the existing frame dump path,
   keeping frame-rate data out of State.
