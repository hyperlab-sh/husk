---
type: Design Spec
title: "Instances and the CLI"
description: "How a Husk process becomes an addressable instance, how the husk CLI finds one, and the v1 command surface that doubles as the plugin API."
status: planned
generated: { by: claude/sonnet-5-5, at: 2026-10-22T06:00:00Z }
tags: [architecture-v2, pods]
---

# Instances and the CLI

## Level 0: What and why

One Husk process is one **instance**: its own socket, its own plugins, its own State. Several instances may run at once. Husk is a runtime an application embeds, not a daemon with clients, so two documents open side by side are two instances, not one Husk with two states.

This spec covers three things:

1. **Identity and registration.** How an instance names itself and advertises its socket in the user runtime directory.
2. **Resolution.** How the `husk` CLI decides which instance a command talks to. The order is flag, environment, discovery, and it never guesses between two.
3. **The command surface.** The v1 subcommands, the protocol method each maps to, exit codes and output format.

The CLI is also the plugin API. A plugin in any language runs `husk ...` or writes the same JSON lines to the socket. No SDK ships with Husk. A language library, if one appears later, wraps this surface from inside the plugin's process and is not part of Husk.

What this is not:

- Not a daemon. No process outlives its instance, and no instance serves another's state.
- Not a session manager. Sharing state between instances is an application's concern.
- Not the wire format. Envelopes, method schemas and error codes belong to [Protocol](protocol.md).
- Not the socket listener. The [Pods](pods.md) Server owns the listener and the connection lifecycle; this spec defines what it registers and what the CLI does with it.

Dependencies:

- Depends on [Protocol](protocol.md) for envelope and method names, on [Pods](pods.md) for the Server and Wrapper that register the socket and inject the environment, and on [State](state.md), [Calls](calls.md), [Message kinds](message-kinds.md) and [Keybindings](keybindings.md) for what each method returns.
- Depended on by plugin authors (the CLI is their API), by test harnesses, and by the Wrapper (which injects the variables defined here).

## Level 1: Contract

### Instance identity

An instance id is a string. Two forms exist:

| Form | Example | When |
|---|---|---|
| pid id | `4242` | Default. The decimal pid of the Husk process. |
| name id | `notes` | `husk --instance notes`. |

Rules:

- A name matches `^[a-z0-9][a-z0-9-]*$` and is at most 32 bytes.
- A name made only of digits is rejected, so a name never equals a pid id.
- A second instance that asks for a name held by a live instance fails to start (see failure modes).
- Only named instances are eligible for workspace snapshots (see [Store](store.md)). The id is the key.

### Runtime directory

```text
$XDG_RUNTIME_DIR/husk/        (when XDG_RUNTIME_DIR is set; if it fails the
                              private-dir check, that is an error — no fallback)
/tmp/husk-<uid>/              (only when XDG_RUNTIME_DIR is empty; short on
                              purpose, see sun_path limit)
    <id>.sock                 the instance's listening socket, mode 0600
    <id>.json                 the registration entry, mode 0600
```

The directory is created with mode 0700. Husk uses it only if `lstat` shows a real directory (not a symlink) owned by the current uid with no group or world bits; otherwise the instance refuses to start and the CLI refuses to read it. The directory is shared by all instances of the user and is never removed. Each instance removes only its own two files.

The runtime directory holds sockets and entries and nothing else, so an instance never needs to keep it alive.

### Registration entry

```json
{
  "id": "notes",
  "name": "notes",
  "pid": 4242,
  "socket": "/run/user/1000/husk/notes.sock",
  "started_at": "2026-10-22T06:00:00Z",
  "protocol_version": "2.0",
  "husk_version": "0.9.0"
}
```

`name` is absent for pid instances. `protocol_version` is the version the Server speaks (see [Protocol](protocol.md)); `husk_version` is informational.

