---
type: Design Spec
title: "Calls: correlation, timeouts and replies"
description: "The Stem module that owns every outstanding Husk-to-plugin request: ids, the pending table, timeouts, caps, exit cleanup and reply delivery on the Bubble Tea loop."
status: planned
generated: { by: claude/claude-opus-5-5, at: 2026-10-22T06:00:00Z }
tags: [architecture-v2, stem]
---

# Calls: correlation, timeouts and replies

## Level 0: What and why

Calls is the Stem module that tracks every request Husk sends down a plugin's stdin and waits on. It assigns the wire `id`, records the call in a pending table keyed by `(plugin, id)`, matches the reply that comes back on the plugin's stdout, fails the call when its deadline passes or its plugin exits, and hands each outcome to the Stem module that asked. It runs entirely on the Bubble Tea loop: no goroutines, no locks, no blocking.

It solves one problem once. Actions and hook calls both wait on a plugin; without a shared module, each would grow its own id scheme, timeout sweep and exit cleanup, and the two would disagree on what a dead plugin looks like.

What it is not:

- It knows no message kinds. It never sees the words "action", "hook" or "event". The caller passes the method, params, timeout and its own requester name.
- It does not route, authorize or check liveness. [Dispatcher](message-kinds.md) picks the owner and checks it is live in [State](state.md); the hook runner picks the subscribers.
- It does not carry notifications. Events go through the Event bus and never touch Calls.
- It does not serve plugin-to-Husk requests. Those arrive at the socket [Server](pods.md) with ids the plugin chose; correlating their replies is the Server's job.
- It does not write bytes. The Wrapper's queued writer in [Pods](pods.md) does.

Who depends on it: Dispatcher and the hook runner (callers), and the Loop (owns the table and feeds it replies, ticks and exits). What it depends on: the wire shapes in [Protocol](protocol.md) and an `Outbox` interface that [Pods](pods.md) implements.

## Level 1: Contract

### Package and dependency direction

Calls is Go package `calls` in the Stem tree (`stem/calls`). Every spec writes paths relative to one import root for the Stem, Services, Substrate and Pods trees; that root is chosen once at implementation and is not part of any spec.

- Dispatcher imports Calls. Hook runner imports Calls. Calls imports neither.
- Calls imports Protocol for `protocol.Message`, `protocol.ID`, `protocol.Error` and `protocol.Code`. Protocol never imports Calls.
- Calls defines `Outbox`; Pods implements it. Calls never imports Pods.
- Calls never reads or writes State and never imports Services or Substrate.
- Loop owns the one `*Table`, routes `ReplyMsg`, `SweepMsg` and plugin-exit messages into it, and delivers `Result`s to requesters. Calls never imports Loop.
- Calls imports `charm.land/bubbletea/v2` for `tea.Cmd` and `tea.Tick` only.

### Types

```go
package calls

// PluginID is the id from the plugin's manifest, the same string State uses for liveness.
type PluginID string

// ID is the wire id of one Husk-to-plugin request. Positive, below 2^53 so
// every JSON implementation round-trips it exactly. Unique per instance.
type ID uint64

// Requester names the Stem module that sent the call and receives its Result.
// Opaque to Calls. Today: "dispatcher", "hookrunner".
type Requester string

// Handle identifies one pending call. Requesters key their own context by it.
type Handle struct {
	Plugin PluginID
	ID     ID
}

type Options struct {
	Timeout   time.Duration // 0 means DefaultTimeout; else MinTimeout..MaxTimeout
	Requester Requester     // required
}

// TableOptions is instance configuration, separate from per-call Options.
// Clock defaults to the system clock; nil Logger is a no-op. Logger must not
// block or reenter Calls. Zero caps use the limits below; negative caps panic.
type TableOptions struct {
	Clock               Clock
	Logger              func(format string, args ...any)
	MaxPending          int
	MaxPendingPerPlugin int
}

// Result is the single outcome of a call. Exactly one of Value or Err is set.
type Result struct {
	Handle    Handle
	Requester Requester
	Method    string          // echoed from the request, for logs only
	Value     json.RawMessage // the reply's "result", verbatim
	Err       *Error          // the reply's "error", or one Calls produced
	Elapsed   time.Duration   // Send to resolution, on the table's clock
}

// Error is the protocol error object; Code is one of the string codes
// Protocol defines.
type Error = protocol.Error

// Outbox writes one request line to a plugin's stdin. Implemented by Pods over
// the Wrapper's queued writer. It must not block; it returns an error when
// the plugin has no live writer or the writer has failed.
type Outbox interface {
	Send(plugin PluginID, req protocol.Message) error
}

// Clock is a value type so model-purity tests can deep-compare the table
// when Logger is nil; reflect.DeepEqual never equates non-nil functions.
type Clock interface{ Now() time.Time }
```

