---
type: Design Spec
title: "Protocol: wire shapes"
description: "The envelopes, framing, versioning, limits, error codes and method names shared by Husk, plugins and the CLI."
status: planned
generated: { by: claude/claude-opus-5-5, at: 2026-10-22T06:00:00Z }
tags: [architecture-v2, protocol]
---

# Protocol: wire shapes

## Level 0: What and why

The protocol is the set of byte-level shapes every Husk channel speaks: the plugin's stdio pipe (Husk to plugin on stdin, the plugin's replies back on stdout) and the instance socket (plugins calling in, and the CLI). It defines three message forms on two envelopes (request, reply, notification), one JSON object per line, a `MAJOR.MINOR` version checked before a plugin starts, a per-line size limit, a closed set of error codes, the rule for answering malformed input, and the method namespace.

It solves one problem: today's wire has one envelope per message type (`{"type":"slot_update","slot_update":{...}}`) and no generic correlation. Every reply-bearing exchange invents its own id (`hook_call.request_id`) and every new message type adds a field to `Envelope`. The target splits the wire the JSON-RPC way, so [Calls](calls.md) can correlate any request without knowing what it means, and the Dispatcher, Event bus and Hook runner ([Message kinds](message-kinds.md)) route by method name.

It is **not**:

- a transport. Process launch, queues and write deadlines belong to the Wrapper and Server ([Pods](pods.md)); socket placement and discovery to [Instances](instances.md).
- correlation or timeouts. [Calls](calls.md) owns pending tables, deadlines and caps; the protocol only defines the `id` field they key on.
- semantics. Which kind a method is (action, event, hook) and who may send it is [Message kinds](message-kinds.md); who may fire a hook, its modes and its outcome shape are [Hooks](hooks.md); what a surface or slice is belongs to Surfaces and [State](state.md).
- JSON-RPC 2.0. It borrows the split, not the spec: no `jsonrpc` member, string error codes, no batches.

Who depends on it: Calls, Dispatcher, Event bus, Hook runner, Server, Wrapper, the CLI, and every plugin in any language. It depends on nothing in Husk: the Go package imports the standard library only.

## Level 1: Contract

### Framing

1. Every message is one JSON object encoded as UTF-8 on one line, terminated by `\n`. A trailing `\r` is stripped. Blank lines are skipped.
2. The same framing applies on the plugin's stdin, the plugin's stdout and every socket connection. A plugin's stderr is not a protocol channel.
3. A line is at most **4 MiB** (4,194,304 bytes) excluding the terminator. Reason: a full-surface render of a 500x200 cell area with truecolor SGR runs fits in about 1 MiB; 4x headroom covers wide terminals and hit-target payloads, and the bound keeps one bad peer from exhausting memory. A longer line is discarded up to its newline and answered with `line_too_long`; the stream stays in sync and the connection stays open.
4. Readers use a 64 KiB buffered reader and grow a line only up to the limit, as `pkg/protocol/read.go` does.

### Envelopes

```go
package protocol // pkg/protocol; imports the standard library only

// ID correlates a request with its reply. On the wire it is a JSON string or
// a JSON integer; it is opaque to the receiver and echoed byte-for-byte.
type ID struct{ raw json.RawMessage }

// Message is any line on any channel. Exactly one of three forms is valid:
//   request:      ID set, Method set, Params optional, no Result, no Error
//   notification: ID absent, Method set, Params optional, no Result, no Error
//   reply:        ID set, Method absent, exactly one of Result or Error
type Message struct {
	ID     *ID             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"` // a JSON object when present
	Result json.RawMessage `json:"result,omitempty"` // presence-tracked; null is a valid result
	Error  *Error          `json:"error,omitempty"`
	Token  string          `json:"token,omitempty"`  // socket requests only: the plugin token from HUSK_PLUGIN_TOKEN, see Pods
}

type Kind int

const (
	KindRequest Kind = iota + 1
	KindNotification
	KindReply
)

// Kind classifies m or returns an *Error with CodeInvalidMessage.
func (m Message) Kind() (Kind, error)

type Error struct {
	Code    Code            `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"` // optional object, code-specific
}

// Reader and writer. ReadLoop keeps reading after a bad line.
const MaxLineBytes = 4 * 1024 * 1024
func ReadLoop(r io.Reader, onMessage func(Message), onError func(*LineError)) error
type LineError struct {
	Code Code    // CodeParseError, CodeInvalidMessage or CodeLineTooLong
	ID   *ID     // the request or reply id when it was still readable, else nil
	Method string // the method when it was still readable, else ""
	Err  error
}
type Writer struct{ /* synchronous or queued */ }
func NewWriter(w io.Writer) *Writer
func NewQueuedWriter(w io.Writer, opts QueueOptions) *Writer
func (w *Writer) Send(m Message) error
```

Wire examples:

```json
{"id":7,"method":"action.invoke","params":{"action":"files.open","args":{"path":"README.md"}}}
{"id":7,"result":{"opened":true}}
{"id":8,"error":{"code":"not_found","message":"no action \"files.opne\""}}
{"method":"input.key","params":{"surface":"main-stage","key":"ctrl+g","modifiers":{"alt":false,"ctrl":true,"shift":false,"super":false}}}
```

Rules:

1. `id` is a JSON string or a JSON integer in the range of an IEEE-754 double's exact integers (|n| < 2^53). `null`, floats, arrays and objects are `invalid_message`.
2. Ids are scoped to **sender and channel**. Husk's requests on a plugin's stdin come from one integer counter ([Calls](calls.md)); a socket caller chooses its own ids, string or integer, and two connections may reuse the same values. A sender never reuses an id while a request with it is pending.
3. `params` is a JSON object or absent. Absent equals `{}`. Arrays are `invalid_params`.
4. A success reply carries `result`. A method with nothing to return replies `{}`. `null` is accepted.
5. Unknown top-level members are ignored, including a `jsonrpc` member sent by a JSON-RPC library. Unknown members inside `params`, `result` and `error.data` are ignored.
6. Husk never trusts identity inside `params`. Any `plugin_id` a plugin puts there is ignored; Husk derives the caller from the channel (the stdio pipe, or the `token` member on a socket request, see [Pods](pods.md)) and sets `plugin_id` itself where a payload reports one. A socket request without `token` is the user's own CLI. Presence is distinct from value: an explicit `"token":""` is a present, invalid credential and is denied, never downgraded to CLI. The Go envelope exposes `Message.HasToken()` using decoded field presence; this adds no wire member.

### Caller

The derived identity is one Go type, `protocol.Caller`, defined in this package because its kind strings are wire-visible (`params.caller` in `action.invoke`, [Message kinds](message-kinds.md)) and because every module that handles a request already imports it. It is the only caller-identity type in the codebase: the Server, the host adapters, the Dispatcher, the hook runner and Services use it unchanged ([Services](services.md), "Callers").

```go
type Caller struct {
    Kind     CallerKind `json:"kind"`                // "cli" | "plugin" | "input" | "host"
    PluginID string     `json:"plugin_id,omitempty"` // set only for "plugin"; the exact plugin id
    ConnID   uint64     `json:"-"`                   // Server's per-connection number; 0 for in-process callers; never serialized
}