```go
// Package instance is a leaf: standard library only. The Server, Store and
// the CLI import it; it owns the runtime directory rule, the entry shape,
// name validation and resolution.
package instance

type Entry struct {
    ID              string    `json:"id"`
    Name            string    `json:"name,omitempty"`
    PID             int       `json:"pid"`
    Socket          string    `json:"socket"`
    StartedAt       time.Time `json:"started_at"`
    ProtocolVersion string    `json:"protocol_version"`
    HuskVersion     string    `json:"husk_version,omitempty"`
}

func ValidName(s string) error
func RuntimeDir() (string, error)             // resolves and verifies, creates 0700
func CheckRuntimeDir(dir string) error        // same private-directory rule, no fallback
func SocketPath(dir, id string) string
func Register(dir string, e Entry) error      // atomic: write temp, rename
func Unregister(dir, id string) error         // removes entry then socket; ENOENT is not an error
func List(dir string) ([]Entry, error)        // reads entries, no liveness check
func RemoveIfStale(dir string, e Entry) error // removes only if the file still holds e (same pid, started_at)

type Source int // SourceFlag, SourceSocketFlag, SourceEnv, SourceDiscovery

type Resolved struct {
    Entry  *Entry // nil when resolved from a bare socket path
    Socket string
    Source Source
}

func Resolve(opts ResolveOptions) (Resolved, error) // implements the order below
```

### Start sequence (Server side)

1. Validate the name, if given. Resolve and verify the runtime directory.
2. `bind` the socket at `<id>.sock`. `bind` is the atomic claim.
   - On `EADDRINUSE`: dial the existing socket and send `session.ping`. If it answers, fail with `instance name taken`. If the dial fails with `ECONNREFUSED`, take an advisory flock on the existing runtime-directory FD, re-probe under the lock, then unlink the still-refused stale socket and bind again once before unlocking. A live replacement is never unlinked. The first bind remains lock-free, and no lock file is created.
3. `chmod 0600` the socket and start listening. The peer-uid check on accept is unchanged from today.
4. Write `<id>.json` with `Register`. An entry exists only once the socket listens, so a registered entry means ready.
5. Hand the socket path and id to the Wrapper for plugin environments.

Shutdown (normal exit, or `SIGINT` and `SIGTERM`): stop accepting, `Unregister`, then stop plugins as today. A crash or `SIGKILL` leaves the entry behind; the stale-entry rule below handles it.

### Plugin and shell environment

The Wrapper sets these in every plugin process, overwriting any inherited value:

| Variable | Value |
|---|---|
| `HUSK_SOCKET_PATH` | Absolute path of this instance's socket. Name kept from today. |
| `HUSK_INSTANCE` | This instance's id. |
| `HUSK_BIN_PATH` | Absolute path of the running `husk` binary, so a plugin can call the CLI without relying on `PATH`. |

[Pods](pods.md) adds three more for the plugin itself (`HUSK_PLUGIN_ID`, `HUSK_PLUGIN_TOKEN`, `HUSK_PROTOCOL_VERSION`); a terminal plugin drops the token before spawning a user shell. Children of a plugin inherit the rest, so a shell opened inside a terminal surface resolves to the instance that hosts it. The variables are set for every plugin regardless of how Husk talks to it (see [Pods](pods.md)): the stdio channel carries Husk-to-plugin traffic, and the socket is the plugin-to-Husk path, so it is always reachable.

### Resolution order

The CLI resolves exactly one socket per command, using the first rule that applies:

1. **Flag.** `--instance <id>` looks up `<id>.json` in the runtime directory. `--socket <path>` uses the path directly. Giving both is a usage error.
2. **Environment.** `HUSK_SOCKET_PATH` if set and non-empty; otherwise `HUSK_INSTANCE`, looked up like `--instance`.
3. **Discovery.** `List`, then ping each entry in parallel. Exactly one live instance: use it. Zero or several: fail and print the list.

Invariants:

