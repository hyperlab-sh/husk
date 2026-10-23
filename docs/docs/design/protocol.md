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
| `conflict` | a compare-and-set precondition failed | State |
| `failed` | the handler ran and reported an application failure | plugin action or hook handlers |
| `internal` | a bug in the receiver, including a reply that is neither result nor error | either side |

`message` is a human-readable sentence for logs and toasts; programs branch on `code` only. `data` is optional and carries the specifics a program may need: `{"limit":4194304}` for `line_too_long` and size failures, `{"reason":"plugin_exited"}` or `{"reason":"shutdown"}` for `cancelled`, `{"reason":"needs_grant"}` for a `denied` secret read, `{"revision":9}` for `conflict`. Every other spec names its errors from this table and says what it puts in `data`; no spec defines codes of its own.

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
| Major-1 `hello` on a plugin's stdout | Wrapper | one major-1 `protocol_error` line (see Versioning) | plugin stopped |

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

What changes: this envelope is a breaking change, so the target major is **2** (`protocol.Major = 2`); `protocol.Version` is `"2.0"` for the envelope and `"2.1"` once [Hooks](hooks.md) lands. Husk speaks one major at a time; there is no dual stack.

Minor 2.1 ([Hooks](hooks.md), [Keybindings](keybindings.md)): `hook.list`; `hook.call` outcome fields `kind`, `data`, `allowed`, `vetoes` and the per-entry `items`, `veto`, `reason`; subscriber params `kind`; the optional `capture` field of `surface.update`. Owner-only firing (the CLI included) tightens 2.0 without a major bump because no shipped plugin fires a hook it does not own and no CLI exists yet.

Where the version is checked, since there is no handshake:

- A plugin states its protocol major in its manifest (`"protocol": 2`). Manifest conversion ([Pods](pods.md)) refuses a plugin whose major differs before it is launched. The Wrapper tells the plugin the host's full version in `HUSK_PROTOCOL_VERSION`.
- A socket caller learns the host's version from the `session.ping` result and from the registration entry ([Instances](instances.md)). The CLI compares majors and exits with its protocol error status when they differ; a plugin calling in does the same check once if it cares.

A major-1 plugin that slipped through (a manifest without a version, read by the v1 converter) is recognized by its first stdout line (`{"type":"hello",...}`). Husk answers it once on stdin in the major-1 shape so the plugin can log a useful reason, then stops it:

```json
{"type":"protocol_error","protocol_error":{"message_type":"hello","message":"unsupported protocol major 1: host speaks 2.0"}}
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
| `session` | `session.ping` | request; result `{"id","pid","protocol_version","host_version","uptime_ms"}` | socket caller to Husk; the CLI uses it for liveness, discovery and the version check |
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
| `input` | `input.key`, `input.mouse`, `input.key_state` | notification | Husk to the plugin owning the target surface |
| `theme` | `theme.changed` | notification | Husk to plugin |
| | `theme.get` | request | plugin or CLI to Husk |
| | `theme.check` | request | CLI to Husk |
| `slot` | `slot.contribute` | request | plugin to Husk |
| | `slot.contributions` | notification | Husk to the publisher |
| `toast` | `toast.show` | notification | plugin or CLI to Husk |
| `settings` | `settings.get` | request | plugin or CLI to Husk |
| | `settings.list`, `settings.schema`, `settings.validate` | request | CLI to Husk |
| `secrets` | `secrets.get`, `secrets.names` | request | plugin or CLI to Husk |
| | `secrets.set`, `secrets.grant`, `secrets.revoke`, `secrets.reveal` | request | CLI to Husk; a plugin caller gets `denied` ([Services](services.md)) |
| `keys` | `keys.list` | request | plugin or CLI to Husk |
| `profile` | `profile.list`, `profile.use` | request | CLI to Husk |
| `plugin` | `plugin.list`, `plugin.log` | request | CLI to Husk |
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
| `theme_changed` | `theme.changed` | notification | palette values stay `#RRGGBB` |
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
- Calls, Dispatcher, Event bus, Hook runner, Server, Wrapper and the CLI import `pkg/protocol`; `pkg/protocol` never imports them.
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

## Decisions made here

1. The protocol is JSON-RPC shaped but not JSON-RPC 2.0: no `jsonrpc` member (ignored if sent), string error codes instead of integers, no batches. String codes are readable in logs and in shell plugins; integer codes buy nothing without JSON-RPC library interop, which the CLI already replaces.
2. The new envelope is protocol major **2.0**, with no dual stack. A major-1 `hello` on stdout gets one major-1 `protocol_error` on stdin and the plugin is stopped.
3. The version is checked before launch (manifest `protocol` major) and told to the plugin in `HUSK_PROTOCOL_VERSION`; socket callers read it from `session.ping`. No handshake carries it.
4. Ids are strings or integers, scoped per sender and channel. Husk's own are integers from the Calls counter.
5. Error codes are the fifteen in the table; a minor may add codes and older receivers map unknown codes to `internal`. No other spec defines codes; specifics go in `data`.
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

## Open questions

1. Resolved in the consistency pass: stdout carries replies only (decision 9). An earlier draft let stdout carry plugin requests so a render needs no CLI spawn; the per-request token in [Pods](pods.md) gives a long-lived socket connection the identity that argument said was missing, so a plugin that renders every frame keeps one socket connection open and pays one connect.
2. Are input, surface and theme deliveries a fourth message kind, or events with one implicit subscriber? This spec gives them their own namespaces (decision 13) and leaves the classification to [Message kinds](message-kinds.md). Recommended: events with an implicit subscriber, so the Event bus carries them and coalescing applies to `input.key_state` and `surface.resized`.
3. Aggregating `hook.call` into one reply (decision 11) delays the caller to the slowest subscriber, up to the hook's timeout. Settled in [Hooks](hooks.md): accepted; a publisher bounds the wait with `timeout_ms`, and streaming can come back as a minor addition if a plugin needs it.
4. Resolved (no shim): should Husk keep a major-1 compatibility shim for the external plugin crate that exists today? Recommended: no; update it in step with the change and rely on the one-line major-1 rejection to tell stale builds what happened.
5. Resolved: `pkg/huskipc` was deleted; the only known importers were in this repository.