type CallerKind string

const (
    CallerCLI    CallerKind = "cli"    // socket request with no token: the user's own shell
    CallerPlugin CallerKind = "plugin" // socket request with a valid per-launch token
    CallerInput  CallerKind = "input"  // a key binding resolved by Input, however it reaches the Dispatcher (directly, or relayed through Frontend.InvokeAction, which carries the kind)
    CallerHost   CallerKind = "host"   // an action the host starts itself: the palette, a rail item, Stem internals
)
```

Only `cli` and `plugin` are ever produced from a socket request. A zero value is not a valid caller; each receiving module says what it answers to one.

### FocusContext

`FocusContext` lives here for the same reason as `Caller`: it is the `params.focus` member of the `action.invoke` request Husk sends an owner ([Message kinds](message-kinds.md)), and it is the one focus type the Dispatcher, the frontend seam (`Frontend.InvokeAction`, [Pods](pods.md)) and the Server share; no module defines its own copy.

```go
// FocusContext is the focus at dispatch time: State's focus slice, or, for an
// `input` caller, the snapshot Input read on the key press.
type FocusContext struct {
    Surface string `json:"surface"` // surface id, "" when nothing has focus
    Owner   string `json:"owner"`   // plugin id occupying Surface, "" when unoccupied or host
}
```

### Problem

`Problem` lives here for the same reason as `Caller`: it crosses the wire as is (the `problems` array of `keys.list`, `settings.validate` and `theme.check`, and every report in `config.reload`, `profile.use`, `settings.set` and `services.reloaded`), and the host contracts that carry those results must not import Services. It is the one diagnostic type every Service, the coordinator and the Keybinding listing use ([Services](services.md), "Snapshots, rebuilds and reports"; [Keybindings](keybindings.md), "What `keys.list` reports"); no module defines its own copy. The codes themselves are owned by the spec that raises them: [Services](services.md) for the Service and coordinator tables, [Keybindings](keybindings.md) for the loader table. Store's `Resolved.Problems` entry ([Store](store.md), "Layers": a code, a message and the file, path and layers involved, with no severity) is a different type for a different reader: Services import it into one of these.

```go
// Problem is one diagnostic: a stable code, where it came from, and prose.
type Problem struct {
    Severity string  `json:"severity"`         // "error" | "warning"
    Code     string  `json:"code"`             // stable snake_case id from the owning spec's table; what tests and tools match on
    Layer    string  `json:"layer"`            // "husk" | "distribution" | "user" | "profile" | "instance" | "plugin:<id>" | "" when not layer-specific
    Key      string  `json:"key,omitempty"`    // settings key, palette role, secret name, or the keymap entry's keys
    Message  string  `json:"message"`          // human-readable; for people, never for matching; never a secret value or a purpose
    Source   *Source `json:"source,omitempty"` // where the offending text is, when the reporter knows
}

