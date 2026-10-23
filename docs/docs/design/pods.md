---
type: Design Spec
title: "Pods: server, wrapper, manifest conversion, decoration"
description: "How an instance hosts plugins: one socket server per instance, one pod per plugin that converts its manifest, runs its process over stdio and frames its surfaces."
status: planned
generated: { by: claude/claude-opus-5-5, at: 2026-10-22T06:00:00Z }
tags: [architecture-v2, pods]
---

# Pods: server, wrapper, manifest conversion, decoration

## Level 0: What and why

The Pods group hosts plugins. It has one shared module and one unit per plugin:

- **Server** (one per instance) owns the instance's Unix socket. It serves two kinds of caller: plugins calling in (through `husk ...` or by writing JSON lines to the socket) and the user's own CLI. It authenticates the peer, attributes each request to a caller, and routes the request to the module that owns its method (Dispatcher, State, Event bus, Services). It registers the socket in the runtime directory so the CLI can find the instance.
- **Pod** (one per plugin) owns everything about one plugin:
  - **Manifest conversion** reads the plugin's manifest in any supported format and produces Husk's plugin spec: action catalog, surfaces, focus-local bindings, event subscriptions, hook registrations, slice declaration and secret requests, all as config.
  - **Wrapper** launches and supervises the process, injects the environment, carries Husk-to-plugin messages on the plugin's stdin and replies back from its stdout, sends stderr to a log file, and writes the plugin's liveness to State.
  - **Decoration** turns a surface's content plus its frame spec into decorated cells. Only its boundary is fixed here.

Problem solved: today one module (`cmd/husk/transport.go`, `cmd/husk/lifecycle.go`) carries both directions over either a socket or stdio, identifies plugins by a `hello` and by walking process ancestry, and keeps a launcher hand-off timer for plugins that reconnect later. The target splits the directions. Husk to plugin goes only down the stdin the Wrapper owns, so the pipe is the identity. Plugin to Husk goes only through the socket, so the Server serves short request/reply traffic and never pushes plugin messages.

What Pods is NOT:

- Not a router of message kinds. Correlation ids, timeouts and reply matching live in [Calls](calls.md); owner lookup and permission live in the [Dispatcher](message-kinds.md); fan-out lives in the Event bus. The Wrapper is a pipe with a supervisor attached.
- Not a store. Liveness goes to [State](state.md); the converted catalog goes to [Store](store.md) as config.
- Not a client. The CLI is a socket client in `cmd/husk`; it has no pod.
- Not a restart manager. v1 has no restart policy.

Dependencies: Pods reads [Protocol](protocol.md) wire shapes, writes [State](state.md) (its own `husk.pods` slice, and each plugin's slice on that plugin's behalf), writes plugin catalog config to [Store](store.md), hands replies to [Calls](calls.md), and asks the [Services](services.md) coordinator to reload whenever its declared documents change. [Instances](instances.md) owns how the CLI picks an instance; the Server owns the files that choice reads. [Keybindings](keybindings.md) consumes the focus-local bindings conversion produces. The Stem's Dispatcher, Event bus and Hook runner reach a plugin only through its Wrapper's `Channel`.

## Level 1: Contract

### Package layout and dependency direction

```
pods/server      socket listener, peer auth, caller attribution, method routing
pods/manifest    Converter interface, registry of converters, Spec type, husk-plugin.json converters
pods/wrapper     process launch, env, stdin queue, stdout reader, stderr log, liveness
pods/decoration  frame + content -> cells (boundary only)
pods             Pod: ties one plugin's spec, wrapper and decoration together; Host: the set of pods
```

Directions, stated once:

- Server imports `protocol`, the `instance` leaf package ([Instances](instances.md)) and its own `Handler` interface. Server never imports Dispatcher, State or Services; the host's composition root (`internal/host.New`) registers their handlers on it.
- Wrapper imports `protocol` and the `ReplySink` interface it is handed. Calls never imports Wrapper; the Pods `Host` implements `calls.Outbox` and the Message kinds `Sink` over the Wrappers' `Channel`s and is handed to Calls and the Event bus at startup.
- Pods writes State; State never imports Pods.
- Pods writes Store (catalog config); Store never imports Pods.
- Manifest conversion imports nothing from Stem, Services or Substrate. It is a pure function from files to `Spec`.
- Decoration imports the frame and theme value types only. The Renderer calls Decoration; Decoration never calls the Renderer.

### Server

```go
package server

// Caller is who sent a request, as the Server attributes it. The kinds are
// the socket subset of the Caller kinds in Message kinds ("cli", "plugin").
type Caller struct {
    Kind     CallerKind // CallerCLI or CallerPlugin
    PluginID string     // set when Kind == CallerPlugin
    ConnID   uint64     // unique per accepted connection, for logs and watch cleanup
}

type CallerKind uint8

const (
    CallerCLI    CallerKind = iota + 1 // no token: the user's CLI, a script, a test harness
    CallerPlugin                       // valid plugin token
)

// Handler serves one method family. It runs on connection workers and
// must not block the Bubble Tea loop; it posts to the loop and waits on ctx.
// ctx is cancelled when the connection closes. Both values are
// protocol.Message: a request in, a reply out.
type Handler interface {
    Serve(ctx context.Context, c Caller, req protocol.Message, notifier Notifier) protocol.Message
}

// Notifier lets a handler push notifications on the caller's connection
// (only used by watch methods, see below).
type Notifier interface {
    Notify(n protocol.Message) error
}

type Config struct {
    InstanceID string        // pid by default, or the --instance name
    RuntimeDir string        // from instance.RuntimeDir(), see "Runtime directory"
    Entry      instance.Entry // what Start registers once the socket listens
    Tokens     TokenResolver // Pods' token table
    Post       func(any) bool // posts a host notice to the loop; false after shutdown
    Clock      protocol.Clock // queue watchdogs, diagnostic windows and uptime; nil is system time
}

type TokenResolver interface {
    // Resolve returns the plugin id that owns token, or ok=false.
    Resolve(token string) (pluginID string, ok bool)
}

type Server interface {
    // Handle routes every method whose name starts with prefix + "." to h.
    // Registration happens before Start; a duplicate prefix panics.
    Handle(prefix string, h Handler)
    Start() error       // bind, register, start accepting
    SocketPath() string
    Close() error       // stop accepting, close connections, unregister; idempotent
}
```

#### Runtime directory and registration

