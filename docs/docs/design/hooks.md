---
type: Design Spec
title: "Hooks"
description: "The Hook runner contract: a hook is an extension point inside its owner's operation; the owner fires it, declares its kind (transform, collect or gate), and uses the subscribers' answers to decide what it does next."
status: planned
generated: { by: claude/fable-5-1, at: 2026-10-23T12:00:00Z }
tags: [architecture-v2, stem]
---

# Hooks

A hook is an extension point inside an operation. The code that owns and is
running the operation stops at a defined point, asks whoever registered for
that point, and uses their answers to decide what it does next. Only that code
fires the hook, because only that code is at the point. This note is the full
contract. It replaces the cut-out in [Message kinds](message-kinds.md), which
now only places the Hook runner among the three message kinds.

## Level 0: What and why

Husk has three ways for one party to make another party run code, and they
answer three different questions:

- An **action** is "do this for me": one owner, one reply, invoked by a user,
  a plugin or the CLI.
- An **event** is "this happened": the publisher tells whoever listens and
  moves on. It does not wait, does not know who listened, and nothing a
  listener does changes what the publisher does.
- A **hook** is "I am about to do this; what do you say?": the owner of an
  operation stops, asks, and continues according to the answers.

The test that separates an event from a hook is the last clause. If the
answers cannot change what the owner does next, the point is not a hook, it
is an event, and the owner should publish one and keep going. A hook whose
subscribers answer nothing useful is an event that waits, and Husk has no
such thing: every hook kind defines an answer the owner consumes.

Two needs drive the design:

- **A plugin wants to let others participate in its operation.** A file list
  is about to open a path and wants other plugins to say which viewer to use.
  A list wants other plugins to add commands to each row. A plugin is about to
  delete something and wants to give an indexer a chance to object.
- **Husk wants to let plugins participate in the host's own operations.**
  Before the user's CLI resets a slice, whoever holds unsaved state for it
  gets a chance to finish or object.

Both are one mechanism: one runner, one wire method, three kinds.

### Three kinds, from prior art

What a hook point lets its subscribers do is the heart of its contract, so
the owner declares it per hook, and it is not an option a firer picks. Husk
has three kinds; each has a well-known precedent:

| Kind | What a subscriber may do | The owner receives | Precedent |
|---|---|---|---|
| `transform` | replace a value, in turn | the value after the last subscriber | WordPress filters; Kubernetes mutating admission webhooks |
| `collect` | contribute items | every subscriber's items, in priority order | Emacs hooks that push onto a list; VS Code contribution points; WordPress menu and toolbar filters |
| `gate` | allow or veto, with a reason | allowed or not, and every veto | git `pre-commit`; Kubernetes validating admission webhooks; Emacs `run-hook-with-args-until-failure` |

How Husk differs from the systems it borrows from:

- **WordPress actions** run every callback synchronously and return nothing to
  the caller. In Husk's terms that is an event with listeners that happen to
  run in the caller's thread; Husk's events are asynchronous and coalesced
  ([Message kinds](message-kinds.md)), and a WordPress action with no return
  value has no hook counterpart here. WordPress filters are Husk's
  `transform`, including the priority order and whole-value replacement.
- **VS Code `onWill*` events with `waitUntil`** let a listener delay the
  operation but not change or stop it. That has no Husk counterpart: a delay
  with no decision fails the test above and is an event. What Husk takes from
  it is the failure policy: a listener that is slow or broken past the budget
  is logged and skipped, never a veto by accident.
- **git hooks** gate with an exit status and stop at the first failure. Husk's
  `gate` asks every subscriber and reports every veto, so the owner, and the
  user behind it, sees all objections at once rather than one per attempt.
- **Kubernetes admission** runs mutating webhooks, then validating ones. Husk
  keeps the kinds pure: an owner that wants both declares a `transform` hook
  and a `gate` hook and fires them in that order. A single kind that could do
  both would need two reply shapes and a rule for mixing them.

What hooks are **not**:

- not events: an event has no reply and is coalesced; a hook has one reply
  per subscriber and is never coalesced.
- not actions: an action has one owner and is something a user invokes; a
  hook is fired by code reaching a point in its own operation and has many
  answerers. Hooks do not appear in the command palette and cannot be fired
  from the CLI.
- not a transport or a correlation table: delivery and timeouts belong to
  [Calls](calls.md), bytes to the Wrapper in [Pods](pods.md), envelopes to
  [Protocol](protocol.md).