// Source locates a declaration: a problem's, or a keymap entry's in `keys.list`.
type Source struct {
    Layer  string `json:"layer"`            // "husk" | "distribution" | "user" | "profile" | "instance" | "focus-local"
    File   string `json:"file,omitempty"`   // config-relative: "config.json", "profiles/<name>/keymap.json"; "builtin" for husk; empty for focus-local
    Line   int    `json:"line,omitempty"`   // 1-based when the parser reports one, else 0
    Plugin string `json:"plugin,omitempty"` // declaring plugin; focus-local and plugin:<id> only
    Order  int    `json:"order,omitempty"`  // index in the layer's array (keymap bind, manifest secrets)
}
```

### Error codes

A closed enum per protocol minor version. A receiver built for an older minor treats an unknown code as `internal`.

| Code | Meaning | Sent by |
| :--- | :--- | :--- |
| `parse_error` | the line is not valid JSON or not UTF-8 | reader, either side |
| `invalid_message` | valid JSON, but not one of the three forms | reader, either side |
| `line_too_long` | the line exceeds 4 MiB | reader, either side |
| `version_mismatch` | the peer's protocol major differs, or its version is unparsable or absent | Husk, to a major-1 plugin; the CLI, locally |
| `unknown_method` | no handler for this method at this receiver | either side |
| `invalid_params` | params missing, mistyped, over a size cap, or an enumerated field out of range | handler |
| `not_found` | the named target (action, hook, event, surface, slice, secret) does not exist | Dispatcher, handlers |
| `denied` | the caller may not do this (namespace, ownership, permission, bad token, missing grant) | Dispatcher, Server, handlers |
| `unavailable` | the target exists but cannot serve now: owner not live, encrypted store unreadable | Dispatcher, handlers |
| `busy` | a pending-call cap or a rate limit is full | Calls, Server |
| `timeout` | the receiver did not reply in time; Husk synthesizes this reply | Calls |
| `cancelled` | the request was dropped: owner exited or disconnected, or the instance is shutting down | Calls |
| `conflict` | a compare-and-set precondition failed, or the target already exists or is owned by an operation in progress (`plugin.add` of a loaded or removing id, a host action id a plugin owns) | State, handlers |
| `failed` | the handler ran and reported an application failure (a plugin's own failure, or a manifest that fails conversion in `plugin.add`) | plugin action or hook handlers, `plugin.add` |
| `internal` | a bug in the receiver, including a reply that is neither result nor error | either side |

`message` is a human-readable sentence for logs and toasts; programs branch on `code` only. `data` is optional and carries the specifics a program may need: `{"limit":4194304}` for `line_too_long` and size failures, `{"reason":"plugin_exited"}` or `{"reason":"shutdown"}` for `cancelled`, `{"reason":"user_denied"}` for a `denied` secret read, `{"revision":9}` for `conflict`. Every other spec names its errors from this table and says what it puts in `data`; no spec defines codes of its own.

### Answering bad input

| Input | Who detects | Answer | Connection |
| :--- | :--- | :--- | :--- |
| Not JSON, or not UTF-8 | reader | `protocol.error` notification, `parse_error` | stays open |
| Over 4 MiB | reader | `protocol.error`, `line_too_long` | stays open, line skipped to its newline |
| JSON but no valid form, `id` readable and `method` present | reader | error reply to that `id`, `invalid_message` | stays open |
| JSON but no valid form, otherwise | reader | `protocol.error`, `invalid_message` | stays open |
| Request for an unknown method | router | error reply, `unknown_method` | stays open |
| Notification for an unknown method | router | none; logged at debug level (a newer minor may send it) | stays open |
| Request with bad params | handler | error reply, `invalid_params` | stays open |
| Notification with bad params | handler | `protocol.error`, `invalid_params`, with `method` | stays open |
| Reply whose `id` matches nothing pending (late after `timeout`, or bogus) | Calls | none; logged | stays open |
| Request or notification on a plugin's stdout | Wrapper | `protocol.error`, `invalid_message` ("stdout carries replies only") | stays open; the plugin stays up |
| Major-1 `hello` on a plugin's stdout, or as the first line of a socket connection | Wrapper / Server | one major-1 `protocol_error` line (on stdin / on that connection, see Versioning) | plugin stopped; a socket connection is closed after the line |

`protocol.error` is a notification:

```json
{"method":"protocol.error","params":{"code":"parse_error","message":"invalid character 'o' in literal null","method":""}}
```

Invariants:

1. A peer never answers a reply, and never answers a `protocol.error` notification. This keeps two strict peers from looping.
2. A bad line never closes a channel. Only a major-1 `hello` and transport failures ([Pods](pods.md)) do.
3. Husk sends at most 10 `protocol.error` notifications per second per channel; excess rejections are counted and logged as one line per second. Reason: a plugin that prints debug text to stdout would otherwise get one notification per line back on stdin.
4. Rejections are also diagnosed to the host. Stdio uses `husk: protocol error from <caller>: <code>: <message>`, as today. Socket diagnostics are counted and coalesced into at most one per-connection loop notice per second carrying the count and last code ([Pods](pods.md)); the reader never waits on loop logging, and notices may be dropped if the loop is behind. Error replies are never throttled by this diagnostic policy.

### Versioning

The rule (`pkg/protocol/version.go`) is:

1. Versions are `"MAJOR.MINOR"`; `"MAJOR"` alone parses as minor 0.
2. A minor bump only adds: optional params or result fields, methods, error codes, enum values. Receivers ignore what they do not know.
3. A major bump is any other change. Husk accepts any minor of its own major and rejects every other major.
4. Version decisions use `protocol_version` only; `host_version` is informational.
5. Minors are cumulative. `protocol.Version` advances to a minor only when every method of that minor and of every lower minor is served; until then that minor is "specified, not landed", whatever lands of it.

What changes: this envelope is a breaking change, so the target major is **2** (`protocol.Major = 2`); `protocol.Version` is `"2.0"` for the envelope and `"2.1"` once [Hooks](hooks.md) lands. Husk speaks one major at a time; there is no dual stack.

Minor 2.1 ([Hooks](hooks.md), [Keybindings](keybindings.md), [Pods](pods.md)): `hook.list`; `hook.call` outcome fields `kind`, `data`, `allowed`, `vetoes` and the per-entry `items`, `veto`, `reason`; subscriber params `kind`; the optional `capture` field of `surface.update`; `plugin.remove` and the `pods.plugin-removed` event name. Owner-only firing (the CLI included) tightens 2.0 without a major bump because no shipped plugin fires a hook it does not own and no CLI exists yet.

Minor 2.2 ([Pods](pods.md), "Adding a plugin"): `plugin.add` and the `pods.plugin-added` event name. **Landed in 2.2:** the CLI-only handler loads a batch and returns its launch outcomes and five reload reports.

Minor 2.3 ([Services](services.md), "Theme" and "Settings"; [Settings center](settings-center.md)): `theme.list` and `settings.set`, plus the `settings` reload reason in `services.reloaded`, plus the `dir` field of each `plugin.list` entry and `husk.pods` entry (additive; the plugin's directory as the converter resolved it, `""` for an implicit command line; [Pods](pods.md), "plugin.* wire shapes"). **Landed in 2.3:** both `theme.list` and `settings.set` are served after minor 2.2's `plugin.add` (rule 5 above); `dir` is landed as an additive field and does not move the version. `plugin.remove` admitting a `host` caller at its port is also landed; it is a port rule, not a wire change, and is not part of any minor. The socket adapter remains CLI-only. The two methods are described below under "Theme selection and the settings write (minor 2.3)" and landed together with their handlers: the constants, the schema entries and the rows of the method table were added in the change that serves them, so `pkg/protocol` never names a method the host answers `unknown_method` to.

Minor 2.4 ([Built-in themes](themes.md), "Role values"; [Services](services.md), "Theme"): the optional `ansi` object beside `palette` in the `theme.get` result and the `theme.changed` params, mapping each palette role a theme set as an ANSI index to that index (`0`–`15`). No method is added and no existing field changes: `palette` values stay `#rrggbb`. **Landed in 2.4:** the host serves `ansi` and minor 2.3 is also landed (rule 5). The field's type is `protocol.ANSIRoles`, used by `ThemeChangedParams` and by the `theme.get` result; the generated schema constrains it on `theme.changed` (role-name keys, integers `0`–`15`), while the `theme.get` result stays an open object there because [Services](services.md) owns it. The shape is under "ANSI palette roles (minor 2.4)" below.

Minor 2.5 serves terminal attachment, output, detachment and encoded input on already claimed surfaces, with full-key capture and its release action. `protocol.Version` is `2.5`; all earlier minors remain served. Runtime surfaces and terminal links are deferred. The Go API changes `SurfaceUpdateParams.Capture` from `*bool` to `*CaptureValue`; migrate boolean callers to `protocol.CaptureBool(true)` / `protocol.CaptureBool(false)` and string-mode callers to `protocol.CaptureValueAll.Ptr()` / `protocol.CaptureValueText.Ptr()`. `TerminalAttachParams.Capture` uses the same optional value type; wire booleans remain compatible.

Where the version is checked, since there is no handshake:

- A plugin states its protocol major in its manifest (`"protocol": 2`). Manifest conversion ([Pods](pods.md)) refuses a plugin whose major differs before it is launched. The Wrapper tells the plugin the host's full version in `HUSK_PROTOCOL_VERSION`.
- A socket caller learns the host's version from the `session.ping` result and from the registration entry ([Instances](instances.md)). The CLI compares majors and exits with its protocol error status when they differ; a plugin calling in does the same check once if it cares.