- Base: `instance.RuntimeDir()` ([Instances](instances.md) owns the rule): `$XDG_RUNTIME_DIR/husk/` when `XDG_RUNTIME_DIR` is set — a set but unsafe root is fatal, never a fallback — otherwise `/tmp/husk-<uid>/`, created `0700`, refused unless it is a real directory owned by the current uid with no group or other bits. This replaces today's fresh `mkdtemp` directory: a stable path is required for discovery, so the ownership check takes over the job the random name did.
- Socket: `<base>/<instance-id>.sock`, mode `0600` (chmod after bind, as `listenPrivate` does today).
- Logs: `$XDG_STATE_HOME/husk/logs/<instance-id>/<plugin-id>.log`, one per launch, mode `0600`, opened with `O_EXCL` as today, through `Store.OpenPluginLog` ([Store](store.md)); the runtime directory holds sockets and entries only.
- Instance id: the pid by default, or the `--instance` name, validated by `instance.ValidName` (`[a-z0-9][a-z0-9-]*`, at most 32 bytes, never all digits, so a name never collides with a pid entry).
- Claim: `bind` at `<base>/<instance-id>.sock`. On `EADDRINUSE` the Server dials the socket and sends `session.ping` with a 500 ms deadline. A reply means the name is in use: start fails with `instance "<id>" is already running`. `ECONNREFUSED` starts stale recovery: take an advisory flock on the existing runtime-directory FD, re-probe under that lock, and unlink/rebind once only if it is still refused. A newly live socket is never unlinked. The normal first bind stays lock-free; recovery creates no lock file. `gofrs/flock` opens the directory read-only on Linux/macOS.
- Registration: once listening, the Server writes `<base>/<instance-id>.json` with `instance.Register` (the entry shape is in [Instances](instances.md)). An entry therefore always means ready. A registration failure is fatal: `Start` removes its newly claimed socket, closes the listener and returns the error; it never keeps an undiscoverable listener up.
- On `Close` the Server calls `instance.Unregister`, which removes the entry and the socket. The base directory is never removed.

#### What is served

Framing: one JSON object per line, at most 4 MiB (`MaxLineBytes`, unchanged), request shape from [Protocol](protocol.md). Requests from socket clients carry an optional top-level `token`:

```json
{"id":7,"method":"action.invoke","token":"9f2c...e1","params":{"action":"files.open","args":{"path":"README.md"}}}
{"id":7,"result":{"ok":true}}
{"id":8,"method":"action.invoke","params":{"action":"nope"}}
{"id":8,"error":{"code":"not_found","message":"no action \"nope\""}}
```

