---
type: Design Spec
title: "Message kinds"
description: "Contracts for the Dispatcher, Event bus and Hook runner: how actions, events and hooks move between Husk, plugins and the CLI."
status: planned
generated: { by: claude/sonnet-5-5, at: 2026-10-22T06:00:00Z }
tags: [architecture-v2, stem]
---

# Message kinds

Husk moves three kinds of message between the host and a plugin. They look
alike on the wire and are not alike in semantics. This note fixes the
differences so the contracts can be written one per kind.

## Level 0: What and why

Three Stem modules carry the three kinds. The **Dispatcher** routes an action
to its single owner and returns the owner's reply to whoever asked. The **Event
bus** fans a named event out to every subscriber and never waits. The **Hook
runner** calls the subscribers of a hook and collects their replies; it is a
cut-out, with chain and veto semantics deferred.

None of them is a transport. Waiting on a plugin (correlation ids, timeouts,
reply matching) belongs to [Calls](calls.md). Writing bytes to a plugin
belongs to the Wrapper in [Pods](pods.md). The wire envelopes belong to
[Protocol](protocol.md). These three modules decide only who receives a
message, whether the sender may send it, and what comes back.

What they are not: they do not know what any action does, they hold no
application state, and they do not read files. Whether an action exists comes
from the action catalog (config: the `plugins` document Pods declare in
[Store](store.md), plus the host's own actions); whether its owner is alive
comes from [State](state.md).

Dependencies:

- Dispatcher depends on Calls, the action catalog, State (liveness and focus).
- Event bus depends on State (liveness of subscribers) and on the subscription
  table derived from manifests. It is also published to by State, which makes
  State a publisher, not a dependency.
- Hook runner depends on Calls and the hook subscription table.
- Input ([Keybindings](keybindings.md)) and the Server ([Pods](pods.md)) depend
  on the Dispatcher. The Wrapper depends on none of the three.

## What differs

| | Action | Event | Hook |
|---|---|---|---|
| Who starts | user, plugin, CLI | Husk or plugin | Husk |
| Receivers | one owner, looked up in the store | every subscriber | registered plugins, in order |
| Reply | result or error, with timeout | none | required, can veto or modify |
| Failure mode | owner gone, no such action, slow owner | subscriber slow, bursts | one hung plugin freezes the UI |
| Kind-specific policy | permission (may X invoke Y's action), focus context | coalescing and throttling | per-hook timeout, priority order, reentrancy guard |

What they share: one JSON object per line, a correlation id for replies,
ordering per plugin, timeouts, and cleanup when a plugin exits.

## Two directions, two channels

Husk to plugin and plugin to Husk do not use the same channel.

- **Husk to plugin** goes down the plugin's stdin. The Pod's wrapper owns the
  process, so it owns the pipe. Actions, events and hook calls are written
  there as JSON lines; replies come back on stdout carrying the same id. The
  pipe is the plugin's identity: no handshake, no subscription, no second
  socket.
- **Plugin to Husk** is the CLI. A plugin runs `husk ...` or writes the same
  JSON lines to the instance socket. There is no plugin SDK; the CLI is the
  plugin API, in any language. A language library, if one appears, wraps the
  CLI shape and runs inside the plugin's process.

On the wire there are two shapes only: a request that expects a reply
(actions, hooks) and a notification that does not (events). This is the
JSON-RPC split.

Accepted cost: a plugin cannot be attached from outside or restarted
independently of its wrapper, and anything that drives Husk without being
launched by it, such as a test harness, uses the CLI path only.

## Layout

- **Calls** (Stem): correlation ids, timeouts and reply matching for anything
  that waits on a plugin. Knows no message kinds. Used by Dispatcher and Hook
  runner.
- **Dispatcher** (Stem): owner lookup, permission, focus context, result
  routing. One owner per action.
- **Event bus** (Stem): subscriptions, fan-out, coalescing. Any part may
  publish: Input, State, Surfaces, Services, plugins.
- **Hook runner** (Stem): chain order, veto or modify, per-hook timeout,
  reentrancy guard. Today only the hook call path exists, in the registry
  (timeout, pending-call caps); no plugin uses hooks yet. The boundary is cut
  now so that path moves behind it, and chain and veto semantics are added
  later without touching Calls or Dispatcher.
- **Wrapper** (Pod, one per plugin): launches and supervises the process,
  injects the instance socket path into its environment, and carries the
  Husk-to-plugin direction over its stdio.
- **Server** (Pod group, one per instance): the socket listener that serves
  plugins calling in and the CLI.

Rejected: a per-plugin connection object in the Pod carrying both directions
over the socket, because it needs plugin identity on every connection and a
subscription protocol for events. Also rejected: three fully separate
message modules, because cross-kind ordering and exit cleanup would be
nobody's job; and one engine, because it grows a switch on kind, hides three
contracts behind one word, and cannot be tested in isolation.

## Instances

One Husk process is one instance: its own socket, its own Pods, its own
State. Several instances may run at once; shared state between them is an
application's concern, not Husk's. Each instance registers its socket under
the user runtime directory with an id (the pid by default, a name when
given) and removes the entry on exit. Stale entries fail a ping and are
removed by the next CLI call that sees them.

The CLI picks an instance in this order and never guesses between two:

1. an explicit `--instance <name>` or `--socket <path>` flag;
2. the socket path in the environment, which the wrapper sets for every
   plugin process and which shells opened inside a Husk surface inherit;
3. discovery: exactly one live instance is used; zero or several is an error
   that prints the list.

## Terms

- An **action** is a named capability with an id, title, owner and optional
  default key. VS Code calls it a command. It is not an event handler.
- The **interaction model** is what a user or plugin can do and what the system
  does in response: keys become actions, actions go to one owner, events fan
  out, hooks wait.
- The **event loop** is the mechanism Stem runs messages on, Bubble Tea's
  update loop. It is described, not designed.

## Level 1: Contract

All three modules run on the loop goroutine. Their methods are called from
`Update` and never block; anything slow (waiting on a plugin) is a pending
entry in Calls that resumes as a loop message. None of them performs I/O
itself: delivery goes through Calls (requests) or a `Sink` (notifications).

Envelopes follow [Protocol](protocol.md): request `{id, method, params}`, reply
`{id, result}` or `{id, error: {code, message}}`, notification `{method,
params}`. This spec fixes the `method` names and `params` shapes.

### Shared types

```go
// Caller says who started a request. Set by the module that accepts the
// request, never read from params: a plugin cannot claim another identity.
type Caller struct {
    Kind     CallerKind // CallerInput | CallerPlugin | CallerCLI | CallerHost
    PluginID string     // set only when Kind == CallerPlugin
}

// FocusContext is the focus slice of State at dispatch time.
type FocusContext struct {
    Surface string // surface id, "" when nothing has focus
    Owner   string // plugin id occupying Surface, "" when unoccupied or host
}

// Sink delivers a notification to one subscriber and never blocks.
// Implemented by each Pod over its Wrapper's Channel (plugin stdin, see
// Pods) and by the Server (a CLI or plugin socket connection that
// subscribed for its own lifetime).
type Sink interface {
    Notify(method string, params any) error // error means "drop this subscriber's message"
}
```

### Dispatcher

```go
type ActionRequest struct {
    Action string         // catalog id, "<plugin_id>.<name>" or "husk.<name>"
    Args   map[string]any // JSON object, may be nil
    Caller Caller
    Focus  FocusContext   // Input passes its own snapshot; all other callers get State's
    Reply  func(ActionReply) // invoked once, on the loop goroutine
}

type ActionReply struct {
    Result map[string]any // nil on error
    Err    *ActionError   // nil on success
}

type ActionError struct {
    Code    string         // see failure table
    Message string
    Data    map[string]any // the wire error's data member, nil when none; e.g. {"reason":"plugin_exited"}
}

type Dispatcher interface {
    // Invoke never blocks. Reply is called exactly once, possibly before
    // Invoke returns (immediate failures and host actions).
    Invoke(req ActionRequest)
    // RegisterHost binds an in-process handler for a husk.* action id or a
    // rail item id. Host handlers run synchronously on the loop.
    RegisterHost(action string, h func(ActionRequest) ActionReply)
    // Complete turns Calls results (Resolve, Expire, DropPlugin, Close) into
    // the pending Replies. Results of other requesters are ignored.
    Complete(results ...calls.Result) int
    // List returns the catalog entries a caller may invoke (the permission
    // rule below), with owner liveness; it backs action.list.
    List(caller Caller) []Listed
}

// Read-only views the Dispatcher is given. It owns neither. ActionCatalog is
// implemented outside Stem, by cmd/husk over Store.Declared("plugins") and
// the host action list, because Stem never imports Store (Store invariant 4).
type ActionCatalog interface {
    Lookup(action string) (ActionDef, bool) // config, reloadable
    List() []ActionDef                      // every entry, for action.list
}
type ActionDef struct {
    ID     string
    Owner  string // plugin id, or "husk"
    Public bool   // manifest marks the action invocable by other plugins
}
type Liveness interface {
    Live(pluginID string) bool // read of the husk.pods slice in State: status == "live"
}
```

Resolution, in order, for one `Invoke`:

1. Validate `Action` is a well-formed id and `Args` is a JSON object. Failure:
   `invalid_params`.
2. `catalog.Lookup(Action)`. Missing: `not_found`.
3. Permission (below). Failure: `denied`.
4. If `Owner == "husk"`, run the registered host handler synchronously and
   call `Reply`. A missing handler is `not_found`.
5. `liveness.Live(Owner)`. False: `unavailable`. A Pod writes `live` only
   after the plugin's declarations have reached Store and every Service (the
   launch gate in [Pods](pods.md)), and keeps the catalog entry across process
   exit, so an exited owner is `unavailable`, not `not_found`, and an action is
   never invocable before its keys, settings schema and secret declarations
   exist.
6. `Focus` is filled: Input keeps its snapshot; every other caller gets the
   focus slice read now. A `focus` field in a caller's params is ignored.
7. Send `action.invoke` to the owner through Calls with the action timeout and
   the caller's `Reply` as the continuation.

Permission rule. Let X be the caller and Y the owner:

| Caller | Allowed |
|---|---|
| Input (the user pressed a key or chose a palette entry) | always |
| CLI from a shell outside any plugin (the user typing) | always |
| Host | always |
| Plugin X, X == Y | always |
| Plugin X, X != Y | only if the catalog entry has `Public == true` |

The rule is `Public`-based for host actions too: a plugin may invoke a `husk.*`
action only when the host's catalog marks it public. A caller of unknown kind,
or a plugin caller with no plugin id, is denied.

Notes fixed by the implementation (`internal/stem/dispatcher`):

- A well-formed id has at least two non-empty dot-separated segments, no
  whitespace, `;`, `=` or backslash, and at most 256 bytes. `args` over 1 MiB, or not
  encodable as a JSON object, is `invalid_params`.
- Owner result `null` is an empty object; any other non-object result is
  `internal`. Numbers keep their exact text (`json.Number`).
- The catalog adapter accepts a plugin document's action only when its id is
  `<source>.<name>` with a single-segment name, so a document cannot claim
  another owner's id or `husk.*`.
- `params.caller.kind` for a host caller is `"host"`.
- `action.list` returns the entries the caller could invoke: Input, CLI and
  Host see every entry, a plugin sees its own and the public ones. `live` is
  true for host actions. The `--owner` filter of `husk action list` is applied
  by the CLI, since the method has no parameters.
- A Stem caller arriving over the `CallsPort` is a Host caller, not Input: it
  gets State's focus like every caller except Input.
- A host handler that panics yields one `internal` reply and a log line.

Default is deny. A plugin marks an action public in its manifest
(`"actions": [{"id": "notes.new", "title": "New note", "public": true}]`). The
public flag is the whole policy in v2: no per-caller allow lists. `husk.*`
actions are not public, so a plugin cannot invoke host actions until one is
marked public in the host's own catalog.

Request to the owner (down the owner's stdin):

```json
{"id":41,"method":"action.invoke","params":{"action":"notes.new","args":{"title":"Todo"},"caller":{"kind":"input"},"focus":{"surface":"sidebar-list","owner":"notes"}}}
```

Reply from the owner (the id is the integer Calls assigned; error codes come
from the [Protocol](protocol.md) registry, `failed` for an application failure):

```json
{"id":41,"result":{"path":"/notes/todo.md"}}
{"id":41,"error":{"code":"failed","message":"disk full"}}
```

Caller identity in `params.caller` is `{"kind":"plugin","plugin_id":"other"}`
for plugin callers, `{"kind":"cli"}`, `{"kind":"input"}` or `{"kind":"host"}`
otherwise.

Request from a plugin or the CLI over the socket (the Server sets the Caller
from the request's `token`, see [Pods](pods.md), then calls `Invoke`):

```json
{"id":"7","method":"action.invoke","token":"9f2c...e1","params":{"action":"notes.new","args":{"title":"Todo"}}}
```

The Server's `Reply` writes `{"id":"7","result":{...}}` or `{"id":"7","error":{...}}`
back on that connection using the caller's own id. Calls generates a distinct
id (`41`) for the leg to the owner; the caller's id never reaches the owner.

Reply routing. The continuation is fixed at `Invoke` time and runs on the loop:

- **Input**: the continuation posts an `actionDone` loop message. A success is
  silent; an error becomes a toast (`title` = action id, `message` = error
  message). Input never waits.
- **Plugin or CLI via the Server**: the continuation writes the reply to the
  originating connection. If the connection closed meanwhile, the reply is
  dropped and logged at debug level.
- **Host**: the continuation is the caller's own function.

CLI surface: `husk action invoke <id> [--arg key=value ...]` prints the
`result` as JSON, or the error to stderr with exit code 1; `husk action list`
prints catalog entries with a `live` column.

Dispatcher invariants:

1. `Reply` is called exactly once per `Invoke`, whatever happens, including
   owner exit and timeout.
2. An action has one owner; the Dispatcher never fans out.
3. The Dispatcher never retries and never queues for a non-live owner.
4. Permission is checked before liveness, so a denied caller learns nothing
   about whether the owner is running.
5. `Caller` is set by the accepting module from authenticated context; no
   `params` field can set it.
6. Actions to one owner are written in `Invoke` order. Replies may return in
   any order.
7. The Dispatcher reads the catalog and State; it never writes either. Calls
   does not import the Dispatcher; the Dispatcher does not import the Server,
   Input or any Pod type.

Failure modes:

| Condition | Detected by | What happens | Caller sees |
|---|---|---|---|
| Malformed id or non-object `args` | Dispatcher | Not sent | `invalid_params` |
| Action not in catalog, or `husk.*` with no handler | Dispatcher | Not sent | `not_found` |
| Plugin calls a non-public action of another plugin | Dispatcher | Not sent, logged at info | `denied` |
| Owner not live (not started, crashed, exited) | Dispatcher via State | Not sent | `unavailable` |
| Owner exits or disconnects with the call pending | Calls | Pending entry failed | `cancelled`, `data.reason` `plugin_exited` |
| Owner silent past the timeout | Calls | Entry failed, late reply refused and logged | `timeout` |
| Owner replies with `error` | Owner | Passed through verbatim | the owner's code (`failed` by convention) and message |
| Pending cap reached for the owner or overall | Calls | Not sent | `busy` |
| Owner's outbound queue overflows | Wrapper | Plugin killed per [Pods](pods.md); pending calls fail | `cancelled` |

Limits:

| Limit | Value | Reason |
|---|---|---|
| Action timeout | 10 s | Matches today's hook timeout; long work should return a handle, not hold the call |
| Pending actions per owner | 64 | Matches today's per-subscriber hook cap |
| Pending requests overall (actions and hooks) | 1,024 | Matches today's overall cap |
| `args` size | 1 MiB | Under the 4 MiB line limit with room for the envelope |

### Event bus

```go
type Event struct {
    Name   string         // "state.changed", "notes.selection-changed"
    Key    string         // optional coalescing key; "" means name alone
    Source string         // plugin id, "husk" for core publishers, "user" for a bare-shell CLI emit
    Data   map[string]any
}

type EventBus interface {
    // Publish never blocks and never reports delivery. Called on the loop.
    Publish(e Event)
    // Flush delivers the coalesced batch. A tick ends at the end of one
    // Update call; the loop flushes State first, then the Event bus.
    // Follow-up messages are separate ticks, not part of this batch.
    Flush()
    // Subscribe adds a connection-lifetime subscription (CLI watch, socket
    // client). Manifest subscriptions come from the table, not from here.
    Subscribe(sink Sink, pattern string) (cancel func(), err error)
}
```

In code the bus writes `event.deliver` through the host's `Notifier`
(one `protocol.Message` per notification) instead of `Sink`; `Subscribe` may be
called, and cancelled, from any goroutine, while `Publish` and `Flush` are
loop-only. A malformed pattern makes `Subscribe` fail with `invalid_params`
and register nothing. The manifest table (patterns per plugin, plus the event
names declared `"coalesce": false`) is replaced as a whole on reload. A
plugin with a malformed pattern loses its own subscriptions and is reported by
name; other plugins' subscriptions are applied.

Subscriptions. A plugin subscribes in its manifest, once, by exact name or by
prefix:

```json
{"events": {"subscribe": ["state.changed", "notes.*"]}}
```

A pattern is an exact event name, or a prefix ending in `.*` that matches any
name with that prefix followed by at least one more component (`notes.*`
matches `notes.saved` and `notes.view.opened`, not `notes`). No other
wildcards. The table is config: a manifest change takes effect on
`husk config reload` or plugin add, like the keymap. A pattern may match no
declared event; subscribing needs no prior declaration.

Event as delivered, the `event.deliver` notification with no id, on the
subscriber's stdin (or on a watching socket connection):

```json
{"method":"event.deliver","params":{"name":"state.changed","source":"husk","data":{"slice":"husk.focus","owner":"husk","revision":42}}}
{"method":"event.deliver","params":{"name":"notes.saved","source":"notes","data":{"path":"/notes/todo.md"}}}
```

Publishing. Core modules call `Publish` directly. A plugin publishes with an
`event.emit` request to the Server, which sets `source` from the caller's
token and replies `{}` once the event is on the bus (so a CLI caller gets an
exit status); delivery to subscribers stays fire-and-forget:

```json
{"id":3,"method":"event.emit","token":"9f2c...e1","params":{"name":"notes.saved","data":{"path":"/notes/todo.md"}}}
{"id":3,"result":{}}
```

Names. A plugin event name must start with `<plugin_id>.`; the Server
rejects others with a `denied` reply. A bare-shell CLI caller may emit only
under the `user.` prefix. Core event names have no plugin-id prefix and are
reserved: `state.changed`, `pods.plugin-exited`, `services.reloaded` and
`secrets.read` now, and the `state.`, `surface.`, `input.`, `pods.`,
`services.`, `secrets.`, `user.` and `husk.` prefixes. A plugin may declare
events in its manifest for introspection
(`"events": {"publish": [{"name": "notes.saved", "title": "...", "coalesce": false}]}`).

`state.changed` is published by State once per slice per tick, after the
writes are applied, and carries the slice name, its owner and its revision,
never the value ([State](state.md)):

```json
{"slice":"husk.pods","owner":"husk","revision":17}
```

It carries no value. A subscriber that wants the value reads State
(`husk state get <slice>`). Its coalescing key is the slice name.

Delivery. For each published event the bus finds subscribers whose pattern
matches (manifest table plus connection subscriptions), skips plugin
subscribers that are not live in State (checked when the event is
published, not at flush), and queues the event for each. A subscription
cancelled before the flush receives nothing from that tick. Order
of fan-out across subscribers is unspecified. Order per subscriber is the
order of first arrival in the tick. A publisher receives its own events if it
subscribed to them.

Coalescing. Within one loop tick, for one subscriber, events with the same
`(Name, Key)` collapse to the last one; the position in the batch is that of
the first. Events with different names or keys never collapse. A publisher
may declare `"coalesce": false` for an event whose every occurrence matters;
those are queued in full, up to 256 per subscriber per tick, and the newest
excess events are dropped, with one log line per subscriber per tick. The cap
restarts each tick. A refused `Notify` drops only that notification. A tick
ends at the end of one `Update` call. The loop flushes State first, so its change events join that call's
batch, then flushes the Event bus. Follow-up messages are separate ticks;
there is no posted `flushEventsMsg` extending the coalescing window.

CLI surface: `husk event emit <name> [--data json]`, and `husk event watch
<pattern>`, which keeps the socket open and prints each event as a JSON line
until interrupted. `husk state watch` is not an event watch: it uses
`state.watch`, which also carries values ([State](state.md)).

Event bus invariants:

1. `Publish` and `Flush` never block and never wait for a subscriber.
2. A slow or dead subscriber affects only itself: the Wrapper's bounded queue
   disconnects it per existing policy; other subscribers are unaffected.
3. No ordering across subscribers; per-subscriber order is stable within a
   tick and preserved across ticks.
4. Coalescing never merges different `(Name, Key)` pairs and never drops the
   last event of a burst.
5. A plugin cannot publish outside its own namespace nor under a core name.
6. The bus holds no history: an event published while a subscriber is not
   live is not replayed when it starts.
7. The bus does not import State's internals: State calls `Publish`; the bus
   reads liveness through the same `Liveness` view as the Dispatcher.

Failure modes:

| Condition | Detected by | What happens | Who sees it |
|---|---|---|---|
| Subscriber not live | Bus via State | Event skipped for it | Nobody; no replay |
| Subscriber stdin queue full or write times out | Wrapper | Plugin disconnected (1,024 queue, 5 s write deadline), logged and toasted | User sees the existing disconnect toast |
| Burst of the same `(Name, Key)` | Bus | Collapsed to the last | Subscriber gets one event |
| More than 256 non-coalescing events to one subscriber in a tick | Bus | Excess dropped, one log line per subscriber per tick | Subscriber misses them |
| Plugin emits under another namespace or a core name | Server | Dropped, logged | `denied` reply to the `event.emit` request |
| Pattern is malformed (`*foo`, `a.*.b`) | Manifest conversion at load | Plugin refused with a manifest error | User sees the launch failure |
| `event.watch` connection closes | Server | Subscription cancelled | CLI exits |

### Hook runner

Cut-out. The runner exists so the hook call path that the registry holds today
moves behind a boundary and keeps working. Chain order, veto, modify,
priorities and the reentrancy guard are deferred to a later spec; this
section fixes only the request and reply shapes and today's behaviour.

```go
type HookResult struct {
    Hook     string
    Plugin   string         // the responding subscriber, set by Husk
    Data     map[string]any
    Err      *ActionError   // Protocol codes: "timeout", "cancelled", or the subscriber's own ("failed")
}

type HookRunner interface {
    // Call sends hook.call to every live subscriber of hook and invokes
    // onDone exactly once, when every subscriber has replied, timed out or
    // exited, with one HookResult per subscriber in completion order. With
    // no live subscribers onDone is called at once with an empty slice.
    // Never blocks.
    Call(caller Caller, hook string, data map[string]any, onDone func([]HookResult)) error
}
```

Request to each subscriber, and its reply:

```json
{"id":52,"method":"hook.call","params":{"hook":"notes.before-refresh","data":{"reason":"manual"}}}
{"id":52,"result":{"data":{"ready":true}}}
{"id":52,"error":{"code":"failed","message":"not ready"}}
```

Each subscriber gets its own Calls id; the runner maps the ids back to the
single `Call`. Subscribers are determined by the manifest table
(`"hooks": {"subscribe": ["notes.before-refresh"]}`). The caller gets one
aggregated reply to its own `hook.call` once every subscriber is accounted for
([Protocol](protocol.md)); today's streamed `hook_result` per subscriber goes.
A subscriber that is silent for 10 s is answered for with `timeout`. A
subscriber that exits is answered for with `cancelled`. Pending caps are the
shared Calls caps. A hook has no veto and no result merging in this version.

Chain and veto semantics are deferred.

CLI surface: `husk hook call <id> [--data json]` prints the aggregated result
as one JSON document. It exists for tests and diagnostics.

Hook runner invariants:

1. `onDone` is called exactly once per `Call` and never after the caller
   disconnects (the continuation is dropped, the subscribers' replies are
   still consumed).
2. A hung subscriber never blocks the loop; it costs one pending entry until
   the timeout.
3. The runner reads the hook subscription table and State liveness; it writes
   neither.
4. The runner depends on Calls only; it does not import the Dispatcher or the
   Event bus.

### Dependency directions

- Input and Server import Dispatcher and Event bus. Neither is imported by them.
- Dispatcher imports Calls; Calls imports nothing from Stem's message modules.
- Hook runner imports Calls; Hook runner never imports Dispatcher.
- Event bus imports nothing from Calls; it writes through `Notifier`.
- Dispatcher, Event bus and Hook runner read State and the catalog through
  interfaces (`Liveness`, `ActionCatalog`, the subscription tables). State
  never imports them; it calls `EventBus.Publish` through an interface the
  loop injects.
- Each Pod implements `Sink` and the Pods host implements `calls.Outbox`, both
  over the Wrapper's `Channel` ([Pods](pods.md)). Neither imports the Stem
  message modules; the Wrapper inspects nothing it carries.

## Level 2: Migration from today

- `cmd/husk/registry.go`: split. Slot declaration, contribution and surface
  claims stay (Surfaces). Hook code (`callHook`, `hookResult`, `expireHooks`,
  `Pending`, `pendingHook`, `hookTimeout`, `maxPendingHookCalls`,
  `maxPendingPerSubscriber`) moves to the Hook runner, with the timeout and
  caps moving into Calls. `emitEvent`, `declareNamed` and `subscribe` for
  events move to the Event bus; runtime `event_subscribe` stays accepted for
  legacy plugins until the major protocol bump, then goes. The catalog half
  of `hello.commands` and manifest declarations becomes the action catalog;
  plugin liveness becomes a State slice.
- `cmd/husk/commands.go`: `executeCommand`'s `husk.*` switch cases become
  `RegisterHost` handlers. The rail item fallback becomes a host handler per
  rail item id, registered by the frontend through `Frontend.RegisterHostAction`
  ([Pods](pods.md), "Frontend seams"). The loop over `cp.Info.Commands` becomes `catalog.Lookup`.
  `sendCommandInvoke` (fire and forget, no reply) is replaced by
  `Dispatcher.Invoke`, which adds a reply and a timeout. `runPluginBinding`
  and `isHostCommand` disappear: Input passes an action id and the Dispatcher
  decides host or plugin. `keyBinding` and `isChordKey` leave for the
  Keybinding service.
- `cmd/husk/input.go`: key resolution still ends in an action id, now passed
  to `Dispatcher.Invoke` with a continuation. `slot_key` to the focused
  surface is not an action and is outside this spec.
- `pkg/huskipc`: `CommandInvokePayload`, `HookCallPayload`,
  `HookResultPayload`, `EventPayload`, `HookSubscribePayload` and
  `EventSubscribePayload` are replaced by the Protocol request and
  notification envelopes. They remain for the legacy `type`-keyed envelope
  until the major bump.
- `pkg/manifest`: gains `actions[].public`, `events.subscribe`,
  `events.publish[].coalesce` and `hooks.subscribe`. Existing `hooks` and
  `events` declaration arrays are read as publish declarations.
- `docs/docs/architecture.md`: "Hooks" and "Events and lifetime" describe the
  legacy path and are rewritten when the move lands; "Input routing" step 4
  changes to "resolve to an action id, then Dispatcher".

## Decisions made here

1. Caller identity has four kinds (`input`, `plugin`, `cli`, `host`). A socket
   request without a token counts as the user and is always permitted; a
   request carrying a valid `HUSK_PLUGIN_TOKEN` is a plugin caller
   ([Pods](pods.md)); peer-pid ancestry is gone.
2. `public` is a per-action boolean in the manifest. No per-caller allow lists.
3. `husk.*` actions are not public, so plugins cannot invoke host actions
   until the host marks one.
4. Permission is checked before liveness so a denied caller cannot probe
   whether a plugin runs.
5. The action timeout is 10 s with per-owner and overall pending caps of 64
   and 1,024, copied from the hook caps; they are shared Calls limits.
6. `focus` in a caller's params is ignored. State's focus slice is read at
   dispatch time; Input passes its own same-tick snapshot.
7. Action `args` is a JSON object with any JSON values (today: string values
   only, a subset).
8. Event subscriptions are manifest-only for plugins. The existing runtime
   `event_subscribe` is legacy. Socket connections (CLI watch) may subscribe
   for their own lifetime so `husk state watch` and `husk event watch` work.
9. Subscribing needs no prior declaration, so a prefix can match events
   declared later.
10. Prefix patterns end in `.*` and need at least one more component. No other
    wildcards.
11. Event names for plugins start with `<plugin_id>.`. Core names have no
    plugin prefix and are reserved: `state.`, `surface.`, `input.`, `pods.`,
    `services.`, `secrets.`, `husk.`, and `user.` for bare-shell emits.
    `event.emit` is a request so refusals are replies.
12. Coalescing keys on `(Name, Key)`, not name alone, so `state.changed` for
    different slices does not collapse.
13. `state.changed` carries `{slice, owner, revision}` and no value.
14. A publisher receives its own events when it subscribed to them.
15. Events with `coalesce: false` are queued in full up to 256 per subscriber
    per tick, excess dropped with one log line per subscriber per tick.
16. Events are not replayed to a subscriber that was not live when they were
    published.
17. Hook results are aggregated into one reply to the caller, one entry per
    subscriber in completion order, `timeout` for silence, `cancelled` for
    exit. Streaming can return as a minor addition if a plugin needs it.
18. `husk action invoke|list`, `husk event emit|watch` and `husk hook call` are
    the CLI surface of these modules.

## Open questions

1. The agreed rule keys coalescing on "the same event name". With that rule
   `state.changed` for the `focus` and `plugins` slices in one tick would
   collapse to one event and a subscriber would miss a slice. Decision 12 adds
   a key. Recommended: accept the key; the alternative is one event name per
   slice (`state.changed.focus`), which makes prefix subscriptions the only
   way to watch State.
2. Should an action timeout be overridable per action in the manifest?
   Recommended: not in v2; an action that takes longer returns a handle and
   emits an event when done.
3. Should the host be allowed to mark some `husk.*` actions public (for
   example `husk.toast`)? Recommended: yes, but decide the list when the first
   plugin needs one, not now.
4. Should a plugin that subscribes to `state.changed` be limited to some
   slices? The State design says anyone reads anything, so none. Recommended: keep it
   open, revisit if a secrets-carrying slice appears.
5. Where does the `husk` owner of an action stop being in-process? If
   distributions ship host-side extensions, `husk.*` handlers may need a
   registry outside `cmd/husk`. Recommended: defer; `RegisterHost` already
   covers it.
6. Today's hook path checks the global and per-subscriber caps before any
   envelope is delivered, and the cut-out keeps that: a cap refusal refuses
   the whole call and nothing is sent. Delivery itself is per-subscriber, as
   today's publisher (`deliverPublications` ignores one plugin's failed send
   and delivers the rest): a send that fails mid fan-out becomes that
   subscriber's entry — `unavailable`, reason `disconnected` — and the
   remaining subscribers still get theirs. The spec names the caps but not
   the per-subscriber failure entry. Recommended: keep; parity.
7. The runner can refuse before any subscriber is asked: `not_found` for an
   undeclared hook id (today's `unknown hook`), `busy` for the caps, and
   `unavailable` when the call table is closed. The spec's HookResult codes
   name only timeout, cancelled and the subscriber's own codes; these
   refusals answer the caller's `hook.call` directly with no result list.
   Recommended: confirm the codes in the protocol table suffice; they do
   today.
8. A subscriber that is not live when the hook is called is skipped without
   an entry in the aggregate, as today. The spec says "every live subscriber"
   but does not name that a non-live subscriber produces no entry (unlike a
   subscriber that exits mid-call, which gets `cancelled`). Recommended:
   keep; liveness at call time defines the fan-out.
9. Today's registry keys pending hook calls on the caller's `request_id`,
   refusing duplicates in flight and freeing the id on reply or timeout. The
   cut-out drops that seam: the wire envelope `id` correlates the caller's
   request and Calls ids correlate the legs, so there is no caller-supplied
   request id to deduplicate. Recommended: nothing needs it; record that the
   migration drops it.
10. When a plugin with pending hook legs disconnects, today's registry
   answers the waiting callers at once with `"plugin disconnected"`
   (`cmd/husk/registry.go` `removePlugin`). The runner leaves the legs pending
   until their own deadline instead: `DropPlugin` is the loop's call
   (host-root wiring), so until that wiring exists nothing shortens the
   wait. Recommended: behaviour stays for the cut-out; record that host-root
   must feed `DropPlugin` on disconnect to recover v1's immediate answer.