1. Resolution stops at the first rule that applies. A rule that applies and then fails does not fall through. A stale `HUSK_SOCKET_PATH` (the shell outlived its Husk) is an error, never a silent switch to discovery.
2. Discovery with two or more live instances never picks one, whatever their age.
3. `husk instances` and `husk ping` with an explicit target skip rule 3 by definition. `husk instances` always lists, never resolves.
4. A flag naming an instance that is not registered, or whose socket does not answer, is an error naming the instance.

### Stale entries

An entry is stale when its socket cannot be reached. The CLI classifies a dial and `ping` as follows:

| Outcome | Classification | Action |
|---|---|---|
| Reply within the ping timeout | live | none |
| `ENOENT` or `ECONNREFUSED` on dial | stale | `RemoveIfStale`, skip this entry |
| Dial succeeds, no reply within the ping timeout | unresponsive | keep the entry, skip it in discovery, report it in `husk instances` |

`RemoveIfStale` re-reads the entry and removes the entry file — never the socket, which belongs to whoever bound it (possibly the next instance of the same name) — only if the file still holds the same `pid` and `started_at`. That closes the race where a new instance reuses a name between the failed ping and the unlink.

Any CLI call that sees a stale entry removes it, including `husk instances`. A pid whose process is dead does not by itself mark an entry stale (pid reuse makes that check unsafe); the failed dial does.

### Command surface

