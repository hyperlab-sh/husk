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
- Not a restart manager. v1 has no restart policy: a plugin that exited stays exited until the user unloads it (`husk plugin remove`) and loads it again (`husk plugin add`), two explicit commands and never an automatic relaunch.

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

- Server imports `protocol`, the `instance` leaf package ([Instances](instances.md)) and its own `Handler` interface. Server never imports Dispatcher, State or Services; the host's composition root (`pkg/host` (`New`)) registers their handlers on it.
- Wrapper imports `protocol` and the `ReplySink` interface it is handed. Calls never imports Wrapper; the Pods `Host` implements `calls.Outbox` and the Message kinds `Sink` over the Wrappers' `Channel`s and is handed to Calls and the Event bus at startup.
- Pods writes State; State never imports Pods.
- Pods writes Store (catalog config); Store never imports Pods.
- Manifest conversion imports nothing from Stem, Services or Substrate. It is a pure function from files to `Spec`.
- Decoration imports the frame and theme value types only. The Renderer calls Decoration; Decoration never calls the Renderer.

### Server

```go
package server

// Caller is protocol.Caller, the one caller-identity type ([Protocol](protocol.md),
// "Caller"; defined once, used unchanged by the Server, the host adapters, the
// Dispatcher, the hook runner and Services). The Server produces only the two
// socket kinds, CallerCLI (no token: the user's CLI, a script, a test harness)
// and CallerPlugin (valid plugin token), and fills ConnID, the number unique
// to the accepted connection that logs and watch cleanup use. CallerInput and
// CallerHost never arrive over the socket.
type Caller = protocol.Caller

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
    RuntimeDir string        // checked by instance.CheckRuntimeDir, see "Runtime directory"
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
- Claim: `bind` at `<base>/<instance-id>.sock`; the normal first bind is lock-free. On `EADDRINUSE` the Server runs the one shared stale-recovery protocol, `instance.RecoverStale` ([Instances](instances.md), "Stale recovery"): a `syscall.Flock` on an `O_RDONLY` directory FD, `LOCK_NB` retried every 50 ms for up to 2.5 s (`ErrLocked`), a re-probe under the lock, and unlink/rebind once only if the socket is still refused. A newly live socket is never unlinked and recovery creates no lock file. The three start failures are `instance "<id>" is already running`, `instance "<id>" did not answer; it may be busy` and `runtime dir <path> is locked`. After each successful bind the Server takes `instance.SocketIdentity` of the path and passes it to `Unregister` on `Close`.
- Registration: once listening, the Server writes `<base>/<instance-id>.json` with `instance.Register` (the entry shape is in [Instances](instances.md)). An entry therefore always means ready. A registration failure is fatal: `Start` removes its newly claimed socket, closes the listener and returns the error; it never keeps an undiscoverable listener up.
- On `Close` the Server calls `instance.Unregister`, which removes the entry and the socket. The base directory is never removed. Each stream then drains: request handlers already running finish and their replies are flushed before the stream closes, so `husk action invoke husk.quit` gets its `{}` reply. The wait is bounded by the write timeout per stream (streams drain in parallel) on the injected clock; after it the stream is cut and its handlers are cancelled. A request arriving while a stream drains is refused with `cancelled` and `{"reason":"shutdown"}`. Notification handlers are not drained: they are cancelled when the stream closes. The host ends the context of socket requests still waiting on the loop before it closes the Server, so a request queued behind the shutdown fails at once instead of holding the drain. The `husk.quit` host action itself only posts a message to the loop; the shutdown runs in a later `Update`, after the invocation's reply has left the loop.

#### What is served

Framing: one JSON object per line, at most 4 MiB (`MaxLineBytes`, unchanged), request shape from [Protocol](protocol.md). Requests from socket clients carry an optional top-level `token`:

```json
{"id":7,"method":"action.invoke","token":"9f2c...e1","params":{"action":"files.open","args":{"path":"README.md"}}}
{"id":7,"result":{"ok":true}}
{"id":8,"method":"action.invoke","params":{"action":"nope"}}
{"id":8,"error":{"code":"not_found","message":"no action \"nope\""}}
```

The first line of a connection that is a major-1 `hello` is answered and the connection closed as Versioning in [Protocol](protocol.md) says, without counting as `invalid_message`; the per-request token rule is unchanged.

Method families and their handlers (registered by `pkg/host` (`New`); names and forms are [Protocol](protocol.md)'s):

| Prefix | Handler owner | Notes |
| :--- | :--- | :--- |
| `session.` | Server itself | `session.ping` returns `{id, pid, protocol_version, host_version, uptime_ms}`; `host_version` is always present, `""` when the build carries no version, the same rule as the registration entry ([Instances](instances.md)). No caller restriction. |
| `action.` | Dispatcher | `action.invoke`, `action.list`. The Dispatcher applies permission using `Caller`. |
| `event.` | Event bus | `event.emit` (a request; a plugin may emit only under its own id, the bare CLI only under `user.`), `event.watch` (streams `event.deliver` notifications on this connection; for the CLI and debugging). |
| `state.` | State | `state.get`, `state.snapshot`, `state.watch` for everyone. `state.set` and `state.patch` are accepted only from a plugin caller and only for that plugin's own slice; the Server forwards them to that plugin's Pod, which writes State as the slice owner. `state.clear` is CLI only. |
| `settings.`, `theme.`, `secrets.`, `keys.`, `profile.`, `config.` | Services | `secrets.get` returns only what the Secrets service granted to `Caller.PluginID`. `config.reload` triggers the reload described in [Services](services.md). |
| `plugin.` | Pods | `plugin.list` (spec summary plus liveness), `plugin.log` (log path for an id, not_found before a log exists), `plugin.remove` (unload one plugin from this instance, see "Removing a plugin"), `plugin.add` (load a plugin from a path into this instance, see "Adding a plugin"). All four are the user's own: CLI-only callers, denied for plugins, Stem and zero-kind callers, with one landed addition (a port rule, no wire change): `plugin.remove` also admits a `host` caller, the frontend's Settings center acting for the user ([Settings center](settings-center.md), "Changes to other specs"); all read loop-owned Pod state through `Loop.Do` and answer unavailable when the loop no longer runs. |
| `surface.`, `slot.`, `toast.` | Surfaces | `surface.update` and `surface.close` are notifications a plugin sends over a connection it keeps open; `slot.contribute` and `toast.show` likewise. |

An unknown prefix gets error code `unknown_method`. Watch methods are the only place the Server pushes notifications to a socket connection; a plugin never needs them, because its subscribed events arrive on stdin.

#### plugin.* wire shapes

`plugin.list` takes no params. Its result is a bare JSON array, sorted by plugin id and `[]` when no Pod exists, of:

```json
{"id":"alpha","name":"Alpha","version":"1","description":"","status":"live","pid":4242,"started_at":"2026-01-02T03:04:05Z","last_error":"","log":"/run/user/1000/husk/logs/alpha.log","dir":"/home/u/plugins/alpha"}
```

`status` is one of `starting`, `live`, `exited`, `failed`. `pid` is 0 and `started_at` the zero time before a process started; `last_error` and `log` are empty strings when unknown. `dir` (landed; additive field listed with protocol minor 2.3) is the plugin's directory as the converter resolved it (the directory holding its manifest, or the directory a bare executable or manifest file was found in), `""` for a plugin given as an implicit command line; the same field is in the plugin's `husk.pods` entry ([State](state.md)), written at `starting` and never changed. It answers where a plugin came from for every load path ([Settings center](settings-center.md), Installed tab).

`plugin.log` takes `{"plugin":"<id>"}` (a missing or empty `plugin` is `invalid_params`) and answers `{"path":"<log file>"}`. An id that names no Pod is `not_found`, and so is a Pod whose Wrapper has not opened a log yet; the result never carries an empty path.

`plugin.remove` takes `{"plugin":"<id>"}` (a missing or empty `plugin` is `invalid_params`) and answers once the plugin is gone from the instance and the Services reload that no longer sees it has completed:

```json
{"id":9,"method":"plugin.remove","params":{"plugin":"alpha"}}
{"id":9,"result":{"plugin":"alpha","status":"exited","log":"/home/u/.local/state/husk/logs/4242/alpha.log","reports":[{"service":"profile","gen":4,"applied":true},{"service":"settings","gen":4,"applied":true},{"service":"theme","gen":3,"applied":true},{"service":"keybinding","gen":4,"applied":true},{"service":"secrets","gen":4,"applied":true}]}}
```

`status` is the plugin's last `husk.pods` status before its entry was removed: `exited` when a live process was stopped by this call and reaped; `failed` when it survived SIGKILL past the kill wait (`last_error` `did not exit within 1s of SIGKILL`); otherwise the status the Pod had (`starting`, `exited` or `failed`); it is never `live`. `log` is the log file path `plugin.log` would have answered, `""` when no log was opened; after this reply `plugin.log` answers `not_found`, so this is the last place the path is reported. `reports` is the reload's one report per Service, the same objects `config.reload` returns ([Services](services.md)); a report with `applied: false` does not make the call an error. The sequence, its ordering, idempotency and failure modes are in "Removing a plugin" below.

`plugin.add` takes exactly one of `path` or `dir`, an absolute path; otherwise the reply is `invalid_params` with `data.reason` `path_and_dir` (both members present, even when one is empty or null), `missing_path` (neither, an empty string or a non-string) or `relative_path`. `path` loads one plugin from what `--plugin` accepts when given a path (a manifest file, a plugin directory, a bare executable; the flag's implicit command-line form, an executable followed by arguments, is not a path and is not accepted); `dir` loads every immediate subdirectory of a directory, as `--plugin-dir` does. It answers once the plugins it added are declared, the Services reload that read their declarations has completed and their processes have been started:

```json
{"id":10,"method":"plugin.add","params":{"path":"/home/u/plugins/alpha"}}
{"id":10,"result":{"added":[{"plugin":"alpha","status":"live","log":"/home/u/.local/state/husk/logs/4242/alpha.log","warnings":[]}],"refused":[],"reports":[{"service":"profile","gen":5,"applied":true},{"service":"settings","gen":5,"applied":true},{"service":"theme","gen":4,"applied":true},{"service":"keybinding","gen":5,"applied":true},{"service":"secrets","gen":5,"applied":true}]}}
{"id":11,"method":"plugin.add","params":{"dir":"/home/u/plugins"}}
{"id":11,"result":{"added":[{"plugin":"beta","status":"failed","log":"","warnings":[{"code":"suggestion_ignored","message":"rail item beta.open: shortcut is a suggestion; bind it in a keymap layer"}]}],"refused":[{"path":"/home/u/plugins/alpha","plugin":"alpha","code":"duplicate_plugin_id","message":"plugin alpha is already loaded"},{"path":"/home/u/plugins/old","plugin":"","code":"conversion","message":"husk-plugin.json: plugin speaks protocol 1, host speaks 2"}],"reports":[...]}}
```

`added` has one entry per plugin that got a Pod, sorted by plugin id. Its `status` is the `husk.pods` status after the launch gate: `live` (the process started), `failed` (declaration or slice registration failed, or `cmd.Start` failed; `last_error` is in `plugin.list`), or `removed`, a reply-only value that is not a `husk.pods` status: a `plugin.remove` for the id arrived while the gate was pending and took the Pod down, so the Pod no longer exists. `log` is what `plugin.log` answers now, `""` when no log was opened. `warnings` are the conversion warnings for that manifest (`{"code","message"}`, the same codes conversion logs; `[]` when none). `refused` has one entry per plugin directory or manifest that got no Pod, in directory order, with `plugin` the id when conversion got that far (`""` otherwise) and `code` one of the closed set under "Adding a plugin". `reports` is the one reload's report per Service, as for `config.reload` and `plugin.remove`; it is `[]` when `added` is empty, because then nothing was declared and no reload ran. In the `path` form the one plugin's refusal is an error reply rather than an entry in `refused` (the table under "Adding a plugin"); in the `dir` form refusals never make the call an error, as a bad manifest under `--plugin-dir` fails only itself.

All four methods are the user's own: a CLI caller is served, every other caller kind is `denied`, except that `plugin.remove` also serves a `host` caller (landed; a port rule, not a wire change: the frontend's Settings center, [Settings center](settings-center.md), through the `RemovePlugin` seam, which reaches `PluginPort.Remove` in process; on the wire the method stays `cliOnly` in `MethodHomes`, as `settings.set` does). A plugin never reaches any of the four.

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
    LoadPath(path string) (*Spec, []Warning, error) // existing path only, never a command line
    LoadDir(dir string) ([]Result, error)       // --plugin-dir: each immediate subdirectory, a symlink to a directory included ([Bundles](bundles.md))
}

type Result struct {
    Dir      string // claimed immediate subdirectory
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
    Keybindings     []FocusBinding     // key -> action, active only while one of this plugin's surfaces has focus
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
2. A built-in converter is pure: it reads only `Source` and the files under `Source.Dir`, writes nothing, and starts nothing. An executable converter is pure from the host's point of view (see "Executable converters").
3. Plugin ids match `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`. Every declared id (action, event, hook, published slot) is `<plugin-id>.<name>`, validated as today.
4. A plugin whose `Protocol` major differs from the host's fails conversion with `plugin speaks protocol N, host speaks M`. v1 manifests count as the current major only if the plugin passes the launch; they cannot state a version, so the v1 converter sets the host's major.
5. Conversion errors fail that plugin only, except for a `husk.plugins.paths` entry, which is fatal at start ([Services](services.md), "Reload"). The consequence of a load failure depends on where the entry came from, never on which converter (built-in or executable) claimed the directory or why it failed. A `husk.plugins.paths` entry, including one supplied by `--plugin` (which replaces that array in the instance layer) or appended by `--add-plugin`, that fails to load for any reason aborts the start; a converter failure counts, including a transient one such as the deadline, and is never retried. A plugin reached through `husk.plugins.dirs`, `--plugin-dir`, the bundle's `plugins/` folder, a converter's `discover` output or `plugin.add` is refused alone and the others continue. Warnings go to the log.
6. Focus-local bindings may target this plugin's own actions or `husk.*` host actions. A binding to another plugin's action is dropped with a warning (`foreign_action`); an unscoped or foreign-surface binding is dropped (`global_not_allowed`). `Spec.Keybindings` and the declared `keymap` document's `bind` hold only bindings that pass these conversion checks. The document is `{version: 1, bind: [...]}` with optional `problems: [{index, code, message, key?}]`; `index` is the original zero-based position in the manifest list the problem is about (the `keybindings` entry, or for `cli_only_event` the subscription entry, in `events.subscribe` or `subscriptions`); `key` names the manifest member for a problem not about a binding position (for example `events.subscribe[1]`); Services imports conversion problems under `plugin:<id>`. Conversion keeps at most the first 128 binding entries in document order, dropping the remainder with one `too_many` problem. Key spellings pass through unchanged: only Services parses and reports unparsable keys. Global bindings do not exist in any manifest format.

7. Rail items (`rail_items[]`, v2 manifests only; the v1 converter ignores the field) are declarations, not runtime registrations. Each id is `<plugin-id>.<name>` in the same id space as actions, because a rail item is addressable as an action by its id ([Keybindings](keybindings.md)); a rail id that repeats another rail id or an action id fails conversion. `icon`, `label` and a `placement` of `modal`, `full-canvas` or `stage` are required; `badge`, `description` and `frame` (the border, label and close the host draws around the surface the item opens) are optional. A `shortcut` on a rail item is accepted and dropped from the declaration: it is recorded as a suggestion and warned as `suggestion_ignored`, like every manifest shortcut. The converter validates shape only: it does not check that the claimed surface exists, and it writes no placement. Rail items live in the `plugins` document under `rail_items`, which is where the frontend reads them (see Frontend seams); which item is active, and the resulting `placement` and `overlays` in `husk.surfaces`, are the frontend's State writes ([State](state.md)).

What the Pod does with a `Spec`, in order, on launch:

1. Refuse a duplicate id (`duplicate plugin id`; the first loaded wins).
2. Write `starting` to `husk.pods`.
3. Write the catalog to Store as the plugin's declared documents (actions, surfaces, rail items, focus bindings, declarations, subscriptions, hook registrations, secret requests, settings schema; `Store.Declare`, [Store](store.md)), then ask the Services coordinator to reload with reason `plugin` ([Services](services.md)). This is config, reloadable, and is what the Dispatcher and the Keybinding, Settings and Secrets services read. The reload runs on the coordinator's goroutine, never on the loop; the Host posts its completion to the loop as a message.
4. Register the plugin's slice in State from `Slice` (owner: this Pod).
5. `SecretRequests` reach the Secrets service through that reload. Grants are held by Secrets and served over `secrets.get`; nothing secret goes into the environment.
6. **Launch gate.** Wait for the reload requested in step 3 to complete (every Service reported, applied or not; a reload that failed or timed out counts as completed, and its report says so). Only then start the Wrapper. `live` is written after `cmd.Start` succeeds, so by the time the Dispatcher can invoke anything on this plugin its actions, focus-local keys, settings schema and secret declarations are in Store and in every Service snapshot. The gate costs at most one reload (2 s wall time, [Services](services.md)).

When several plugins launch together (startup, `--plugin-dir`, one `plugin.add`), the Host runs steps 1 to 5 for all of them as one **batch**, requests one reload, and starts every Wrapper after that single reload completes. At startup this plugin flight follows the startup flight, which read no declarations; the plugin flight is the first that reads the batch's declarations ([Services](services.md), "Reload"; [Store](store.md), startup order). A `plugin.add` while the instance runs is a later batch of the same shape and goes through the same steps; nothing in the launch is startup-only ("Adding a plugin").

On process exit (`exited` or `failed`) the Pod revokes the token and keeps the slice's value; the catalog stays declared, so the plugin's actions resolve `unavailable` rather than `not_found` and its focus-local keys stay listed as `unavailable` ([Message kinds](message-kinds.md), [Keybindings](keybindings.md)). Only when the Pod is torn down, which in v1 happens through `plugin.remove` and nowhere else (shutdown stops processes and tears nothing down), does the host call `Store.Undeclare`, ask the coordinator to reload with reason `plugin`, and unregister the slice; the persisted copies of a persistent slice are kept ([State](state.md), [Store](store.md)). The full sequence is "Removing a plugin".

#### Plugin paths at startup

`--plugin` and `--plugin-dir` replace `husk.plugins.paths` and `husk.plugins.dirs` in the instance layer. The repeatable alternatives `--add-plugin PATH` and `--add-plugin-dir DIR` preserve the effective configured arrays, including the named-instance file, at startup, then append additions in command-line order, deduplicating by cleaned absolute path with first occurrence winning. The owning rules, including selected-profile resolution, invalid-array diagnostics and fallback, and start-consumed/restart behaviour, are [Services](services.md), "Additive plugin launch flags". An unrelated settings error never resets either validated array.

Using replace and add for the same array is exit 2 with a message naming both flags; mixing across arrays is allowed. All entries still pass through the same converter registry after the startup flight, and bundle `plugins/` loads last in this batch ([Bundles](bundles.md)). These flags change only the launch CLI, not the wire protocol or its version.

#### Executable converters

**Landed.** Without an executable adapter, the Go-only `Converter` interface would require a plugin format the host does not know to be compiled into the host, where format knowledge must not live. `manifest.ExecConverter` wraps an external executable as a `Converter` and keeps every rule above:

- Registration: the repeatable launch flag `--converter <path>` and the launch-only setting `husk.plugins.converters` (strings, restart; [Services](services.md)), in that order, each appended to the `Registry` after the built-in `husk-plugin.json/v2` and `/v1` converters, so a directory with a `husk-plugin.json` is never claimed by an external converter. The executable must be an absolute path or resolvable on `PATH`; a converter that cannot be executed is logged and surfaced through `Frontend.Refusals` once at start (`converter_unavailable`) and skipped. Registration deduplicates resolved absolute executable paths, retaining the first occurrence.
- Protocol, three subcommands, each a one-shot process with a 5 s deadline, run with the converter's own environment plus `HUSK_PROTOCOL_VERSION`, no socket and no token: `<exe> detect <dir>` exits 0 to claim the directory and 1 to decline (`Detect`); any detect outcome other than exit 0 or 1 claims the directory and fails it with `converter_failed`; `<exe> convert <dir>` prints exactly one `husk-plugin.json` v2 document on stdout, with `command` an absolute argv, and zero or more problem lines `{"code","message","field"}` on stderr (`Convert`: the document goes through the built-in v2 converter unchanged, so every rule above applies to it, and the problems join the plugin's warnings as `converter:<code>` and its declared `plugins` document's `problems` ([Store](store.md))); `<exe> discover` prints zero or more absolute plugin directories, one per line, on stdout. `Format()` is `exec:<basename>`.
- Discovery runs once at start, after `--plugin`, `--plugin-dir`, the `husk.plugins.*` entries and the bundle's `plugins/` folder ([Bundles](bundles.md)), for each registered executable converter; each printed directory is resolved like a `--plugin` directory argument, through the full `Registry` (so the printing converter is not assumed to be the one that claims it). Directories already attempted at start are skipped silently, including failed conversion and duplicate-id refusals. Empty lines are ignored and a trailing carriage return is removed from each line, accepting CRLF output. More than 256 non-empty directory lines fails that converter's entire discovery for this start; the host does not truncate it. A discovered directory that fails to load, including one no converter claims, is refused like a bad plugin under `--plugin-dir` (logged, `Frontend.Refusals`); start continues, because a discovered directory is not a `husk.plugins.paths` entry (conversion rule 5).
- Conversion stays pure from the host's point of view: the converter process may read what it likes, but the host passes it nothing about the instance and gives it no credential; a converter that writes files is a bug in the converter.
- Vendor data: a v2 document may carry one object under a key beginning with `x-`, which the v2 converter retains in the declared `plugins` document and the host otherwise ignores ([Store](store.md)); a converter uses it to carry what its runtime needs without asking the host for a field.
- Failure modes join the table below: a converter that exits non-zero (except detect exit 1), exceeds the deadline, prints an unparsable document or supplies a command that is not absolute argv fails that plugin only, except a `husk.plugins.paths` entry, which is fatal at start ([Services](services.md)) (`converter_failed`, with the exit status or deadline and the first stderr line; conversion rule 5). Each stdout/stderr stream is limited to 1 MiB; exceeding either bound also fails the plugin. Plain-text stderr lines are ignored on success; JSON problem lines are retained. A failing `discover` loses that converter's discovery for this start and is logged and surfaced through `Frontend.Refusals` once.

The seam is generic on purpose: the host learns no format name. It is the extension point for every manifest format a distribution wants to read, and the only one.

#### Removing a plugin (`plugin.remove`)

`husk plugin remove <id>` unloads one plugin from the running instance (D59). It is an **unload, not an uninstall**: it touches no file the plugin or the user owns, deletes no persisted slice copy, forgets no secret grant, and is not remembered across restarts. A plugin named by `--plugin` or found under `--plugin-dir` is loaded again by the next start of the instance; v1 has no installed-plugin registry to record a removal in, and a plugin whose directory the user has deleted is simply not found next time. A removed id is free again: `husk plugin add` ("Adding a plugin") may load the same plugin, or another plugin with the same id, into this instance, and that is the only relaunch v1 has. So a removal can race a launch of the same id; the two rules that settle it are step 2 below (an add never joins or overlaps a removal: an id with a removing entry is refused `conflict`) and step 3 (a removal that finds the id in a pending launch gate waits for the gate and the gate does not start it). The command exists because a plugin that crashed otherwise keeps its catalog declared until the instance exits: its actions stay listed as `unavailable`, its keys as `unavailable`, its surfaces claimed, and nothing else calls `Store.Undeclare`.

Terms used below: **removing** is a per-id entry on the Host (not on the Pod, which dies at step 5), created at step 2 and deleted when the reload of step 7 completes; it is loop-owned like the Pod map (created, completed and deleted on the loop, never touched off it), and it holds a completion channel that the waiting handlers hold, the `status` and `log` recorded at step 5, and, once the reload completed, the reports. Step 2 consults it before the Pod map, so a request that arrives at any point of the sequence joins it. **The launch gate** is step 6 of the launch; **teardown** is the loop half already described under "Host and loop boundary" (`TeardownMsg`); **stop** is the per-plugin form of the shutdown stop policy.

The sequence, in order. Steps 2, 3, 5 and 6 run on the host loop; steps 1 and 8 run on the Server handler goroutine; steps 4 and 7 run on host workers. Nothing in it blocks the loop.

| Step | Where | What happens |
| :--- | :--- | :--- |
| 1. Admit | Server handler | A caller that is neither `cli` nor `host` is `denied` before anything is looked up (host admission is landed at the port for the frontend's Settings center; the socket adapter remains CLI-only, and every later step is caller-blind). Missing or empty `plugin` is `invalid_params`. The handler then hands the request to the loop (`Loop.Do` or a posted message) and waits off-loop under the request's `ctx`. |
| 2. Look up | loop, inside one `Loop.Do` that returns at once | The removing entries are consulted first: an id with one makes this request **join** the removal in progress; it receives the same reply when that completes, and no second stop, teardown or reload runs. Otherwise: no Pod with this id (never added, refused before a Pod existed, or already removed and not added again since): `not_found`. The Host is closing (shutdown began): `unavailable`, `data.reason` `shutdown`, nothing changes. Otherwise a removing entry is created for the id and the sequence continues at step 3 in the same `Do`. |
| 3. Settle the launch | loop | A Pod in `starting` always belongs to a launch gate, pending or not yet requested (the batch is added before `Prepare` is called), whether the batch is the startup one or a `plugin.add`; the removal waits until that gate completes (at most one reload, 2 s, once requested). When it completes, a removing Pod is **not started**: the gate-completion handler treats it like a torn-down id, writes nothing to `husk.pods` for it, reports it `removed` to the add that launched it (if one is waiting), and runs the teardown (steps 5 and 6) in its own tick by calling the teardown handler directly: it is already on the loop, and on-loop code never posts (Wrapper invariant 5, "Host and loop boundary"). A gate cannot declare, register or reload after it has completed, so no stale declaration can reappear after step 7. The gate handler starts Wrappers synchronously (D57), so no Pod is observed in `starting` after its gate completed. A Pod in `exited` or `failed` has no process: the loop runs the teardown (steps 5 and 6) in this same tick, inside the same `Do`, again by calling the teardown handler directly; no message is posted. A Pod in `live`: continue at step 4 by starting the stop worker. |
| 4. Stop | worker | Exactly the shutdown stop for one plugin: the process is marked as stopped by Husk (so its exit classifies `exited`, never `failed`, and raises no toast), its stdin queue is flushed and closed (the plugin sees EOF), its process group gets SIGTERM, survivors get SIGKILL after the 2 s stop grace, and Husk waits at most the 1 s kill wait more. Children that left the process group are not Husk's to stop, as at shutdown. The Wrapper's ordinary notices run: the token is revoked on reap or channel close, `ReplySink.Closed` fails every pending call `cancelled` with `data.reason` `plugin_exited` ([Calls](calls.md)), `ExitMsg` writes `exited` to `husk.pods` and publishes `pods.plugin-exited`. If the process is still not reaped when the kill wait expires, liveness becomes `failed` with `last_error` `did not exit within 1s of SIGKILL`, one warning is logged, and the removal **continues**: the token is already revoked and the pipes closed, so the orphan can reach nothing. The worker ends by posting `TeardownMsg` for the id. A later `ExitMsg` for an id that is no longer a Pod is ignored. |
| 5. Tear down | loop, one tick | The teardown handler, which is the `TeardownMsg` handler reached by the stop worker's message (step 4) or called directly on the loop by step 3's two paths: revoke the token (a no-op after step 4), unregister `<id>.state` through the stored owner handle (State keeps its tombstone revision; Store flushes any pending persistent write and keeps every persisted copy, [Store](store.md) invariant 5), unregister `<id>.occupants` and `<id>.contributions`, remove the id from any pending gate (a no-op here: step 3 already settled the gate; it is `TeardownMsg`'s general contract), release the registry id, which deletes the plugin's `husk.pods` entry, and delete the Pod. Before deleting the Pod, the handler records on the removing entry the `status` the reply will carry (the rule under "plugin.* wire shapes") and the log path, `""` when no log was opened: the Pod and its `husk.pods` entry are the only other sources, and both are gone after this tick. The handler runs only when the id has a removing entry and shutdown is not marked. A message for an id with no entry is stale (its removal was completed by shutdown, invariant 7) or was posted by a test, and is ignored; after the shutdown mark, every teardown is ignored (invariant 7). All of it commits in the tick that handles the message, so one State flush carries the three `removed: true` changes and the `husk.pods` change, and the Surfaces engine's pass in that tick drops the removed owner's claims and slot entries ([State](state.md), "Surfaces resolution"). |
| 6. Undeclare | loop, same tick | Still in the teardown handler: `Store.Undeclare(id)` removes the plugin's `plugins` and `keymap` documents (its first and only caller in v1); `Store.DeclaredRevision` advances, so every reader derived from the declared documents refreshes now, before the reload: the Dispatcher's action catalog (the plugin's actions are now `not_found`, [Message kinds](message-kinds.md)), the Event bus subscription table, the hook catalog ([Hooks](hooks.md), which rebuilds on every `DeclaredRevision` change), and `Frontend.RailItems`, `Actions`, `PublishedSlots` and `SurfaceFrames`. The engine's `CatalogChanged()` is called when the published-slot catalog changed. The frontend re-registers its rail-item host actions from `RailItems`, so the removed plugin's rail items leave the palette and the Dispatcher. Then `pods.plugin-removed` is published (shape below), and the handler starts the reload worker of step 7 as a goroutine. This is the one place on-loop code starts a worker directly rather than returning a command: the teardown handler is reached from a `Loop.Do` closure and from `Host.Handle`, and neither returns a command. |
| 7. Reload | worker | The worker requests one Services reload with reason `plugin` **with the Host's context, never a request's**, and waits for it to complete (applied, failed or timed out all count as completed, as for the launch gate, D48; a reload cut short by the coordinator's close is the one exception and answers `cancelled`, invariant 7). Coalescing applies: the request joins or queues a flight exactly as [Services](services.md) says, and the flight it is answered by captured its inputs after step 6. When the reload returns, the worker posts `RemoveDoneMsg` for the id, carrying the reports (or the cancellation); it never touches the removing entry itself. The loop handles that message in one tick: it records the reports on the entry, closes the entry's completion channel, and deletes the entry. A `RemoveDoneMsg` for an id with no entry (shutdown completed it, invariant 7) is ignored. |
| 8. Reply | handler | Each handler that joined at step 2 took the entry's completion channel out of its `Do` and has been waiting on it, off the loop, under its own request `ctx`; it holds the channel, never the entry. When the channel closes it replies `{plugin, status, log, reports}` as in "plugin.* wire shapes", from the `status` and `log` recorded at step 5 and the reports recorded by `RemoveDoneMsg` (or the `cancelled` error recorded by shutdown, invariant 7), or drops the reply if its `ctx` ended first (invariant 5). Nothing counts replies: the entry was already deleted on the loop at step 7, so a request that arrives after that deletion finds no entry and no Pod and is `not_found`. |

Who drives the sequence: step 2 runs inside one `Loop.Do` and returns; every later step is driven by the message that ends the previous one, or runs inline when the previous step already ran on the loop (the gate-completion handler for a `starting` Pod calls the teardown handler directly; step 3 calls it directly for an `exited` or `failed` Pod; the stop worker's completion posts `TeardownMsg`; the teardown handler spawns the reload worker; the reload worker posts `RemoveDoneMsg`, whose handler completes and deletes the removing entry). Only workers post; on-loop code calls handlers directly or returns (Wrapper invariant 5, "Host and loop boundary"). The removing entry is mutated on the loop alone. No step waits on the loop for anything, and no handler holds the loop while it waits: the reload is requested with the Host's context and each waiting handler waits on the id's completion under its own request `ctx`, so a caller that disconnects detaches only itself (the same rule as [Services](services.md), "Waiting").

Ordering invariants, each testable:

1. A plugin's process is never reachable after its documents are undeclared: its token is revoked and its pipes closed before step 6 (steps 4 and 5 precede it), so the Dispatcher never sends `action.invoke` to a process whose catalog is gone and the launch-gate promise ("live means declared everywhere") holds in reverse. A process that survived SIGKILL (the failure row below) is an orphan Husk no longer talks to.
2. `pods.plugin-exited` (if a process was stopped) precedes `state.changed` for the four slices, which precedes `pods.plugin-removed`, which precedes `services.reloaded` for the reload of step 7. A watcher of `husk.pods` therefore sees `exited` before the entry disappears.
3. Steps 5 and 6 happen in one tick: no flush observes the slices gone while the documents remain, or the documents gone while the slices remain.
4. The reply is sent after the reload of step 7 completed, never before, so `husk plugin remove X && husk action list` never lists X's actions after the command returned 0 (the action catalog is a Store view, not a Service). When the step-7 Keybinding report says `applied: true`, `husk keys` no longer lists X's focus-local bindings either; when it says `applied: false` (a failed or timed-out reload), they stay listed, as `not_found`, until the next reload applies (the failure row below). A user-layer binding to one of X's actions stays listed, as `not_found`, until the user edits it.
5. A removal never undoes itself. If the request's `ctx` ends (the CLI disconnected or timed out) after step 2, the removal runs to completion and the reply is dropped; the next `plugin.list` shows the plugin gone.
6. Removals of different ids run independently (each has its own stop worker; their loop steps interleave by tick). Two requests for one id share one removal. An add of the id is refused while the removing entry exists (step 2 of "Adding a plugin"), so from step 2 to the end of step 7 the id is owned by the removal alone, and a `TeardownMsg` or `RemoveDoneMsg` for it can never reach a Pod the removal did not create.
7. Shutdown that begins during a removal takes over. `Host.Stop`, in the tick that marks shutdown, completes every removing entry with `cancelled`, `data.reason` `shutdown` (records the error, closes the channel, deletes the entry). Delivery of that reply is best effort, exactly as for the `cancelled` replies Calls' `Close` produces at shutdown ([Calls](calls.md)): each handler writes it under its own `ctx`, the Server's close may end that `ctx` first, and `Host.Stop` does not wait for any handler. After the mark, a `TeardownMsg` or `RemoveDoneMsg` for an id with no entry is ignored, and the teardown handler checks the mark first. If shutdown begins before step 5, nothing further runs: the shared stop covers the process, and no teardown, undeclaration or reload happens (shutdown does not undeclare either; the Store closes with the documents in memory). If it begins during step 7, the coordinator's close ends the flight. In both cases the waiting handlers are answered `cancelled`, `data.reason` `shutdown`, and no reports are returned.

What removal **fires**, following the owner-only model of [Hooks](hooks.md): events only, no hook.

| Notice | Kind | When | Payload |
| :--- | :--- | :--- | :--- |
| `pods.plugin-exited` | event | step 4, only when a live process was stopped | the Pod's `husk.pods` entry plus `plugin_id`, status `exited` (unchanged rule) |
| `state.changed` | event, one per slice | step 5, one tick | `<id>.state`, `<id>.occupants`, `<id>.contributions` with `removed: true`; `husk.pods` with its new revision |
| `pods.plugin-removed` | event | step 6, after `Store.Undeclare` | `{"plugin_id":"alpha","status":"exited"}`, `status` as in the reply. Delivered to every subscriber of `pods.plugin-removed` or `pods.*` that is live, and to CLI watchers; the removed plugin is not live, so it never receives its own removal. |
| `services.reloaded` | event | step 7 | reason `plugin`, the five reports ([Services](services.md)); CLI watchers only, as for every reload |

Removal is **not a gate**, and no `husk.before-plugin-remove` hook exists in v1. The host owns the operation, so under the owner-only rule it is the one party that could fire such a hook; it does not, for these reasons:

- The party with a stake in the operation is the plugin being removed, and step 4 already gives it what a gate would: EOF on stdin, SIGTERM and 2 s to flush, the same signal and grace it gets at instance exit, which every plugin must already handle. A hook would add a second way to say the same thing.
- Removal destroys nothing: persisted slice copies, pending flushes, grants and config survive it. The one host-fired gate [Hooks](hooks.md) specifies, `husk.before-state-clear`, guards a deletion of data a plugin may hold unsaved; nothing in removal is irreversible beyond what a restart undoes, so that reason does not apply here.
- The motivating case (D59) is a crashed plugin. It cannot answer, so a gate would be pure latency (the leg timeout) in the common case.
- A veto by another plugin over the user's decision to unload a plugin would put a plugin between the user and their own instance; the user's override (`--force`) would be the expected path, and a gate whose expected use is to be forced is the wrong shape. The after-the-fact `pods.plugin-removed` event tells other plugins what happened; a plugin that depends on another reads liveness and that event, as it must already for a crash.

Adding such a hook later is a protocol minor (a new `husk.*` hook id), so nothing is foreclosed.

What **stays** after removal, each owned elsewhere: persisted copies of the plugin's slice in every scope (Store; `Store.RemovePlugin` is not called, see the open question), secret grants ([Services](services.md), D88), the user's settings keys and keymap entries that name the plugin (now unknown-key warnings and `not_found` bindings until the user edits them), the log file (Store's GC removes it only if empty; the reply carried its path), and the refusal list of plugins that never got a Pod (`Frontend.Refusals` is not touched, because there is nothing to remove). Everything that stays is what a later `plugin.add` of the same id picks up again: the persisted slice copies hydrate its slice, a grant whose digest still matches needs no new dialog, and the settings keys and keymap entries resolve again after its reload.

Failure modes of this operation are in the table under "Failure modes" (rows marked `plugin.remove`).

#### Adding a plugin (`plugin.add`)

`husk plugin add <path>` and `husk plugin add --dir <dir>` load plugins into the running instance from the same inputs the start flags take: `path` is what `--plugin` accepts, `dir` is what `--plugin-dir` scans. A plugin installed while the instance runs is loaded without a restart, and a plugin removed with `husk plugin remove` is loaded again. It is a **load, not an install**: it writes no configuration, so it is not remembered across restarts. The persistent list of plugins is `husk.plugins.paths` and `husk.plugins.dirs` in the configuration layers ([Store](store.md), [Services](services.md)), read once at start and `restart_required` afterwards; an add neither reads nor writes them, exactly as a removal does not. A user who wants the plugin next time adds it to a layer file or passes the flag; `plugin.list` is the truth about this instance. The symmetry with removal is deliberate: both are instance-session actions on the set of Pods, both leave every file alone, and together they are the only way to relaunch a plugin in v1.

Conversion and validation are the launch's, unchanged: the converter registry claims the path (`Registry.LoadPath` for `path`, `Registry.LoadDir` for `dir`), conversion rules 1 to 7 apply, warnings are logged as at startup and also returned in the reply, and a manifest that fails conversion gets no Pod. Conversion is a pure function from files to `Spec`, so it runs on the Server handler goroutine before anything in the instance is touched; the loop is reached only with converted specs.

Terms: an **adding entry** is one per batch on the Host, loop-owned like the Pod map and the removing entries: it holds the batch's ids, a completion channel the waiting handler holds, and, once the gate completed, each id's `status` and `log` and the reload's reports. It is created at step 3 and deleted when the gate completes at step 5. A Pod belongs to at most one adding entry; a Pod of the startup batch belongs to none.

The sequence, in order. Steps 1, 2 and 6 run on the Server handler goroutine; steps 3 and 5 run on the host loop; step 4 runs on a host worker. Nothing in it blocks the loop.

| Step | Where | What happens |
| :--- | :--- | :--- |
| 1. Admit | Server handler | A caller that is not `cli` is `denied` before anything is read. Params that are not exactly one absolute path are `invalid_params` with the reasons under "plugin.* wire shapes". |
| 2. Convert | Server handler | `path`: `Registry.LoadPath(path)`. Nothing exists at the path: `not_found`. Other stat failures (including permission denied and symlink loops) on either form are `failed`, `data.reason` `conversion`, with the OS message. Something exists but no converter claims it, or conversion fails: `failed` with `data.reason` `conversion` and the converter's message; nothing in the instance has changed. `dir`: a path that is not a directory is `invalid_params`, `data.reason` `not_a_directory`; a missing one is `not_found`. `Registry.LoadDir(dir)` yields one `Result` per immediate subdirectory a converter claims (an unclaimed subdirectory is skipped silently, as under `--plugin-dir`); each with an error becomes a `refused` entry with code `conversion`. Two results with one id: the second in directory order is refused `duplicate_plugin_id` (the same first-wins rule as at startup). If no spec survives, the handler replies now, `added` and `reports` empty, without touching the loop. |
| 3. Admit the batch | loop, inside one `Loop.Do` that returns at once | The Host is closing (shutdown began): `unavailable`, `data.reason` `shutdown`, nothing changes. For each surviving spec, in directory order: an id with a removing entry is refused `removing` (the removal owns the id until its reload completes, "Removing a plugin" invariant 6); an id with a Pod in any status, `starting` of another pending gate included, is refused `duplicate_plugin_id`. In the `path` form a refusal here is the error reply `conflict` with `data.reason` set to that code and `data.plugin` to the id. If every spec was refused, the handler replies now (the `dir` form) with the refusals and no reload. Otherwise the Do closure adds the survivors as one batch (`Host.AddPod`: `starting` written to `husk.pods` for each; launch credentials are registered before each Wrapper starts), creates the adding entry, and starts the Prepare worker of step 4 through the Host's tracked worker seam, as the teardown handler starts the removal's reload worker: on-loop code never posts and a `Do` closure returns no command. |
| 4. Prepare | worker | `Host.Prepare` for the batch, exactly the startup worker: `Store.Declare` of the `plugins` and `keymap` documents for each plugin, `RegisterSlicesMsg` posted and acknowledged (step 4 of the launch; a persistent slice hydrates inside `Register`, so an id added again gets the copies its removal kept, and its revision continues past the tombstone, [State](state.md)), then one Services reload with reason `plugin`, requested **with the Host's context, never the request's**, waited for with applied, failed and timed out all counting as completed. `Store.DeclaredRevision` advanced at the declaration, so the readers derived from the declared documents refresh before the reload: the Dispatcher's action catalog (the new actions resolve `unavailable` until `live`, never `not_found`), the Event bus table, the hook catalog ([Hooks](hooks.md)), and `Frontend.RailItems`, `Actions`, `PublishedSlots` and `SurfaceFrames`; the engine's `CatalogChanged()` runs when the published-slot catalog changed, and the frontend registers the new rail items' host actions from `RailItems`, as it re-registers after a removal. The worker ends by posting `PreparedMsg`. |
| 5. Open the gate | loop, one tick | `Host.Handle(PreparedMsg)`, the launch gate as at startup: a plugin whose declaration or slice registration failed stays `failed` with that `last_error`; an id a removal claimed meanwhile is not started (removal step 3) and is recorded `removed`; every other Wrapper starts synchronously with `Loop.Opening` queued, and `husk.pods` gets `live`, or `failed` when `cmd.Start` failed. Then the handler publishes one `pods.plugin-added` event per live or failed plugin of the batch, never for a `removed` outcome (shape below), records each id's `status` and `log` and the reports on the adding entry, closes its completion channel and deletes the entry. A reload report error is logged but opens the gate, as at startup. |
| 6. Reply | handler | The handler took the completion channel out of its `Do` at step 3 and has been waiting on it off the loop under its own request `ctx`; it holds the channel, never the entry. When it closes, it replies `{added, refused, reports}` from what step 5 recorded and the refusals steps 2 and 3 collected, or drops the reply if its `ctx` ended first. |

Ordering invariants, each testable:

1. The launch gate holds for an added plugin exactly as at startup: `live` is written only after the add's own `plugin` reload completed, so by the time anything can reach the process its actions, focus-local keys, settings schema and secret declarations are in Store and in every Service snapshot. `husk plugin add X && husk keys` lists X's focus-local bindings when the Keybinding report says `applied: true`.
2. The reply is sent after every Wrapper of the batch started or failed, never before, so `husk plugin add X && husk plugin list` shows X `live` or `failed`, never `starting`.
3. `state.changed` for `husk.pods` (`starting`) precedes `services.reloaded` for the add's reload, which precedes `state.changed` for `husk.pods` (`live` or `failed`), which precedes `pods.plugin-added`. A watcher of `husk.pods` therefore sees the final status before the event that announces it.
4. One add is one batch is one reload. Two adds in flight are two batches with two gates and two reload requests; the coordinator coalesces them as [Services](services.md) says (the second joins the flight or the queued follow-up), and each gate waits for the flight that captured after its own declaration.
5. An id is in at most one batch, and never in a batch and a removal at once: step 3 runs on the loop, serialized with removal step 2, and refuses an id that has a Pod or a removing entry. Two concurrent adds of one id get one Pod and one `duplicate_plugin_id`.
6. An add never undoes itself. If the request's `ctx` ends after step 3, the batch runs to completion and the reply is dropped; the next `plugin.list` shows the plugins.
7. Shutdown that begins during an add takes over. `Host.Stop`, in the tick that marks shutdown, completes every adding entry with `cancelled`, `data.reason` `shutdown`, and a pending gate never launches after the mark ("Host and loop boundary"). Delivery is best effort, as for removal.
8. A plugin added after a removal of the same id is a new launch in every respect: a new Pod, a new token, a new log file, and `starting` written afresh; nothing of the old Pod is reused, and the old process, if one survived SIGKILL as an orphan, holds a revoked token and closed pipes.

Refusal codes, the closed set of `refused[].code` and of `data.reason` on the `path` form's `conflict` and `failed` replies:

| Code | Raised at | Condition | `path` form |
| :--- | :--- | :--- | :--- |
| `conversion` | step 2 | in the `path` form no converter claims the path; in either form conversion failed (invalid manifest, protocol major mismatch, duplicate declared ids, a bad setting key); the message is the converter's | error reply `failed`, `data.reason` `conversion` |
| `duplicate_plugin_id` | step 2 (within the batch) or step 3 (against the Pod map) | a Pod with this id exists in any status, or an earlier directory of the same batch declared the id | error reply `conflict`, `data.reason` `duplicate_plugin_id`, `data.plugin` |
| `removing` | step 3 | the id has a removing entry: a `plugin.remove` is between its step 2 and the end of its step 7 | error reply `conflict`, `data.reason` `removing`, `data.plugin`; retry when the removal has replied |

What an add **fires**, following the owner-only model of [Hooks](hooks.md): events only, no hook, for the reasons given for removal (the host owns the operation, the plugin being added is not yet live and cannot answer, and a veto by another plugin would put a plugin between the user and their instance).

| Notice | Kind | When | Payload |
| :--- | :--- | :--- | :--- |
| `state.changed` | event | step 3 | `husk.pods` with its new revision (`starting` entries) |
| `services.reloaded` | event | step 4 | reason `plugin`, the five reports ([Services](services.md)); CLI watchers only, as for every reload |
| `state.changed` | event | step 5 | `husk.pods` (`live` or `failed`) |
| `pods.plugin-added` | event, one per live or failed plugin of the batch | step 5, after the liveness change's State flush | `{"plugin_id":"alpha","status":"live"}`, `status` `live` or `failed`; a `removed` outcome emits only the removal's `pods.plugin-removed`. Delivered to every live subscriber of `pods.plugin-added` or `pods.*` and to CLI watchers; the added plugin itself is live by then and receives it if it subscribed. |

The frontend toasts nothing for a successful add: the CLI caller holds the reply, and the `husk.pods` transition into `failed` already toasts a failed start. Refusals are in the reply and are not appended to `Frontend.Refusals`, which lists the start-time refusals the frontend toasts once. The `services.reloaded` toast rule applies to the add's reload like any other.

Failure modes of this operation are in the table under "Failure modes" (rows marked `plugin.add`).

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
    Prepare(context.Context, uint64, []manifest.Spec) Preparation
    Liveness(LivenessMsg)
    Log(LogMsg)
    Do(context.Context, func()) error
}

type Preparation struct {
    Failed map[string]error // fatal declaration/slice failure for this plugin
    ReloadError error      // completed reload report; does not prevent launch
    Reports []contracts.ReloadReport // the shared config.reload report type
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

`Host.AddPod` rejects duplicates and reports `starting` through `Loop.Liveness`, without starting anything. After adding a batch, `Host.Prepare(ids...)` returns one command scoped to exactly those ids, never sweeping another batch's starting Pods. That worker declares the documents, then posts `RegisterSlicesMsg`: the loop applies each slice registration (step 4, a State write; the owner handle is stored on the Host) and acknowledges it, and only then does the worker request the reload — the batch's `plugin` flight, the first reload after the `startup` flight ([Services](services.md), "Reload"), observes every registered slice ([Store](store.md) startup order; the startup flight runs before any manifest is converted, so it sees none of them). Its `PreparedMsg` is consumed by `Host.Handle` on the loop: failed plugins (declaration or registration failures, reported once each) stay failed, a reload report error is logged but opens the gate, and surviving wrappers start with `Loop.Opening` queued before `live`. Late or duplicate gate completions never relaunch a Pod; shutdown prevents a pending gate from launching.

The stored owner handles serve two later duties. The Stem State port's write path resolves a plugin's `<plugin_id>.state` handle through the Host's slice-handle lookup (a plugin writes only its own slice, [State](state.md)); that lookup runs off the loop on a Server handler goroutine, while registration and teardown mutate the map on the loop, so this one map is guarded by a mutex that covers only the map access — the handle's own writes still run on the loop through the port's apply. `TeardownMsg` — the loop half of Pod teardown — revokes the credential, unregisters the slice through that handle (persisted copies are kept, [State](state.md) and [Store](store.md)), releases the registry id, and drops the id from any pending launch gate; a slice registration is accepted only while the gate that owns it still holds the id — a stale registration from a torn-down launch of the same id, or one arriving during shutdown, is refused rather than applied (the closure is a State write that would register a slice nobody owns, or steal one the new launch owns). Store undeclaration and the follow-up reload stay with the composition root. `TeardownMsg` is posted by exactly one production path, the removal's stop worker ("Removing a plugin", step 4); the other two paths into the teardown (a removal of an `exited` or `failed` Pod, and the gate-completion handler for a removing `starting` Pod) already run on the loop and call the same handler directly, because on-loop code never posts (Wrapper invariant 5). The reload worker that follows the teardown posts `RemoveDoneMsg`, the only other removal message. The gate handler starts Wrappers synchronously, and a composition that injects an asynchronous `Options.Starter` must keep that property, or the removal's step 3 can observe a Pod in `starting` after its gate completed (open question 7). The `PluginPort` gains `Remove(ctx, caller, pluginID)` beside `List` and `Log`, implemented by the Pods host over the same `Loop` seam, and its reply type carries the `config.reload` report type unchanged (one shared type, no copy). The Host keeps one removing entry per id, loop-owned like the Pod map: created at step 2, completed and deleted by the `RemoveDoneMsg` handler at step 7, never read or written off the loop. Handlers hold only its completion channel. The gate-completion handler skips a removing id exactly as it skips a torn-down one. `PluginPort` also gains `Add(ctx, caller, source)` ("Adding a plugin"), whose reply type carries the same report type; the Host keeps one adding entry per batch, loop-owned in the same way, created when the batch is added and completed and deleted by the gate-completion handler, which records each id's final status on it. `Host.AddPod` and `Host.Prepare(ids...)` serve the startup batch and every later `plugin.add` batch alike; the one difference is who waits: the composition root at startup, an adding entry's handler later.

`Loop.Liveness` is the explicit seam for writing `husk.pods` and publishing exit events. Workers post typed notices; `Host.Handle` applies them through `Loop.Liveness` and `Loop.Log` on the loop. `ClosedMsg` is handled by the caller with Calls cleanup before a later `ExitMsg` changes liveness. Message types are exported from `pods` (aliases of leaf Wrapper notices avoid an import cycle). Tokens are held in a worker-safe local registry, not State: they are registered before `Start` so even an immediate exit can revoke them. A `ReapedMsg` revokes authentication immediately after Wait; `ChannelClosedMsg` does the same on EOF or a closed stdin pipe, without cancelling Calls. Final stdout replies drain before normal `ClosedMsg` and `ExitMsg`, so a successful reply is not overtaken by process cleanup.

`Host.Stop` marks shutdown on the loop and returns a worker command. In the tick that marks shutdown it also completes every removing entry and every adding entry with `cancelled`, `data.reason` `shutdown` and deletes it ("Removing a plugin", invariant 7; "Adding a plugin", invariant 7); a later `TeardownMsg` or `RemoveDoneMsg` for an id with no entry is ignored, and a `PreparedMsg` after the mark launches nothing. The caller closes Calls and the Server first; that command closes stdin queues, cancels the shared process context, signals groups, and uses one absolute stop deadline plus one absolute kill-wait deadline across the whole set. Providers must keep `Post` callable, or make it return without delivery, after the UI stops; a blocked posting callback must not prevent process cleanup.

`pods/manifest.Spec` contains process fields and every plain declaration type above. `Spec.Clone` detaches all mutable declaration data. `CatalogJSON` produces the normalized `plugins` document (identity/display and declaration fields, no protocol/argv/work directory); `KeymapJSON` produces the declared `keymap` document (D64). These methods are the serialization seam for `Loop.Prepare`; this package does not implement Store/Services/State preparation itself. Manifest setting keys are single-segment `[a-z0-9_-]+` local names; dotted and already-qualified keys fail conversion. They normalize once to `plugins.<exact-plugin-id>.<local-key>` without case-folding plugin ids. This prevents a local key from overlapping another plugin's dotted-id namespace; Services separately restricts access to declared keys, never an arbitrary prefix subtree. Explicit relative executable paths resolve against the absolute manifest directory; later relative arguments resolve through `cmd.Dir`, and bare executable names retain PATH lookup.

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
- After `cmd.Start`, the Pod queues the opening notifications [Protocol](protocol.md) lists, in order and before any request: `theme.changed`, one `surface.resized` carrying every surface the plugin occupies (a single snapshot, never one message per surface), `input.key_state`. Pods cannot build them alone: the modifier state is Input bookkeeping that is not in State ([State](state.md)). So `pods.Loop.Opening(pluginID string) []protocol.Message` builds them from Theme, the `husk.geometry` rects and Input's modifier state; the Pod calls it on the loop, starts the process and queues the lines before reporting `live`. Later `theme.changed` pushes follow a reload that published a palette different from the previous one ([Services](services.md), "Theme"): the Pods host subscribes to `services.reloaded` in process, and when a report has `service` `theme` and `changed` true it reads the serving palette from Theme and sends it through each live `Channel`.

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
| `live` | `exited` | status 0, or killed by Husk during shutdown or `plugin.remove` (step 4) |
| `live` | `failed` | non-zero status, a signal Husk did not send, or Husk killed it after a stdin failure (`last_error`: `stopped reading: ...`) |

`exited` and `failed` are terminal for that launch. There is no restart in v1: a failed plugin stays failed until the instance restarts or the user removes it with `husk plugin remove`, which deletes its entry. The Pod also publishes `pods.plugin-exited`, whose data is the Pod's `husk.pods` entry plus `plugin_id`, through the publisher interface the loop injects at construction (the same way State and Services publish; Pods import nothing from Stem), for other watchers; the frontend toasts the `husk.pods` transition into `failed`, not this event.

#### Wrapper invariants

1. The Wrapper is the only writer of a plugin's stdin and the only reader of its stdout.
2. `Send` never blocks the caller. It marshals on the caller's goroutine and enqueues through `protocol.NewQueuedWriter` (capacity 1,024, 5 s write timeout, watchdog close on pipes). A private stdin mux serializes all physical writes; the single legacy rejection is a graceful-close trailer after queued v2 lines, not a second public writer.
3. Stdout carries replies only. A stdout line that is a reply goes to `ReplySink.Reply`. For a malformed reply, the Wrapper salvages the protocol reader's `LineError.ID` and forwards a bare-id message through `ReplySink.Reply`, so the Loop posts `ReplyMsg` and Calls fails a matching call with `internal` / `data.reason: "invalid_reply"` instead of waiting for its timeout. Any other valid line is logged as `plugin <id> sent a non-reply on stdout` and answered with a `protocol.error` notification on stdin; the plugin stays up. The one exception is a major-1 `hello`, which gets one major-1 `protocol_error` line and stops the plugin ([Protocol](protocol.md)).
4. A stdin failure (queue overflow, write timeout, write error) is fatal for the launch: `ReplySink.Closed` is called, the process group gets SIGTERM, then SIGKILL after the stop grace, and liveness becomes `failed`. Pipe-closed errors (`EPIPE`, closed pipe/file) can race a clean exit, so they share the injected EOF-reap grace (100 ms by default). Credentials are revoked immediately on EOF/EPIPE; once-only `ReplySink.Closed` fires after Wait and the final replies, or at reap-grace expiry, carrying the final reason: `exited` on a clean exit, `crashed` with the exit status otherwise, or `stopped reading: ...` if the child is still alive. Pending calls therefore fail at most 100 ms later. Overflow and write-deadline failures keep immediate `Closed`. A plugin that cannot read its stdin cannot be reached any other way, so keeping it alive only hides the fault. The one exception is `terminal.input` (encoded input and emulator replies): the host offers it to the queue without failing it, so a full queue drops that message and logs it, and the Pod stays `live` ([Protocol](protocol.md), "Runtime and terminal surfaces").
5. `ReplySink.Closed` is called exactly once per launch, before liveness leaves `live`, so the Dispatcher never sees a `live` owner whose pending calls are already orphaned. Post is only ever called from a wrapper goroutine, never from a method invoked on the loop. In particular, launch and opening-send failures must not synchronously call Post or ReplySink from `Start`.
6. Stdout EOF without process exit within the reap grace is treated as a stdin failure (rule 4): the reply channel is gone. `EOFReapGrace` defaults to 100 ms and uses the injected clock, allowing an already-exiting child to be reaped without misclassifying its clean exit; it is the same option used for pipe-closed writes.
7. Background goroutines never write the log or State directly; they post to the loop, which writes both.

#### Shutdown

On instance exit: the Loop closes the Calls table first, so socket callers waiting on an action get `cancelled` replies ([Calls](calls.md)); then the Server stops accepting and unregisters the instance (entry, then socket); every Wrapper closes its stdin queue (flush, then close, so the plugin sees EOF) and the shared plugin context is cancelled, which sends SIGTERM to every process group; one 2 s deadline covers all plugins; survivors get SIGKILL; Husk waits up to 1 s more for those. Exits during shutdown are `exited`, never reported as crashes. Non-empty logs are kept and their paths logged, as today; empty ones are removed by Store's GC ([Store](store.md)), since only Store deletes files.

Process supervision remains attached to the group after its leader is reaped: children without inherited stdio still receive SIGTERM and, if necessary, SIGKILL after the grace. The group is polled every 50 ms on the injected clock; an empty group completes promptly and cancels escalation without a false survivor warning. Observing the group empty fences all later signals immediately, independently of reader/final-notice completion, avoiding a later signal to a reused pid or process-group id. Every SIGKILL outcome, including a signalling error, terminates the group wait; errors are logged. SIGKILL does not close stdout immediately: buffered replies drain until EOF or the injected kill-wait deadline. The launch's completion stays pending through that cleanup, including during the Host's shared shutdown deadline. Native group signalling is supported on Linux and macOS; unsupported platforms refuse launch rather than falling back to killing only the leader.

### Decoration (boundary only)

Decoration takes one surface's content (the cells or lines the plugin supplied through Surfaces), the surface's frame spec (border, label, close, from the plugin's surface declaration), the theme styles for chrome, and the outer rect. It returns decorated cells for the outer rect and the inner rect the content occupies, which Surfaces reports to the plugin as its size. It is a pure function of those inputs: no State reads, no I/O, no plugin calls. The Renderer calls it per surface per frame. Its exact types, whether it lives per Pod or as one shared function, and whether plugins can supply their own decorations are undecided; today's `drawChrome` in `pkg/workbench/render.go` is the behaviour it starts from.

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
| `converter_unavailable`: executable is neither absolute nor resolvable on PATH, or cannot be executed | Root at start | Converter skipped | Logged once and surfaced through `Frontend.Refusals` for a warning toast |
| `converter_failed`: detect/convert exits non-zero (other than detect exit 1), exceeds the 5 s deadline, prints an unparsable document, supplies a command that is not absolute argv, or exceeds 1 MiB on stdout/stderr | Registry | That plugin only, except a `husk.plugins.paths` entry (`--plugin` and `--add-plugin` included), which is fatal at start ([Services](services.md)); otherwise other plugins continue | Exit status or deadline and first stderr line in the refusal, or in the startup error for a `paths` entry; startup toasts via `Frontend.Refusals`. At `plugin.add`: error `failed`, `data.reason` `conversion` for a path; a directory result has refusal code `conversion` |
| `discover` fails: non-zero exit, deadline, a non-absolute non-empty directory line, more than 256 non-empty directory lines, or output over 1 MiB | Root at start | That converter's discovery is lost for this start; already attempted plugins are unaffected | Logged once and surfaced through `Frontend.Refusals` for a warning toast; blank lines and trailing carriage returns are accepted |
| Manifest invalid | Converter | That plugin is not launched; others are, except for a `husk.plugins.paths` entry, which is fatal at start (conversion rule 5) | Log and a warning toast naming the manifest and the problem. No Pod exists, so there is no `husk.pods` entry: the host lists the refusal (`Frontend.Refusals`) and the frontend toasts it once |
| Duplicate plugin id | Host (plugin loading) | Second plugin not launched | Log and a warning toast `<id>: duplicate plugin id`, listed like an invalid manifest |
| Protocol major mismatch | Converter | Not launched; fatal at start for a `husk.plugins.paths` entry (conversion rule 5) | Log and a warning toast naming the manifest and both versions, listed like an invalid manifest |
| `cmd.Start` fails | Wrapper | Liveness `failed`; the catalog stays declared | One stderr report per plugin and warning toast `<id>: <last_error>` (the frontend toasts on the transition into `failed`, without repeating the stderr report). An absent explicit relative command resolved against the plugin directory records `command "<manifest executable>" not found in <plugin directory>; build it first`, including a dangling command symlink. Bare commands missing from PATH, absolute commands, missing interpreters and missing work directories retain their OS text. Pods prefixes the stderr cause with `launch failed: ` without changing `last_error` |
| Plugin exits non-zero or by a foreign signal | Wrapper | Pending calls failed via `Closed`, liveness `failed`, token revoked; the catalog stays declared | Warning toast `<id>: <last_error>`; Pods logs `last_error` with the exit status and the last 3 stderr lines, without an additional frontend failure log |
| Plugin exits 0 | Wrapper | Same cleanup, liveness `exited` | Pods logs one info-level exit line with the exit status, then the last 3 stderr lines at the same level; no frontend duplicate or toast |
| Plugin stops reading stdin | Stdin queue | Rule 4: immediate once-only `Closed` for overflow/deadline, kill, `failed` with `last_error` prefixed `stopped reading: ` | Warning toast `<id>: stopped reading: protocol: send queue full (1024 messages)` or `<id>: stopped reading: protocol: write timed out after 5s` |
| EOF/EPIPE or closed pipe/file | Wrapper | Credentials revoked immediately; once-only `Closed` after Wait and final replies, or at reap-grace expiry; pending calls fail at most 100 ms later | Final reason `exited`, `crashed` with status, or `stopped reading: ...` while the child survives |
| Plugin writes a non-reply or malformed line on stdout | Wrapper reader | Line dropped, `protocol.error` notification sent | Plugin sees the notification; log line |
| Reply with an unknown or expired id | Calls | Dropped | Debug log only |
| Plugin ignores SIGTERM | Shutdown, `plugin.remove` step 4 | SIGKILL after 2 s | Log `did not exit within 2s of SIGTERM; killing it` |
| `plugin.remove` from a plugin, Stem or zero-kind caller | Handler | Nothing looked up | `denied` |
| `plugin.remove` with a missing or empty `plugin` | Handler | Nothing done | `invalid_params` |
| `plugin.remove` for an id with no Pod (never added, refused before a Pod existed, already removed) | Loop (step 2) | Nothing done | `not_found` |
| `plugin.remove` while the Host is already closing | Loop (step 2) | Nothing done | `unavailable`, `data.reason` `shutdown` |
| `plugin.remove` for an id whose removal is in progress | Loop (step 2) | Joins it; nothing runs twice | The same reply as the first caller, when it completes |
| `plugin.remove` for a `starting` Pod whose launch gate is pending | Loop (step 3) | Waits for the gate; the gate does not start it | Reply after the sequence completes; `status` `starting` |
| `plugin.remove` for a `starting` Pod whose launch gate is not yet requested (added, `Prepare` not yet called) | Loop (step 3) | Waits for the gate to be requested and to complete; the gate does not start it | Reply after the sequence completes; `status` `starting` |
| Process survives SIGKILL past the 1 s kill wait during `plugin.remove` | Stop worker | Liveness `failed`, `last_error` `did not exit within 1s of SIGKILL`; removal continues | Warning log line; reply `status` `failed` |
| Reload of `plugin.remove` step 7 fails or times out | Coordinator | Documents are already undeclared; the next reload of any reason reads the catalog without the plugin | Success reply whose `reports` carry `applied: false` and the problems; the `services.reloaded` toast rule applies |
| Request context ends (CLI disconnect or timeout) after step 2 of `plugin.remove` | Handler | The removal completes anyway; the reply is dropped | The CLI's transport error; `husk plugin list` shows the plugin gone |
| Shutdown begins during `plugin.remove`, before step 5 | Loop (`Host.Stop`'s mark completes the entry) | The shared stop takes over; no teardown, undeclaration or reload | `cancelled`, `data.reason` `shutdown`, no reports |
| Shutdown begins during `plugin.remove` step 7 | Coordinator `Close` | The flight ends; teardown and undeclaration already happened | `cancelled`, `data.reason` `shutdown`, no reports |
| `plugin.add` from a plugin, Stem or zero-kind caller | Handler | Nothing read | `denied` |
| `plugin.add` with both or neither of `path` and `dir`, an empty or non-string value, or a relative path | Handler | Nothing done | `invalid_params`, `data.reason` `path_and_dir`, `missing_path` or `relative_path` |
| `plugin.add` whose `path` or `dir` does not exist | Handler (step 2) | Nothing done | `not_found` |
| `plugin.add` whose `dir` is not a directory | Handler (step 2) | Nothing done | `invalid_params`, `data.reason` `not_a_directory` |
| `plugin.add` `path` that no converter claims or that fails conversion (invalid manifest, protocol major mismatch) | Converter (step 2) | Nothing in the instance changes; the manifest problem is in the message | `failed`, `data.reason` `conversion` |
| `plugin.add` `dir` with subdirectories that fail conversion | Converter (step 2) | Those get no Pod; the rest proceed | Success reply; each failure is a `refused` entry with code `conversion` |
| `plugin.add` `dir` with no plugin, or every one refused | Handler (step 2 or 3) | No batch, no reload | Success reply with `added: []`, `reports: []` and the refusals |
| `plugin.add` for an id that has a Pod (any status) or appears twice in the batch | Loop (step 3) or converter order (step 2) | That plugin gets no Pod | `path`: `conflict`, `data.reason` `duplicate_plugin_id`; `dir`: a `refused` entry with that code |
| `plugin.add` for an id whose removal is in progress | Loop (step 3) | That plugin gets no Pod; the removal is untouched | `path`: `conflict`, `data.reason` `removing`; `dir`: a `refused` entry with that code. Retry after `husk plugin remove` returned |
| `plugin.add` while the Host is already closing | Loop (step 3) | Nothing done | `unavailable`, `data.reason` `shutdown` |
| Declaration or slice registration fails for an added plugin | Prepare worker (step 4) | The Pod stays `failed` with that `last_error`, as at startup; its documents, if declared, stay declared until removed | Success reply; `added[].status` `failed`; the `husk.pods` transition toasts |
| `cmd.Start` fails for an added plugin | Wrapper (step 5) | Liveness `failed`; the catalog stays declared | Success reply; `added[].status` `failed`; warning toast `<id>: <last_error>` |
| Child spawned but failed before becoming live during opening | Host (step 5) | The gate records one failed terminal fact; the later process exit enriches pid, exit status, stderr tail and log only, preserving the first error and emitting no second exit event | `added[].status` `failed`; the first error remains in `husk.pods`; later reap details may update that entry without another failure report |
| Unexpected registry error while admitting a `plugin.add` batch | Loop (step 3) | Earlier admitted members settle their gate; the remaining members are not admitted | `internal`; `data.added` lists the already admitted ids in sorted order |
| Reload of `plugin.add` step 4 fails or times out | Coordinator | The gate opens anyway; the documents are declared and the next reload of any reason reads them | Success reply whose `reports` carry `applied: false` and the problems; the `services.reloaded` toast rule applies. Until a reload applies, the Keybinding snapshot lacks the plugin's focus-local keys and Settings its schema |
| `plugin.remove` for an added id while its gate is pending | Loop (removal step 3) | The gate does not start it; the removal tears it down | The add's reply lists it with `status` `removed`; the removal replies `status` `starting` |
| Request context ends (CLI disconnect or timeout) after step 3 of `plugin.add` | Handler | The batch completes anyway; the reply is dropped | The CLI's transport error; `husk plugin list` shows the plugins |
| Shutdown begins during `plugin.add` | Loop (`Host.Stop`'s mark completes the entry) | A pending gate never launches; the shared stop covers any started process | `cancelled`, `data.reason` `shutdown`, no reports |

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
| Stop grace (SIGTERM to SIGKILL) | 2 s | unchanged; shared by shutdown and `plugin.remove` |
| Kill wait after SIGKILL | 1 s | unchanged; only matters for unreapable processes; shared by shutdown and `plugin.remove` |
| `plugin.remove` wall time | at most 2 s (a pending launch gate) + 2 s + 1 s (stop) + 2 s (reload) plus loop hops | no bound of its own: the sum of the bounds above; the CLI default of 10 s ([Instances](instances.md)) covers it |
| `plugin.add` wall time | conversion (reading the plugin's own files) + 2 s (reload) + the Wrapper starts plus loop hops | no bound of its own: conversion is bounded by the manifest limits and the reload by the coordinator's deadline; the CLI default of 10 s covers it |
| Plugins per `plugin.add` batch | the subdirectories of one directory | the same as `--plugin-dir`; no cap of its own |
| EOF / pipe-close reap grace | 100 ms | avoids racing a clean process exit against stdout or stdin pipe closure; one injected option |
| Stderr tail on crash | 3 lines from the last 8 KiB | unchanged |
| Log file name attempts | 100 | unchanged |
| Token | 32 random bytes, hex | unguessable; fits any env |
| Plugin id | 64 characters | keeps log names readable |
| Instance name | 32 bytes ([Instances](instances.md)) | keeps socket paths under `sun_path` |

No per-request timeout lives in the Server. A request waits as long as its handler allows; the Dispatcher's timeout comes from [Calls](calls.md).

### What the CLI gets

- The socket and registration entry (`<base>/<id>.sock`, `<base>/<id>.json`) that [Instances](instances.md) resolves.
- `session.ping`, `plugin.list`, `plugin.log`, `plugin.remove`, `plugin.add`. `husk plugin remove <id>` prints the reply in the generic table rendering ([Instances](instances.md)); exit 0 whatever the `reports` say, exit 1 on an error reply. `husk plugin add <path>` and `husk plugin add --dir <dir>` send `plugin.add` with the argument made absolute against the CLI's own working directory (the instance's working directory is not the shell's, so a relative path would name the wrong file; the instance refuses one with `invalid_params`); the CLI checks nothing else about the path. The full result is printed on stdout, including mixed outcomes, refusals and `applied: false` reports. If any admitted plugin has `status: failed`, the CLI also reports `failed` on stderr, names those plugins, and exits 1: the command asked for that plugin to run and it did not, so the request failed even though the reply is a success. Otherwise a success reply is exit 0, `refused` entries and `applied: false` reports included (a refusal changed nothing, and a report is the reload's, not the plugin's). An error reply is exit 1. This CLI outcome check does not change the wire reply or roll back the batch. Giving both the positional and `--dir`, or neither, is exit 2.
- Attribution: the CLI forwards `HUSK_PLUGIN_TOKEN` when present, so the same `husk` command is a user call from a shell and a plugin call from inside a plugin.

## Level 2: Migration from today

Host contracts. `pkg/host/contracts` holds the consumer-defined Go interfaces the host's adapters and composition root code against: the adapter `Handler` and `Notifier` (same shapes as the Server's; the caller is `protocol.Caller` on both sides, so nothing converts it), one owner port per method family (`StatePort`, `CallsPort`, `SessionPort`, `SettingsPort`, `SecretsPort`, `ProfilePort`, `ThemePort`, `KeybindingPort`, `ConfigPort`, `EventPort`, `HookPort`, `PluginPort`, `SurfacePort`; `MethodHomes` maps every wire method to its one port), the launch seams for the steps above (`PodRegistry`, `CatalogDeclarer`, `Reloader`, `SliceRegistrar`, `WrapperStarter`), the `Limiter` for the 20 calls per second per plugin rule, the `Config` and `Host` of the composition root, and `Frontend` with its value types (see Frontend seams). Ports return errors that carry a wire code (`CodedError`); adapters map by code and never read error text. The package imports neither Services nor the Server.

Composition root. The v2 host boots from `pkg/host.New(contracts.Config)`, not from `cmd/husk`. `New` builds, in order, Store (with instance-layer documents from flags), State and Services, runs the synchronous startup flight, resolves plugin specs from `husk.plugins.paths` / `husk.plugins.dirs` through the converter registry, then builds the Stem modules (Calls, Dispatcher, Hook runner, Event bus, Surfaces), Pods and the Server, registers every adapter from `pkg/host/handlers` on one Mux and the Mux on the Server for each inbound prefix of `MethodHomes` except `session` (the Server answers `session.ping` from its registration entry), and wires the launch seams. It listens and starts nothing until `Run`, which starts the Server and adds the configured plugins as one launch batch. The caller owns the Bubble Tea loop: it posts what `Config.Post` receives back into the host through `Update` on the loop goroutine. `Close` is idempotent and follows [Shutdown](#shutdown): the Calls table closes, the Server stops and unregisters, plugins stop, then the final flush and Store close (the Store stays open if host workers are still running after a bounded wait, so no worker sees it closed). `Config.Post` may block for backpressure, but once the loop owner has closed its post gate it returns false without blocking; the owner closes that gate before calling `Close` from `Update`, and `pkg/workbench` honours that precondition. A failure in any stage closes what was already built. The root also owns the content of the Pod opening (`theme.changed`, `surface.resized`, `input.key_state`), registers each plugin's occupants and contributions declaration slices from its manifest before the launch batch's `plugin` flight (after the `startup` flight, which precedes manifest conversion) and drops them with the Pod, and calls the Surfaces engine's `CatalogChanged()` whenever the published-slot catalog changes. `pkg/workbench` runs on this root, using the host constructor the binary supplies in `workbench.Options`.

Plugin removal. **Landed:** `plugin.remove` is `PluginPort.Remove` on the Pods Host (`pkg/pods/removal.go`), and `husk plugin remove <id>` is the CLI command. The Host keeps a removing entry per id; the stop worker reuses the shutdown stop policy (`stopProcesses`, shared with `Host.Stop`); `TeardownMsg` carries `Unreaped` when the process survived SIGKILL and runs only for an id with a removing entry, never after the shutdown mark; `RemoveDoneMsg` completes the entry after the reload. The composition root supplies what Pods may not import through `Options.Workers` (`Undeclare`: hook subscriptions, declaration slices, `Store.Undeclare` and the catalog refresh; `Reload`: one Services reload with reason `plugin`; `Go`: a tracked worker). A Pod owned by an injected Starter has no process to wait for, so teardown records it `exited` and publishes `pods.plugin-exited` itself. `Host.Teardown()` is gone: teardown has one entry, the `TeardownMsg` handler. The reload reports are the `config.reload` report type, built by one function (`ports/services.Reports`) shared with `config.reload`.

Plugin addition. **Landed:** `plugin.add` is `PluginPort.Add(ctx, caller, source)` on the Pods Host beside `Remove`, and `husk plugin add <path>` / `husk plugin add --dir <dir>` is the CLI command (its own 10 s default timeout, like `plugin remove`; the CLI makes the path absolute). The method is served by `handlers/plugin`, homed CLI-only, and first advertised by protocol minor 2.2. The Host gains an adding entry per batch; the port runs conversion on the handler goroutine through the same converter registry the composition root uses at start (the registry moves behind a seam the Host is handed, `Options.Converter`, so Pods still import no conversion wiring); `Host.AddPod` is the shared registration path and `Host.Prepare(ids...)` scopes each launch gate to its batch; the Prepare worker is started from the `Do` closure through `Options.Workers.Go`, and `Options.Workers.Added` attaches the startup delivery bookkeeping; the gate-completion handler in `Host.Handle` completes the adding entry after starting the Wrappers and publishes `pods.plugin-added` through `Options.Publisher`. The refused list and the `removed` status are the only new reply vocabulary. Coverage lives in `pkg/pods/addition_test.go` and `addition_admission_test.go` (conversion, admissions, gate scoping, removal, shutdown and disconnect), `addition_registration_test.go` (gate ownership and registry failures during admission), `addition_completion_test.go` and `opening_exit_test.go` (one terminal failure per launch), `pkg/host/handlers/plugin/addition_test.go` (params and caller kinds), `pkg/cli/plugin_add_test.go` (paths, flags and exits), `TestPluginAddRealHostCatalogEventOrderAndPersistentReadd` (real wrappers, keys, event order, persisted slices, fresh credentials/logs and retained grants), and `TestPluginAddDirectoryPublishesEveryOutcomeWithoutCoalescing` (mixed outcomes and refreshed subscriptions). The shared Services coordinator supplies its tested capture/coalescing semantics. Tests pin: the gate holds for an added plugin (`live` only after its reload; `keys.list` sees its bindings when the report applied); the reply follows the Wrapper start (`plugin.list` never shows `starting` after `add` returned); one add is one reload (two adds coalesce as the coordinator says and each gate waits for a flight captured after its declaration); a duplicate id is refused against the Pod map, against a `starting` Pod of another batch, and within one directory; an id with a removing entry is refused `removing` at every step of the removal and loads once the removal replied; a removal during the add's gate yields `removed` in the add's reply and `starting` in the removal's; a re-add after removal hydrates the persisted slice, continues the revision past the tombstone, gets a new token and log, and keeps its grant without a dialog; conversion failure in the `path` form changes nothing in the instance (`failed`, no `husk.pods` entry, no declared document, no reload); an empty or wholly refused directory runs no reload; shutdown during the gate answers `cancelled` and launches nothing; a disconnected caller does not stop the batch; the event order of invariant 3; the CLI absolutizes a relative argument and rejects both or neither of positional and `--dir`.

Frontend seams. A frontend (the TUI in `pkg/workbench` or any embedding application) drives the host through `Host.Frontend()`, a `contracts.Frontend` the root implements; it depends on that contract, `pkg/protocol` and `pkg/substrate/state`, never on Stem, Pods or the Services and Store implementations. The code that constructs the host may use their value types and helpers through host options: `pkg/workbench` imports `pkg/host` for the constructor seam in `workbench.Options` and supplies its `secrets.Prompt` to `secrets.Answer` dialog through `host.WithPrompt`; `cmd/husk` applies those options while constructing the host and validates `--profile` with `store.ValidProfile`. The existing `Snapshot` and `OwnSlices` methods return `*state.Snapshot` and `*state.Handle` values; those types are now public, so embedders can use the seam directly without any API change. Each method documents whether it is loop-only or callable from any goroutine. In summary:

- **Content.** `Config.Content` is a `ContentSink`, `func(plugin string, params protocol.SurfaceUpdateParams)`. The Surface port calls it on the loop, after `surfaces.Engine.SurfaceUpdate` accepts and only then, with the authenticated plugin id and the params unchanged (title, subtitle, badge, lines, hit targets, status meta). A denied, unoccupied or invalid update never reaches it. Surface buffers are not State; the sink is the only route they take to the renderer. A nil sink drops content.
- **Reads.** `Snapshot` (State, any goroutine), `StartupReports` (a detached copy of the five reports of the construction-time `startup` flight, unchanged by later reloads, for the startup toast; [Services](services.md), "Reload"; any goroutine), `Palette` (the Theme service), `PaletteANSI` (landed: the palette and a detached index map from one Theme snapshot read, an in-process seam independent of the minor 2.4 wire field, [Built-in themes](themes.md); any goroutine), `Resolve`, `Hints` and `KeysFor` (the Keybinding service, pure and lock-free, any goroutine). The Keybinding results are carried by the small `KeyContext`, `KeyResolution` and `KeyHint` types in `contracts`, reusing `KeyEntry` for the binding, because `contracts` may not import Services; protocol values are used as they are.
- **Themes and the one setting write (server ports, shared result types and frontend seams landed; protocol minor 2.3 landed).** `Themes() []contracts.ThemeEntry` returns what `theme.list` returns (the serving Theme snapshot's themes in `theme.list` order; any goroutine), where `contracts.ThemeEntry` is `{Name, Source string; Current bool}` with JSON tags `name`, `source`, `current`, the shared type of the wire result. `SetSetting(key string, value any, reply func(contracts.SettingSetResult, error))` is `settings.set` with a `host` caller ([Services](services.md), "Writing one key"), where `contracts.SettingSetResult` is `{Key string; Value any; Source string; Reports []ReloadReport}` with JSON tags `key`, `value`, `source`, `reports`, following `ProfileUseResult`: it runs the write and the reload off the loop on a tracked host worker and calls `reply` on the loop exactly once, never synchronously on the caller's stack, with the result or a `CodedError`; the frontend never blocks on it. Shutdown is the one exception to exactly once: a write whose worker finishes after shutdown began replies `cancelled` with `data.reason` `shutdown`, and a reply is dropped when the worker is refused or the post gate has already closed. The Settings center is their only caller in v1 ([Settings center](settings-center.md); its Theme tab is the theme selector, [Services](services.md), "The theme selector", and its General tab writes every other registered key). On the Server side the same two methods are `ThemePort.List(ctx, c Caller) ([]ThemeEntry, error)` and `SettingsPort.Set(ctx, c Caller, key string, value any) (SettingSetResult, error)`, routed by the existing `settings.` and `theme.` prefixes; in `MethodHomes`, `theme.list` is homed `anyCaller` like `theme.get` and `settings.set` is homed `cliOnly` like `profile.use` (the frontend reaches `Set` through this seam, not through the adapter, so the home's caller kinds follow the protocol Direction column).
- **Settings center seams (landed; in-process seams, no wire change; [Settings center](settings-center.md), "Frontend seams").** `SettingsSchema()` (every schema entry, as `settings.schema` with no owner), `Setting(key)` (`settings.get` for the host: value and source), `KeyListing()` (`keys.list` as a `cli` caller sees it, states included; the seam calls the existing Keybinding port with a `cli` caller, sharing the handler's listing path), `Profiles()` (`profile.list`) and `Catalog()` (**landed**: `contracts.CatalogResult`, the validated `catalog` document's entries with its problems, from a cache the host warms on a worker after the startup flight and after every `services.reloaded`, never read from Store on the loop, [Store](store.md) invariant 6) are reads from any goroutine; `ConfigDir()` is the user layer's configuration directory. `CatalogRevision()` is an atomic, any-goroutine cache-publication token: it advances only after an accepted catalog worker publishes, including after the corresponding reload event, so idle frontend updates need not copy `Catalog()`. `ServicesRevision()` is a counter the host advances on every `services.reloaded` event delivered on the loop (unchanged and failed reloads included), readable from any goroutine with no I/O; it is a cache-invalidation token for the center's sources, not a wire method. `UseProfile(name string, reply func(contracts.ProfileUseResult, error))` is `profile.use` and `RemovePlugin(id string, reply func(contracts.PluginRemoval, error))` is `plugin.remove` (`PluginPort.Remove` with a `host` caller), both running the method off the loop and calling `reply` on the loop exactly once with the result or a `CodedError`, like `SetSetting`. As with `settings.set`, the frontend reaches `Remove` through this seam and not through the adapter, so `plugin.remove` stays homed `cliOnly` in `MethodHomes` while the port itself admits `cli` and `host`. The frontend still imports neither Services, Pods nor Store.
- **Actions.** `InvokeAction(action string, args map[string]any, kind protocol.CallerKind, focus *protocol.FocusContext, reply)` runs an action through the Dispatcher with the caller kind the frontend passes: `protocol.CallerInput` for a key binding Input resolved (the binding's kind travels with it; a key binding is `input` everywhere, never `host`), `protocol.CallerHost` for an action the host starts itself (a palette choice, a rail item). `focus` is the `protocol.FocusContext` Input read on that key press ([Protocol](protocol.md), "FocusContext": one type, wire-visible as `params.focus`, no per-layer copy) and is required when `kind` is `input` (the Dispatcher uses it instead of State's focus, [Message kinds](message-kinds.md)); it must be nil for `host`. The two mismatches are composition bugs and are rejected, not repaired: `input` with a nil `focus`, or `host` with a non-nil `focus`, dispatches nothing; `reply` runs synchronously with a `CodedError` `invalid_params` and `Root.InvokeAction` logs one line naming the kind (nothing reaches the Dispatcher). A kind other than `input` or `host` is `denied`, the Dispatcher's answer to any unknown caller kind ([Message kinds](message-kinds.md), permission rule). It answers on the loop exactly once, possibly before it returns (always before, for a host action), with the result, a `CodedError`, or the host-only declined flag: `reply func(result map[string]any, declined bool, err error)` ([Keybindings](keybindings.md)). `RegisterHostAction` binds the in-process handler (it declines a key by returning `contracts.ErrDeclined`, which becomes `ActionReply.Declined` and never a `CodedError`) of a `husk.*` action or of a declared rail item id on the Dispatcher and lists it in the action catalog under the host, by id only; plugins cannot invoke host actions. It returns an error: any other id is `invalid_params`, and an id a declared plugin owns as an action is `conflict`, because the Dispatcher consults host actions before plugin documents and accepting it would shadow the plugin. A rail item action stays listed while its plugin is dead. Both are loop-only.
- **Delivery.** `SendKey`, `SendMouse` and `SendKeyState` send `input.key`, `input.mouse` and `input.key_state` to one plugin's Pod. An unknown plugin is `not_found`, a plugin that is not live is `unavailable`, params that fail validation are `invalid_params`. Loop-only, because the Pod map is.
- **Terminal surfaces (landed in protocol 2.5).** The host owns one terminal emulator per attached surface, in a Stem package (`pkg/stem/terminals`) built on `github.com/charmbracelet/x/vt` (an `Emulator` with `Write`, `Resize`, `Draw` into an Ultraviolet screen, `SendKey`, `SendMouse`, `Paste`, an input pipe of the bytes it encoded, scrollback and mode callbacks); chosen because it draws into the same cell model as the frontend's compositor and encodes keys from the same event types the frontend already has, so no second key spelling appears. Through the seam: `TerminalView(surface)` returns a `TerminalDrawer`, or nil for a line surface; the drawer's `Draw` receives the frontend's canvas and clipping rect (loop-only; the emulator is resized to the rect the frontend reports in `husk.geometry`, and the host sends `surface.resized`); `SendTerminalKey`, `SendTerminalMouse` and `SendTerminalPaste` feed the emulator, which encodes for the application's modes and whose output the host forwards as `terminal.input` to the occupant's Pod (so the Pod map rule above applies). `terminal.write` is served by a `TerminalPort` (`TerminalAttach`, `TerminalWrite`, `TerminalDetach`) in `contracts`, plugin-only in `MethodHomes`; `surface.open` is served by the Surface port from protocol 2.8; the engine records `husk.surfaces.terminals` and the capture mode ([State](state.md)). A terminal's screen is never State and never passes through `Config.Content`. `ctrl+]` releases capture for one keypress or 1500 ms. While any terminal has focus the frontend suppresses `input.key_state` broadcasts for all plugins (not just its occupant); ordinary broadcasts resume after focus leaves or detach. The drawer is loop-only; its background input/query-response drain is joined before detach, close, occupant loss or shutdown completes. `terminal.write` requests acknowledge emulator ingestion, enabling bounded plugin backpressure without waiting for a paint.
- **Runtime surfaces (landed in protocol 2.8).** `SurfacePort.SurfaceOpen` serves plugin-only `surface.open`. The Surfaces engine owns runtime declarations, occupancy, the modal singleton and close cleanup. The loop-only frontend seams `RuntimeSurfaces` (detached declarations in opening order) and `CloseSurface` (host force-close) let the frontend place modal and canvas surfaces, hide and restore the global canvas stack, and route user close controls through the same engine release path. Runtime geometry, visibility and focus use the same State writes and resize delivery as layout surfaces. Modal dimensions are outer-frame requests; the sizing bounds and tiny-terminal fallback are owned by [Protocol](protocol.md), "Runtime and terminal surfaces".
- **Catalog.** `RailItems` lists every declared plugin's rail items from the declared `plugins` documents, sorted by plugin id and then in manifest order; it is any-goroutine and changes when a launch gate or a teardown re-declares a plugin.
- **Declared catalog and toasts.** `RailItems`, `PublishedSlots` (kind, and the rect of a region) and `SurfaceFrames` (each plugin's `surfaces[].frame`) and `Actions` (each declared action's plugin, id, title and description, which the palette lists for live plugins) read the declared `plugins` documents, decoded once per Store declared-documents revision (`Store.DeclaredRevision`); `ActiveToast` returns the oldest live entry of the Surfaces engine's toast queue and expires stale ones on the host clock (loop-only). `Planned()` on the root reports how many plugins the arguments resolved to, so a headless run knows how many Pods to wait for. A frontend that writes State without posting a host message calls `Tick()` on the root (`host.Looped`) at the end of its Update, which runs the same flushes as a host message.
- **Frontend-owned State.** `OwnSlices` registers the owner handles of `husk.layout`, `husk.geometry` and `husk.focus` once, before `Run` or later on the loop (the persistent `husk.layout` hydrates from Store's preloaded values either way), and `UpdateSurfaceView` writes the visibility, placement and overlays of `husk.surfaces`. Who owns what, and how a geometry write reaches the occupants, is in [State](state.md), "Frontend-owned slices".

| File | Fate |
| :--- | :--- |
| `cmd/husk/transport.go` | Accept loop moves to `pods/server`. `serveStreamOrigin`, `streamOrigin`, the `hello` gate and `ConnectedPlugin` registration are removed: there is no hello and no plugin stream on the socket. The loop's posting function is now the private `programRef` in `pkg/workbench`, supplied to host construction through `workbench.Options.NewHost`. |
| `cmd/husk/socket_security.go` | `listenPrivate`, `checkPeerUID`, `authorizePeer` move to `pods/server`. `createSocketDir` (random `husk-*` dir) is replaced by `instance.RuntimeDir` with the ownership check. `socketName` goes; the file is `<instance-id>.sock`. |
| `cmd/husk/peercred_*.go` | `peerUID` moves to `pods/server`. `peerPID` is removed. |
| `cmd/husk/procparent_*.go`, `peerAncestry` and `launchedID` in `cmd/husk/publications.go`, `cmd/husk/peer_ancestry_test.go`, `cmd/husk/procparent_linux_test.go` | Removed; the token replaces ancestry. |
| `cmd/husk/lifecycle.go` | `spawnPlugin`, `tailLines`, `exitStatus`, `stopPlugins` move to `pods/wrapper`; `openPluginLog` and `sanitizeLogName` move to Store as `OpenPluginLog` ([Store](store.md)). The transport branch is removed (always stdio). `pluginEnv` is rewritten for the new variables. `pluginHandoffGrace`, `handoffExpiredMsg`, `handoffPending` and `pluginExitedMsg.handedOff` are removed. `handleLifecycleMsg` toasts become a liveness write plus a `pods.plugin-exited` event. |
| `cmd/husk/registry.go` (`declareFromManifest`) | Replaced by the Pod writing the `Spec` catalog to Store. Liveness leaves the registry for the `pods` State slice. |
| `cmd/husk/main.go` | `--plugin` and `--plugin-dir` replace the instance arrays of `husk.plugins.paths` / `husk.plugins.dirs`; `--add-plugin` and `--add-plugin-dir` append to their effective configured arrays, including the named-instance file ("Plugin paths at startup"). Entries go through the converter registry after the startup flight. `--bundle <dir>` adds the bundle's `plugins/` as one more directory, loaded after them ([Bundles](bundles.md)). `--instance` and `--socket` are added (resolution rules in [Instances](instances.md)). |
| `pkg/manifest/manifest.go` | **Done (v2-cutover-retire):** deleted; it became the `husk-plugin.json/v1` converter in `pods/manifest`. `Loader.Load`, `Loader.LoadDir`, `splitCommand`, the implicit-plugin forms and command resolution (`Spec.Argv`, `Spec.WorkDir`) are kept. `CheckHello`, `TransportSocket`, `TransportStdio` and the transport validation are removed. |
| `pkg/huskipc/queue.go` | **Done (v2-cutover-retire):** moved to `pkg/protocol/queue.go`. Kept and reused for stdin and socket replies. `Send` takes a `protocol.Message` or a pre-marshalled line; the queue mechanics did not change. |
| `pkg/huskipc/read.go` | **Done (v2-cutover-retire):** moved to `pkg/protocol/read.go`. `protocol.ReadLoop` is reused for plugin stdout and socket connections. |
| `pkg/workbench/render.go` (`drawChrome`) | Starting point for Decoration; moves when its shape is decided. |
| `cmd/husk/lifecycle_test.go`, `cmd/husk/socket_security_test.go`, `cmd/husk/send_queue_test.go` | Move with their code; hand-off and ancestry cases are deleted; token, base-dir ownership and stdin-failure kill cases are added. |
| `hello` on the socket (v1 plugins) | A major-1 `hello` as the first line of a connection is answered with the major-1 `protocol_error` line and the connection is closed; the Server asks Pods (`Config.Legacy`) to fail the live pod named by `hello.plugin_id` with `version_mismatch`. No peer-PID lookup. |
| `plugins/reference`, `plugins/reference-actions` | Rewritten to read stdin and call the CLI; their manifests gain `"manifest_version": 2`. |

Removed behaviour, stated for the changelog: the socket transport for plugins (plugins no longer connect to receive messages, only to send), the `hello`/`welcome` exchange, the launcher hand-off grace, and pid-ancestry binding.

## Decisions made here

1. Caller identity is a per-launch token injected as `HUSK_PLUGIN_TOKEN` and sent by the CLI on every request; pid ancestry is removed. Reasons: portable, survives daemonizing, no pid-reuse race, O(1) per CLI call. Identity is attribution for policy, not isolation; the uid check remains the security boundary.
2. A request without a token is a user call; a request with a bad token is refused, never downgraded.
3. The runtime directory is stable (`$XDG_RUNTIME_DIR/husk/` or `/tmp/husk-<uid>/`) with an ownership and mode check, replacing the random `mkdtemp` directory, because discovery needs a known place. The rule and the registration entry live in the `instance` package ([Instances](instances.md)); the Server calls it.
4. Instance names follow Instances: lowercase, at most 32 bytes, never all digits, so they never collide with pid ids.
5. The Server routes by method prefix to handlers registered by the composition root (`pkg/host` (`New`)); it does not import Stem, State or Services.
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
19. **`plugin.remove` is an unload for this instance, not an uninstall**. It stops the process with the shutdown stop policy, tears the Pod down, undeclares the documents, reloads Services and replies with the reload's reports; it deletes no persisted slice copy, no grant, no file, and is not remembered across restarts. CLI-only on the wire, like `plugin.list` and `plugin.log`; the landed `RemovePlugin` frontend seam also admits the in-process `host` caller acting for the same user. Plugins stay denied. Rejected: a `--purge` that deletes the persisted slice copies (no v1 need; the open question records it) and a self-removal by the plugin's own token (removal is the user's decision about their instance).
20. **Removal fires events and no hook.** `pods.plugin-exited` when a process was stopped, `state.changed` for the removed slices, `pods.plugin-removed` after undeclaration, `services.reloaded` for the reload. No `husk.before-plugin-remove` gate: the stop already gives the plugin EOF, SIGTERM and 2 s, the operation destroys nothing, the motivating case is a dead plugin that cannot answer, and a veto would put a plugin between the user and their instance. The rationale is under "Removing a plugin".
21. **`plugin.add` is a live load for this instance, not an install.** It takes what `--plugin` and `--plugin-dir` take, runs the ordinary launch as one batch (one reload, the launch gate, the same conversion), and is not remembered across restarts: the persistent list is `husk.plugins.paths` and `husk.plugins.dirs`, which the add neither reads nor writes. CLI-only, like the other `plugin.*` methods. Rejected: a `--save` that appends to the user layer (Services write no configuration files in v1; the same deferral as `profile.use --save`), an automatic relaunch of a failed plugin (the user removes and adds), and an add that waits for a removal of the same id instead of refusing it (the caller already serializes `remove && add`; waiting would hide a race between two callers behind a reply that looks ordinary).
22. **The add/remove race is settled by ownership, not ordering.** An id with a removing entry belongs to the removal until its reload completes and an add of it is refused `conflict` `removing`; an id in a pending launch gate belongs to the gate and a removal of it waits for the gate, which does not start it. Neither operation ever observes a Pod the other created, so the teardown and gate messages of one cannot act on the other's Pod.
23. **Addition fires events and no hook.** `state.changed` for `husk.pods`, `services.reloaded` for the reload, `pods.plugin-added` after the gate, mirroring removal; the plugin being added cannot answer a gate before it is live, and the same veto argument applies.
24. **`plugin.add` refusals are data in the `dir` form and errors in the `path` form.** A directory load fails only the plugins that are bad, as `--plugin-dir` does, so the reply lists them; a single path is one target and its refusal is the error reply, so `husk plugin add ./x` exits non-zero when nothing was loaded. The codes are one closed set (`conversion`, `duplicate_plugin_id`, `removing`) used as `refused[].code` and as `data.reason`.

## Open questions

1. **Kill on stdin stall.** Killing a plugin that falls 1,024 messages behind is strict for a plugin that is merely slow during a burst of events. Recommended: keep the kill, and let the Event bus coalesce so events alone rarely fill the queue.
2. **Requests on stdout.** Resolved with [Protocol](protocol.md): no. A plugin that needs throughput (renders every frame) keeps one socket connection open with its token, which costs one connect, and the one-direction-per-channel rule stays simple.
3. **Catalog in Store.** The notes put the converted action catalog in Store as config. A catalog that appears and disappears with process launches is closer to runtime data than to layered config; putting it in Store couples plugin launch to a config reload. Recommended: keep it in Store as decided, as its own non-layered `plugins` namespace that does not participate in `distribution < user < profile < instance` merging, and confirm with the Store spec. Resolved in the consistency pass: the coupling is deliberate and is the launch gate (launch step 6); [Store](store.md) names the documents `plugins` and `keymap`.
4. **v1 plugins after the protocol major bump.** Old plugins speak `hello` over a socket and cannot work with stdio delivery. Recommended: bump the wire major, convert the two reference plugins, and let the v1 converter exist only to read old manifests (it still launches the plugin, which then fails visibly).
5. **Readiness.** If Surfaces needs to know a plugin has drawn its first frame, `live` on `cmd.Start` is too early. Recommended: leave readiness to Surfaces (first content received), not to liveness.
6. **Token in terminal surfaces.** Relying on a terminal plugin to drop `HUSK_PLUGIN_TOKEN` for user shells is a convention, not an enforcement. Recommended: accept it, and document it in the plugin author guide.
7. **`pid` and `started_at` provenance under an injected Starter.** Liveness `pid`/`started_at` come from the Wrapper's process info. A composition that injects its own `Options.Starter` (bypassing the built-in `Host.Start`) never builds a Wrapper, so those fields stay zero even though liveness says `live`. Recommended: leave the fields zero — a Starter owns the whole step and liveness is honest about what it knows — and revisit only if a real composition injects a Starter.
8. **Operator question: should `plugin.remove` be remembered across restarts?** This spec says no: it is an unload for this instance, and the plugin returns at the next start that names it. The alternative, a persisted "removed" list that `--plugin-dir` consults, is an installed-plugin registry Husk does not have; recommended: no, and let an uninstall be deleting the plugin's directory. The spec is written on the recommendation.
9. **Operator question: should `plugin.remove` delete the plugin's persisted slice copies (`Store.RemovePlugin`)?** This spec says no: the copies are the user's data and the plugin may be loaded again next start; `Store.RemovePlugin` therefore has no caller in v1. Recommended: no, and add an explicit `husk plugin remove <id> --purge` only when a user asks for it; until then `husk state clear <ns>` clears this instance's and the user's copies.
10. **Operator question: is "no gate" the right call for removal?** This spec fires `pods.plugin-removed` as an event and no `husk.before-plugin-remove` hook (decision 20). Recommended: keep it; a gate here would mostly be forced. Reverse by adding the hook as a protocol minor if plugins need to object or to run a hand-over before another plugin disappears.
11. **May a plugin remove itself? Resolved: no.** `plugin.remove` admits `cli` on the socket and `cli` or `host` at the in-process port; host admission for the Settings center is landed. Plugins remain denied. A plugin that wants to go away exits, which already gets it `exited` and its calls cancelled; the user decides whether its catalog stays.
12. **May a `plugin` caller call `plugin.add`?** Resolved: no, CLI-only like every `plugin.*` method; a plugin manager ships a user-run command (a `cli` caller) instead.
13. **Should `husk plugin add` offer `--save`?** Resolved: not now; the persistent list stays `husk.plugins.paths` and `husk.plugins.dirs`, revisited with `profile.use --save` once Store has a user-layer write API.
14. **Should an add wait for a removal of the same id, or a removal for a pending add?** Resolved: neither; an add of a removing id is refused (`conflict`, `removing`), and a removal wins over an add still at the launch gate (the add reports `removed`).
15. **Runtime-declared actions.** A plugin whose action set is not known at conversion would need an `action.declare` method. Every format seen so far is manifest-only and an executable converter runs at every start, so nothing needs it. Recommended: do not add it; a plugin that must change its catalog re-declares by restarting (`plugin.remove` and `plugin.add`).