### Operations

All methods run on the Loop goroutine only. None blocks. None calls a requester.

```go
func New(out Outbox, opts TableOptions) *Table

// Send assigns an id, records the call, then writes it through Outbox.
// On any error nothing stays recorded and no Result is ever produced for it.
func (t *Table) Send(plugin PluginID, method string, params json.RawMessage, opts Options) (Handle, error)

// CanSend reports whether one call to each plugin would pass the caps right now.
// The hook runner calls it before a fan-out so a fan-out is all or nothing.
func (t *Table) CanSend(plugins ...PluginID) error

// Resolve matches a reply. ok is false for an orphan (unknown (plugin, id)),
// which Resolve has already logged and dropped.
func (t *Table) Resolve(plugin PluginID, reply protocol.Message) (r Result, ok bool)

// Expire fails every call whose deadline is at or before now with "timeout".
func (t *Table) Expire(now time.Time) []Result

// DropPlugin fails every call pending on plugin with "cancelled" (plugin_exited).
func (t *Table) DropPlugin(plugin PluginID, reason string) []Result

// Cancel forgets a call the requester no longer wants. No Result is produced;
// a later reply is an orphan. Reports whether the call was pending.
func (t *Table) Cancel(h Handle) bool

// Close fails every pending call with "cancelled" (shutdown) and refuses later Sends.
func (t *Table) Close() []Result

// NextSweep claims the single sweep slot and returns the delay, without
// scheduling anything. False means no calls pend, a sweep is already in flight,
// or the table is closed. A true result obliges the caller to schedule the sweep.
func (t *Table) NextSweep() (time.Duration, bool)

// Sweep acknowledges the tick, clears its in-flight flag and expires at Clock.Now.
func (t *Table) Sweep() []Result

// SweepCmd is a thin adapter in a separate file: it calls NextSweep and builds
// tea.Tick only on success. The table and Clock contain no scheduler. The Loop
// batches it after every Send and after delivering sweep results.
func (t *Table) SweepCmd() tea.Cmd

// Stats returns detached pending counts and cumulative counters since New.
func (t *Table) Stats() Stats

type Stats struct {
	Pending   int
	PerPlugin map[PluginID]int // fresh map, only nonzero counts
	Timeouts  uint64
	Orphans   uint64
}

// Messages the Loop receives.
type ReplyMsg struct { // posted by the Wrapper's stdout reader goroutine
	Plugin PluginID
	Reply  protocol.Message
}
type SweepMsg struct{} // produced by SweepCmd
```

Synchronous `Send` errors are Go sentinels; the requester turns them into whatever its own caller sees:

| Sentinel | Condition |
|---|---|
| `ErrBusy` | the configured overall pending cap is full, or not enough representable ids remain for the requested calls |
| `ErrPluginBusy` | the configured per-plugin pending cap is full for this plugin |
| `ErrUnavailable` | `Outbox.Send` failed (no live writer, writer closed, queue full); wraps the cause |
| `ErrBadOptions` | timeout outside bounds, or empty `Requester` |
| `ErrClosed` | `Close` already ran |