Method families and their handlers (registered by `internal/host.New`; names and forms are [Protocol](protocol.md)'s):

| Prefix | Handler owner | Notes |
| :--- | :--- | :--- |
| `session.` | Server itself | `session.ping` returns `{id, pid, protocol_version, host_version, uptime_ms}`. No caller restriction. |
| `action.` | Dispatcher | `action.invoke`, `action.list`. The Dispatcher applies permission using `Caller`. |
| `event.` | Event bus | `event.emit` (a request; a plugin may emit only under its own id, the bare CLI only under `user.`), `event.watch` (streams `event.deliver` notifications on this connection; for the CLI and debugging). |
| `state.` | State | `state.get`, `state.snapshot`, `state.watch` for everyone. `state.set` and `state.patch` are accepted only from a plugin caller and only for that plugin's own slice; the Server forwards them to that plugin's Pod, which writes State as the slice owner. `state.clear` is CLI only. |
| `settings.`, `theme.`, `secrets.`, `keys.`, `profile.`, `config.` | Services | `secrets.get` returns only what the Secrets service granted to `Caller.PluginID`. `config.reload` triggers the reload described in [Services](services.md). |
| `plugin.` | Pods | `plugin.list` (spec summary plus liveness), `plugin.log` (log path for an id, not_found before a log exists). Both are the user's own: CLI-only callers, denied for plugins, Stem and zero-kind callers (D57); both read loop-owned Pod state through `Loop.Do` and answer unavailable when the loop no longer runs. |
| `surface.`, `slot.`, `toast.` | Surfaces | `surface.update` and `surface.close` are notifications a plugin sends over a connection it keeps open; `slot.contribute` and `toast.show` likewise. |

An unknown prefix gets error code `unknown_method`. Watch methods are the only place the Server pushes notifications to a socket connection; a plugin never needs them, because its subscribed events arrive on stdin.

#### plugin.* wire shapes

`plugin.list` takes no params. Its result is a bare JSON array, sorted by plugin id and `[]` when no Pod exists, of:

```json
{"id":"alpha","name":"Alpha","version":"1","description":"","status":"live","pid":4242,"started_at":"2026-01-02T03:04:05Z","last_error":"","log":"/run/user/1000/husk/logs/alpha.log"}
```

`status` is one of `starting`, `live`, `exited`, `failed`. `pid` is 0 and `started_at` the zero time before a process started; `last_error` and `log` are empty strings when unknown.

`plugin.log` takes `{"plugin":"<id>"}` (a missing or empty `plugin` is `invalid_params`) and answers `{"path":"<log file>"}`. An id that names no Pod is `not_found`, and so is a Pod whose Wrapper has not opened a log yet; the result never carries an empty path.

Both methods are the user's own: a CLI caller is served, every other caller kind is `denied`.

#### Caller identity: per-plugin token

The Wrapper generates a token for each launch (32 bytes from `crypto/rand`, hex encoded) and injects it as `HUSK_PLUGIN_TOKEN`. The `husk` CLI reads that variable and puts it in the top-level `token` member of every request it sends; a plugin writing the socket itself does the same. A request with a valid token is `CallerPlugin` for that plugin; a request with no token is `CallerCLI`; a request with an unknown or revoked token is refused with `denied` and never downgraded to the user. The token is registered before process start and revoked immediately when the process is reaped or its channel closes; it never appears in lifecycle snapshots, notices or host logs.

This replaces peer-pid ancestry (`launchedID`, `peerAncestry`, `procparent_*.go`) because:

1. Ancestry works on Linux and macOS only; tokens work wherever the socket works.
2. Ancestry cannot attribute a detached daemon reparented to init; a token travels with whatever the plugin hands it to.
3. Ancestry races pid reuse between the kernel lookup and the walk; a token is checked in a map.
4. The CLI path is a fresh process per call, so the Server would walk ancestry on every request; a token lookup is O(1).

Scope of the guarantee: identity is attribution for permission policy between plugins of the same user. It is not isolation. Any process with the same uid can read another's environment on some platforms, and the peer uid check is the security boundary, exactly as today. A plugin that spawns a user shell (a terminal surface) should drop `HUSK_PLUGIN_TOKEN` and keep `HUSK_SOCKET_PATH`, so commands typed there act as the user.

#### Server invariants

1. Every accepted connection passes the peer uid check (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS) before a byte is read; unreadable credentials or a foreign uid close it. Platforms without a credential API refuse every connection.
2. The Server never calls a handler on the loop goroutine and never writes a socket from the loop. Replies go through the bounded queued writer (`protocol.NewQueuedWriter`, the former v1 queued writer, capacity 1,024, 5 s write timeout).
3. Requests on one connection may be pipelined; replies carry the request id and may arrive out of order. At most 64 request handlers run per connection. The reader never waits on admission: a request arriving while all slots are taken receives `busy` with `data.reason: "too_many_in_flight"` immediately, so peer EOF can still be read.
4. Every request gets exactly one reply (including an admission refusal), unless the connection closes first. Closing the connection cancels the `ctx` of every in-flight handler. Notifications on one connection are handled in order by a separate worker; they do not consume request slots. Its independent 64-message cap includes the active notification. Overflow sends `protocol.error` with `busy` / `too_many_in_flight`, never blocks the reader, and disconnect cancels its context.
5. A malformed or oversized line is answered as [Protocol](protocol.md) rules: an error reply when the request `id` was readable, otherwise a `protocol.error` notification. The connection stays open.
6. The Server knows method prefixes, not method semantics.
7. Error replies go only through the queued writer; the reader never waits on `Post`, including rejected requests, ignored replies and unknown notifications. Socket diagnostics coalesce per connection into at most one loop notice per second carrying a count and the last code (plus suppressed wire-notification count). Only a non-reader worker calls `Post`; at most one diagnostic callback is outstanding across the Server, and offers are dropped while the loop is behind. Closing a connection never waits for that callback. The wire's separate 10-per-second `protocol.error` budget remains unchanged.

### Pod and manifest conversion

```go
package manifest

// Source is one manifest found on disk.
type Source struct {
    Path  string // the manifest file, or "" for an implicit plugin
    Dir   string // absolute directory the plugin lives in
    Bytes []byte // file contents, nil for an implicit plugin
    Command []string // implicit argv; nil when Path names a manifest
}

// Converter adapts one manifest format to Husk's Spec.
type Converter interface {
    Format() string                // "husk-plugin.json/v2", "husk-plugin.json/v1", ...
    Detect(dir string) (Source, bool) // claims a plugin directory
    Convert(src Source) (*Spec, []Warning, error)
}

// Registry tries converters in registration order; the first Detect wins.
type Registry interface {
    Register(c Converter)
    Load(arg string) (*Spec, []Warning, error)  // --plugin argument
    LoadDir(dir string) ([]Result, error)       // --plugin-dir: each immediate subdirectory
}

type Result struct {
    Spec     *Spec
    Warnings []Warning
    Err      error // a bad manifest fails only itself, not the whole directory
}

// Spec is Husk's own plugin description. Everything in it is config.
type Spec struct {
    ID          string
    Name        string
    Version     string
    Description string
    Protocol    int      // wire major the plugin speaks
    Command     []string // resolved argv (ResolvedCommand rules, unchanged)
    WorkDir     string

    Actions         []ActionDecl       // id "<plugin-id>.<name>", title, description, args schema, public ([Message kinds](message-kinds.md))
    Surfaces        []SurfaceDecl      // slot claims and frame specs
    RailItems       []RailItemDecl     // activity-rail items: id "<plugin-id>.<name>", icon, label, badge, description, placement (modal | full-canvas | stage), optional frame; today's rail item minus its shortcut
    FocusBindings   []FocusBinding     // key -> action, active only while one of this plugin's surfaces has focus
    Subscriptions   []string           // event patterns (exact name or "<prefix>.*", [Message kinds](message-kinds.md)) delivered on stdin
    EventsDeclared  []EventDecl        // events this plugin may publish
    HooksDeclared   []HookDecl         // hook points this plugin publishes: id, kind, title, description, timeout_ms ([Hooks](hooks.md))
    HookRegistrations []HookRegistration // hook points this plugin answers, with priority ([Hooks](hooks.md))
    PublishedSlots  []SlotDecl         // unchanged shape from today's manifest
    Slice           *SliceDecl         // scope, persist, retention, size cap request
    SecretRequests  []SecretRequest    // name and purpose; granting is the Secrets service's job
    SettingsDeclared []SettingDecl     // settings schema entries, keys prefixed plugins.<id>. ([Services](services.md))
}
```

Conversion rules:

1. Two converters ship in v1. `husk-plugin.json/v2` reads files with `"manifest_version": 2` and reads `"protocol"` (the wire major, [Protocol](protocol.md)) into `Spec.Protocol`; a v2 manifest with absent or null `protocol` fails conversion. `husk-plugin.json/v1` reads every other `husk-plugin.json` and the implicit forms (`Load` today: bare executable, command line). v1 maps `id`, `name`, `version`, `description`, `command`, `slots` (to `Surfaces`), `published_slots`, `hooks`, `events`; everything v2 adds is empty. v1 `transport` and `language` are ignored with a warning naming the field.
2. A converter is pure: it reads only `Source` and the files under `Source.Dir`, writes nothing, and starts nothing.
3. Plugin ids match `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`. Every declared id (action, event, hook, published slot) is `<plugin-id>.<name>`, validated as today.
4. A plugin whose `Protocol` major differs from the host's fails conversion with `plugin speaks protocol N, host speaks M`. v1 manifests count as the current major only if the plugin passes the launch; they cannot state a version, so the v1 converter sets the host's major.
5. Conversion errors fail that plugin only. Warnings go to the log.
6. Focus-local bindings may target this plugin's own actions or `husk.*` host actions. A binding to another plugin's action is dropped with a warning (`foreign_action`); an unscoped or foreign-surface binding is dropped (`global_not_allowed`). `Spec.FocusBindings` and the declared `keymap` document's `bind` (D64) hold only bindings that pass these conversion checks. The document is `{version: 1, bind: [...]}` with optional `problems: [{index, code, message}]`; Services imports conversion problems under `plugin:<id>`. Conversion keeps at most the first 128 binding entries in document order, dropping the remainder with one `too_many` problem. Key spellings pass through unchanged: only Services parses and reports unparsable keys. Global bindings do not exist in any manifest format.

7. Rail items (`rail_items[]`, v2 manifests only; the v1 converter ignores the field) are declarations, not runtime registrations. Each id is `<plugin-id>.<name>` in the same id space as actions, because a rail item is addressable as an action by its id ([Keybindings](keybindings.md)); a rail id that repeats another rail id or an action id fails conversion. `icon`, `label` and a `placement` of `modal`, `full-canvas` or `stage` are required; `badge`, `description` and `frame` (the border, label and close the host draws around the surface the item opens) are optional. A `shortcut` on a rail item is accepted and dropped from the declaration: it is recorded as a suggestion and warned as `suggestion_ignored`, like every manifest shortcut. The converter validates shape only: it does not check that the claimed surface exists, and it writes no placement. Rail items live in the `plugins` document under `rail_items`, which is where the frontend reads them (see Frontend seams); which item is active, and the resulting `placement` and `overlays` in `husk.surfaces`, are the frontend's State writes ([State](state.md)).

What the Pod does with a `Spec`, in order, on launch:

1. Refuse a duplicate id (`duplicate plugin id`; the first loaded wins).
2. Write `starting` to `husk.pods`.
3. Write the catalog to Store as the plugin's declared documents (actions, surfaces, rail items, focus bindings, declarations, subscriptions, hook registrations, secret requests, settings schema; `Store.Declare`, [Store](store.md)), then ask the Services coordinator to reload with reason `plugin` ([Services](services.md)). This is config, reloadable, and is what the Dispatcher and the Keybinding, Settings and Secrets services read. The reload runs on the coordinator's goroutine, never on the loop; the Host posts its completion to the loop as a message.
4. Register the plugin's slice in State from `Slice` (owner: this Pod).
5. `SecretRequests` reach the Secrets service through that reload. Grants are held by Secrets and served over `secrets.get`; nothing secret goes into the environment.
6. **Launch gate.** Wait for the reload requested in step 3 to complete (every Service reported, applied or not; a reload that failed or timed out counts as completed, and its report says so). Only then start the Wrapper. `live` is written after `cmd.Start` succeeds, so by the time the Dispatcher can invoke anything on this plugin its actions, focus-local keys, settings schema and secret declarations are in Store and in every Service snapshot. The gate costs at most one reload (2 s wall time, [Services](services.md)).

When several plugins launch together (startup, `--plugin-dir`), the Host runs steps 1 to 5 for all of them, requests one reload, and starts every Wrapper after that single reload completes. At startup that reload is the Services first load, which therefore already includes every declared plugin ([Store](store.md), startup order).

On process exit (`exited` or `failed`) the Pod revokes the token and keeps the slice's value; the catalog stays declared, so the plugin's actions resolve `unavailable` rather than `not_found` and its focus-local keys stay listed as `unavailable` ([Message kinds](message-kinds.md), [Keybindings](keybindings.md)). Only when the Pod is torn down (the plugin removed from the instance) does it call `Store.Undeclare`, ask the coordinator to reload with reason `plugin`, and unregister the slice according to its scope and retention ([State](state.md) decides).

#### Host and loop boundary

`pods.Host` is the concrete set of Pods, not the loop's interface. It implements `calls.Outbox`; each `Pod.Notify(method, params)` is the targeted notification Sink. `Host.Notify` broadcasts to live Pods in stable id order. The host imports no State, Store, Services or Instances implementation in this process-side slice.

One `pods.Loop` interface replaces independent opening, preparation, liveness and log callbacks:

```go
// Post is worker-safe, may apply inbound backpressure, and must return during
// shutdown. Opening, Liveness and Log run only on the loop and never block.
// Prepare runs in a tea.Cmd worker and marshals any State writes back to the
// owning loop; it may wait for Store declarations and the Services reload.
// Do runs fn on the loop and returns once it has run (an error, without
// running fn, when ctx ends first or the loop has shut down): the ports read
// loop-owned Pod state from off-loop callers through it, answering
// unavailable rather than racing the loop (D57).
type Loop interface {
    Post(tea.Msg)
    Opening(pluginID string) []protocol.Message
    Prepare(context.Context, []manifest.Spec) Preparation
    Liveness(LivenessMsg)
    Log(LogMsg)
    Do(context.Context, func()) error
}

type Preparation struct {
    Failed map[string]error // fatal declaration/slice failure for this plugin
    ReloadError error      // completed reload report; does not prevent launch
}

type RegisterSlicesMsg struct {
    Register map[string]func() (SliceHandle, error) // loop-only State writes
    Done     chan Registered // the worker's acknowledgment
}

type Registered struct {
    Handles map[string]SliceHandle // applied owner handles, kept until teardown
    Failed  map[string]error       // slice registration failures, per plugin
}
```

`Host.Add` rejects duplicates and reports `starting` through `Loop.Liveness`, without starting anything. After adding a batch, `Host.Prepare` returns one command. That worker declares the documents, then posts `RegisterSlicesMsg`: the loop applies each slice registration (step 4, a State write; the owner handle is stored on the Host) and acknowledges it, and only then does the worker request the reload — the first reload observes every registered slice ([Store](store.md) startup order). Its `PreparedMsg` is consumed by `Host.Handle` on the loop: failed plugins (declaration or registration failures, reported once each) stay failed, a reload report error is logged but opens the gate, and surviving wrappers start with `Loop.Opening` queued before `live`. Late or duplicate gate completions never relaunch a Pod; shutdown prevents a pending gate from launching.

The stored owner handles serve two later duties. The Stem State port's write path resolves a plugin's `<plugin_id>.state` handle through the Host's slice-handle lookup (a plugin writes only its own slice, [State](state.md)); that lookup runs off the loop on a Server handler goroutine, while registration and teardown mutate the map on the loop, so this one map is guarded by a mutex that covers only the map access — the handle's own writes still run on the loop through the port's apply. `TeardownMsg` — the loop half of Pod teardown — revokes the credential, unregisters the slice through that handle ([State](state.md) decides scope and retention), releases the registry id, and drops the id from any pending launch gate; a slice registration is accepted only while the gate that owns it still holds the id — a stale registration from a torn-down launch of the same id, or one arriving during shutdown, is refused rather than applied (the closure is a State write that would register a slice nobody owns, or steal one the new launch owns). Store undeclaration and the follow-up reload stay with the composition root.

`Loop.Liveness` is the explicit seam for writing `husk.pods` and publishing exit events. Workers post typed notices; `Host.Handle` applies them through `Loop.Liveness` and `Loop.Log` on the loop. `ClosedMsg` is handled by the caller with Calls cleanup before a later `ExitMsg` changes liveness. Message types are exported from `pods` (aliases of leaf Wrapper notices avoid an import cycle). Tokens are held in a worker-safe local registry, not State: they are registered before `Start` so even an immediate exit can revoke them. A `ReapedMsg` revokes authentication immediately after Wait; `ChannelClosedMsg` does the same on EOF or a closed stdin pipe, without cancelling Calls. Final stdout replies drain before normal `ClosedMsg` and `ExitMsg`, so a successful reply is not overtaken by process cleanup.

`Host.Stop` marks shutdown on the loop and returns a worker command. The caller closes Calls and the Server first; that command closes stdin queues, cancels the shared process context, signals groups, and uses one absolute stop deadline plus one absolute kill-wait deadline across the whole set. Providers must keep `Post` callable, or make it return without delivery, after the UI stops; a blocked posting callback must not prevent process cleanup.

`pods/manifest.Spec` contains process fields and every plain declaration type above. `Spec.Clone` detaches all mutable declaration data. `CatalogJSON` produces the normalized `plugins` document (identity/display and declaration fields, no protocol/argv/work directory); `FocusKeymapJSON` produces the declared `keymap` document (D64). These methods are the serialization seam for `Loop.Prepare`; this package does not implement Store/Services/State preparation itself. Manifest setting keys are single-segment `[a-z0-9_-]+` local names; dotted and already-qualified keys fail conversion. They normalize once to `plugins.<exact-plugin-id>.<local-key>` without case-folding plugin ids. This prevents a local key from overlapping another plugin's dotted-id namespace; Services separately restricts access to declared keys, never an arbitrary prefix subtree. Explicit relative executable paths resolve against the absolute manifest directory; later relative arguments resolve through `cmd.Dir`, and bare executable names retain PATH lookup.

### Wrapper

```go
package wrapper

// Channel is how Stem reaches one plugin. Calls, Event bus and Hook runner hold Channels.
type Channel interface {
    PluginID() string
    // Send queues one request or notification for the plugin's stdin. It never blocks.
    // It returns ErrNotLive before start and after exit, and the queue's terminal
    // error after a stdin failure.
    Send(m protocol.Message) error
}

// ReplySink receives what comes back. The Loop implements it: Reply posts a
// calls.ReplyMsg and Closed posts the exit message that makes the Loop call
// calls.DropPlugin; the Wrapper never touches the Calls table.
type ReplySink interface {
    Reply(pluginID string, r protocol.Message) // a parsed reply, or a salvaged bare-id malformed reply
    Closed(pluginID string, reason error)      // the stdin channel is gone: fail every pending call
}

// Liveness values are the status strings of the husk.pods slice in State.
type Liveness string

const (
    Starting Liveness = "starting"
    Live     Liveness = "live"
    Exited   Liveness = "exited"
    Failed   Liveness = "failed"
)
```

The Pods `Host` implements `calls.Outbox` (`Send(plugin, msg)`, keyed by plugin id) and each Pod implements the Message kinds `Sink`, both over `Channel.Send`, so Calls and the Event bus reach a plugin without importing the Wrapper.

Launch:

- `exec.CommandContext(pluginCtx, argv[0], argv[1:]...)` with `cmd.Cancel` sending SIGTERM and `cmd.WaitDelay` = stop grace, as today. `cmd.Dir` = `Spec.WorkDir`.
- `SysProcAttr.Setpgid = true`; Cancel and kill signal the process group, so a plugin started through a wrapper script stops with its children and no grandchild keeps a pipe open.
- stdin and stdout are pipes; stderr is the log file handed over as an `*os.File` (no copy goroutine), as today.
- After `cmd.Start`, the Pod queues the opening notifications [Protocol](protocol.md) lists, in order and before any request: `theme.changed`, one `surface.resized` carrying every surface the plugin occupies (a single snapshot, never one message per surface), `input.key_state`. Pods cannot build them alone: the modifier state is Input bookkeeping that is not in State ([State](state.md)). So `pods.Loop.Opening(pluginID string) []protocol.Message` builds them from Theme, the `husk.geometry` rects and Input's modifier state; the Pod calls it on the loop, starts the process and queues the lines before reporting `live`. Later `theme.changed` pushes follow a Theme generation change ([Services](services.md)); the Pods host sends them through each `Channel`.

Environment, built from Husk's own environment with every inherited `HUSK_*` variable removed first:

| Variable | Value | Why |
| :--- | :--- | :--- |
| `HUSK_SOCKET_PATH` | this instance's socket | the CLI's env step in instance resolution ([Instances](instances.md)) |
| `HUSK_INSTANCE` | instance id | the CLI's fallback env step; logs |
| `HUSK_BIN_PATH` | absolute path of the running `husk` binary | a plugin calls the CLI of the same build without a PATH lookup |
| `HUSK_PLUGIN_ID` | the spec id | the plugin knows its namespace without parsing its manifest |
| `HUSK_PLUGIN_TOKEN` | per-launch token | caller identity on the socket |
| `HUSK_PROTOCOL_VERSION` | `"MAJOR.MINOR"` | replaces the version exchange `hello`/`welcome` did |

The first three are defined by [Instances](instances.md); the Wrapper sets all six and removes every other inherited `HUSK_*` variable.

Stdio channel:

```json
{"id":41,"method":"action.invoke","params":{"action":"files.open","args":{"path":"README.md"},"caller":{"kind":"input"},"focus":{"surface":"sidebar-list","owner":"files"}}}
{"method":"event.deliver","params":{"name":"state.changed","source":"husk","data":{"slice":"husk.focus","owner":"husk","revision":42}}}
{"id":41,"result":{"ok":true}}
```

The first two lines go down stdin; the third comes back on stdout. Method names and payloads belong to [Protocol](protocol.md) and [Message kinds](message-kinds.md); the Wrapper does not inspect them.

Liveness slice `husk.pods`, owner Pods, scope `instance`, not persisted; the shape is fixed in [State](state.md):

```json
{
  "plugins": {
    "files": {"status": "live", "pid": 48211, "started_at": "2026-10-22T06:01:02Z", "last_error": null, "log": "/home/u/.local/state/husk/logs/4242/files.log"},
    "git":   {"status": "failed", "pid": 48212, "started_at": "2026-10-22T06:01:02Z", "last_error": "exit status 2", "stderr_tail": ["panic: nil map"], "log": "..."}
  }
}
```

Transitions:

| From | To | Trigger |
| :--- | :--- | :--- |
| (none) | `starting` | Pod begins launch (launch step 2), before the launch gate and `cmd.Start` |
| `starting` | `live` | `cmd.Start` succeeded, after the launch gate (step 6) and the opening notifications are queued |
| `starting` | `failed` | `cmd.Start` failed (`last_error`: the start error) |
| `live` | `exited` | status 0, or killed by Husk during shutdown |
| `live` | `failed` | non-zero status, a signal Husk did not send, or Husk killed it after a stdin failure (`last_error`: `stopped reading: ...`) |

`exited` and `failed` are terminal for that launch. There is no restart in v1: a failed plugin stays failed until the instance restarts. The Pod also publishes `pods.plugin-exited`, whose data is the Pod's `husk.pods` entry plus `plugin_id`, through the publisher interface the loop injects at construction (the same way State and Services publish; Pods import nothing from Stem), for other watchers; the frontend toasts the `husk.pods` transition into `failed`, not this event.

#### Wrapper invariants

1. The Wrapper is the only writer of a plugin's stdin and the only reader of its stdout.
2. `Send` never blocks the caller. It marshals on the caller's goroutine and enqueues through `protocol.NewQueuedWriter` (capacity 1,024, 5 s write timeout, watchdog close on pipes). A private stdin mux serializes all physical writes; the single legacy rejection is a graceful-close trailer after queued v2 lines, not a second public writer.
3. Stdout carries replies only. A stdout line that is a reply goes to `ReplySink.Reply`. For a malformed reply, the Wrapper salvages the protocol reader's `LineError.ID` and forwards a bare-id message through `ReplySink.Reply`, so the Loop posts `ReplyMsg` and Calls fails a matching call with `internal` / `data.reason: "invalid_reply"` instead of waiting for its timeout. Any other valid line is logged as `plugin <id> sent a non-reply on stdout` and answered with a `protocol.error` notification on stdin; the plugin stays up. The one exception is a major-1 `hello`, which gets one major-1 `protocol_error` line and stops the plugin ([Protocol](protocol.md)).
4. A stdin failure (queue overflow, write timeout, write error) is fatal for the launch: `ReplySink.Closed` is called, the process group gets SIGTERM, then SIGKILL after the stop grace, and liveness becomes `failed`. Pipe-closed errors (`EPIPE`, closed pipe/file) can race a clean exit, so they share the injected EOF-reap grace (100 ms by default). Credentials are revoked immediately on EOF/EPIPE; once-only `ReplySink.Closed` fires after Wait and the final replies, or at reap-grace expiry, carrying the final reason: `exited` on a clean exit, `crashed` with the exit status otherwise, or `stopped reading: ...` if the child is still alive. Pending calls therefore fail at most 100 ms later. Overflow and write-deadline failures keep immediate `Closed`. A plugin that cannot read its stdin cannot be reached any other way, so keeping it alive only hides the fault.
5. `ReplySink.Closed` is called exactly once per launch, before liveness leaves `live`, so the Dispatcher never sees a `live` owner whose pending calls are already orphaned. Post is only ever called from a wrapper goroutine, never from a method invoked on the loop. In particular, launch and opening-send failures must not synchronously call Post or ReplySink from `Start`.
6. Stdout EOF without process exit within the reap grace is treated as a stdin failure (rule 4): the reply channel is gone. `EOFReapGrace` defaults to 100 ms and uses the injected clock, allowing an already-exiting child to be reaped without misclassifying its clean exit; it is the same option used for pipe-closed writes.
7. Background goroutines never write the log or State directly; they post to the loop, which writes both.

#### Shutdown

On instance exit: the Loop closes the Calls table first, so socket callers waiting on an action get `cancelled` replies ([Calls](calls.md)); then the Server stops accepting and unregisters the instance (entry, then socket); every Wrapper closes its stdin queue (flush, then close, so the plugin sees EOF) and the shared plugin context is cancelled, which sends SIGTERM to every process group; one 2 s deadline covers all plugins; survivors get SIGKILL; Husk waits up to 1 s more for those. Exits during shutdown are `exited`, never reported as crashes. Non-empty logs are kept and their paths logged, as today; empty ones are removed by Store's GC ([Store](store.md)), since only Store deletes files.

Process supervision remains attached to the group after its leader is reaped: children without inherited stdio still receive SIGTERM and, if necessary, SIGKILL after the grace. The group is polled every 50 ms on the injected clock; an empty group completes promptly and cancels escalation without a false survivor warning. Observing the group empty fences all later signals immediately, independently of reader/final-notice completion, avoiding a later signal to a reused pid or process-group id. Every SIGKILL outcome, including a signalling error, terminates the group wait; errors are logged. SIGKILL does not close stdout immediately: buffered replies drain until EOF or the injected kill-wait deadline. The launch's completion stays pending through that cleanup, including during the Host's shared shutdown deadline. Native group signalling is supported on Linux and macOS; unsupported platforms refuse launch rather than falling back to killing only the leader.

### Decoration (boundary only)

Decoration takes one surface's content (the cells or lines the plugin supplied through Surfaces), the surface's frame spec (border, label, close, from the plugin's surface declaration), the theme styles for chrome, and the outer rect. It returns decorated cells for the outer rect and the inner rect the content occupies, which Surfaces reports to the plugin as its size. It is a pure function of those inputs: no State reads, no I/O, no plugin calls. The Renderer calls it per surface per frame. Its exact types, whether it lives per Pod or as one shared function, and whether plugins can supply their own decorations are undecided; today's `drawChrome` in `cmd/husk/render.go` is the behaviour it starts from.

### Failure modes

| Condition | Who detects | What happens | What the user or plugin sees |
| :--- | :--- | :--- | :--- |
| Base dir is a symlink, foreign-owned or group/world accessible | Server at start | Instance refuses to start | CLI error naming the path and the problem |
| Instance name already live | Server at start (ping reply) | Instance refuses to start | `instance "<id>" is already running` |
| Stale socket for this id | Server at start (ping timeout) | Socket removed, bind proceeds | Nothing |
| Socket path longer than `sun_path` (104 bytes on macOS, 108 on Linux) | Server at start | Instance refuses to start | Error naming the path and suggesting a shorter `--instance` or `XDG_RUNTIME_DIR` |
| Accept fails while running | Server accept loop | Listener stops; plugins already running keep their stdio | Log and warning toast: `accept failed: ...; plugins and the CLI can no longer call in` |
| Peer uid mismatch or unreadable | Server per connection | Connection closed before reading | Log and toast `rejected IPC connection: ...` |
| Unknown or revoked token | Server per request | Error reply `denied` | The plugin's CLI call fails with that error |
| Unknown method prefix | Server | Error reply `unknown_method` | CLI prints it |
| `state.set` on another owner's slice | Server | Error reply `denied` | CLI prints it |
| Socket client stops reading replies | Reply queue | Connection closed | Client sees EOF |
| Manifest invalid | Converter | That plugin is not launched; others are | Log and a warning toast naming the manifest and the problem. No Pod exists, so there is no `husk.pods` entry: the host lists the refusal (`Frontend.Refusals`) and the frontend toasts it once |
| Duplicate plugin id | Host (plugin loading) | Second plugin not launched | Log and a warning toast `<id>: duplicate plugin id`, listed like an invalid manifest |
| Protocol major mismatch | Converter | Not launched | Log and a warning toast naming the manifest and both versions, listed like an invalid manifest |
| `cmd.Start` fails | Wrapper | Liveness `failed`; the catalog stays declared | Warning toast `<id>: <last_error>` (the frontend toasts on the transition into `failed`) |
| Plugin exits non-zero or by a foreign signal | Wrapper | Pending calls failed via `Closed`, liveness `failed`, token revoked; the catalog stays declared | Warning toast `<id>: <last_error>`; log has the last 3 stderr lines |
| Plugin exits 0 | Wrapper | Same cleanup, liveness `exited` | Info-level log line, no toast |
| Plugin stops reading stdin | Stdin queue | Rule 4: immediate once-only `Closed` for overflow/deadline, kill, `failed` with `last_error` prefixed `stopped reading: ` | Warning toast `stopped reading: send queue full (1024 messages)` or `stopped reading: write timed out after 5s` |
| EOF/EPIPE or closed pipe/file | Wrapper | Credentials revoked immediately; once-only `Closed` after Wait and final replies, or at reap-grace expiry; pending calls fail at most 100 ms later | Final reason `exited`, `crashed` with status, or `stopped reading: ...` while the child survives |
| Plugin writes a non-reply or malformed line on stdout | Wrapper reader | Line dropped, `protocol.error` notification sent | Plugin sees the notification; log line |
| Reply with an unknown or expired id | Calls | Dropped | Debug log only |
| Plugin ignores SIGTERM | Shutdown | SIGKILL after 2 s | Log `did not exit within 2s of SIGTERM; killing it` |

Failures that arrive while a failure toast is showing join it as one "N plugins failed" toast; the log has each one.

### Limits

| Limit | Value | Reason |
| :--- | :--- | :--- |
| Line size, socket and stdio | 4 MiB | unchanged `MaxLineBytes`; bounds one allocation |
| Stdin queue | 1,024 messages | unchanged; a slow plugin falls this far behind before it is killed |
| Stdin and reply write timeout | 5 s | unchanged; a stalled reader is detected without freezing the loop |
| Reply queue per socket connection | 1,024 messages | same writer, same reasoning |
| In-flight requests per socket connection | 64 | Reader never blocks on admission; the 65th receives `busy` / `too_many_in_flight`, leaving EOF detection able to cancel all handlers |
| Pending notifications per socket connection | 64, including the active notification | One ordered worker, reader never blocks; excess gets `protocol.error` / `busy` / `too_many_in_flight` |
| Concurrent socket connections | 256 | above any real count of plugins plus CLI calls; past it, accept closes the newest |
| Stale-instance ping | 500 ms | a live instance answers `session.ping` without touching the loop |
| Stop grace (SIGTERM to SIGKILL) | 2 s | unchanged |
| Kill wait after SIGKILL | 1 s | unchanged; only matters for unreapable processes |
| EOF / pipe-close reap grace | 100 ms | avoids racing a clean process exit against stdout or stdin pipe closure; one injected option |
| Stderr tail on crash | 3 lines from the last 8 KiB | unchanged |
| Log file name attempts | 100 | unchanged |
| Token | 32 random bytes, hex | unguessable; fits any env |
| Plugin id | 64 characters | keeps log names readable |
| Instance name | 32 bytes ([Instances](instances.md)) | keeps socket paths under `sun_path` |

No per-request timeout lives in the Server. A request waits as long as its handler allows; the Dispatcher's timeout comes from [Calls](calls.md).

### What the CLI gets

- The socket and registration entry (`<base>/<id>.sock`, `<base>/<id>.json`) that [Instances](instances.md) resolves.
- `session.ping`, `plugin.list`, `plugin.log`.
- Attribution: the CLI forwards `HUSK_PLUGIN_TOKEN` when present, so the same `husk` command is a user call from a shell and a plugin call from inside a plugin.

## Level 2: Migration from today

Host contracts. `internal/host/contracts` holds the consumer-defined Go interfaces the host's adapters and composition root code against: the adapter `Handler`, `Caller` and `Notifier` (same shapes as the Server's, converted by a one-line adapter), one owner port per method family (`StatePort`, `CallsPort`, `SessionPort`, `SettingsPort`, `SecretsPort`, `ProfilePort`, `ThemePort`, `KeybindingPort`, `ConfigPort`, `EventPort`, `HookPort`, `PluginPort`, `SurfacePort`; `MethodHomes` maps every wire method to its one port), the launch seams for the steps above (`PodRegistry`, `CatalogDeclarer`, `Reloader`, `SliceRegistrar`, `WrapperStarter`), the `Limiter` for the 20 calls per second per plugin rule, the `Config` and `Host` of the composition root, and `Frontend` with its value types (see Frontend seams). Ports return errors that carry a wire code (`CodedError`); adapters map by code and never read error text. The package imports neither Services nor the Server.

Composition root. The v2 host boots from `internal/host.New(contracts.Config)`, not from `cmd/husk`. `New` builds, in order, the plugin specs (`--plugin` / `--plugin-dir` through the converter registry), Store, State, Services, the Stem modules (Calls, Dispatcher, Hook runner, Event bus, Surfaces), Pods and the Server, registers every adapter from `internal/host/handlers` on one Mux and the Mux on the Server for each inbound prefix of `MethodHomes` except `session` (the Server answers `session.ping` from its registration entry), and wires the launch seams. It listens and starts nothing until `Run`, which starts the Server and adds the configured plugins as one launch batch. The caller owns the Bubble Tea loop: it posts what `Config.Post` receives back into the host through `Update` on the loop goroutine. `Close` is idempotent and follows [Shutdown](#shutdown): the Calls table closes, the Server stops and unregisters, plugins stop, then the final flush and Store close (the Store stays open if host workers are still running after a bounded wait, so no worker sees it closed). `Config.Post` may block for backpressure, but once the loop owner has closed its post gate it returns false without blocking; the owner closes that gate before calling `Close` from `Update`, and `cmd/husk` honours that precondition. A failure in any stage closes what was already built. The root also owns the content of the Pod opening (`theme.changed`, `surface.resized`, `input.key_state`), registers each plugin's occupants and contributions declaration slices from its manifest before the first reload and drops them with the Pod, and calls the Surfaces engine's `CatalogChanged()` whenever the published-slot catalog changes. `cmd/husk` runs on this root.

Frontend seams. A frontend (the TUI in `cmd/husk`, later any embedding application) drives the host through `Host.Frontend()`, a `contracts.Frontend` the root implements; it depends on that contract and on `pkg/protocol`, never on Stem, Services, Pods or Store. Each method documents whether it is loop-only or callable from any goroutine. In summary:

- **Content.** `Config.Content` is a `ContentSink`, `func(plugin string, params protocol.SurfaceUpdateParams)`. The Surface port calls it on the loop, after `surfaces.Engine.SurfaceUpdate` accepts and only then, with the authenticated plugin id and the params unchanged (title, subtitle, badge, lines, hit targets, status meta). A denied, unoccupied or invalid update never reaches it. Surface buffers are not State; the sink is the only route they take to the renderer. A nil sink drops content.
- **Reads.** `Snapshot` (State, any goroutine), `Palette` (the Theme service), `Resolve`, `Hints` and `KeysFor` (the Keybinding service, pure and lock-free, any goroutine). The Keybinding results are carried by the small `KeyContext`, `KeyResolution` and `KeyHint` types in `contracts`, reusing `KeyEntry` for the binding, because `contracts` may not import Services; protocol values are used as they are.
- **Actions.** `InvokeAction` runs an action through the Dispatcher with the Stem as caller (a `contracts.CallerStem`, the Dispatcher's host caller: a key binding or palette choice acting for the user) and answers on the loop exactly once, possibly before it returns, with the result or a `CodedError`. `RegisterHostAction` binds the in-process handler of a `husk.*` action or of a declared rail item id on the Dispatcher and lists it in the action catalog under the host, by id only; plugins cannot invoke host actions. It returns an error: any other id is `invalid_params`, and an id a declared plugin owns as an action is `conflict`, because the Dispatcher consults host actions before plugin documents and accepting it would shadow the plugin. A rail item action stays listed while its plugin is dead. Both are loop-only.
- **Delivery.** `SendKey`, `SendMouse` and `SendKeyState` send `input.key`, `input.mouse` and `input.key_state` to one plugin's Pod. An unknown plugin is `not_found`, a plugin that is not live is `unavailable`, params that fail validation are `invalid_params`. Loop-only, because the Pod map is.
- **Catalog.** `RailItems` lists every declared plugin's rail items from the declared `plugins` documents, sorted by plugin id and then in manifest order; it is any-goroutine and changes when a launch gate or a teardown re-declares a plugin.
- **Declared catalog and toasts.** `RailItems`, `PublishedSlots` (kind, and the rect of a region) and `SurfaceFrames` (each plugin's `surfaces[].frame`) and `Actions` (each declared action's plugin, id, title and description, which the palette lists for live plugins) read the declared `plugins` documents, decoded once per Store declared-documents revision (`Store.DeclaredRevision`); `ActiveToast` returns the oldest live entry of the Surfaces engine's toast queue and expires stale ones on the host clock (loop-only). `Planned()` on the root reports how many plugins the arguments resolved to, so a headless run knows how many Pods to wait for. A frontend that writes State without posting a host message calls `Tick()` on the root (`host.Looped`) at the end of its Update, which runs the same flushes as a host message.
- **Frontend-owned State.** `OwnSlices` registers the owner handles of `husk.layout`, `husk.geometry` and `husk.focus` once, before `Run` or later on the loop (the persistent `husk.layout` hydrates from Store's preloaded values either way), and `UpdateSurfaceView` writes the visibility, placement and overlays of `husk.surfaces`. Who owns what, and how a geometry write reaches the occupants, is in [State](state.md), "Frontend-owned slices".

| File | Fate |
| :--- | :--- |
| `cmd/husk/transport.go` | Accept loop moves to `pods/server`. `serveStreamOrigin`, `streamOrigin`, the `hello` gate and `ConnectedPlugin` registration are removed: there is no hello and no plugin stream on the socket. `post` and `hostNoticeMsg` stay in `cmd/husk` as the loop's posting function the Server is configured with. |
| `cmd/husk/socket_security.go` | `listenPrivate`, `checkPeerUID`, `authorizePeer` move to `pods/server`. `createSocketDir` (random `husk-*` dir) is replaced by `instance.RuntimeDir` with the ownership check. `socketName` goes; the file is `<instance-id>.sock`. |
| `cmd/husk/peercred_*.go` | `peerUID` moves to `pods/server`. `peerPID` is removed. |
| `cmd/husk/procparent_*.go`, `peerAncestry` and `launchedID` in `cmd/husk/publications.go`, `cmd/husk/peer_ancestry_test.go`, `cmd/husk/procparent_linux_test.go` | Removed; the token replaces ancestry. |
| `cmd/husk/lifecycle.go` | `spawnPlugin`, `tailLines`, `exitStatus`, `stopPlugins` move to `pods/wrapper`; `openPluginLog` and `sanitizeLogName` move to Store as `OpenPluginLog` ([Store](store.md)). The transport branch is removed (always stdio). `pluginEnv` is rewritten for the new variables. `pluginHandoffGrace`, `handoffExpiredMsg`, `handoffPending` and `pluginExitedMsg.handedOff` are removed. `handleLifecycleMsg` toasts become a liveness write plus a `pods.plugin-exited` event. |
| `cmd/husk/registry.go` (`declareFromManifest`) | Replaced by the Pod writing the `Spec` catalog to Store. Liveness leaves the registry for the `pods` State slice. |
| `cmd/husk/main.go` | `--plugin` and `--plugin-dir` stay and go through the converter registry. `--instance` and `--socket` are added (resolution rules in [Instances](instances.md)). |
| `pkg/manifest/manifest.go` | **Done (v2-cutover-retire):** deleted; it became the `husk-plugin.json/v1` converter in `pods/manifest`. `Loader.Load`, `Loader.LoadDir`, `splitCommand`, the implicit-plugin forms and command resolution (`Spec.Argv`, `Spec.WorkDir`) are kept. `CheckHello`, `TransportSocket`, `TransportStdio` and the transport validation are removed. |
| `pkg/huskipc/queue.go` | **Done (v2-cutover-retire):** moved to `pkg/protocol/queue.go`. Kept and reused for stdin and socket replies. `Send` takes a `protocol.Message` or a pre-marshalled line; the queue mechanics did not change. |
| `pkg/huskipc/read.go` | **Done (v2-cutover-retire):** moved to `pkg/protocol/read.go`. `protocol.ReadLoop` is reused for plugin stdout and socket connections. |
| `cmd/husk/render.go` (`drawChrome`) | Starting point for Decoration; moves when its shape is decided. |
| `cmd/husk/lifecycle_test.go`, `cmd/husk/socket_security_test.go`, `cmd/husk/send_queue_test.go` | Move with their code; hand-off and ancestry cases are deleted; token, base-dir ownership and stdin-failure kill cases are added. |
| `plugins/reference`, `plugins/reference-actions` | Rewritten to read stdin and call the CLI; their manifests gain `"manifest_version": 2`. |

Removed behaviour, stated for the changelog: the socket transport for plugins (plugins no longer connect to receive messages, only to send), the `hello`/`welcome` exchange, the launcher hand-off grace, and pid-ancestry binding.

## Decisions made here

1. Caller identity is a per-launch token injected as `HUSK_PLUGIN_TOKEN` and sent by the CLI on every request; pid ancestry is removed. Reasons: portable, survives daemonizing, no pid-reuse race, O(1) per CLI call. Identity is attribution for policy, not isolation; the uid check remains the security boundary.
2. A request without a token is a user call; a request with a bad token is refused, never downgraded.
3. The runtime directory is stable (`$XDG_RUNTIME_DIR/husk/` or `/tmp/husk-<uid>/`) with an ownership and mode check, replacing the random `mkdtemp` directory, because discovery needs a known place. The rule and the registration entry live in the `instance` package ([Instances](instances.md)); the Server calls it.
4. Instance names follow Instances: lowercase, at most 32 bytes, never all digits, so they never collide with pid ids.
5. The Server routes by method prefix to handlers registered by the composition root (`internal/host.New`); it does not import Stem, State or Services.
6. `state.set` from a plugin goes through that plugin's Pod, which writes as the slice owner; plugins never write State directly.
7. Watch methods (`event.watch`, `state.watch`) are the only socket pushes and exist for the CLI; plugins get events on stdin.
8. Stdout carries replies only. Plugin-originated requests go through the socket.
9. A stdin failure kills the plugin and marks it `failed`. Stdout EOF is treated the same way.
10. `live` means `cmd.Start` succeeded. There is no readiness handshake; messages sent early wait in the stdin queue. The process is started only after the launch gate (the plugin's declared documents are in Store and the Services reload that read them has completed), so `live` also means the plugin's actions, keys, settings schema and secret declarations exist in every consumer. The catalog stays declared across process exit; only Pod teardown undeclares it.
11. Plugins run in their own process group, and shutdown signals the group.
12. Shutdown closes stdin (EOF) together with SIGTERM.
13. Secrets are never injected into the environment; a plugin fetches granted secrets over `secrets.get` with its token.
14. The protocol version reaches the plugin as `HUSK_PROTOCOL_VERSION` and is checked against the manifest's declared major at conversion; this replaces `hello.protocol_version`.
15. `husk-plugin.json` gains `"manifest_version": 2`; files without it go through the v1 converter, which ignores `transport` and `language` with a warning.
16. Exit with status 0 produces no toast; non-zero or a foreign signal produces a warning toast (the frontend toasts the `husk.pods` transition into `failed`; `pods.plugin-exited` carries the same entry for other watchers).
17. Logs live at `$XDG_STATE_HOME/husk/logs/<instance-id>/<plugin-id>.log`, opened through Store, so the runtime directory holds only sockets and entries.
18. `HUSK_BIN_PATH` is injected so a plugin calls the same build's CLI; the environment variable names for socket, instance and binary are Instances' (`HUSK_SOCKET_PATH`, `HUSK_INSTANCE`, `HUSK_BIN_PATH`).

## Open questions

1. **Kill on stdin stall.** Killing a plugin that falls 1,024 messages behind is strict for a plugin that is merely slow during a burst of events. Recommended: keep the kill, and let the Event bus coalesce so events alone rarely fill the queue.
2. **Requests on stdout.** Resolved with [Protocol](protocol.md): no. A plugin that needs throughput (renders every frame) keeps one socket connection open with its token, which costs one connect, and the one-direction-per-channel rule stays simple.
3. **Catalog in Store.** The notes put the converted action catalog in Store as config. A catalog that appears and disappears with process launches is closer to runtime data than to layered config; putting it in Store couples plugin launch to a config reload. Recommended: keep it in Store as decided, as its own non-layered `plugins` namespace that does not participate in `distribution < user < profile < instance` merging, and confirm with the Store spec. Resolved in the consistency pass: the coupling is deliberate and is the launch gate (launch step 6); [Store](store.md) names the documents `plugins` and `keymap`.
4. **v1 plugins after the protocol major bump.** Old plugins speak `hello` over a socket and cannot work with stdio delivery. Recommended: bump the wire major, convert the two reference plugins, and let the v1 converter exist only to read old manifests (it still launches the plugin, which then fails visibly).
5. **Readiness.** If Surfaces needs to know a plugin has drawn its first frame, `live` on `cmd.Start` is too early. Recommended: leave readiness to Surfaces (first content received), not to liveness.
6. **Token in terminal surfaces.** Relying on a terminal plugin to drop `HUSK_PLUGIN_TOKEN` for user shells is a convention, not an enforcement. Recommended: accept it, and document it in the plugin author guide.
7. **`pid` and `started_at` provenance under an injected Starter.** Liveness `pid`/`started_at` come from the Wrapper's process info. A composition that injects its own `Options.Starter` (bypassing the built-in `Host.Start`) never builds a Wrapper, so those fields stay zero even though liveness says `live`. Recommended: leave the fields zero — a Starter owns the whole step and liveness is honest about what it knows — and revisit only if a real composition injects a Starter.
