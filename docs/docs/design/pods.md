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

- Server imports `protocol`, the `instance` leaf package ([Instances](instances.md)) and its own `Handler` interface. Server never imports Dispatcher, State or Services; `cmd/husk` registers their handlers on it at startup.
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

// Handler serves one method family. It runs on the connection's goroutine and
// must not block the Bubble Tea loop; it posts to the loop and waits on ctx.
// ctx is cancelled when the connection closes. Both values are
// protocol.Message: a request in, a reply out.
type Handler interface {
    Serve(ctx context.Context, c Caller, req protocol.Message) protocol.Message
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
- Claim: `bind` at `<base>/<instance-id>.sock`. On `EADDRINUSE` the Server dials the socket and sends `session.ping` with a 500 ms deadline. A reply means the name is in use: start fails with `instance "<id>" is already running`. `ECONNREFUSED` means a stale socket: the Server unlinks it and binds once more.
- Registration: once listening, the Server writes `<base>/<instance-id>.json` with `instance.Register` (the entry shape is in [Instances](instances.md)). An entry therefore always means ready.
- On `Close` the Server calls `instance.Unregister`, which removes the entry and the socket. The base directory is never removed.

#### What is served

Framing: one JSON object per line, at most 4 MiB (`MaxLineBytes`, unchanged), request shape from [Protocol](protocol.md). Requests from socket clients carry an optional top-level `token`:

```json
{"id":7,"method":"action.invoke","token":"9f2c...e1","params":{"action":"files.open","args":{"path":"README.md"}}}
{"id":7,"result":{"ok":true}}
{"id":8,"method":"action.invoke","params":{"action":"nope"}}
{"id":8,"error":{"code":"not_found","message":"no action \"nope\""}}
```

Method families and their handlers (registered by `cmd/husk`; names and forms are [Protocol](protocol.md)'s):

| Prefix | Handler owner | Notes |
| :--- | :--- | :--- |
| `session.` | Server itself | `session.ping` returns `{id, pid, protocol_version, host_version, uptime_ms}`. No caller restriction. |
| `action.` | Dispatcher | `action.invoke`, `action.list`. The Dispatcher applies permission using `Caller`. |
| `event.` | Event bus | `event.emit` (a request; a plugin may emit only under its own id, the bare CLI only under `user.`), `event.watch` (streams `event.deliver` notifications on this connection; for the CLI and debugging). |
| `state.` | State | `state.get`, `state.snapshot`, `state.watch` for everyone. `state.set` and `state.patch` are accepted only from a plugin caller and only for that plugin's own slice; the Server forwards them to that plugin's Pod, which writes State as the slice owner. `state.clear` is CLI only. |
| `settings.`, `theme.`, `secrets.`, `keys.`, `profile.`, `config.` | Services | `secrets.get` returns only what the Secrets service granted to `Caller.PluginID`. `config.reload` triggers the reload described in [Services](services.md). |
| `plugin.` | Pods | `plugin.list` (spec summary plus liveness), `plugin.log` (log path for an id). |
| `surface.`, `slot.`, `toast.` | Surfaces | `surface.update` and `surface.close` are notifications a plugin sends over a connection it keeps open; `slot.contribute` and `toast.show` likewise. |

An unknown prefix gets error code `unknown_method`. Watch methods are the only place the Server pushes notifications to a socket connection; a plugin never needs them, because its subscribed events arrive on stdin.

#### Caller identity: per-plugin token

The Wrapper generates a token for each launch (32 bytes from `crypto/rand`, hex encoded) and injects it as `HUSK_PLUGIN_TOKEN`. The `husk` CLI reads that variable and puts it in the top-level `token` member of every request it sends; a plugin writing the socket itself does the same. A request with a valid token is `CallerPlugin` for that plugin; a request with no token is `CallerCLI`; a request with an unknown or revoked token is refused with `denied` and never downgraded to the user. The token is revoked when the process exits.

This replaces peer-pid ancestry (`launchedID`, `peerAncestry`, `procparent_*.go`) because:

1. Ancestry works on Linux and macOS only; tokens work wherever the socket works.
2. Ancestry cannot attribute a detached daemon reparented to init; a token travels with whatever the plugin hands it to.
3. Ancestry races pid reuse between the kernel lookup and the walk; a token is checked in a map.
4. The CLI path is a fresh process per call, so the Server would walk ancestry on every request; a token lookup is O(1).

Scope of the guarantee: identity is attribution for permission policy between plugins of the same user. It is not isolation. Any process with the same uid can read another's environment on some platforms, and the peer uid check is the security boundary, exactly as today. A plugin that spawns a user shell (a terminal surface) should drop `HUSK_PLUGIN_TOKEN` and keep `HUSK_SOCKET_PATH`, so commands typed there act as the user.

#### Server invariants

1. Every accepted connection passes the peer uid check (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS) before a byte is read; unreadable credentials or a foreign uid close it. Platforms without a credential API refuse every connection.
2. The Server never calls a handler on the loop goroutine and never writes a socket from the loop. Replies go through the bounded queued writer (`protocol.NewQueuedWriter`, today's `huskipc.NewQueuedConnWriter`, capacity 1,024, 5 s write timeout).
3. Requests on one connection may be pipelined; replies carry the request id and may arrive out of order.
4. Every request gets exactly one reply, unless the connection closes first. Closing the connection cancels the `ctx` of its in-flight handlers.
5. A malformed or oversized line is answered as [Protocol](protocol.md) rules: an error reply when the request `id` was readable, otherwise a `protocol.error` notification. The connection stays open.
6. The Server knows method prefixes, not method semantics.

### Pod and manifest conversion

```go
package manifest

// Source is one manifest found on disk.
type Source struct {
    Path  string // the manifest file, or "" for an implicit plugin
    Dir   string // absolute directory the plugin lives in
    Bytes []byte // file contents, nil for an implicit plugin
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
    FocusBindings   []FocusBinding     // key -> action, active only while one of this plugin's surfaces has focus
    Subscriptions   []string           // event patterns (exact name or "<prefix>.*", [Message kinds](message-kinds.md)) delivered on stdin
    EventsDeclared  []EventDecl        // events this plugin may publish
    HooksDeclared   []HookDecl         // hook points this plugin publishes
    HookRegistrations []HookRegistration // hook points this plugin answers (order and veto deferred)
    PublishedSlots  []SlotDecl         // unchanged shape from today's manifest
    Slice           *SliceDecl         // scope, persist, retention, size cap request
    SecretRequests  []SecretRequest    // name and purpose; granting is the Secrets service's job
    SettingsDeclared []SettingDecl     // settings schema entries, keys prefixed plugins.<id>. ([Services](services.md))
}
```

Conversion rules:

1. Two converters ship in v1. `husk-plugin.json/v2` reads files with `"manifest_version": 2` and reads `"protocol"` (the wire major, [Protocol](protocol.md)) into `Spec.Protocol`; a v2 manifest without `protocol` fails conversion. `husk-plugin.json/v1` reads every other `husk-plugin.json` and the implicit forms (`Load` today: bare executable, command line). v1 maps `id`, `name`, `version`, `description`, `command`, `slots` (to `Surfaces`), `published_slots`, `hooks`, `events`; everything v2 adds is empty. v1 `transport` and `language` are ignored with a warning naming the field.
2. A converter is pure: it reads only `Source` and the files under `Source.Dir`, writes nothing, and starts nothing.
3. Plugin ids match `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`. Every declared id (action, event, hook, published slot) is `<plugin-id>.<name>`, validated as today.
4. A plugin whose `Protocol` major differs from the host's fails conversion with `plugin speaks protocol N, host speaks M`. v1 manifests count as the current major only if the plugin passes the launch; they cannot state a version, so the v1 converter sets the host's major.
5. Conversion errors fail that plugin only. Warnings go to the log.
6. Focus-local bindings may only target this plugin's own actions. A binding to another plugin's action is dropped with a warning. Global bindings do not exist in any manifest format.

What the Pod does with a `Spec`, in order, on launch:

1. Refuse a duplicate id (`duplicate plugin id`; the first loaded wins).
2. Write `starting` to `husk.pods`.
3. Write the catalog to Store as the plugin's declared documents (actions, surfaces, focus bindings, declarations, subscriptions, hook registrations, secret requests, settings schema; `Store.Declare`, [Store](store.md)), then ask the Services coordinator to reload with reason `plugin` ([Services](services.md)). This is config, reloadable, and is what the Dispatcher and the Keybinding, Settings and Secrets services read. The reload runs on the coordinator's goroutine, never on the loop; the Host posts its completion to the loop as a message.
4. Register the plugin's slice in State from `Slice` (owner: this Pod).
5. `SecretRequests` reach the Secrets service through that reload. Grants are held by Secrets and served over `secrets.get`; nothing secret goes into the environment.
6. **Launch gate.** Wait for the reload requested in step 3 to complete (every Service reported, applied or not; a reload that failed or timed out counts as completed, and its report says so). Only then start the Wrapper. `live` is written after `cmd.Start` succeeds, so by the time the Dispatcher can invoke anything on this plugin its actions, focus-local keys, settings schema and secret declarations are in Store and in every Service snapshot. The gate costs at most one reload (2 s wall time, [Services](services.md)).

When several plugins launch together (startup, `--plugin-dir`), the Host runs steps 1 to 5 for all of them, requests one reload, and starts every Wrapper after that single reload completes. At startup that reload is the Services first load, which therefore already includes every declared plugin ([Store](store.md), startup order).

On process exit (`exited` or `failed`) the Pod revokes the token and keeps the slice's value; the catalog stays declared, so the plugin's actions resolve `unavailable` rather than `not_found` and its focus-local keys stay listed as `unavailable` ([Message kinds](message-kinds.md), [Keybindings](keybindings.md)). Only when the Pod is torn down (the plugin removed from the instance) does it call `Store.Undeclare`, ask the coordinator to reload with reason `plugin`, and unregister the slice according to its scope and retention ([State](state.md) decides).

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
- After `cmd.Start`, the Pod queues the opening notifications [Protocol](protocol.md) lists, in order and before any request: `theme.changed`, one `surface.resized` carrying every surface the plugin occupies (a single snapshot, never one message per surface), `input.key_state`. Pods cannot build them alone: the modifier state is Input bookkeeping that is not in State ([State](state.md)). So `cmd/husk` injects at construction an `Opening func(pluginID string) []protocol.Message` that the loop builds from Theme, the `husk.geometry` rects and Input's modifier state; the Pod calls it on the loop right after `cmd.Start`, queues the lines, and only then writes `live`. Later `theme.changed` pushes follow a Theme generation change ([Services](services.md)); the Pods host sends them through each `Channel`.

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

`exited` and `failed` are terminal for that launch. There is no restart in v1: a failed plugin stays failed until the instance restarts. The Pod also publishes `pods.plugin-exited` with the same payload through the publisher interface the loop injects at construction (the same way State and Services publish; Pods import nothing from Stem), so a notifier can toast it as today.

#### Wrapper invariants

1. The Wrapper is the only writer of a plugin's stdin and the only reader of its stdout.
2. `Send` never blocks the caller. It marshals on the caller's goroutine and enqueues on the bounded queue from `pkg/huskipc/queue.go` (capacity 1,024, 5 s write timeout, watchdog close on pipes).
3. Stdout carries replies only. A stdout line that is a reply goes to `ReplySink.Reply`. For a malformed reply, the Wrapper salvages the protocol reader's `LineError.ID` and forwards a bare-id message through `ReplySink.Reply`, so the Loop posts `ReplyMsg` and Calls fails a matching call with `internal` / `data.reason: "invalid_reply"` instead of waiting for its timeout. Any other valid line is logged as `plugin <id> sent a non-reply on stdout` and answered with a `protocol.error` notification on stdin; the plugin stays up. The one exception is a major-1 `hello`, which gets one major-1 `protocol_error` line and stops the plugin ([Protocol](protocol.md)).
4. A stdin failure (queue overflow, write timeout, write error) is fatal for the launch: `ReplySink.Closed` is called, the process group gets SIGTERM, then SIGKILL after the stop grace, and liveness becomes `failed`. A plugin that cannot read its stdin cannot be reached any other way, so keeping it alive only hides the fault.
5. `ReplySink.Closed` is called exactly once per launch, before liveness leaves `live`, so the Dispatcher never sees a `live` owner whose pending calls are already orphaned.
6. Stdout EOF without process exit (the plugin closed stdout) is treated as a stdin failure (rule 4): the reply channel is gone.
7. Background goroutines never write the log or State directly; they post to the loop, which writes both.

#### Shutdown

On instance exit: the Loop closes the Calls table first, so socket callers waiting on an action get `cancelled` replies ([Calls](calls.md)); then the Server stops accepting and unregisters the instance (entry, then socket); every Wrapper closes its stdin queue (flush, then close, so the plugin sees EOF) and the shared plugin context is cancelled, which sends SIGTERM to every process group; one 2 s deadline covers all plugins; survivors get SIGKILL; Husk waits up to 1 s more for those. Exits during shutdown are `exited`, never reported as crashes. Non-empty logs are kept and their paths logged, as today; empty ones are removed by Store's GC ([Store](store.md)), since only Store deletes files.

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
| Manifest invalid | Converter | That plugin is not launched; others are | Log and error toast `failed to launch plugin <id>: ...` |
| Duplicate plugin id | Pod | Second plugin not launched | Same toast, `duplicate plugin id` |
| Protocol major mismatch | Converter | Not launched | Same toast, versions named |
| `cmd.Start` fails | Wrapper | Liveness `failed`; the catalog stays declared | Error toast with the start error |
| Plugin exits non-zero or by a foreign signal | Wrapper | Pending calls failed via `Closed`, liveness `failed`, token revoked; the catalog stays declared | Warning toast with status and last stderr line; log has the last 3 stderr lines |
| Plugin exits 0 | Wrapper | Same cleanup, liveness `exited` | Info-level log line, no toast |
| Plugin stops reading stdin | Stdin queue | Rule 4: kill, `failed` with `last_error` | Warning toast `stopped reading: send queue full (1024 messages)` or `write timed out after 5s` |
| Plugin writes a non-reply or malformed line on stdout | Wrapper reader | Line dropped, `protocol.error` notification sent | Plugin sees the notification; log line |
| Reply with an unknown or expired id | Calls | Dropped | Debug log only |
| Plugin ignores SIGTERM | Shutdown | SIGKILL after 2 s | Log `did not exit within 2s of SIGTERM; killing it` |

### Limits

| Limit | Value | Reason |
| :--- | :--- | :--- |
| Line size, socket and stdio | 4 MiB | unchanged `MaxLineBytes`; bounds one allocation |
| Stdin queue | 1,024 messages | unchanged; a slow plugin falls this far behind before it is killed |
| Stdin and reply write timeout | 5 s | unchanged; a stalled reader is detected without freezing the loop |
| Reply queue per socket connection | 1,024 messages | same writer, same reasoning |
| In-flight requests per socket connection | 64 | stops one client from flooding the loop; the 65th waits for a slot |
| Concurrent socket connections | 256 | above any real count of plugins plus CLI calls; past it, accept closes the newest |
| Stale-instance ping | 500 ms | a live instance answers `session.ping` without touching the loop |
| Stop grace (SIGTERM to SIGKILL) | 2 s | unchanged |
| Kill wait after SIGKILL | 1 s | unchanged; only matters for unreapable processes |
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

| File | Fate |
| :--- | :--- |
| `cmd/husk/transport.go` | Accept loop moves to `pods/server`. `serveStreamOrigin`, `streamOrigin`, the `hello` gate and `ConnectedPlugin` registration are removed: there is no hello and no plugin stream on the socket. `post` and `hostNoticeMsg` stay in `cmd/husk` as the loop's posting function the Server is configured with. |
| `cmd/husk/socket_security.go` | `listenPrivate`, `checkPeerUID`, `authorizePeer` move to `pods/server`. `createSocketDir` (random `husk-*` dir) is replaced by `instance.RuntimeDir` with the ownership check. `socketName` goes; the file is `<instance-id>.sock`. |
| `cmd/husk/peercred_*.go` | `peerUID` moves to `pods/server`. `peerPID` is removed. |
| `cmd/husk/procparent_*.go`, `peerAncestry` and `launchedID` in `cmd/husk/publications.go`, `cmd/husk/peer_ancestry_test.go`, `cmd/husk/procparent_linux_test.go` | Removed; the token replaces ancestry. |
| `cmd/husk/lifecycle.go` | `spawnPlugin`, `tailLines`, `exitStatus`, `stopPlugins` move to `pods/wrapper`; `openPluginLog` and `sanitizeLogName` move to Store as `OpenPluginLog` ([Store](store.md)). The transport branch is removed (always stdio). `pluginEnv` is rewritten for the new variables. `pluginHandoffGrace`, `handoffExpiredMsg`, `handoffPending` and `pluginExitedMsg.handedOff` are removed. `handleLifecycleMsg` toasts become a liveness write plus a `pods.plugin-exited` event. |
| `cmd/husk/registry.go` (`declareFromManifest`) | Replaced by the Pod writing the `Spec` catalog to Store. Liveness leaves the registry for the `pods` State slice. |
| `cmd/husk/main.go` | `--plugin` and `--plugin-dir` stay and go through the converter registry. `--instance` and `--socket` are added (resolution rules in [Instances](instances.md)). |
| `pkg/manifest/manifest.go` | Becomes the `husk-plugin.json/v1` converter in `pods/manifest`. `Load`, `LoadDir`, `ResolvedCommand`, `WorkDir`, `splitCommand` and the implicit-plugin forms are kept. `CheckHello`, `TransportSocket`, `TransportStdio` and the transport validation are removed. |
| `pkg/huskipc/queue.go` | Kept and reused for stdin and socket replies. `Send` generalizes from `huskipc.Envelope` to a protocol message (or pre-marshalled line); the queue mechanics do not change. Moves with the wire package when [Protocol](protocol.md) renames it. |
| `pkg/huskipc/read.go` | `ReadLoopReporting` is reused for plugin stdout and socket connections. |
| `cmd/husk/render.go` (`drawChrome`) | Starting point for Decoration; moves when its shape is decided. |
| `cmd/husk/lifecycle_test.go`, `cmd/husk/socket_security_test.go`, `cmd/husk/send_queue_test.go` | Move with their code; hand-off and ancestry cases are deleted; token, base-dir ownership and stdin-failure kill cases are added. |
| `plugins/reference`, `plugins/reference-actions` | Rewritten to read stdin and call the CLI; their manifests gain `"manifest_version": 2`. |

Removed behaviour, stated for the changelog: the socket transport for plugins (plugins no longer connect to receive messages, only to send), the `hello`/`welcome` exchange, the launcher hand-off grace, and pid-ancestry binding.

## Decisions made here

1. Caller identity is a per-launch token injected as `HUSK_PLUGIN_TOKEN` and sent by the CLI on every request; pid ancestry is removed. Reasons: portable, survives daemonizing, no pid-reuse race, O(1) per CLI call. Identity is attribution for policy, not isolation; the uid check remains the security boundary.
2. A request without a token is a user call; a request with a bad token is refused, never downgraded.
3. The runtime directory is stable (`$XDG_RUNTIME_DIR/husk/` or `/tmp/husk-<uid>/`) with an ownership and mode check, replacing the random `mkdtemp` directory, because discovery needs a known place. The rule and the registration entry live in the `instance` package ([Instances](instances.md)); the Server calls it.
4. Instance names follow Instances: lowercase, at most 32 bytes, never all digits, so they never collide with pid ids.
5. The Server routes by method prefix to handlers registered by `cmd/husk`; it does not import Stem, State or Services.
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
16. Exit with status 0 produces no toast; non-zero or a foreign signal produces a warning toast through a `pods.plugin-exited` event.
17. Logs live at `$XDG_STATE_HOME/husk/logs/<instance-id>/<plugin-id>.log`, opened through Store, so the runtime directory holds only sockets and entries.
18. `HUSK_BIN_PATH` is injected so a plugin calls the same build's CLI; the environment variable names for socket, instance and binary are Instances' (`HUSK_SOCKET_PATH`, `HUSK_INSTANCE`, `HUSK_BIN_PATH`).

## Open questions

1. **Kill on stdin stall.** Killing a plugin that falls 1,024 messages behind is strict for a plugin that is merely slow during a burst of events. Recommended: keep the kill, and let the Event bus coalesce so events alone rarely fill the queue.
2. **Requests on stdout.** Resolved with [Protocol](protocol.md): no. A plugin that needs throughput (renders every frame) keeps one socket connection open with its token, which costs one connect, and the one-direction-per-channel rule stays simple.
3. **Catalog in Store.** The notes put the converted action catalog in Store as config. A catalog that appears and disappears with process launches is closer to runtime data than to layered config; putting it in Store couples plugin launch to a config reload. Recommended: keep it in Store as decided, as its own non-layered `plugins` namespace that does not participate in `distribution < user < profile < instance` merging, and confirm with the Store spec. Resolved in the consistency pass: the coupling is deliberate and is the launch gate (launch step 6); [Store](store.md) names the documents `plugins` and `keymap`.
4. **v1 plugins after the protocol major bump.** Old plugins speak `hello` over a socket and cannot work with stdio delivery. Recommended: bump the wire major, convert the two reference plugins, and let the v1 converter exist only to read old manifests (it still launches the plugin, which then fails visibly).
5. **Readiness.** If Surfaces needs to know a plugin has drawn its first frame, `live` on `cmd.Start` is too early. Recommended: leave readiness to Surfaces (first content received), not to liveness.
6. **Token in terminal surfaces.** Relying on a terminal plugin to drop `HUSK_PLUGIN_TOKEN` for user shells is a convention, not an enforcement. Recommended: accept it, and document it in the plugin author guide.