Asynchronous failures arrive as a `Result` with `Err.Code` set by Calls, from the registry in [Protocol](protocol.md); `Err.Data` says which case:

| Code | Data | Message |
|---|---|---|
| `timeout` | | `plugin <id> did not reply to <method> within <timeout>` |
| `cancelled` | `{"reason":"plugin_exited"}` | `plugin <id> exited: <reason>` |
| `cancelled` | `{"reason":"shutdown"}` | `husk is shutting down` |
| `internal` | `{"reason":"invalid_reply"}` | `plugin <id> sent an invalid reply to <method>: <why>` |

Any other code in a `Result` came from the plugin, verbatim.

### Wire shapes

Husk to plugin, on stdin, one line:

```json
{"id":42,"method":"<method chosen by the requester>","params":{"any":"json"}}
```

Plugin to Husk, on stdout, one line, `id` echoed as the same JSON number:

```json
{"id":42,"result":{"any":"json"}}
{"id":42,"error":{"code":"failed","message":"nothing selected"}}
```

A `Result` that a requester forwards to a CLI or plugin caller, after a timeout:

```json
{"id":"cli-7","error":{"code":"timeout","message":"plugin notes did not reply to notes.open within 10s"}}
```

(The outer `id` there is the CLI's own id; Dispatcher maps the handle back to it.)

### Delivery on the loop

1. The Wrapper's stdout reader goroutine parses a line, classifies it as a reply (has `id`, has no `method`) and posts `ReplyMsg` to the Loop's inbound channel. It never touches the table.
2. Bubble Tea runs one `Update` at a time. A reply that arrives while `Update` runs waits in the inbound channel (capacity 256 today) and is handled in a later `Update`. When the channel is full the reader goroutine blocks, the plugin's stdout pipe fills, and the plugin blocks on write: backpressure, no drop.
3. In `Update`, the Loop calls `Resolve`, `Expire`, `DropPlugin` or `Close`. Each mutates the table to its final state and only then returns its `[]Result`.
4. The Loop hands each `Result`, in slice order, to the handler registered for `Result.Requester` (`func(calls.Result) tea.Cmd`) in the same `Update`. Results are not re-queued as separate `tea.Msg`s: a second trip would let another reply or an exit interleave, and `tea.Batch` gives no ordering.
5. A handler may call `Send`, `Cancel` or `CanSend` on the same table. This is the only reentrancy, and it is safe because step 3 finished every mutation first.

Malformed reply recovery: if the protocol reader rejects a reply line but exposes a readable `LineError.ID`, the Wrapper salvages that id and forwards a bare-id `protocol.Message` through `ReplySink.Reply`, so the Loop posts `ReplyMsg`. A matching call then fails immediately with `internal` and `data.reason: "invalid_reply"`, rather than waiting for its timeout; an unmatched or non-numeric id is an orphan as usual. The reader goroutine never touches the table.

### Ids

- One counter per `Table`, starting at 1, incremented on every `Send` attempt that passes call-option validation, never reset or reused while the process lives. A failed `Send` burns its id, including cap and outbox failures; rejected call options do not.
- The counter is a `uint64` bounded by the wire's `2^53 - 1` maximum; exhaustion is an instance fault, logged once and rejected permanently with `ErrBusy`, never wrapped or reused.
- The counter is shared across plugins, so an id names one call in logs without the plugin. The table is still keyed by `(plugin, id)`: a reply carrying another plugin's id is an orphan, not a match.
- A reply whose `id` is a string, a fraction, or out of range never matches and is an orphan.

### Timeouts and the sweep

- The deadline is `Send time + timeout`, read from `Clock`. It starts at enqueue, not at write completion; a write that stalls fails the writer within its own 5 s deadline and becomes a plugin exit.
- `NextSweep` claims the one in-flight slot only when calls pend; the separate `SweepCmd` adapter schedules `tea.Tick(SweepInterval)` after a successful claim. On `SweepMsg` the Loop calls `Sweep()` (clear the flag, then `Expire(clock.Now())`), delivers the returned results, and batches `SweepCmd()` again. Clock supplies only `Now`, never scheduling. An idle host runs no ticks; an already-scheduled tick may still arrive after the final call is removed and is then acknowledged without rearming.
- A call therefore fails between `timeout` and `timeout + SweepInterval` after `Send`. A reply handled before the sweep that would expire it wins, even if its deadline has passed.
- `Expire` returns results ordered by deadline, then plugin, then id, so output is deterministic for tests.

### Limits

| Limit | Value | Reason |
|---|---|---|
| `DefaultTimeout` | 10 s | today's `hookTimeout`; long enough for disk or network work, short enough that a hung plugin is visible |
| `MinTimeout` | 1 s | four sweep periods, so lateness stays under 25 % |
| `MaxTimeout` | 60 s | a pending call holds a per-plugin slot; longer work replies early and reports progress through events |
| `MaxPending` | 1024 | today's `maxPendingHookCalls`; now counts every call, one per target plugin |
| `MaxPendingPerPlugin` | 64 | today's `maxPendingPerSubscriber`; one quiet plugin cannot exhaust the global cap, and 64 stays far below the 1024-line outbound queue, so pending requests alone never overflow it |
| `SweepInterval` | 250 ms | today 1 s; 250 ms bounds lateness at 2.5 % of the default timeout for four wakeups per second, only while calls pend |
| Orphan log rate | 1 line per plugin per second, with a suppressed count | a plugin replying in a loop cannot flood the host log |

The caps are constructor-configurable through `TableOptions`; zero selects the defaults above. `CanSend` counts repeated target arguments separately and checks pending capacity plus enough remaining representable ids for the whole fan-out, without reserving entries, burning ids or checking liveness. The caller must not interleave other loop work between the check and fan-out.

Orphan diagnostics use the injected logger. The first orphan logs immediately, later orphans within one second increment a suppressed count, and the next eligible orphan logs that count. Logging never starts an idle sweep solely to flush diagnostics. `DropPlugin` clears that plugin's cooldown and suppressed count so a restarted plugin logs its first orphan immediately; the lifetime Orphans counter remains cumulative.

### Invariants

1. Every successful `Send` produces exactly one `Result`, unless the requester calls `Cancel` first. A failed `Send` produces none.
2. A `(plugin, id)` is in the table from the `Send` that created it until the first of: matching reply, expiry, plugin exit, cancel, close. Nothing else removes it.
3. After `Resolve`, `Expire`, `DropPlugin`, `Cancel` or `Close` returns, no removed key remains and counts match the table.
4. Pending totals and per-plugin counts never exceed their configured caps at any point the Loop can observe; the defaults are `MaxPending` and `MaxPendingPerPlugin`.
5. A successful `NextSweep` claim stays marked until `Sweep` acknowledges it. No second claim succeeds while marked, even if the table temporarily becomes empty; at most one tick is in flight.
6. Calls never blocks the Loop: `Outbox.Send` is non-blocking by contract and no method waits on a channel, timer or lock.
7. Results from one call to `Expire`, `DropPlugin` or `Close` are ordered deterministically (deadline, plugin, id; or id for a single plugin).
8. A reply is matched only against the plugin whose stdout carried it.
9. Calls holds no reference to a requester; routing is by the `Requester` string the Loop resolves.
10. Calls does not order requests to a plugin; the Wrapper's single writer preserves send order on stdin. Replies may arrive in any order.

### Failure modes

| Condition | Who detects | What happens | What the requester / plugin / user sees |
|---|---|---|---|
| Plugin does not reply in time | Sweep (`Expire`) | Call removed, `Result` with `timeout` | Requester gets the error and forwards it; plugin sees nothing; a late reply is an orphan |
| Plugin exits or is disconnected with calls pending | Wrapper, which posts its exit message; Loop calls `DropPlugin` | Every call on that plugin removed, `cancelled` with `data.reason` `plugin_exited` and the Wrapper's diagnostic reason in `message` | Requesters get one error per call; user sees the Wrapper's exit toast, not one per call |
| Outbox write fails at `Send` (no live writer, queue full, writer closed) | `Outbox.Send` | `Send` returns `ErrUnavailable`, nothing recorded | Requester reports failure synchronously; a queue overflow also disconnects the plugin through the Wrapper |
| Global cap reached | `Send` / `CanSend` | `ErrBusy`, nothing recorded or written | Requester refuses its caller |
| Per-plugin cap reached | `Send` / `CanSend` | `ErrPluginBusy` | Same; other plugins are unaffected |
| Reply for unknown `(plugin, id)`: late, duplicate, cancelled, wrong plugin, non-numeric id | `Resolve` | Logged at the orphan rate, dropped, `Stats().Orphans` incremented | Nobody; the plugin is not disconnected |
| Reply matches but has both `result` and `error`, or neither | `Resolve` | Call removed, `internal` with `data.reason` `invalid_reply` | Requester gets the error; log names the plugin and method |
| Requester's own caller went away (CLI disconnected, hook caller exited) | Requester | Requester calls `Cancel` | Plugin still runs the request; its reply is an orphan |
| Loop backlog: a reply waits in the inbound channel past its deadline | Sweep runs first | Call expires; the queued reply becomes an orphan | Requester sees a timeout; accepted, since the deadline is measured on the Loop's clock |
| Instance shutdown | Loop calls `Close` before closing the Server | All calls fail with `cancelled` (shutdown); later `Send` returns `ErrClosed` | CLI callers waiting on an action get an error instead of a dropped socket |
| `Result.Requester` has no registered handler | Loop | Logged as a host bug, result dropped | Nobody; tests assert it never happens |

### CLI exposure

None in this version. `Stats()` exists for tests and a debug overlay. Pending calls are not a State slice (see Open questions).

## Level 2: Migration from today

The historical starting point was plugin-to-plugin hook calls brokered by the host, in `cmd/husk`. The migration below records that split with the paths the code had then; the UI model now lives in `pkg/workbench`, while Calls lives in `pkg/stem/calls`.

- `cmd/husk/registry.go`
  - Moves to `stem/calls`: `pendingHook`, `publicationRegistry.Pending`, `Clock`, `HookTimeout`, the constants `hookTimeout`, `maxPendingHookCalls`, `maxPendingPerSubscriber`, the cap checks and insertion in `callHook`, `pendingFor`, `expireHooks`, the matching half of `hookResult`, and the pending loop at the end of `removePlugin`. `clock` and `realClock` move with them.
  - Changes: one pending entry per hook call with a `Waiting` list becomes one Calls entry per subscriber. The plugin-chosen `request_id` stops being the table key; the hook runner keeps the mapping from the caller's `request_id` to its handles. The cap pre-check across all subscribers becomes `CanSend`.
  - Changes: the error strings `hookTimeoutError` (`"timeout"`) and `"plugin disconnected"` become the `timeout` and `cancelled` codes. The hook runner maps them back to those strings while the old `hook_result` wire shape lives.
  - Stays (for the registry split, then the hook runner and catalog): hook and event declaration, subscription, and caller-side delivery of `hook_result`.
- `cmd/husk/model.go`
  - `hookSweepMsg`, `hookSweepInterval`, `hookSweepOn` and `startHookSweep` become `calls.SweepMsg`, `SweepInterval` and `Table.SweepCmd`. The `case hookSweepMsg` arm becomes `case calls.SweepMsg` with the same shape.
  - The `ipcEventMsg` arm that batches `startHookSweep()` after `routePublication` batches `SweepCmd()` after any requester sent.
  - Replies arrive as `calls.ReplyMsg` from the stdio reader instead of as `hook_result` envelopes inside `ipcEventMsg`.
- `cmd/husk/lifecycle.go`, `cmd/husk/publications.go`: every `removePlugin` call site also calls `Table.DropPlugin` and delivers its results. Shutdown calls `Table.Close` before the listener closes.
- `cmd/husk/transport.go`: `post` and the 256-slot inbound channel stay; the stdio reader gains reply classification.
- `pkg/huskipc/queue.go` (moved to `pkg/protocol/queue.go` when `pkg/huskipc` was deleted): unchanged. Its non-blocking `Send`, 1024-line capacity and 5 s write timeout are what `Outbox` relies on; `ErrSendQueueFull` and `ErrWriterClosed` surface as `ErrUnavailable`.
- `pkg/huskipc/published.go` (deleted): `HookCallPayload.RequestID` and `HookResultPayload.Error` were replaced by the `id` / `result` / `error` envelope of [Protocol](protocol.md).
- `pkg/manifest` (deleted; now `pkg/pods/manifest`): no change from Calls. A per-action timeout field, if added, belongs to the Dispatcher's spec and arrives here as `Options.Timeout`.
- `docs/docs/architecture.md`, Hooks section: the caps and disconnect wording change with the implementation.

## Decisions made here

1. Ids are JSON numbers from one per-instance counter starting at 1, never reused; the table key is still `(plugin, id)`. A reply with a string id is an orphan.
2. Results are delivered synchronously, in the same `Update` that produced them, to a handler the Loop registers per `Requester`; they are not re-queued as `tea.Msg`. Inputs (`ReplyMsg`, `SweepMsg`, plugin exit) are the only messages.
3. `SweepInterval` drops from 1 s to 250 ms; the tick still runs only while calls pend.
4. Timeouts are bounded to 1 s to 60 s, default 10 s, and passed per call in `Options`; Calls has no per-kind table.
5. Caps stay at 1024 overall and 64 per plugin, now counted per target plugin rather than per hook call, and apply to every call regardless of kind.
6. `CanSend` lets a fan-out check all targets before sending any, preserving today's all-or-nothing hook fan-out.
7. A failed `Send` returns a Go sentinel synchronously and never produces a `Result`.
8. A reply handled before the sweep wins over an already-passed deadline.
9. A matched reply with both or neither of `result` and `error` fails the call with `internal` instead of waiting for the timeout.
10. Orphans never disconnect a plugin; they are logged at most once per plugin per second.
11. `Cancel` is local only; no cancel message goes to the plugin. [Protocol](protocol.md) defines none in 2.0.
12. `Close` on shutdown fails all pending calls so requesters can answer their own callers before the Server closes.
13. Calls produces only codes from the Protocol registry (`timeout`, `cancelled`, `internal`) and distinguishes its cases in `data.reason`; it defines no numeric codes.

## Open questions

1. **Wire cancellation.** Should `Cancel` send a `$/cancel`-style notification so a plugin can stop work? Resolved with [Protocol](protocol.md): no in 2.0; a minor version can add a notification the plugin may ignore.
2. **Pending calls in State.** The design notes make State the one place to ask what is true now, and pending calls are live. Writing a slice on every `Send` would emit a change event per call. Recommended: no slice; if `husk state` needs it, publish `Stats()` into a `stem.calls` slice at most once per second.
3. **Timeout ceiling.** 60 s refuses an action that legitimately takes minutes. Recommended: keep the ceiling and require such actions to reply with an acknowledgement and report completion through an event.
4. **Per-plugin cap shared across kinds.** A burst of hook calls can make a plugin's actions fail with `ErrPluginBusy`. Recommended: keep one shared cap; split per requester only if a real plugin hits it.
5. **Error code ownership.** Resolved: [Protocol](protocol.md) owns the string-code registry; Calls uses `timeout`, `cancelled` and `internal` with `data.reason`.