A major-1 plugin that slipped through (a manifest without a version, read by the v1 converter) is recognized by its first stdout line (`{"type":"hello",...}`) or by the first line of a socket connection it opens. Husk answers it once, on stdin or on that connection, in the major-1 shape so the plugin can log a useful reason, then stops it. On the socket the plugin is the live pod named by `hello.plugin_id`; a hello naming no live pod gets the line and the close and nothing else is stopped:

```json
{"type":"protocol_error","protocol_error":{"message_type":"hello","message":"unsupported protocol major 1: host speaks 2.4"}}
```

### Session start and end

There is no handshake on either channel. `hello` and `welcome` have no successor.

**Stdio.** The pipe is the plugin's identity and the manifest is its declaration, so the plugin has nothing to claim. Everything `welcome` carried arrives as ordinary notifications, in this order, before any request: `theme.changed` (the palette), one `surface.resized` snapshot containing all surfaces the plugin occupies, `input.key_state` (the modifier state). Its identity, the socket path, the token and the host's protocol version are in its environment ([Pods](pods.md)). A plugin is `live` from the moment its process starts; messages sent before it reads them wait in its stdin queue.

**Socket.** A caller writes requests from the first line. Identity is the `token` member on each request, or its absence (the user). The CLI opens one connection per command and closes it when the reply arrives; a watch request keeps the connection open.

**End.** Husk closes the plugin's stdin (the plugin sees EOF) and follows the stop policy in [Pods](pods.md) (SIGTERM, 2 s grace, SIGKILL). There is no end notification; EOF on stdin is the signal.

A requester that stops waiting ([Calls](calls.md) times a request out, or a socket caller disconnects) sends nothing. The receiver's late reply is dropped as unmatched. A cancel notification can be added in a minor version when a plugin has work worth stopping.

### Method naming

1. A method is lowercase dot-separated segments, each matching `[a-z0-9_-]+`. At most 256 bytes.
2. The first segment is the namespace. Husk reserves every namespace in the table below; a plugin cannot register a method name, only action, hook and event ids.
3. A method name has exactly two segments: `<namespace>.<verb>`. Resource ids (action, event and hook ids) travel in `params`, never in the method name, so each router has one method to route: `action.invoke` carries `params.action`, `event.deliver` carries `params.name`, `hook.call` carries `params.hook`.