Dependencies: the runner depends on Calls, on the hook catalog (declarations
and registrations from the `plugins` documents in [Store](store.md), plus the
host's own hook list) and on State liveness. The Server and the host's own
modules (the `state.clear` handler) depend on the runner. Nothing in Calls,
Dispatcher or Event bus depends on it.

### Terms

- **Hook id**: `<owner>.<name>`, where the owner is a plugin id or `husk`.
  Well-formed means: at least two non-empty dot-separated segments, no
  whitespace or control characters, `;`, `=`, backslash or `*`, at most 256 bytes: the Dispatcher's
  action-id rule ([Message kinds](message-kinds.md)) plus the `*` ban, which
  the Dispatcher does not have (hooks are stricter on purpose). Resolution step 1, the
  publish validation and the subscribe validation all apply this one rule.
- **Owner**: the plugin that declared the hook, or the host for `husk.*`. The
  owner is the only party that fires it. Ownership is read from the catalog,
  never inferred from the id's spelling.
- **Subscriber**: a plugin that registered for the hook in its manifest.
- **Kind**: `transform`, `collect` or `gate`, declared by the owner.
- **Leg**: one `hook.call` request from Husk to one subscriber, one Calls
  entry.
- **Stage**: one leg of a `transform` chain; stages run one at a time.
- **Outcome**: the single reply the owner receives.

## Level 1: Contract

### Who fires a hook

Only the owner, because only the owner is at the point:

| Firer | `husk.*` hooks | `<plugin>.*` hooks |
|---|---|---|
| The host (a core module reaching the point in its own operation) | yes | no |
| Plugin X, X is the hook's owner | no | yes |
| Plugin X, X is not the owner, or the hook does not exist | `denied` | `denied` |
| The user's CLI | `denied` | `denied` |

This is not a policy knob. Letting another party fire a hook would make every
subscriber act as if the owner had reached that point when it had not, and
would hand the answers to someone who is not running the operation and
cannot use them. The CLI is denied for the same reason: there is no
operation behind it. To exercise a hook for a test or a diagnostic, start the
owner's operation (for example `husk action invoke <owner action>`) and the
hook fires inside it; `husk hook list` shows what would fire and who would
answer.

Ownership comes from the catalog: `HookDef.Owner` is the authority. Plugin ids
may contain dots (`go` and `go.reference` are both valid ids), so a
`<caller>.` prefix test would let plugin `go` fire `go.reference.before-refresh`.
The runner's first step after syntax looks the hook up; for a plugin caller, a
missing hook and a hook whose `Owner` is not the caller answer `denied` with
the same message, so a plugin cannot probe for hook ids it does not own. The
refusal is uniform, not secret: `hook.list` shows every hook to every caller.
The host may fire only hooks whose `Owner` is `husk`, and a host module firing
anything else is a host bug answered `denied`. The Server sets the `Caller`
from the request's token ([Pods](pods.md)); no `params` field can change it.
At the adapter, `hook.call` is open to `plugin` callers only (`MethodHomes`);
the runner repeats the check so the rule does not depend on the adapter.

### Kinds

| | `transform` | `collect` | `gate` |
|---|---|---|---|
| Legs | one at a time, in priority order; stage *k* receives stage *k-1*'s value | all at once, in the same `Update` | all at once, in the same `Update` |
| Subscriber receives | the current `data` | the owner's `data` | the owner's `data` |
| Subscriber replies | `{"data": {...}}` to replace, or `{}` to pass through | `{"items": [...]}`; `{}` for none | `{}` to allow, or `{"veto": true, "reason": "..."}` |
| Outcome carries | `data` after the last stage | each subscriber's `items`, in priority order | `allowed`, every veto with its reason |
| No live subscriber | `data` unchanged | no items | allowed |
| A failed, slow or exited subscriber | stage skipped, value unchanged | no items from it | counts as allow |
| A reply field for another kind (`veto` in a transform, `data` in a gate) | ignored, logged once per (subscriber, hook) | ignored, logged once per (subscriber, hook) | ignored, logged once per (subscriber, hook) |

Every outcome also carries `results`, one entry per subscriber asked, in
priority order, so the owner can see who answered what and who failed. The
failure policy is one rule for all kinds: a subscriber that is broken, slow
or gone is recorded and skipped, and never silently changes the result. For
a `gate` that means a dead subscriber does not veto. An owner whose operation
must not proceed unless every subscriber answered reads `results` and treats
an error entry as its own veto; the runner does not decide that for it.

### Declaration

Both sides are manifest config, reloadable, no wire method:

```json
{
  "hooks": {
    "publish": [
      {"id": "notes.resolve-viewer", "kind": "transform", "title": "Resolve viewer", "description": "Which viewer opens a path", "timeout_ms": 2000},
      {"id": "notes.row-commands", "kind": "collect", "title": "Row commands"},
      {"id": "notes.before-delete", "kind": "gate", "title": "Before delete"}
    ],
    "subscribe": [
      "notes.before-delete",
      {"id": "notes.resolve-viewer", "priority": 5}
    ]
  }
}
```

Publish entry fields:

| Field | Required | Meaning |
|---|---|---|
| `id` | yes | `<plugin-id>.<name>`; must be prefixed by the declaring plugin's id and a dot (see the catalog rules below) |
| `kind` | yes | `transform`, `collect` or `gate`. No default: the kind is the contract, and a subscriber written against one kind cannot answer another |
| `title`, `description` | no | kept for introspection (`hook.list`) |
| `timeout_ms` | no | per-leg timeout, 1000 to 10000; default 10000 (the Calls default) |

Subscribe entries are a string (the hook id, priority 10) or an object
`{"id", "priority"}`. `priority` is an integer from 0 to 100, default 10,
lower first, the WordPress convention. It is the order stages run in a
`transform` and the order items and entries are listed in a `collect` or
`gate`. A plugin may subscribe to a hook declared by any plugin or by the
host, including one declared later; a registration for a hook nobody
declares is kept in the table and simply never fires. A plugin may subscribe
to its own hooks; it then has to serve the leg while its own `hook.call` is
pending, or the leg times out.

Validation at manifest conversion ([Pods](pods.md)). Each of these fails that
plugin's conversion:

- a publish `id` that is not well-formed (Terms), is outside the plugin's
  namespace, or is declared twice by the same plugin;
- a missing `kind`, or a `kind` other than the three, or a `timeout_ms`
  outside 1000 to 10000;
- a subscribe `id` that is not well-formed (so no `*`: subscriptions are
  exact ids, never patterns);
- a duplicate subscribe `id` (the string and object forms for the same id
  count as duplicates, whatever their priorities);
- a `priority` that is not a JSON integer from 0 to 100;
- any `husk.*` publish id.

Conversion writes the resolved `timeout_ms` (default 10000) and `priority`
(default 10) into the declared document, so the catalog never re-applies
defaults. The legacy `husk-plugin.json/v1` converter maps its `hooks` array to
publish entries of kind `gate`, the kind closest to the v1 wait-for-everyone
behaviour; a v1 subscriber's `{"data": ...}` reply is an allow.

Catalog rules, enforced where the catalog is built from the declared
documents (not at single-manifest conversion, because they compare plugins).
They run in this order on every catalog rebuild, which happens whenever
`Store.DeclaredRevision` advances, so at each `Declare` (a launch) and
`Undeclare` (a removal), over the plugins loaded at that moment. A plugin is
loaded when it has declared documents in Store ([Pods](pods.md) keeps them
across process exit), whatever its liveness: a `failed` plugin still owns its
namespace. `husk plugin remove` unloads it: its documents leave Store, the
catalog refresh that follows undeclaration drops its hooks and subscriptions,
and its namespace is no longer owned.

1. **Namespace.** A loaded plugin owns its whole namespace: every hook id
   that starts with its id followed by a dot. A hook id belongs to the loaded
   plugin with the longest such prefix, and any other plugin's declaration
   inside that namespace is refused and reported as a problem on the
   declarer, whether or not the owner declares that id itself. Plugin `go`
   may not declare `go.reference.x` while `go.reference` is loaded, even if
   `go.reference` declares no hooks at all. When `go.reference` is not
   loaded, `go` may declare it. A refused declaration is simply absent from
   the catalog and returns when the conflict ends.
2. **Duplicate.** A hook id declared by more than one plugin after rule 1 is
   refused for every declarer, with a problem on each. Because publish ids
   carry the declarer's prefix and plugin ids are unique, rule 1 resolves
   every real case; rule 2 is the defensive invariant that makes a
   contested id unfireable rather than letting launch order pick an owner.

Where a refusal under rule 1 or 2 is reported: the catalog build is not a
reload, so its problems are not Services problems and ride no
`config.reload` report. Each refused id is logged at `warning` once per
rebuild, that is once for each `DeclaredRevision` value, and so is
re-logged at each later rebuild while the conflict lasts (bounded in v1:
rebuilds happen only at launch, `plugin.add` and removal). The line names the declarer
and the plugin it collides with. The refused id is simply absent from `hook.list`, which lists catalog
entries and has no problems field. "A problem on the declarer" in the rules
above and in the failure table means that log line.

Calls already in flight keep their subscriber set (invariant 10).

Host hooks are declared in-process by the composition root, with the same
fields, under the `husk` owner. The one in v1 is `husk.before-state-clear`
(below).

### Shared types

```go
// Caller is protocol.Caller ([Protocol](protocol.md), "Caller"), the one
// caller-identity type: set by the accepting module, never read from params.
// The hook runner accepts CallerPlugin and CallerHost; CallerCLI is denied.
type Caller = protocol.Caller

type Kind string
const (
    Transform Kind = "transform"
    Collect   Kind = "collect"
    Gate      Kind = "gate"
)

// HookDef is one declared hook, from a plugin's document or the host list.
type HookDef struct {
    ID      string
    Owner   string        // plugin id, or "husk"
    Kind    Kind
    Timeout time.Duration // per leg
    Title, Description string
}

// Subscription is one registration. The catalog returns them sorted.
type Subscription struct {
    Plugin   string
    Priority int
}

// Catalog is the read-only view the runner is given, implemented outside
// Stem over the declared plugins documents and the host hook list.
type Catalog interface {
    Lookup(hook string) (HookDef, bool)
    // Subscribers returns the registrations for hook sorted by priority
    // ascending, then plugin id ascending. Empty when none.
    Subscribers(hook string) []Subscription
    // List returns every declared hook, for hook.list.
    List() []HookDef
}

type Liveness interface {
    Live(pluginID string) bool // husk.pods status == "live"
}

// HookResult is one subscriber's entry in the outcome.
type HookResult struct {
    Plugin string
    Data   map[string]any // transform: the replacement this stage returned; nil on pass-through
    Items  []any          // collect: the subscriber's items; nil when none
    Veto   bool           // gate: this subscriber vetoed
    Reason string         // gate: the veto's reason, may be ""
    Err    *HookError     // nil on success; code timeout, cancelled, busy, unavailable, invalid_params or the subscriber's own
}

// HookError is a coded failure: a refusal of the whole Call (returned by
// Call and Fire) or one leg's failure (HookResult.Err). Code is a protocol
// registry code; Data is the wire error's data member (for example Calls'
// {"reason":"plugin_exited"}), nil when none. A leg's error is the protocol
// Error object the leg produced, verbatim, data included.
type HookError struct {
    Code    protocol.Code
    Message string
    Data    map[string]any
}

// Listed is one hook.list entry.
type Listed struct {
    HookDef
    Subscribers []ListedSubscriber // sorted as Catalog.Subscribers
}
type ListedSubscriber struct {
    Plugin   string
    Priority int
    Live     bool
}

// HookFirer is how a host module fires a husk.* hook from inside its own
// operation; the composition root implements it over the runner. Fire is
// called off the loop (handler goroutines): it posts runner.Call onto the
// loop and waits for onDone or ctx. It is never called on the loop, where
// the wait would deadlock.
//
// Returns, in this order of precedence: the runner's refusal as a *HookError
// (denied, not_found, invalid_params, busy) when Call refused; a *HookError
// with code unavailable when the loop no longer runs; a *HookError with code
// cancelled wrapping ctx.Err() when ctx ends before onDone; otherwise the
// Outcome and nil.
type HookFirer interface {
    Fire(ctx context.Context, hook string, data map[string]any) (Outcome, error)
}

// Veto is one gate objection.
type Veto struct {
    Plugin string
    Reason string
}

// Outcome is what the owner receives, once.
type Outcome struct {
    Kind    Kind
    Data    map[string]any // transform: the value after the last stage; otherwise nil
    Allowed bool           // gate: no subscriber vetoed; true for the other kinds
    Vetoes  []Veto         // gate: every veto, in priority order
    Results []HookResult   // one per subscriber asked, in priority order
}

// HookRunner is loop-confined: Call, Complete and List run on the loop
// goroutine only, as the Calls table they drive does; adapters post onto the
// loop and wait off it.
type HookRunner interface {
    // Call never blocks. onDone runs exactly once on the loop goroutine
    // unless Call returned an error, in which case it never runs.
    Call(caller Caller, hook string, data map[string]any, onDone func(Outcome)) error
    // Complete feeds Calls results (Resolve, Expire, DropPlugin, Close) to
    // the pending legs; results of other requesters are ignored.
    Complete(results ...calls.Result) int
    // List backs hook.list: every declared hook with its subscribers and
    // their liveness.
    List() []Listed
}

// Sender is the slice of *calls.Table the runner uses.
type Sender interface {
    CanSend(plugins ...calls.PluginID) error
    Send(plugin calls.PluginID, method string, params json.RawMessage, opts calls.Options) (calls.Handle, error)
}

// Options configures the runner. MaxInFlight is the per-hook-id reentrancy
// cap (resolution step 4); zero selects 16. There is no default timeout:
// HookDef.Timeout is always set, because conversion writes the resolved
// timeout_ms into the declared document and the host list declares one. A
// HookDef with a zero Timeout is a catalog error: the catalog adapter logs it
// once and leaves the hook out of the catalog, so it is unfireable (denied
// for a plugin, not_found for the host).
type Options struct {
    Catalog     Catalog
    Liveness    Liveness
    Calls       Sender
    MaxInFlight int
    Logger      func(format string, args ...any)
}
```

Size rule, used everywhere a limit below says 1 MiB: a size is the byte
length of the compact JSON encoding of the object in question. On the owner
side that is `params.data`, checked by the port on the raw bytes before
decoding. On the subscriber side it is the whole `result` object of the
reply, checked by the runner on `calls.Result.Value` before it reads any
field, for every kind: an oversized reply is that subscriber's
`invalid_params` entry whatever it contains.

### Resolution, in order, for one `Call`

1. **Id syntax.** `hook` well-formed (Terms). Failure: `invalid_params`;
   syntax leaks nothing about the catalog.
2. **Lookup and ownership.** `catalog.Lookup(hook)`, then the firing table
   against `HookDef.Owner`. A CLI caller is `denied` whatever the hook. For a
   plugin caller, a missing hook and a hook with `Owner != caller.PluginID`
   both answer `denied` with the same message. For the host, a missing hook
   is `not_found` and a hook whose `Owner` is not `husk` is `denied`.
3. **Data.** `data` a JSON object. Failure: `invalid_params`. The 1 MiB size
   check is the `HookPort`'s alone, on the raw `params.data` before decoding
   (size rule above); the runner never measures, and a host firer passes data
   the host built, trusted for size.
4. **In-flight cap.** `MaxInFlight` (16) calls of this hook id already in
   flight (refuse when in-flight >= 16): `busy`, `data.reason`
   `hook_in_flight`. This is the reentrancy guard. A call is in flight from
   the moment step 7 sends its first leg (or runs `onDone` at once) until its
   `onDone` has run, including when the owner's continuation was dropped and
   the legs are only draining. A call refused in steps 1 to 6 never counts.
   The count is per hook id (a hook has one owner, so per owner too), and
   `Close` empties it.
5. **Subscribers.** `catalog.Subscribers(hook)`, then drop those not live in
   State. Liveness at call time defines the set: a subscriber that is not
   live gets no leg and no entry. None left: `onDone` runs at once with the
   empty outcome for the kind (`data` equal to the input, no items, or
   allowed).
6. **Caps preflight.** `CanSend` over the live set. Failure: `busy`, nothing
   sent. For `transform` the preflight covers the first stage only; later
   stages check as they start.
7. **Run** per kind.

**`collect` and `gate`.** One leg per live subscriber, all sent in the same
`Update`, in priority order. Each leg carries the same `data`. The outcome
completes when every leg has resolved: a reply, a `timeout`, a `cancelled`
(subscriber exited, or shutdown), or a send failure. `Results` is in priority
order whatever the completion order. For `collect`, a reply's `items` is a
JSON array (any element values; the owner defines their shape) and
contributes no items when absent. For `gate`, a reply with `veto` true is a
veto; its `reason` is copied into `Vetoes`; `Allowed` is true when `Vetoes`
is empty.

Reply fields, by kind. A field of the wrong JSON type is that subscriber's
`invalid_params` entry (a gate counts it as allow, per the failure policy);
a field that belongs to another kind is ignored and logged once per
(subscriber, hook) per process lifetime, since a legacy subscriber may send
it on every call:

| Kind | Field | Accepted | Meaning |
|---|---|---|---|
| `transform` | `data` | object; or `null` or absent | object replaces the value, even `{}`; `null` or absent passes it through |
| `collect` | `items` | array; or absent | the subscriber's items; absent is none |
| `gate` | `veto` | boolean; or absent | `true` vetoes; `false` or absent allows |
| `gate` | `reason` | string; or absent | copied into the veto; ignored when `veto` is not `true` |
| any | a field of another kind | anything | ignored, logged once |
| any | the whole `result` | object, `null` or absent, at most 1 MiB | `null` or absent reads as `{}` |

**`transform`.** One leg at a time, in priority order. Stage *k* receives the
data stage *k-1* produced (stage 1 receives the owner's data). The subscriber
replies one of:

| Reply | Effect |
|---|---|
| `{"result": {"data": {...}}}` | replaces the data for the next stage (whole-object replacement, not a merge; `{}` is a replacement) |
| `{"result": {}}`, `{"result": null}`, `{"result": {"data": null}}` or `data` absent | passes the data through unchanged |
| `{"error": {...}}`, `timeout`, `cancelled`, send failure | this stage is skipped: recorded in `Results`, data unchanged, the chain continues |

Replacement data must be a JSON object (reply at most 1 MiB, size rule
above); anything else is that stage's `invalid_params` entry and the data
passes through unchanged. Before
each stage the runner checks `CanSend` for that one subscriber; a cap hit is
that stage's `busy` entry and the chain continues. A stage whose `CanSend` or
`Send` fails because Calls is closed ends the chain: the entry is `cancelled`,
reason `shutdown`, and no later stage is sent. The outcome completes when the
last stage resolved. A `transform` cannot veto: a `veto` field in a stage's
reply is ignored and logged once per (subscriber, hook); an owner that needs a veto
declares a `gate` and fires it after the transform.

### Wire shapes

Request from the owner to Husk (the socket; the owner's token identifies it):

```json
{"id":"7","method":"hook.call","token":"9f2c...e1","params":{"hook":"notes.resolve-viewer","data":{"path":"/lists/todo.md"}}}
```

Request from Husk to each subscriber (its stdin; Calls assigns the id).
`kind` tells the subscriber which reply shape the owner reads. There is no
`source` field: the firer is always the owner, which the subscriber knows
from the hook id and `hook.list`:

```json
{"id":52,"method":"hook.call","params":{"hook":"notes.resolve-viewer","kind":"transform","data":{"path":"/lists/todo.md"}}}
```

Subscriber replies, by kind:

```json
{"id":52,"result":{"data":{"path":"/lists/todo.md","viewer":"markdown"}}}
{"id":52,"result":{}}
{"id":53,"result":{"items":[{"label":"Preview","action":"preview.open"}]}}
{"id":54,"result":{"veto":true,"reason":"index write in progress"}}
{"id":54,"error":{"code":"failed","message":"index unreadable"}}
```

Outcome to the owner, `transform`:

```json
{"id":"7","result":{"kind":"transform","data":{"path":"/lists/todo.md","viewer":"markdown"},"results":[{"plugin_id":"preview","data":{"path":"/lists/todo.md","viewer":"markdown"}},{"plugin_id":"stats"}]}}
```

Outcome to the owner, `collect`:

```json
{"id":"8","result":{"kind":"collect","results":[{"plugin_id":"preview","items":[{"label":"Preview","action":"preview.open"}]},{"plugin_id":"stats","items":[{"label":"Word count","action":"stats.count"}]},{"plugin_id":"index","error":{"code":"timeout","message":"no reply in 10s"}}]}}
```

Outcome to the owner, `gate`, allowed and vetoed:

```json
{"id":"9","result":{"kind":"gate","allowed":true,"vetoes":[],"results":[{"plugin_id":"index"},{"plugin_id":"preview"}]}}
{"id":"9","result":{"kind":"gate","allowed":false,"vetoes":[{"plugin_id":"index","reason":"index write in progress"}],"results":[{"plugin_id":"index","veto":true,"reason":"index write in progress"},{"plugin_id":"preview"}]}}
```

In a result entry, `data` is present whenever the stage replaced the value,
even with `{}`, and absent when it passed through (in Go, an empty map versus
nil); `items` is what the subscriber contributed (absent when none); `veto`
and `reason` appear only on a subscriber that vetoed; `error` is the protocol
`Error` object the leg produced, verbatim, `data` member included (so a
`cancelled` entry carries `{"reason":"plugin_exited"}`). Entries never carry
`"data":null`. `data` is always present on a `transform` outcome; `allowed`
and `vetoes` are always present on a `gate` outcome. A refusal before any
subscriber is asked is an error reply to the owner's `hook.call`, with no
`results`:

```json
{"id":"7","error":{"code":"denied","message":"only the owner of \"notes.resolve-viewer\" may fire it"}}
```

`hook.list` (request, socket caller to Husk, protocol minor 2.1) returns every
declared hook with its subscribers:

```json
{"id":"8","method":"hook.list","params":{}}
{"id":"8","result":{"hooks":[{"id":"notes.resolve-viewer","owner":"notes","kind":"transform","timeout_ms":2000,"title":"Resolve viewer","description":"Which viewer opens a path","subscribers":[{"plugin_id":"preview","priority":5,"live":true},{"plugin_id":"stats","priority":10,"live":false}]}]}}
```

`hook.list` has no parameters and no permission filter: every caller sees
every hook, as `action.list` shows catalog entries. `title` and
`description` are present when declared and omitted when empty. The
`--owner` filter of `husk hook list` is applied by the CLI.

### Timeouts and limits

| Limit | Value | Reason |
|---|---|---|
| Per-leg timeout | the hook's `timeout_ms`, default 10 s | the Calls default; an owner that knows its subscribers should be quick declares less |
| `timeout_ms` range | 1,000 to 10,000 ms | the Calls minimum (`MinTimeout`, four sweep periods) and the Calls default; above the default a hung plugin stays invisible too long |
| Pending legs per subscriber | 64 | the shared Calls cap |
| Pending requests overall | 1,024 | the shared Calls cap |
| In-flight calls per hook id | 16 (`Options.MaxInFlight`) | the reentrancy guard: a few legitimate concurrent calls of one hook (two refreshes, a retry) plus headroom, and far below the per-subscriber Calls cap, so a cycle is refused by this cap and not by starving the subscriber's actions |
| Owner `params.data`; each subscriber `result` | 1 MiB each (size rule above) | matches action `args`, under the 4 MiB line |
| `collect` and `gate` total wait | one per-leg timeout | legs run at once |
| `transform` chain length | unbounded; total wait at most subscribers x per-leg timeout | the owner sets its own deadline and the legs still drain |

The caps are the shared Calls caps on purpose: a plugin that is slow at hooks
is slow at actions too, and one number is easier to read in a log than two.
Open question 4 of [Calls](calls.md) stands: split per requester only when a
real plugin hits it.

### Reentrancy

A subscriber may fire its own hooks while answering a leg. The runner is
non-blocking, so the host never deadlocks; whether the subscriber can serve
the nested call while its own reply is pending is the subscriber's concern
(a plugin that handles stdin serially answers the nested call after its own
reply). A cycle (A fires `a.x`, subscriber B fires `b.y`, subscriber A fires
`a.x` again while the first is in flight) is bounded by the in-flight cap:
the 17th nested `a.x` is refused with `busy`, reason `hook_in_flight`, and the
chain unwinds. The runner does not detect cycles, because an owner may
legitimately have several independent calls of the same hook in flight (two
refreshes), and a cap is enough to make a bug visible without forbidding the
legitimate case. Rejected: a per-hook mutex that queues the second call;
queuing hides the bug and adds a wait nobody asked for.

### Subscriber and owner death

- **Subscriber exits mid-call.** The Pods host feeds `DropPlugin` to Calls on
  `pods.ClosedMsg`; the leg resolves `cancelled`, `data.reason`
  `plugin_exited`, at once. `collect` and `gate`: the entry is recorded and
  the call waits for the rest. `transform`: the stage is skipped and the
  chain moves on.
- **Subscriber or owner is removed mid-call** (`husk plugin remove`,
  [Pods](pods.md)). The stop makes it a subscriber exit or an owner
  disconnect, handled as the rules here say; the catalog refresh that follows
  does not touch calls already in flight (invariant 10). Removal itself fires
  no hook: it is not a gate, for the reasons given in [Pods](pods.md),
  "Removing a plugin"; other plugins learn of it from the
  `pods.plugin-removed` event.
- **Subscriber is not live when the call starts.** No leg, no entry (today's
  behaviour; liveness at call time defines the set). The set is fixed then
  and there is no per-stage liveness check: a `transform` stage that starts
  minutes later goes to whatever process the Pod runs under that plugin id at
  that moment, so a subscriber that exited and was restarted mid-chain
  receives its leg on the new process. Accepted.
- **Owner disconnects before the outcome.** The continuation is dropped; the
  legs stay pending until their own deadlines and their replies are consumed
  (today's behaviour). A `transform` chain continues to its end so every
  stage sees a consistent chain; there is no cancel notification in protocol
  2.x to stop it. The call stays in the in-flight count until its `onDone`
  runs, so an owner that disconnects repeatedly holds slots for up to one
  chain's duration each. Accepted cost: wasted work in the subscribers and
  held slots, both bounded by the chain length and the timeout; open
  question 5 is the mitigation when it is needed.
- **Shutdown.** `Close` fails every pending leg `cancelled`, reason
  `shutdown`; a `transform` sends no further stage; outcomes still run so the
  Server can answer open connections before the socket closes.

### Host-fired hooks and `husk.before-state-clear`

The host declares hooks under `husk.` and fires them as `CallerHost` from
inside its own operations. The first one is `husk.before-state-clear`, kind
`gate`, per-leg timeout 2 s, fired by the `state.clear` handler before Store
deletes the persisted copies ([State](state.md), Clear):

```json
{"id":60,"method":"hook.call","params":{"hook":"husk.before-state-clear","kind":"gate","data":{"slice":"example.notes.state","owner":"example.notes"}}}
```

`slice` is the slice name, as every `state.*` payload calls it; `owner` is
the id of the plugin that owns the slice, and `husk` for a host-owned
`husk.*` slice.

A subscriber (normally the slice owner) finishes its pending writes and replies
`{}`, or replies `veto` with a reason. When the outcome is not allowed, the
`state.clear` request fails `denied` with `data`
`{"reason":"vetoed","vetoes":[{"plugin_id":"example.notes","reason":"..."}]}`
and nothing is deleted. Any subscriber's veto counts, not only the owner's:
the hook is a courtesy to whoever holds unsaved state for that slice, and
the veto is bounded by the user's override, since `force` skips the hook. A
subscriber learns the name of the slice being cleared, which `husk state`
already lists for every caller. `state.clear` accepts `"force": true`, which skips the
hook: the user owns their data, and a plugin may object but not win. `husk
state clear --force` sets it. The hook's data carries no slice value; a
subscriber reads State if it wants one.

`state.clear` waits for the outcome under the request's context. The wait is
bounded by one 2 s leg timeout, because a `gate` asks every subscriber at
once, so the CLI's 5 s default is enough. Until the Store delete starts,
`Clear` never proceeds after the context ended: a context ended because the
host is closing answers `unavailable`; any other ended context answers
`cancelled`. Nothing is deleted in either case, including when the context
ends between an allowed outcome and the delete, or when `force` skips the
hook. Pending hook legs drain on their own deadlines. From the delete on,
the clear completes and answers its real outcome, regardless of caller
cancellation.

The handler's branches on what `Fire` returns, checked top to bottom (an
outcome can hold both a veto and a shutdown leg; the Store is closing either
way, so shutdown wins):

| `Fire` returned | `Clear` does |
|---|---|
| an `Outcome` with any leg `cancelled`, reason `shutdown` | answers `unavailable`; deletes nothing, the Store is closing |
| an `Outcome` with `Allowed` false | answers `denied`, reason `vetoed`, with the vetoes; deletes nothing |
| an allowed `Outcome` | proceeds (a subscriber skipped for timeout, error or exit still counts as allow) |
| `busy`, from the in-flight cap or the Calls caps | logs the message and proceeds: fail-open, a saturated hook or table must not lock the user out of their own data, and the cap refusal says nothing about the slice |
| `unavailable` (the loop is gone) | answers `unavailable`; deletes nothing |
| `cancelled` (the request context ended first) | answers `cancelled`, or `unavailable` when the context ended because the host is closing; deletes nothing; the legs drain |
| `denied`, `not_found` or `invalid_params` | a host bug (the hook list or the data the handler built is wrong): logs at error, answers `internal`, deletes nothing |

An outcome with no live subscriber is allowed and proceeds. A nil
`HookFirer` means no hooks are wired: the gate passes and the clear proceeds
as if allowed. A slice that is not registered has no owner to name, so the
hook is not fired and the clear answers `not_found`; a subscriber skipped
for timeout, error or exit is logged by the handler. The table covers the
hook wait; shutdown-caused context cancellation there answers `unavailable`
rather than `cancelled`. The handler checks the context again immediately
before starting the delete, including with `force`.

Once started, the Store delete is not interrupted by the request context.
A failed delete answers `internal`. Once deletion succeeds, the apply phase
resets the slice on the loop and answers the real reset outcome, including
`not_found` if the slice was unregistered. Caller cancellation cannot abandon
the reset or turn a deleted slice into a `cancelled` reply. The reset uses
claimed loop execution: on shutdown, a reset not yet started is guaranteed
never to run, while a running reset is waited for. A reset skipped because
the host is closing still answers success, with no result body, and logs one
warning naming the slice and explaining that the in-memory reset was skipped
because the host is closing and the persisted copies are deleted. Store has
dropped the slice's dirty entries and advanced its epoch, so the final flush
cannot resurrect the deleted value; the live value dies with the process,
and the slice starts at `Initial` on the next start. Before shutdown, a
wedged loop holds the handler like every other loop-bound port.

Rationale: `state.changed` already tells an owner that its slice was reset,
after the fact; the only thing a hook adds is a chance to act before, which
needs a `gate`. Shipping it in v1 proves the host-fired path end to end with
one caller. Rejected: firing it with no `force` escape (a stuck plugin could
lock the user out of their own reset until the timeout, and a hostile one for
ever).

### CLI surface

| Command | Method | Prints | Exit |
|---|---|---|---|
| `husk hook list [--owner <id>]` | `hook.list` | one row per hook: id, owner, kind, timeout, subscribers with priority and `live` | 0 |
| `husk state clear <namespace> [--force]` | `state.clear` | per State | 1 with the vetoes when the gate was not allowed |

`hook list` prints the reply object in JSON mode with its `hooks` sorted by
id and, with `--owner`, only that owner's entries; table mode prints the
columns above, one hook per row, each subscriber as
`<plugin_id> (priority <n>, live|not live)`. `--owner` filters in the CLI
([Instances](instances.md), client-side projections). A vetoed `state clear`
exits 1 with the `denied` reply: JSON mode prints the error object with
`data.vetoes`, table mode adds one `plugin_id: reason` line per veto to the
message on stderr. Ids, owners, kinds and veto reasons are printed under the
table rules in [Instances](instances.md): a string holding a control
or bidi control character is shown in its JSON form.

There is no `husk hook call`: the CLI is not at any owner's point, and a
diagnostic that fires a hook from outside would make every subscriber act on
an operation that is not happening. To see a hook fire, invoke the owner's
operation (`husk action invoke <id>` when the owner exposes it as an action)
and read the owner's result or log. Both commands follow the instance
resolution in [Instances](instances.md).

### The frontend and the palette

Hooks are not listed in the command palette and have no `Frontend` seam in
v1. A hook is a point in an operation; nothing outside that operation fires
it. A frontend that wants a diagnostics view calls `hook.list` like any
socket client. Rejected: palette entries that fire hooks, and a
`Frontend.Hooks()` read, until a frontend needs one.

### Invariants

1. `onDone` runs exactly once per accepted `Call`, on the loop goroutine, and
   never after a refused `Call`.
2. Only the catalog's `Owner` fires a hook: a plugin for its own hooks, the
   host for `husk.*`. Every other caller, the CLI included, is `denied`, and
   ownership is never derived from the id's spelling.
3. A hook's kind is declared by its owner and fixed for every call; a
   subscriber's reply is read only through the shapes of that kind.
4. A `transform` chain runs stages strictly one at a time, in priority then
   plugin id order; stage *k* never starts before stage *k-1* resolved.
   `collect` and `gate` legs are all sent in the `Update` that accepted the
   call.
5. A failed, slow or exited subscriber is recorded and skipped; it never
   vetoes, never contributes items and never changes data.
6. A leg carries nothing about the other subscribers: a subscriber sees
   `hook`, `kind` and the data at its stage, never another subscriber's
   reply. Who else subscribes is learned only through `hook.list`, by
   choice.
7. `plugin_id` in results and vetoes is set by Husk from the leg's target; no
   reply field can set it.
8. The runner reads the catalog and State liveness and writes neither. It
   imports Calls and nothing else from Stem.
9. The per-leg timeout is the hook's declared value, never the firer's choice;
   an owner that wants to wait less uses its own deadline and the legs drain.
10. A hook has no history: a registration added by a catalog rebuild applies to the next
    `Call`, never to one in flight; an in-flight call keeps the subscriber
    list it started with.
11. `Results` is in priority order for every kind, so an outcome is the same
    whatever order the replies arrived in.
12. `Call`, `Complete` and `List` run on the loop goroutine only; `onDone`
    runs there too. Adapters post onto the loop and wait off it.

### Failure modes

| Condition | Detected by | What happens | Firer sees |
|---|---|---|---|
| Plugin fires a hook it does not own, or one that does not exist | Runner | Nothing sent, logged at info | `denied` (same message for both) |
| CLI sends `hook.call` | Adapter (`MethodHomes`), then Runner | Nothing sent | `denied` |
| Host fires a `<plugin>.*` hook | Runner | Nothing sent; a host bug | `denied` |
| Malformed hook id or non-object `data` | Runner | Nothing sent | `invalid_params` |
| Oversized `params.data` (over 1 MiB raw) | `HookPort`, before decoding | Nothing sent | `invalid_params` |
| A `HookDef` with a zero `Timeout` | Catalog adapter | Logged once; hook left out of the catalog | `denied` (plugin) or `not_found` (host) |
| Hook not declared, fired by the host | Runner | Nothing sent | `not_found` |
| Hook id inside another loaded plugin's namespace (declared by that plugin or not) | Catalog build, each `DeclaredRevision` change | The intruding declaration is refused with a problem on its plugin | nothing |
| Hook id still declared by two plugins after the namespace rule | Catalog build | Every declarer refused, problem on each | nothing |
| A 17th concurrent call of this hook | Runner | Nothing sent, logged | `busy`, `data.reason` `hook_in_flight` |
| Pending cap reached at preflight | Calls via `CanSend` | Nothing sent | `busy` |
| No live subscriber | Runner | Outcome at once | the kind's empty outcome |
| Subscriber silent past the hook's timeout | Calls | Entry `timeout`; `transform` skips the stage; `gate` counts it as allow | the entry |
| Subscriber exits mid-call | Pods, then Calls `DropPlugin` | Entry `cancelled`, reason `plugin_exited`; same skip rule | the entry |
| Subscriber replies `error` | Subscriber | Entry with its code (`failed` by convention); same skip rule | the entry |
| Reply over 1 MiB, or a field of this kind with the wrong JSON type (`data` not an object, `items` not an array, `veto` not a boolean, `reason` not a string) | Runner | Entry `invalid_params`; data unchanged, no items, counts as allow | the entry |
| Reply carries a field of another kind (`veto` in a transform, `data` or `items` in a gate) | Runner | Ignored, logged once per (subscriber, hook) per process | the entry, without the field |
| `gate` subscriber vetoes | Runner | Recorded; other legs still complete | `allowed: false`, the veto in `vetoes` |
| Send to one subscriber fails mid fan-out | Calls `Send` | Entry `unavailable`, reason `disconnected`; others proceed | the entry |
| Per-stage cap hit in a `transform` chain | Calls `CanSend` | Entry `busy`; chain continues | the entry |
| `transform` stage refused because Calls is closed | Calls | Entry `cancelled`, reason `shutdown`; chain ends | the entry |
| Owner disconnects before the outcome | Server | Continuation dropped; legs drain | nothing |
| Host shutting down | Calls `Close` | Every pending leg `cancelled`, reason `shutdown` | entries, or `unavailable` if the loop is gone |
| Manifest declares a hook with a malformed id, outside the plugin's namespace, no or bad `kind`, a duplicate publish or subscribe, a malformed subscribe id, bad `timeout_ms` or `priority` | Manifest conversion | Plugin refused | User sees the launch failure |
| `state.clear` gate not allowed | `state.clear` handler | Nothing deleted | `denied`, `data.reason` `vetoed`, `vetoes` |
| `state.clear` fire refused `busy` (either cap) | `state.clear` handler | Logged; clear proceeds (fail-open) | the clear's result |
| `state.clear` before deletion during shutdown (`Fire` `unavailable`, a leg `cancelled`/`shutdown`, or shutdown-caused request cancellation) | `state.clear` handler | Nothing deleted | `unavailable` |
| `state.clear` request context ends before deletion for any other cause, including with `force` | `state.clear` handler | Nothing deleted; pending legs drain | `cancelled` |
| `state.clear` reset cannot start because the host is closing, after deletion succeeded | `state.clear` handler | Reset abandoned; persisted copies remain deleted; warning logged | success, no result body |
| `Fire` refused `denied`, `not_found` or `invalid_params` | `state.clear` handler | Host bug logged at error; nothing deleted | `internal` |

### Dependency directions

- The runner imports Calls and `pkg/protocol`; never Dispatcher, Event bus,
  Store, Pods, Services or the Server.
- The catalog adapter lives in the host root (over the declared `plugins`
  documents and the host hook list) and is handed to the runner as a view.
- The `HookPort` adapter in the host's ports package posts onto the loop and
  waits off it; the Server handler marshals the outcome.
- The `state.clear` port fires the host hook through the `HookFirer`
  interface (Shared types) the composition root wires; State itself never
  imports the runner.

## Level 2: Migration from today

Today's code is the cut-out: `pkg/stem/hooks` fans out every live
subscriber at once, aggregates in completion order, and knows no owner, kind
or priority. It is the starting point; the changes below extend it.

- `pkg/stem/hooks`: `Subscriptions` becomes `Catalog` (`Lookup`,
  `Subscribers` returning sorted `Subscription`s, `List`). `Call` gains the
  ownership check, the in-flight cap and a per-kind runner: the existing
  fan-out serves `collect` and `gate`, with reply interpretation (`items`,
  `veto`) and results placed in priority order instead of completion order;
  `transform` is a new chain state machine that sends one leg per stage from
  `Complete`. `HookResult` drops today's `Hook` field (the outcome carries
  one hook) and gains `Items`, `Veto` and `Reason`; `Outcome` wraps the
  slice. Params gain `kind`. The per-leg timeout comes from the `HookDef`;
  today's `Options.Timeout` is removed, since every `HookDef` carries one.
- `pkg/pods/manifest`: `HookDecl` gains `Kind` (required) and
  `TimeoutMs`; `HookRegistration` gains `Priority`; `hooks.subscribe` accepts
  strings and objects; conversion validates the ranges and the namespace of
  publish ids; the v1 converter maps legacy entries to `gate`. `CatalogJSON`
  carries the new fields. Hook `title` and `description` must survive
  conversion into the declared document.
- Host root `catalogView`: reads kind, timeout and priority from the declared
  documents, applies the namespace and duplicate rules, adds the host hook
  list, sorts subscribers by priority then plugin id, and implements
  `Catalog`. A refused declaration is recorded as a problem on its plugin
  (`catalogView.Problems`, by plugin id) and logged once when it first
  appears; a hook whose resolved timeout would be zero is logged once and
  left out.
- `pkg/host/contracts`: `HookPort.HookCall` returns an outcome instead of
  a slice; `HookPort.HookList` is new; `MethodHomes` narrows `hook.call` to
  `plugin` callers and gains `hook.list` for `cli` and `plugin` callers;
  `StatePort.Clear` gains `force`. Fakes follow.
- `pkg/host/handlers/stem/hook.go`: marshals the outcome shapes above and
  serves `hook.list`. `state.go` reads `force`.
- `pkg/host/ports/stem/hooks.go`: passes the caller through for the
  ownership check and converts the outcome. `state.go` `Clear` fires
  `husk.before-state-clear` through the injected `HookFirer` before
  `Store.ClearSlice`, maps a veto to `denied`/`vetoed`, and skips the hook
  when `force` is set.
- `pkg/protocol`: `MethodHookList`; the version becomes `2.1`; the schema
  generator names this spec as the owner of `hook.*` payloads;
  `wire-schema-v2.json` is regenerated.
- The reference plugin pair: the publisher declares `go.reference.before-refresh`
  as a `gate` and exposes the refresh as an action (`go.reference.refresh`) so
  the hook can be exercised from the CLI through the owner's operation; the
  contributor subscribes with the object form and replies `{}`. The display
  shows how many subscribers allowed.
- `husk hook list` and `husk state clear --force` land with the CLI
  ([Instances](instances.md)). `husk hook call` is not implemented.
- The v1 path in `cmd/husk` and `pkg/huskipc` (`hook_call`, `hook_result`,
  `hook_declare`, `hook_subscribe`) stays untouched until the retirement unit.
  The architecture document's Hooks section describes v1 and is rewritten
  then.

## Decisions made here

1. **A hook is an extension point inside its owner's operation, and only the
   owner fires it.** A plugin fires its own hooks, the host fires `husk.*`;
   every other caller, the CLI included, is `denied`, as is a plugin naming a
   hook that does not exist. Ownership is `HookDef.Owner` from the catalog,
   not an id prefix test, because plugin ids may contain dots. Rationale: the
   answers only mean something to the code that is at the point. Rejected:
   any authenticated plugin or the CLI (the cut-out's rule), which lets
   plugin B make every subscriber believe A reached a point it did not; and a
   CLI diagnostic firer, which has no operation to feed the answers to and is
   replaced by invoking the owner's action.
2. **Three kinds, declared by the owner, required in the manifest:**
   `transform` (chain, replace a value), `collect` (fan-out, contribute
   items), `gate` (fan-out, allow or veto). Rationale: what subscribers may
   do is the hook's contract, and each kind has one reply shape, one
   sequencing rule and one empty outcome. Rejected: a `notify` mode (fan out,
   wait, nothing comes back), which is an event that waits and whose wait is
   had from a `gate` whose subscribers allow; a single mode with a merge rule
   for concurrent replies (which reply wins is arbitrary); a kind that can
   both transform and veto (two reply shapes in one; declare two hooks); an
   open fourth kind (an untyped request/reply is what actions already are).
3. **A `gate` asks every subscriber at once and reports every veto.**
   Rationale: the owner and the user see all objections in one round;
   parallel legs bound the wait to one timeout. Rejected: stopping at the
   first veto (git's rule), which hides later objections and needs an order
   that only matters for a chain.
4. **Priority ascending, then plugin id ascending (bytewise; ids are
   case-sensitive)**, orders `transform`
   stages and the listing of `collect` items and every kind's `results`.
   Default 10, range 0 to 100. Rationale: deterministic across launches and
   reloads; a manifest table has no meaningful cross-plugin array order.
   Rejected: launch order (varies), owner-chosen order (the owner should not
   have to know its subscribers), completion order for results (varies).
5. **Replacement, not merge**, in a `transform`. A stage returns the whole
   data object or nothing. Rationale: one rule, no array ambiguity, what
   WordPress does. Rejected: RFC 7396 merge patch per stage (a stage that
   wants to remove a key with `null` and one that wants to set it to `null`
   collide, and arrays replace anyway).
6. **Failure skips, never vetoes, for every kind.** A timeout, error, exit or
   cap hit records an entry and the result is as if that subscriber had not
   been asked; the owner reads `results` and applies a stricter policy itself
   if its operation needs one. Rationale: VS Code's `waitUntil` policy; one
   dead plugin must not veto the user, and the owner is the only party that
   knows whether silence should block. Rejected: fail-closed by default, and a
   per-hook `on_failure` knob (Kubernetes' `failurePolicy`), which the owner
   can implement in two lines from `results`.
7. **Per-leg timeout is declared by the owner** (`timeout_ms`, 1,000 to
   10,000: the Calls minimum to the Calls default), not chosen per call. The
   caps are the shared Calls caps.
8. **In-flight cap per hook id is the reentrancy guard**, default 16,
   configurable through `Options.MaxInFlight` like the Calls caps; a call
   counts from its first leg until its `onDone`. No cycle detection, no
   per-hook queue. Rationale: WordPress tracks nesting with no cap and Emacs
   and git have no guard; a cap is the cheapest guard that makes a cycle
   visible without forbidding legitimate concurrent calls.
9. **Subscribers see `hook`, `kind` and the data at their stage**, nothing
   about the other subscribers. There is no `source` field: the firer is
   always the owner.
10. **The owner gets one outcome**, aggregate (Message kinds decision 17 and
    Protocol decision 11 stand), with `results` in priority order and only
    the asked subscribers.
11. **`husk.before-state-clear` is the first host-fired hook**, a `gate`, 2 s
    per leg, any subscriber's veto counts, with `force` on `state.clear` as
    the user's override. Until deletion starts, an ended request context
    deletes nothing and answers `unavailable` for shutdown, `cancelled`
    otherwise. From deletion on, the clear completes with its real outcome;
    a reset skipped because the host is closing still succeeds because the
    persisted copies are gone. A nil firer means no hooks are wired and the
    gate passes. Rejected:
    counting only the slice owner's veto (a second rule for one hook, when
    `force` already bounds the cost of a wrong veto).
12. **`hook.list` is added** (protocol minor 2.1) for introspection; hooks
    stay out of the palette, have no `Frontend` seam, and have no CLI firer.
13. **One method name, `hook.call`, in both directions**, as `action.invoke`
    is; the channel (socket in, stdin out) says which direction a line is.
    Rejected: a separate `hook.run` for the owner's request, which adds a name
    without adding information.
14. **Owner disconnect does not stop a chain.** The legs drain; protocol 2.x
    has no cancel notification.
15. **Liveness at call time defines the subscriber set**; a non-live
    subscriber gets no entry; a reload during a call does not change the set.
16. **Catalog conflicts resolve by namespace first** (a loaded plugin owns
    its whole namespace, declared ids or not; the longest loaded plugin id
    that prefixes the hook id owns it; loaded means declared in Store, any
    liveness), and anything still contested after that is unfireable.
    Rejected: first declarer in launch order (the owner would flip between
    launches); refusing only on an actual collision (the owner would gain or
    lose an id when the other plugin edits its manifest).
17. **`collect` ships in v1 although no host hook uses it.** It is today's
    fan-out plus reading an array, and the kind set is the contract plugins
    write against: removing a kind later is a major bump, adding one is a
    minor. Rejected: two kinds now and `collect` on demand.
18. **The reference plugin exposes its refresh as an action,
    `go.reference.refresh`**, whose operation fires `go.reference.before-refresh`,
    so hooks are exercised from the CLI and from tests through their owner.
    Together with decision 1 this is the whole diagnostic story: there is no
    `husk hook call`.
19. **`husk plugin remove` fires no hook.** The host owns the operation and
    could fire a `husk.before-plugin-remove` gate under decision 1; it does
    not, because the stop already gives the plugin EOF, SIGTERM and the 2 s
    grace it gets at shutdown, the operation destroys no data (unlike the
    clear that `husk.before-state-clear` guards, as this spec specifies it),
    the usual target is a dead plugin that cannot answer, and a veto would put
    a plugin between the user and their own instance. The after-the-fact
    notice is the `pods.plugin-removed` event ([Pods](pods.md)). Adding the
    hook later is a protocol minor.

## Open questions

1. Should `timeout_ms` be allowed above the Calls default for owners whose
   subscribers do real work (an export gate that flushes a file)?
   Recommended: not in v1; a slow subscriber returns at once and finishes in
   the background, emitting an event when done, as actions do.
2. Should a plugin be able to subscribe to a `husk.*` hook only when its
   manifest also declares a slice, since `husk.before-state-clear` is about
   slices? Recommended: no restriction; the data names the namespace and an
   uninterested subscriber replies `{}`.
3. Should a `collect` outcome also carry a flattened `items` array with each
   item's `plugin_id`, in addition to the per-subscriber entries?
   Recommended: not in v1; the per-subscriber entries are already in priority
   order and a flattened copy doubles the payload.
4. Should an owner be able to declare a JSON schema for its hook's data and
   replies so stage replies and items are validated? Recommended: later,
   together with action `args` schemas.
5. Should the runner stop sending `transform` stages once the owner's
   connection is gone (the port telling the runner, as Calls `Cancel`
   allows)? Recommended: later, with the cancel notification; today the waste
   is bounded by the chain length and the timeout.