`husk` with no subcommand starts an instance (today's behaviour, with its existing flags plus `--instance <name>`, `--profile <name>` and `--distribution <dir>`). A subcommand is the first non-flag argument. Everything below is a socket client.

Global flags, accepted by every subcommand:

| Flag | Meaning |
|---|---|
| `--instance <id>` | Target instance by id (step 1 of resolution). |
| `--socket <path>` | Target socket directly. Mutually exclusive with `--instance`. |
| `--format json\|table` | Output format; default by TTY, see below. |
| `--timeout <duration>` | Total time for the command after resolution. Default per command. |

Methods use the two-segment names [Protocol](protocol.md) owns; each subcommand's arguments and reply are owned by the spec of the module it reaches. The table is the CLI's side of the mapping; a subcommand is `husk <noun> <verb>`, singular noun, matching the method namespace.

| Command | Protocol method | Reply | Default timeout |
|---|---|---|---|
| `husk instances` | none (reads the runtime dir; sends `session.ping` to each entry) | list of entries with `status` | 2s total |
| `husk ping` | `session.ping` | `{id, pid, protocol_version, host_version, uptime_ms}` | 2s |
| `husk state`, `husk state get`, `husk state dump`, `husk state watch`, `husk state set`, `husk state patch`, `husk state clear` | `state.snapshot`, `state.get`, `state.snapshot`, `state.watch`, `state.set`, `state.patch`, `state.clear` | per [State](state.md) | 5s; `watch` none (runs until interrupted) |
| `husk action list [--owner <id>]` | `action.list` | action catalog entries with `live` | 5s |
| `husk action invoke <action-id> [--arg k=v]... [--args-json <json>]` | `action.invoke` | the owner's result | 30s |
| `husk event emit <name> [--data <json>]` | `event.emit` | `{}` once accepted onto the bus | 5s |
| `husk event watch <pattern>` | `event.watch` | stream of `event.deliver` notifications | none |
| `husk hook call <id> [--data <json>]` | `hook.call` | the aggregated results | 30s |
| `husk keys [--action <id>] [--focus <surface>] [--shadowed] [--problems]` | `keys.list` | resolved keymap entries and load problems ([Keybindings](keybindings.md)) | 5s |
| `husk settings ...`, `husk theme ...`, `husk profile ...`, `husk secrets ...` | `settings.*`, `theme.*`, `profile.*`, `secrets.*` | per [Services](services.md) | 5s |
| `husk plugin list`, `husk plugin log <id>` | `plugin.list`, `plugin.log` | per [Pods](pods.md) | 5s |
| `husk config reload` | `config.reload` | `{reports: [...]}` per [Services](services.md) | 10s |
| `husk store paths`, `husk store check`, `husk store gc`, `husk store reset-secrets` | none (local, [Store](store.md)) | | |
| `husk rpc <method> [params-json]` | that method | the raw `result` | 30s |

Details:

- `action invoke` sends a request-with-reply. The Server routes it to the Dispatcher ([Message kinds](message-kinds.md)) exactly as a key press would, so one owner handles it and the reply is that owner's result or error. `--arg` pairs build a JSON object of string values; `--args-json` supplies typed params, and `-` reads them from stdin. Using both is a usage error.
- `event emit` is a request whose `{}` reply means the event was accepted onto the Event bus; delivery to subscribers stays fire-and-forget, as in [Message kinds](message-kinds.md). From a bare shell the name must start with `user.`. Without `--data`, the event carries no payload; `--data -` reads stdin.
- `state watch` first replies with the current entries of the named slices, then pushes one `state.changed` notification per change until the CLI is interrupted (`SIGINT`, `SIGTERM`) or the connection drops ([State](state.md)).
- `keys` is read-only and prints the resolved keymap from the Keybinding service. It never writes. There is no `keys adopt`, on purpose: a plugin cannot move its suggestions into the user layer through the CLI.
- `config reload` asks Services to drop caches and re-read layers through Store. Adding a plugin at runtime is a reload, not a State mutation.
- `rpc` is the escape hatch defined in [Protocol](protocol.md): any method, params as JSON, result printed raw.

Exposed to the CLI by other modules, and nothing else: the CLI reaches State, the action catalog, the keymap and config only through the Server. It imports no Stem, Services, Substrate or Pods package.

### Output format

| stdout is | Default | Behaviour |
|---|---|---|
| a TTY | `table` | Human table, one row per record, short columns. Scalars print bare. |
| not a TTY | `json` | One JSON document per command (the reply `result`). `state watch` prints one JSON object per line. |

`--format` overrides both. In JSON mode errors print to stderr as one object: `{"error": {"code": "...", "message": "..."}}`. In table mode errors print as one line `husk: <message>`. stdout carries only results.

`husk instances` rows:

```
ID     NAME   PID    STARTED               PROTO  STATUS
4242          4242   2026-10-22T06:00:00Z  2.0    live
notes  notes  5120   2026-10-22T06:02:11Z  2.0    live
```

JSON form is an array of the registration entries plus `"status": "live" | "unresponsive"`. Stale entries are removed before the list prints and do not appear.

Ambiguity and absence errors print the same list so the user can pick:

```
husk: 2 instances are running; choose one with --instance <id>
  4242   started 2026-10-22T06:00:00Z
  notes  started 2026-10-22T06:02:11Z
```

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. For `state watch`, interrupted by the user. |
| 1 | The instance replied with an error (unknown action, owner failed, bad params, permission denied). The error is on stderr. |
| 2 | Usage error: bad flags, bad arguments, `--instance` with `--socket`, invalid instance name. |
| 3 | No instance: nothing running, the named instance is not registered, or the target is stale. |
| 4 | Ambiguous: more than one live instance and no flag or environment to choose. |
| 5 | Transport: connect failed, the instance did not reply within the timeout, or the connection dropped (including `state watch` losing its instance). |
| 6 | Protocol: reply could not be parsed, or the protocol major differs from the CLI's. |

A plugin script can branch on 3, 4 and 5 without parsing text.

### Limits

| Limit | Value | Reason |
|---|---|---|
| Instance name length | 32 bytes | Fits any `sun_path` with margin and stays readable in a table. |
| Socket path length | 103 bytes | The `sun_path` limit on macOS is 104 including the terminator; Linux allows 107. Start fails with a message naming the limit. |
| Ping timeout in discovery | 500 ms, all entries in parallel | A live local instance answers in milliseconds; discovery must not feel slow. |
| Connect timeout | 2 s | A local connect that takes longer is a failure. |
| Registration entry size | 4 KiB | An entry is small; larger files are ignored as corrupt. |
| Line size on the socket | as set by [Protocol](protocol.md) (4 MiB today) | The CLI uses the same framing as every other client. |
| `state watch` backpressure | the Server's per-connection queue (1,024 messages, 5 s write deadline today) | A watcher that stops reading is disconnected like any slow client, so it cannot stall the instance. |

### Dependency directions

- `cmd/husk` (CLI mode) imports `instance` and `protocol` only. It never imports Stem, Services, Substrate or Pods.
- `instance` imports the standard library only. Server (Pods) and the CLI both import it. `instance` never imports either.
- Server imports `instance` and calls Stem, never the reverse. Stem does not know the Server exists; requests arrive as calls into Dispatcher, State reads and Event bus publishes.
- The Wrapper imports `instance` for the variable names only.

### Failure modes

| Condition | Detected by | What happens | User or plugin sees |
|---|---|---|---|
| `--instance` name invalid | Husk start or CLI parse | Exit 2 before touching the filesystem | `husk: invalid instance name "Foo_1": use [a-z0-9][a-z0-9-]*, at most 32 bytes` |
| Name held by a live instance | Server at `bind` and `ping` | Start aborts, nothing is registered | `husk: instance "notes" is already running (pid 5120)` |
| Name held by a dead instance | Server at `bind` | Stale socket unlinked, start continues | Nothing |
| Runtime directory unsafe (wrong owner, group-writable, symlink) | `instance.RuntimeDir` | Instance refuses to start; CLI refuses to read | `husk: runtime dir <path> is not private to uid <uid>` |
| Socket path too long | Server before `bind` | Start aborts | Message names the length, the limit and the directory used |
| Registration write fails | Server | `Start` fails, closes and removes its newly bound socket, and returns the error; no listening-but-undiscoverable instance is kept up | Startup error naming the registration failure |
| Zero instances (discovery) | CLI | Exit 3 | `husk: no instance is running` |
| Several live instances (discovery) | CLI | Exit 4, prints the list | The list above |
| Entry stale (`ECONNREFUSED`, `ENOENT`) | CLI | Entry file removed, socket left alone, entry skipped; exit 3 if explicitly targeted | `husk: instance "notes" is not running (stale entry removed)` |
| Instance unresponsive (dial ok, no reply) | CLI | Entry kept, exit 5 if explicitly targeted; skipped in discovery with a warning on stderr | `husk: instance "notes" did not answer within 2s` |
| `HUSK_SOCKET_PATH` points at a dead socket | CLI | Exit 3, no fallback to discovery | `husk: HUSK_SOCKET_PATH points at a socket nobody is listening on; unset it or pass --instance` |
| Peer uid differs from instance uid | Server (accept) | Connection closed (unchanged from today) | Exit 5 with `connection closed by host` |
| Method unknown to the instance | Server | `error` reply | Exit 1 with the error message |
| Protocol major differs | CLI after `ping` | Exit 6 | `husk: instance speaks protocol 3.x, this CLI speaks 2.x` |
| Instance exits during `state watch` | CLI (EOF) | Exit 5 | `husk: instance went away` |
| Two instances race for one name | `bind` | One wins; the other takes the taken-name path | As for a live holder |

### Invariants

1. One instance has exactly one socket and at most one registration entry; the id is the key.
2. An entry present in the runtime directory means its socket was listening when the entry was written.
3. An instance removes its own entry and socket on a clean exit. Only the CLI or the next Server start of the same id removes stale ones — and only the stale entry: the socket is left to whoever bound it, since after a crash and restart that may be the next instance of the same name.
4. `RemoveIfStale` never deletes a file whose `pid` and `started_at` differ from the entry that was pinged.
5. The Wrapper overwrites `HUSK_SOCKET_PATH`, `HUSK_INSTANCE` and `HUSK_BIN_PATH` in every plugin process. A plugin never sees another instance's value.
6. The CLI never resolves to an instance that the user, the environment or a unique discovery result did not select.
7. The CLI writes results to stdout and everything else to stderr, in both formats.
8. The CLI holds no state between invocations and does not cache the registry.
9. The runtime directory and every file in it are readable and writable only by the owning uid.

## Level 2: Migration from today

| Today | Target |
|---|---|
| `cmd/husk/main.go`: flag parsing, `flag.Parse()` then start | Split into start mode and CLI mode. The first non-flag argument selects a subcommand and a new `cmd/husk/cli*.go` handles it. Start mode keeps `--plugin`, `--plugin-dir`, `--dump-frame`, `--demo-frame`, `--rail`, `--hold-alt`, `--demo` and gains `--instance <name>`, `--profile <name>` ([Services](services.md)) and `--distribution <dir>` ([Store](store.md)). |
| `cmd/husk/socket_security.go`: `createSocketDir` (fresh `husk-*` mkdtemp per run), `listenPrivate`, `listenSocket` | Replaced. The socket binds at a deterministic `<id>.sock` in the shared runtime directory. `checkPeerUID`, `authorizePeer` and the `peercred_*` files stay. `createSocketDir` goes away and its 0700 rule moves into `instance.RuntimeDir`; `listenPrivate` moves into the Pods Server with its 0600 rule ([Pods](pods.md)). |
| `cmd/husk/transport.go`: `startAcceptLoop`, `serveStreamOrigin` | Moves into the Pods Server. Behaviour that serves plugin traffic is specified in [Pods](pods.md). There is no `hello` any more: every connection is served from its first request, attributed by the request's token or its absence. |
| `cmd/husk/lifecycle.go`: `pluginEnv` sets `HUSK_SOCKET_PATH` for socket plugins and removes it for stdio plugins | Always sets `HUSK_SOCKET_PATH`, `HUSK_INSTANCE` and `HUSK_BIN_PATH`, for every transport. The architecture rule "stdio removes `HUSK_SOCKET_PATH`" is dropped. |
| `$XDG_RUNTIME_DIR/husk-*` with `logs/<id>.log` kept inside it | The runtime directory holds only sockets and entries and persists across runs. Plugin stderr logs move to `$XDG_STATE_HOME/husk/logs/<id>/<plugin>.log` ([Store](store.md)). |
| `pkg/huskipc`: `ProtocolVersion` constant, `CheckProtocolVersion` | **Done (v2-cutover-retire):** `pkg/huskipc` is deleted; the check lives on as the source of the `protocol_version` field in the entry and of the CLI's major check, as `protocol.Version` in the package named in [Protocol](protocol.md). |
| `pkg/manifest` | Unchanged by this spec; deleted later in the retirement, replaced by `internal/pods/manifest`. |
| No `instance` package | New leaf package (see Decisions). |
| `docs/docs/architecture.md`, "Socket security" and "Plugin processes" sections | Updated when this lands: path layout, environment variables, stdio rule. |

## Decisions made here

1. **Pid ids and names cannot collide.** All-digit names are rejected, so `husk --instance 4242` always means a pid instance. The notes allow names matching `[a-z0-9][a-z0-9-]*`, which would otherwise allow `4242`.
2. **Claim by `bind`, register after listen.** The socket bind at a deterministic path is the atomic name claim. The entry is written after listen, so an entry always means ready, and discovery cannot see half-started instances. Two starters racing for one name cannot both win.
3. **Stale means refused or missing, not slow.** Only `ECONNREFUSED` and `ENOENT` remove an entry. A dial that succeeds without a reply is "unresponsive": kept, skipped in discovery, never deleted. A busy instance must not lose its registration to a 500 ms ping. This narrows the notes' "failed ping".
4. **No fallback after a rule applies.** A set but dead `HUSK_SOCKET_PATH` is exit 3, not a drop to discovery. This is the literal reading of "never guess", and it stops a stale shell from silently driving a different instance.
5. **`HUSK_SOCKET_PATH` stays; two siblings join it.** The existing name is kept so deployed plugins keep working. `HUSK_INSTANCE` allows resolving by id and `HUSK_BIN_PATH` lets a plugin find the binary. If both `HUSK_SOCKET_PATH` and `HUSK_INSTANCE` are set, the socket path wins.
6. **The variables are set for every plugin.** Today stdio plugins have `HUSK_SOCKET_PATH` removed. In the target, stdio carries Husk-to-plugin traffic and the socket is how a plugin calls in, so every plugin needs it.
7. **Fixed runtime directory with a short fallback.** `$XDG_RUNTIME_DIR/husk/`, else `/tmp/husk-<uid>/`, never the system temp directory. macOS temp paths are long and combined with a 32-byte name overflow the 104-byte `sun_path`. Today's fresh `husk-*` directory per run is dropped because discovery needs one well-known place.
8. **Registration is a JSON file next to the socket.** `<id>.json`, written atomically, not a lock file or a registry socket. Listing the registry is a directory read; no daemon is needed.
9. **`instance` is a leaf package, not part of Server.** The CLI must read the registry without importing Pods. Both sides share one definition of `Entry`, name validation and resolution.
10. **`event emit` gets an acknowledgement.** `event.emit` is a request ([Protocol](protocol.md)), because a CLI that cannot report whether the Server accepted the event is useless in scripts. The Server replies `{}` once the event reaches the Event bus; delivery to subscribers is a notification and gets no reply.
11. **Two-segment method names, singular nouns.** `session.ping`, `state.get`, `action.invoke`, `event.emit`, `keys.list`, `config.reload`, as [Protocol](protocol.md) names them; subcommands mirror the namespace (`husk action invoke`, `husk event emit`). The CLI table is the only place this spec depends on them.
12. **Output default by TTY, override by flag.** JSON when stdout is not a TTY, table when it is, `--format` forces either. `state watch` always emits line-delimited JSON in JSON mode.
13. **Exit codes 3 and 4 for resolution failures.** Scripts and plugins distinguish "no instance" from "several instances" without parsing text.
14. **`husk keys` is read-only.** No subcommand writes a keymap. The notes reject `keys adopt`; this spec also does not add `keys set`, because user bindings belong to the user layer file.
15. **The CLI keeps no state.** It reads the runtime directory every time. Caching would reintroduce stale-instance bugs for no measurable gain.
16. **No hello.** The Server answers requests from the first line of a connection. Today it answers `protocol_error` before a `hello`. A CLI caller carries no plugin identity unless `HUSK_PLUGIN_TOKEN` is set in its environment ([Pods](pods.md)).

## Open questions

1. **Does the socket Server need to know which plugin a CLI call came from?** Resolved in [Pods](pods.md): a per-launch token in `HUSK_PLUGIN_TOKEN`, sent as the request's `token` member; no token is the user; pid ancestry is removed.
2. **What does `husk event emit` allow from a bare shell?** Resolved: bare-shell events are accepted under the reserved `user.` prefix only; any other prefix is a `denied` reply, exit 1 ([Message kinds](message-kinds.md)).
3. **Should `husk instances` also list instances of other users?** The runtime directory is private, so it cannot. Recommended: no, by design; revisit only if a multi-user machine asks for it.
4. **Where do plugin stderr logs go?** Resolved: `$XDG_STATE_HOME/husk/logs/<id>/<plugin>.log`, opened through [Store](store.md).
5. **Windows.** Unix sockets and `lstat` ownership checks do not map directly. Recommended: out of scope for v1, as the socket already refuses platforms without peer credentials.
6. **A `husk stop` command.** An instance can be asked to quit through `husk action invoke husk.quit` ([Keybindings](keybindings.md)), but there is no first-class stop. Recommended: add none until a distribution asks; the TUI's quit action is invocable.