| Namespace | Methods | Form | Direction |
| :--- | :--- | :--- | :--- |
| `session` | `session.ping` | request; result `{"id","pid","protocol_version","host_version","uptime_ms"}`; `host_version` always present, `""` when the build carries no version, the same rule as the registration entry ([Instances](instances.md)) | socket caller to Husk; the CLI uses it for liveness, discovery and the version check |
| `protocol` | `protocol.error` | notification | either |
| `action` | `action.invoke` | request | Husk to the owning plugin (stdin); plugin or CLI to Husk (socket) |
| | `action.list` | request | socket caller to Husk |
| `event` | `event.emit` | request, result `{}` | plugin or CLI to Husk (socket); the reply says the event was accepted onto the bus, not delivered |
| | `event.deliver` | notification | Husk to a subscriber (stdin, or a watching socket connection) |
| | `event.watch` | request, then `event.deliver` notifications on the same connection | socket caller to Husk |
| `hook` | `hook.call` | request | the hook's owner to Husk (socket, plugin only; the CLI is denied); Husk to each subscriber (stdin). Firing rule and shapes: [Hooks](hooks.md) |
| | `hook.list` | request (minor 2.1) | socket caller to Husk |
| `state` | `state.get`, `state.set`, `state.patch`, `state.snapshot`, `state.watch`, `state.clear` | request | plugin or CLI to Husk |
| | `state.changed` | notification | Husk to a watching socket connection |
| `surface` | `surface.update`, `surface.close` | notification | plugin to Husk (socket) |
| | `surface.resized` | notification | Husk to plugin |
| `terminal` | `terminal.attach` | request | plugin to Husk (socket) |
| | `terminal.write` | request (acknowledged) or notification | plugin to Husk (socket) |
| | `terminal.detach` | request or notification | plugin to Husk (socket) |
| | `terminal.input` | notification | Husk to the attached plugin (stdin) |
| `input` | `input.key`, `input.mouse`, `input.key_state` | notification | Husk to the plugin owning the target surface |
| `theme` | `theme.changed` | notification | Husk to plugin |
| | `theme.get`, `theme.list` | request | plugin or CLI to Husk |
| | `theme.check` | request | CLI to Husk |
| `slot` | `slot.contribute` | request | plugin to Husk |
| | `slot.contributions` | notification | Husk to the publisher |
| `toast` | `toast.show` | notification | plugin or CLI to Husk |
| `settings` | `settings.get` | request | plugin or CLI to Husk |
| | `settings.list`, `settings.schema`, `settings.validate`, `settings.set` | request | CLI to Husk |
| `secrets` | `secrets.get`, `secrets.names` | request | plugin or CLI to Husk |
| | `secrets.set`, `secrets.grant`, `secrets.revoke`, `secrets.reveal` | request | CLI to Husk; a plugin caller gets `denied` ([Services](services.md)) |
| `keys` | `keys.list` | request | plugin or CLI to Husk |
| `profile` | `profile.list`, `profile.use` | request | CLI to Husk |
| `plugin` | `plugin.list`, `plugin.log` | request | CLI to Husk |
| | `plugin.remove` | request (minor 2.1); params, result and sequence: [Pods](pods.md), "Removing a plugin" | CLI to Husk; the host also reaches the port in process, not over the wire (the Settings center's Installed tab, [Settings center](settings-center.md)) |
| | `plugin.add` | request (minor 2.2); params, result and sequence: [Pods](pods.md), "Adding a plugin" | CLI to Husk |
| `config` | `config.reload` | request | CLI to Husk |

Declarations (actions, surfaces, focus-local keys, published slots, hooks, events, slice) are manifest only; there are no `declare` or `subscribe` methods ([Pods](pods.md), [Message kinds](message-kinds.md)).

Direction rule: stdin carries Husk's requests and notifications; stdout carries the plugin's replies and nothing else. Everything a plugin sends on its own initiative, a render above all, goes to the socket, over a connection the plugin keeps open for as long as it likes, with its token on each request. Each method's params schema is owned by the spec named in [Message kinds](message-kinds.md) (`hook.*` by [Hooks](hooks.md)); this spec owns only the name and the form.

### Surface update

`surface.update` replaces the content of one surface the sender occupies: `surface`, `lines` (the frame, required), and the optional `title`, `subtitle`, `badge`, `hit_targets` and `status_meta`. Minor 2.1 adds the optional `capture` (bool): `true` says this surface is taking text input, `false` says it is not, and an absent field leaves the surface's current value unchanged. The host records it in `husk.surfaces` and reads it into the key context so unmodified text keys reach the occupant instead of global bindings ([Keybindings](keybindings.md), [State](state.md)). A `plugin_id` in the params is ignored; identity comes from the token.

```json
{"method":"surface.update","params":{"surface":"main-stage","lines":["> search_"],"capture":true}}
```

### Surface resize snapshot

`surface.resized` carries one consistent array of all affected surfaces, rather than one notification per surface. A terminal resize can change every surface at once. Opening notifications use the same shape, listing all surfaces the plugin occupies:

```json
{"method":"surface.resized","params":{"surfaces":[{"surface":"main-stage","width":80,"height":24,"focused":true,"visible":true}]}}
```

Each entry names the surface and its inner content dimensions in cells, focus and visibility. In moved surface and input params, the host-surface identifier field is `surface`, never the legacy `slot`. Published extension points still use `slot_id`; they are not host surfaces.

### Runtime and terminal surfaces (minor 2.5)

**Terminal surfaces land in 2.5.** The four served methods are `terminal.attach`, `terminal.write`, `terminal.detach` and `terminal.input`. Runtime surfaces (`surface.open`) and `terminal.link` remain Level 1, specified but not landed; they gain constants and schema only with their implementations. Terminal attachment to an already claimed layout surface does not depend on runtime surfaces. The host-side contracts (ports, `MethodHomes`, the Surfaces engine) are named in [Pods](pods.md) and [State](state.md); this section owns only the wire.

Two capabilities a manifest cannot express motivate the minor: a plugin that decides at run time that it needs a modal or the whole canvas (a dialog, a viewer), and a plugin whose content is a terminal, a byte stream from a process, rather than lines of text. Both are generic: no plugin kind is named here.

| Namespace | Method | Form | Direction |
| :--- | :--- | :--- | :--- |
| `surface` | `surface.open` (deferred, not served) | request | plugin to Husk (socket) |
| `terminal` | `terminal.attach` | request | plugin to Husk (socket) |
| | `terminal.write` | request (acknowledged) or notification | plugin to Husk (socket) |
| | `terminal.detach` | request or notification | plugin to Husk (socket) |
| | `terminal.input` | notification | Husk to the attached plugin (stdin) |
| | `terminal.link` (deferred, not served) | notification | Husk to the attached plugin (stdin) |

**`surface.open`** asks the host for a surface the manifest did not claim. Params: `placement` (`modal` or `canvas`; `stage` and `panel` are reserved for a later minor, [State](state.md) open questions), `name` (dot-free, `[a-z0-9_-]{1,32}`), optional `title`, `width` and `height` (cells, or a string percentage `"NN%"` of the terminal; honoured for `modal`, ignored otherwise; clamped to the host's minimum and the terminal), and an optional `frame` (`FrameSpec`). Result: `{"surface": "<plugin_id>.<name>"}`. The surface id is dynamic: it is the plugin id and the name, which cannot collide with the twelve layout surface ids because those contain no dot, and `SurfaceID.Valid()` accepts the two forms. The opened surface is occupied by the caller from the reply on, receives `surface.resized` like a claimed surface, takes `surface.update`, `surface.close` and the `input.*` deliveries, and is released by `surface.close`, by the host (the frame's close, the modal's close key, `plugin.remove`) or by the plugin's Pod leaving `live`, in which case the plugin is told with `surface.resized` carrying `visible: false` and then nothing more. A `modal` surface is a singleton: while one plugin's modal is open, any `surface.open` with `placement: "modal"` answers `busy` with `data.reason` `modal_open`, including the same plugin's. A `name` the plugin already has open answers `conflict`. A plugin may hold at most 8 runtime surfaces (`busy`, `too_many_surfaces`).

```json
{"id":"o1","method":"surface.open","params":{"placement":"modal","name":"picker","title":"Pick one","width":"60%","height":20,"frame":{"border":true,"label":true,"close":true}}}
{"id":"o1","result":{"surface":"example.files.picker"}}
```

**`terminal.attach`** turns a surface the caller occupies (claimed or runtime) into a terminal surface: the host allocates a terminal emulator of the surface's inner size and from then on renders the emulator's screen instead of `surface.update` lines, which answer `invalid_params` (`terminal_attached`) until `terminal.detach`. Params: `surface`, and optional `capture` (`true`, `false`, `"text"` or `"all"`, default `"all"`, see `capture` below) and `scrollback` (lines, default 2000, max 20000). Result: `{"width", "height"}`, the emulator's size in cells, the same numbers the next `surface.resized` carries. A surface already attached answers `conflict`.

**`terminal.write`** carries bytes from the plugin to the emulator: `surface` and `data`, base64, at most 64 KiB decoded per message. The host decodes, feeds the emulator and redraws on its own schedule (writes are coalesced per frame; a plugin never waits for a paint). A notification with invalid params, or a write to a surface the caller does not occupy or has not attached, is logged and dropped, like a refused `surface.update`. Requests with invalid params return the corresponding coded error; writes to an unattached or foreign surface retain the log-and-drop policy regardless of message form. An acknowledged request returns `{}` after ingestion, not after paint. The reference plugin permits exactly one such request in flight and reads the next PTY chunk only after its reply, preserving byte order and applying backpressure without filling the server's 64-notification admission queue.

**Resize.** The host resizes the emulator grid in the same geometry pass as `surface.resized`. The plugin applies the new inner dimensions to its PTY with `TIOCSWINSZ`, which delivers `SIGWINCH` to the foreground process group; the program is expected to redraw and the host renders that subsequent output. The pinned `x/vt v0.1.0` resize truncates cells outside a shrunken grid and does not rewrap already-printed lines. Soft-wrap and history reflow are deferred, not part of the resize contract of this minor.

**`terminal.detach`** returns an attached surface to line rendering with an empty frame and no capture; an already detached surface is a no-op and retains its existing lines; the emulator and its scrollback are discarded. `surface.close` on an attached surface detaches implicitly.

**`terminal.input`** carries bytes from the emulator to the plugin: `surface` and `data`, base64. The host's emulator encodes every key, mouse event and paste it receives for the application's current modes (cursor-key mode, bracketed paste, mouse reporting). This minor encodes legacy keyboard sequences only; kitty/CSI-u and modifyOtherKeys encoding are deferred. Focus in/out is encoded when the program enables focus reporting. OSC 0/2 titles replace a non-empty manifest frame label while set; clearing the title or detaching restores that label. The host sends the encoded bytes here; the plugin writes them to its process verbatim. Key and mouse events on an attached surface are never delivered as `input.key` or `input.mouse` (while a terminal has focus, `input.key_state` broadcasts are suppressed for every plugin, not just its occupant, and resume when focus leaves or it detaches); which keys reach the emulator at all is the keybinding policy's `all` capture mode ([Keybindings](keybindings.md), "Capture modes").

**`terminal.link`** reports a modified click (the host's link modifier, `ctrl` by default) on a URL in an attached surface: `surface`, `url` (an OSC 8 hyperlink target, or a `[a-z][a-z0-9+.-]*://` run detected in the screen text), `row`, `col`. It is informational: the host opens nothing itself.

**`capture`** on `surface.update` (and on `terminal.attach`) accepts, from this minor, `true`, `false`, `"text"` or `"all"`: `true` means `"text"` and `false` means none, so 2.1 plugins are unchanged. `"all"` says the occupant takes every key except the host's release key ([Keybindings](keybindings.md)). The host records the mode in `husk.surfaces.capture` ([State](state.md)). Go callers migrate from `*bool` to `*protocol.CaptureValue`: use `protocol.CaptureBool(true)` / `protocol.CaptureBool(false)` for booleans, or `protocol.CaptureValueAll.Ptr()` / `protocol.CaptureValueText.Ptr()` for modes, on both `SurfaceUpdateParams.Capture` and `TerminalAttachParams.Capture`.

Emulator replies and encoded input use the occupant's existing bounded Pod queue. A full queue drops and logs rather than blocking the host loop or waiting for a paint.

Limits owned here: `terminal.write` 64 KiB decoded per message; runtime surfaces 8 per plugin; `surface.open` `name` 32 bytes; `title` 80 runes; scrollback 20,000 lines.

Why the host emulates: the host owns the cell canvas and the key encoder, so a terminal that lives in the host renders into the canvas like every other surface, keeps its cursor, colours and modes, and encodes keys the way the application asked; a plugin-side emulator would have to send a screen as `lines` every frame and could not learn the application's keyboard mode. Why the plugin owns the process: the host executes nothing on a plugin's behalf; the bytes are the whole contract, so a plugin may attach a PTY, a network stream or a recording.

### Theme selection and the settings write (minor 2.3)

**Landed in 2.3:** both methods and minor 2.2's `plugin.add` are served (rule 5). The method table above, `pkg/protocol/methods.go` and the generated schema contain these rows, constants and payload shapes. The version stamp changes no payload. This section owns only the names, forms and directions; params, results and errors are [Services](services.md)'s ("Theme", "Settings").

| Namespace | Method | Form | Direction |
| :--- | :--- | :--- | :--- |
| `theme` | `theme.list` | request | plugin or CLI to Husk |
| `settings` | `settings.set` | request | CLI to Husk; a plugin caller gets `denied` |

`theme.list` names every theme the Theme Service can resolve, with its source layer and whether it is the one the serving palette was built from. `settings.set` writes **one** settings key with **one** value into the **user** configuration layer through Store's ordinary write, then waits for the reload that write causes and returns its reports, so the write is observed exactly as a hand edit of the file followed by `config.reload` would be. It is the first method that writes configuration; the `theme.name` key is its first user (the Theme tab of the frontend's Settings center, [Services](services.md) "The theme selector"), every other registered key through that center's General tab ([Settings center](settings-center.md)) and `profile.use --save` are its next. The reason string `settings` is added to the `services.reloaded` payload's `reason` enum.

```json
{"id":3,"method":"theme.list","params":{}}
{"id":3,"result":{"themes":[{"name":"charm","source":"built-in","current":false},{"name":"charm-light","source":"built-in","current":false},{"name":"dracula","source":"built-in","current":false},{"name":"base16","source":"built-in","current":false},{"name":"catppuccin-mocha","source":"built-in","current":false},{"name":"catppuccin-latte","source":"built-in","current":false},{"name":"rose-pine","source":"built-in","current":false},{"name":"rose-pine-moon","source":"built-in","current":false},{"name":"rose-pine-dawn","source":"built-in","current":false},{"name":"evening","source":"user","current":true}]}}
{"id":4,"method":"settings.set","params":{"key":"theme.name","value":"evening"}}
{"id":4,"result":{"key":"theme.name","value":"evening","source":"user","reports":[{"service":"profile","gen":5,"applied":true,"changed":false},{"service":"settings","gen":5,"applied":true,"changed":true},{"service":"theme","gen":4,"applied":true,"changed":true},{"service":"keybinding","gen":5,"applied":true,"changed":false},{"service":"secrets","gen":5,"applied":true,"changed":false}]}}
```

### Mapping from today's message types

| Today (`type`) | Target | Form | Notes |
| :--- | :--- | :--- | :--- |
| `hello` | nothing | | declarations move to the manifest; `keybindings` (global) are dropped, see [Keybindings](keybindings.md); identity is the pipe |
| `welcome` | `theme.changed`, `surface.resized`, `input.key_state` as the first lines on stdin; the rest is environment | notification | `context` dropped; workspace content is a State slice |
| `slot_update` | `surface.update`, on the socket | notification | `plugin_id` ignored, derived from the token |
| `slot_resize` | `surface.resized` | notification | |
| `slot_key` | `input.key` | notification | key spelling unchanged |
| `slot_mouse` | `input.mouse` | notification | |
| `key_state` | `input.key_state` | notification | `KeyPhase` values unchanged |
| `command_invoke` | `action.invoke` | request | gains a reply; `command_id` becomes `action` |
| `theme_changed` | `theme.changed` | notification | palette values stay `#rrggbb` strings for every theme; from minor 2.4 an optional `ansi` object beside `palette` maps the roles a theme set as ANSI indices to their index ("ANSI palette roles (minor 2.4)"). Field types are unchanged |
| `workspace_context` | `state.set` on the application's workspace slice; subscribers get the `state.changed` event | request | no dedicated message |
| `toast` | `toast.show` | notification | |
| `close_modal` | `surface.close` with `{"surface":"modal-surface"}` | notification | |
| `slot_declare` | manifest `published_slots` | | |
| `slot_contribute` | `slot.contribute` | request | gains a reply; refusals become `denied` or `invalid_params` |
| `slot_contributions` | `slot.contributions` | notification | |
| `hook_declare`, `hook_subscribe` | manifest `hooks` | | |
| `hook_call` (owner to Husk) | `hook.call` with `{"hook":"a.before-refresh","data":{}}` | request | `request_id` replaced by the envelope `id`; only the hook's owner may send it ([Hooks](hooks.md)) |
| `hook_call` (Husk to subscriber) | `hook.call`, same params, on the subscriber's stdin | request | |
| `hook_result` | reply to the subscriber's `hook.call`; the owner gets one reply to its `hook.call` | reply | see below |
| `event_declare`, `event_subscribe` | manifest `events` | | |
| `event_emit` (envelope key `event`) | `event.emit` with `{"name":"a.selection-changed","data":{}}`; delivered as `event.deliver` with `{"name","source","data"}` | request, then notification | |
| `protocol_error` | error reply when the rejected line had a readable request `id`; `protocol.error` otherwise | reply or notification | |

`hook.call` replies once to the owner, after every subscriber asked has answered, timed out or disconnected:

```json
{"id":12,"result":{"kind":"gate","allowed":true,"vetoes":[],"results":[{"plugin_id":"b"},{"plugin_id":"c","error":{"code":"timeout","message":"no reply in 10s"}}]}}
```

The outcome's fields (`kind`, `data`, `allowed`, `vetoes`, `results`), the subscriber reply shapes per kind and who may send `hook.call` are owned by [Hooks](hooks.md).

### Limits and timeouts owned here

| Limit | Value | Reason |
| :--- | :--- | :--- |
| Line size | 4 MiB | see Framing |
| Method name | 256 bytes | longer than any namespaced id; bounds log lines |
| `protocol.error` rate | 10 per second per channel | stdout debug noise must not flood stdin |

Request deadlines, pending caps (1,024 overall, 64 per plugin today) and the outbound queue (1,024 messages, 5 s write deadline) are owned by [Calls](calls.md) and [Pods](pods.md), not here.

### What the CLI gets

The CLI is a socket client speaking this protocol and nothing else. It connects, sends requests (with the `token` member when `HUSK_PLUGIN_TOKEN` is set) and reads replies. It exposes one raw escape hatch, `husk rpc <method> [params-json]`, which prints the `result` as JSON on stdout or the `error` on stderr with exit status 1, so a shell plugin can call any method without a library. Every other CLI subcommand is a named wrapper over one method.

### Dependency direction

- `pkg/protocol` imports the standard library only. Nothing in it imports Stem, Services, Substrate or Pods.
- Calls, Dispatcher, Event bus, Hook runner, Server, Wrapper, Services and the CLI import `pkg/protocol`; `pkg/protocol` never imports them.
- Calls knows `Message`, `ID`, `Error` and `Code`; it knows no method name. Routers know method names; they do not build ids.
- Plugins written in Go import `pkg/protocol` and nothing else from Husk.

## Level 2: Migration from today

Done in the cutover and retirement: `pkg/huskipc` and `pkg/manifest` are deleted, `pkg/protocol` carries the wire types, reader, writer, queue and version rule, `internal/pods/manifest` replaces `pkg/manifest`, `plugins/reference` speaks 2.0, and `docs/docs/wire-schema.json` (with its guard test) is gone. The rows below stay as the record of where each piece went.

- `pkg/huskipc/protocol.go`: `Envelope` and its per-type pointer fields are replaced by `protocol.Message`. Wire value types and params (`Palette`, `FrameSpec`, `SlotSize`, `ModifierState`, `KeyStatePayload`, `SlotKeyPayload`, `SlotMousePayload`, `SlotUpdatePayload`, `ToastPayload`) move to `pkg/protocol` using v2 surface vocabulary: `SlotSize` becomes `SurfaceSize`, key/mouse/update payloads become `SurfaceKeyParams`, `SurfaceMouseParams` and `SurfaceUpdateParams`, and every host-surface identifier field is renamed from `slot` to `surface`. `KeyStatePayload` becomes `KeyStateParams`, `ToastPayload` becomes `ToastShowParams`; shared value types retain their names. Resize batches become `SurfaceResizedParams` with a `surfaces` array of `SurfaceSize` entries, as defined in Level 1, replacing the old `slot_sizes` map and separate `focused_slot`. Other JSON fields retain their spelling. `HelloPayload`, `CommandSpec`, `RailItemSpec`, `KeybindSpec` leave the wire for the manifest ([Pods](pods.md), [Keybindings](keybindings.md)). `WorkspaceContextPayload` and `RailActivatedPayload` leave the wire. `ConnWriter` becomes `protocol.Writer`.
- `pkg/huskipc/version.go`: parsing and the compatibility rule move unchanged; constants become `"2.0"` and `2` (`Version` becomes `"2.1"` with Hooks, see Versioning). The check runs at manifest conversion and in the CLI, not on the wire.
- `pkg/huskipc/read.go`: the buffering and oversized-line skip move unchanged; `deliver` decodes `Message` and classifies its form; `LineError` gains `Code`, `ID` and `Method`; `Payload()` becomes a builder for `protocol.error` or an error reply.
- `pkg/huskipc/wirevalid.go`: moves unchanged; `Validate()` errors map to `invalid_params`.
- `pkg/huskipc/published.go`: params structs move; `HookCallPayload.RequestID` and `HookResultPayload.RequestID` are removed; `ProtocolErrorPayload` is replaced by `Error` and `protocol.error`.
- `pkg/huskipc/queue.go`: moves into `pkg/protocol` behind `NewQueuedWriter`; limits stay.
- `pkg/protocol/schema` and `cmd/genschema`: regenerate `docs/docs/wire-schema-v2.json` as the envelope schema plus one params and one result schema per method, keyed by method name (`go generate ./pkg/protocol`). The v1 schema `docs/docs/wire-schema.json` was deleted with `pkg/huskipc`; `TestCommittedSchemaIsCurrent` in `pkg/protocol/schema` guards the v2 file.
- `cmd/husk/transport.go` and `cmd/husk/publications.go`: the `switch env.Type` routing becomes a method router; hook `request_id` bookkeeping moves to [Calls](calls.md).
- `pkg/manifest` (deleted; now `internal/pods/manifest`): gained the declarations removed from `hello` (owned by [Pods](pods.md)).
- `plugins/reference`: rewritten to 2.0 in the same change; `pkg/huskipc` was deleted once nothing imported it.
- `docs/docs/architecture.md`: the Protocol, Versioning and Errors sections were rewritten from this spec.
- Major-1 `hello` on a socket connection: the Server tests the first line of each connection with the predicate the Wrapper uses on stdout (`protocol.IsLegacyHello`), answers it with the major-1 line, closes the connection and asks Pods to fail the live pod named by `hello.plugin_id` with `version_mismatch`. This rejects a foreign protocol; it is not a handshake.

### ANSI palette roles (minor 2.4)

**Landed in 2.4.** A theme in the `themes` document may set a role to an ANSI index, "the terminal's colour N" (the built-in `base16` sets every role that way). The wire keeps one grammar for colours: every value in `palette` is a lowercase `#rrggbb`, and an index role carries the index's xterm default there (`0` is `#000000`, `4` is `#000080`, `15` is `#ffffff`; the full table is in [Built-in themes](themes.md), "Role values"). Beside `palette`, the `theme.get` result and the `theme.changed` params carry an optional **`ansi`** object: each key a role name (a JSON tag of `Palette`, never `name`), each value an integer `0`–`15`. It lists exactly the roles that are indices in the serving palette and is **absent** when there are none, so a hex theme's payload is unchanged.

A receiver that knows `ansi` draws each listed role as the terminal's basic colour N and ignores that role's hex; one that does not ignores the field (Versioning rule 2) and draws the xterm defaults, an approximation that is always a valid colour. A plugin must not infer an index from a hex value: `#000080` in `palette` is an index only when `ansi` says so.

```json
{"method":"theme.changed","params":{"palette":{"name":"base16","canvas":"#000000","surface":"#000000","surface_elevated":"#000000","surface_selected":"#808080","surface_hover":"#808080","slot_background":"#000000","border":"#808080","border_subtle":"#808080","border_focused":"#800080","ink":"#ffffff","ink_muted":"#c0c0c0","ink_subtle":"#808080","accent":"#800080","accent_secondary":"#008080","accent_soft":"#000080","success":"#008000","warning":"#808000","danger":"#800000","info":"#000080"},"ansi":{"canvas":0,"surface":0,"surface_elevated":0,"surface_selected":8,"surface_hover":8,"slot_background":0,"border":8,"border_subtle":8,"border_focused":5,"ink":15,"ink_muted":7,"ink_subtle":8,"accent":5,"accent_secondary":6,"accent_soft":4,"success":2,"warning":3,"danger":1,"info":4}}}
```

## Decisions made here

1. The protocol is JSON-RPC shaped but not JSON-RPC 2.0: no `jsonrpc` member (ignored if sent), string error codes instead of integers, no batches. String codes are readable in logs and in shell plugins; integer codes buy nothing without JSON-RPC library interop, which the CLI already replaces.
2. The new envelope is protocol major **2.0**, with no dual stack. A major-1 `hello` on stdout gets one major-1 `protocol_error` on stdin and the plugin is stopped.
3. The version is checked before launch (manifest `protocol` major) and told to the plugin in `HUSK_PROTOCOL_VERSION`; socket callers read it from `session.ping`. No handshake carries it.
4. Ids are strings or integers, scoped per sender and channel. Husk's own are integers from the Calls counter.
5. Error codes are the fifteen in the table; a minor may add codes and older receivers map unknown codes to `internal`. No other spec defines **error** codes; specifics go in `data`. Problem codes (`Problem.Code`) are owned by the spec that raises them.
6. Bad input with a readable request id gets an error reply; everything else gets a `protocol.error` notification. Replies and `protocol.error` are never answered. Unknown notifications are ignored silently, unknown requests get `unknown_method`.
7. `protocol.error` is rate-limited to 10 per second per channel.
8. No handshake on either channel. The pipe is the plugin's identity, the manifest its declaration, the environment its configuration; the socket identifies a caller by the `token` member per request.
9. The plugin's stdout carries replies only. Everything plugin-originated, renders included, goes to the socket over a connection the plugin may keep open.
10. Method names have exactly two segments. Resource ids travel in params: `action.invoke` carries the action, `event.deliver` the event name, `hook.call` the hook.
11. `hook.call` returns one aggregated reply to the owner instead of today's streamed `hook_result` per subscriber; its shape, per hook kind, is owned by [Hooks](hooks.md).
12. No cancel and no end notification in 2.x; EOF on stdin ends a plugin's session. `event.emit` is a request so a CLI caller gets an exit status and a namespace refusal is a `denied` reply.
13. Input, surface and theme deliveries are targeted notifications in their own namespaces, not `event.deliver` deliveries.
14. The Go package is a new `pkg/protocol`; `pkg/huskipc` is deleted after migration rather than kept as a frozen v1.
15. The CLI exposes `husk rpc <method> [params-json]`.
16. Line limit stays 4 MiB.
17. ANSI palette roles travel in a separate optional `ansi` object (minor 2.4), never as index strings inside `palette`. Every `palette` value stays `#rrggbb`, so a receiver that parses colours as hex keeps working and draws the xterm default; the object is absent for hex themes, so their payload does not change.

## Open questions

1. Resolved in the consistency pass: stdout carries replies only (decision 9). An earlier draft let stdout carry plugin requests so a render needs no CLI spawn; the per-request token in [Pods](pods.md) gives a long-lived socket connection the identity that argument said was missing, so a plugin that renders every frame keeps one socket connection open and pays one connect.
2. Are input, surface and theme deliveries a fourth message kind, or events with one implicit subscriber? This spec gives them their own namespaces (decision 13) and leaves the classification to [Message kinds](message-kinds.md). Recommended: events with an implicit subscriber, so the Event bus carries them and coalescing applies to `input.key_state` and `surface.resized`.
3. Aggregating `hook.call` into one reply (decision 11) delays the caller to the slowest subscriber, up to the hook's timeout. Settled in [Hooks](hooks.md): accepted; a publisher bounds the wait with `timeout_ms`, and streaming can come back as a minor addition if a plugin needs it.
4. Resolved (no shim): should Husk keep a major-1 compatibility shim for the external plugin crate that exists today? Recommended: no; update it in step with the change and rely on the one-line major-1 rejection to tell stale builds what happened.
5. Resolved: `pkg/huskipc` was deleted; the only known importers were in this repository.
6. **Terminal surfaces ship in minor 2.5 independently of runtime surfaces.** Constants, homes and payload types land only for served methods; `surface.open` and `terminal.link` remain deferred.
7. **Should `terminal.input` be framed per event** (one notification per key, with the key name beside the bytes) rather than as an opaque byte stream? The stream is what a PTY wants and keeps the host's encoder authoritative; per-event framing would let a plugin see key names it cannot otherwise learn. Recommended: the stream; a plugin that wants key names keeps a line surface and `input.key`.
8. **Reflow of already-printed screen lines and history remains open.** The pinned `x/vt v0.1.0` uses a truncating grid resize and has no soft-wrap/history reflow. A future change may add reflow with explicit wrap metadata; this minor relies on the program redrawing after the PTY's `SIGWINCH` and introduces no replacement emulator or dependency.
