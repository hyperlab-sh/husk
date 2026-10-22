---
type: Design Spec
title: "Instances and the CLI"
description: "How a Husk process becomes an addressable instance, how the husk CLI finds and talks to one, and the shell contract (flags, output, exit codes) every subcommand shares."
status: planned
generated: { by: claude/fable-5-1, at: 2026-10-22T18:00:00Z }
tags: [architecture-v2, pods]
---

# Instances and the CLI

## Level 0: What and why

One Husk process is one **instance**: its own socket, its own plugins, its own State. Several instances may run at once. Husk is a runtime an application embeds, not a daemon with clients: two documents open side by side are two instances, not one Husk with two states.

Three questions follow from that, and this spec answers them:

1. **Identity.** How an instance names itself and advertises its socket so that something outside the process can find it (the *registry*: a directory of socket and entry files).
2. **Resolution.** How the `husk` CLI decides which instance one command talks to. Flag, then environment, then discovery; it never guesses between two.
3. **The shell contract.** What every `husk` subcommand has in common: how the binary tells start mode from command mode, the global flags, how a command connects and identifies itself, output on stdout and stderr in two formats, the error object, exit codes, timeouts and limits. Plus the complete list of v1 subcommands and which spec owns each one's arguments and reply.

The CLI is the plugin API. A plugin in any language runs `husk ...` or writes the same JSON lines to the socket. No SDK ships with Husk; a language library, if one appears, wraps this surface inside the plugin's process and is not part of Husk.

What this spec is not:

- Not the wire. Envelopes, framing, error codes and method names are [Protocol](protocol.md).
- Not the socket listener. The Server in [Pods](pods.md) owns the listener, connection admission, caller attribution and routing. This spec owns the registry files it writes and the protocol it must follow to claim a name.
- Not the meaning of any command. `husk state get` means what [State](state.md) says; this spec fixes only its spelling, the method it sends and the shell behaviour around it.
- Not a session manager. Sharing state between instances is the application's concern ([Store](store.md) says what is shared).
- Not portable to platforms without Unix sockets and peer credentials. Windows is out of scope for v1, as it already is for the socket.

Dependencies: [Protocol](protocol.md) (envelope, `session.ping`, error codes), [Pods](pods.md) (the Server runs the claim protocol and the Wrapper sets the environment), and for each subcommand the spec of the module it reaches. Depended on by plugin authors, test harnesses, the Server (registry) and the Wrapper (variable names).

## Level 1: Contract

### Terms

Each term below has exactly one meaning in this spec and in the ones that reference it.

| Term | Meaning |
|---|---|
| **instance** | One running Husk process, with one socket and at most one registration entry. |
| **id** | The instance's name in the registry. Either a *pid id* (the decimal pid) or a *name* (given with `--instance` at start). |
| **runtime directory** | The one per-user directory that holds every instance's socket and entry. |
| **entry** | The JSON file `<id>.json` an instance writes once its socket listens. |
| **registry** | The runtime directory read as a set of entries. |
| **live** | An entry whose socket answers `session.ping` within the probe timeout. |
| **stale** | An entry whose socket is missing or refuses the connection. Nothing is listening. |
| **unresponsive** | An entry whose socket accepts the connection but does not answer within the probe timeout. |
| **probe** | One `session.ping` request with a 500 ms deadline covering connect, write and read. |
| **target** | The one socket a CLI command talks to, chosen by resolution. |
| **command mode** | `husk <subcommand> ...`: the binary acts as a socket client and exits. |
| **start mode** | `husk [flags]` with no subcommand: the binary becomes an instance. |
| **bare shell** | A process with no `HUSK_PLUGIN_TOKEN` in its environment. Its CLI calls are the user's own. |

### Instance identity

An id is a string of at most 32 bytes that is one of:

| Form | Rule | Example |
|---|---|---|
| pid id | decimal digits only, equal to the process pid | `4242` |
| name | matches `^[a-z0-9][a-z0-9-]*$` and is not digits only | `notes` |

Rules:

1. An instance started without `--instance <name>` has the pid id. One started with `--instance <name>` has that name.
2. A name of digits only is invalid, so a name never equals a pid id and `--instance 4242` always means the pid instance.
3. The id is the file stem of both files: `<id>.sock` and `<id>.json`. Ids are validated before they are used as file names and never sanitized.
4. Only named instances persist instance-scoped and workspace-scoped slices ([Store](store.md)). Whether an id is a name is decided by its spelling (rule 2); no flag or field records it.
5. A second instance that asks for a name held by a live instance fails to start (see the claim protocol).

### Runtime directory

```text
$XDG_RUNTIME_DIR/husk/      when XDG_RUNTIME_DIR is set and non-empty
/tmp/husk-<uid>/            when XDG_RUNTIME_DIR is empty or unset
    <id>.sock               the instance's listening socket, mode 0600
    <id>.json               the registration entry, mode 0600
```

Rules:

1. The directory is created with mode 0700 if absent. It is used only if `lstat` shows a real directory (not a symlink) owned by the current uid with no group or world permission bits. Otherwise the instance refuses to start and the CLI refuses to read it (exit 7).
2. A set but unusable `XDG_RUNTIME_DIR` is an error, never a fallback to `/tmp`. The `/tmp` form is used only when the variable is empty or unset.
3. The directory is shared by all of the user's instances and is never removed. It holds sockets, entries and nothing else; the files of a stale instance are removed by the stale recovery protocol below.
4. The socket path is at most 103 bytes (the `sun_path` limit on macOS is 104 including the terminator; Linux allows 107). A longer path fails start with a message naming the length, the limit and the directory. This is why the fallback is `/tmp/husk-<uid>/` and not the system temp directory, whose macOS value is long.

### Registration entry

```json
{
  "id": "notes",
  "pid": 4242,
  "socket": "/run/user/1000/husk/notes.sock",
  "started_at": "2026-10-22T06:00:00Z",
  "protocol_version": "2.0",
  "host_version": "0.9.0"
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | string | The instance id. Equals the file stem. |
| `pid` | integer | The Husk process pid. Informational and used by stale recovery to recognise the entry it probed; never used as a liveness test. |
| `socket` | string | Absolute path of the socket. Equals `<runtime dir>/<id>.sock`. |
| `started_at` | RFC 3339 UTC | When the instance started. |
| `protocol_version` | `"MAJOR.MINOR"` | The protocol the Server speaks ([Protocol](protocol.md)). |
| `host_version` | string | The Husk build version, informational. Same name and value as the `host_version` member of the `session.ping` result. Absent when the build has no version. |

Rules:

1. The entry is written atomically (temporary file in the same directory, then rename) with mode 0600, and only after the socket listens. An entry on disk therefore means the instance was ready when the entry was written.
2. An entry file over 4 KiB, not valid JSON, missing `id`, `pid` or `socket`, whose `id` differs from its file stem, or whose `socket` is not `<runtime dir>/<id>.sock`, is corrupt. Readers skip corrupt entries; the next `Register` for the same id overwrites them. A corrupt entry is never listed, probed or removed by the CLI.
3. There is one `Entry` type, in the `instance` package, used by the Server (writes it), the CLI (reads it) and Store (reads the id). No module keeps its own copy.

### Registry protocol

The Server ([Pods](pods.md)) runs these steps at start and exit. The CLI runs the stale recovery part whenever it meets a stale entry. The protocol is defined here once so that the two never disagree about a file.

**Claim and register (Server start):**

1. Validate the id. Resolve and check the runtime directory. Check the socket path length.
2. `bind` and listen at `<id>.sock`. A successful bind is the claim; this fast path takes no lock.
3. On `EADDRINUSE`: probe the socket.
   - Reply: the name is held. Start fails with `instance "<id>" is already running`. Nothing is registered or removed.
   - Timeout: the holder is unresponsive. Start fails with `instance "<id>" did not answer; it may be busy`. Nothing is removed. A busy instance never loses its name to a newcomer.
   - Refused or missing: run stale recovery (below), then bind once more. A second `EADDRINUSE` fails start.
4. `chmod 0600` the socket.
5. Write the entry with `Register`. If this fails, close the listener, unlink the socket just bound, and fail start: no instance may listen without being discoverable.
6. Hand the socket path and id to the Wrapper for plugin environments ([Pods](pods.md)).

**Unregister (Server exit, normal or on `SIGINT` / `SIGTERM`):** stop accepting, remove `<id>.json`, then remove `<id>.sock` only if it is still the inode this Server bound (a replacement socket belongs to its owner). A missing file is not an error. A crash or `SIGKILL` leaves both files behind for stale recovery.

**Stale recovery (Server on a refused claim; CLI on a stale entry):**

1. Take an exclusive advisory lock (`flock`) on a file descriptor opened read-only on the runtime directory itself. No lock file is created.
2. Probe the socket again under the lock. A reply or a timeout ends recovery with nothing removed (live or unresponsive respectively).
3. If `<id>.json` exists and still holds the same `pid` and `started_at` as the entry that was probed, remove it. The Server, which has no entry in hand, removes `<id>.json` if present.
4. If `<id>.sock` exists and `lstat` says it is a socket, unlink it. If the path exists and is not a socket, leave it and fail (Server: start fails naming the path; CLI: the entry is skipped and a warning names the path).
5. Release the lock.

Why the lock: two starters probing one dead socket could otherwise both unlink and both bind, and a CLI could unlink a socket a starter bound between the probe and the unlink. Under the lock, a starter's first bind either precedes the probe (and answers it) or hits `EADDRINUSE` and waits for the lock. The `pid` and `started_at` comparison in step 3 is defence in depth, not the mechanism.

**Liveness classification**, used by every probe:

| Probe outcome | Class | Consequence |
|---|---|---|
| Reply within 500 ms | live | Candidate for discovery; listed as `live`. |
| `ENOENT`, `ECONNREFUSED` or `ENOTSOCK` on connect | stale | Stale recovery runs; the entry is not listed and not a candidate. |
| Connect succeeds, no complete reply line within 500 ms; any other error | unresponsive | Nothing removed; not a candidate; listed as `unresponsive`; a warning on stderr when discovery skips it. |

A dead pid is never evidence of staleness (pid reuse makes that check unsafe). The failed connect is.

### The `instance` package

A leaf package (`internal/instance` today; see the open question on public packages) that imports the standard library only. The Server, Store and the CLI import it; it imports none of them. It owns:

| Responsibility | Exported surface |
|---|---|
| Id validation | `ValidName(s) error` (names only), `ValidID(s) error` (pid id or name). |
| Runtime directory rule | `RuntimeDir() (string, error)` resolves, creates and checks; `CheckRuntimeDir(dir) error` checks a given directory without fallback. |
| Paths | `SocketPath(dir, id)`, `EntryPath(dir, id)`, `CheckSocketPath(path) error`. |
| The shared type | `Entry` as in the table above. |
| Entry files | `Register(dir, Entry) error` (atomic write), `Unregister(dir, id) error` (entry then socket, idempotent), `List(dir) ([]Entry, error)` (corrupt skipped, no probing). |
| Probing and classification | `Probe(socket) error` (one `session.ping`, 500 ms deadline) and the three-way classification. |
| Stale recovery | `RecoverStale(dir, Entry) error` implementing steps 1 to 5 above, usable by the Server (entry optional) and the CLI. |
| Survey | `Survey(dir) ([]Status, error)`: lists, probes every entry in parallel, runs stale recovery on stale ones, returns live and unresponsive entries with their class, sorted by id. |
| Resolution | `Resolve(ResolveOptions) (Resolved, error)` implementing the resolution order below and returning a typed error whose kind maps to an exit code. |
| Environment names | The constants `HUSK_SOCKET_PATH`, `HUSK_INSTANCE`, `HUSK_BIN_PATH`. |

Test seams (an injectable probe, clock or pid) are the implementation's business and are not part of this contract.

### Plugin and shell environment

The Wrapper ([Pods](pods.md)) sets these in every plugin process, whatever the plugin's transport, overwriting any inherited value:

| Variable | Value |
|---|---|
| `HUSK_SOCKET_PATH` | Absolute path of this instance's socket. |
| `HUSK_INSTANCE` | This instance's id. |
| `HUSK_BIN_PATH` | Absolute path of the running `husk` binary, so a plugin calls the CLI of the same build without a `PATH` lookup. |

Pods defines three more for the plugin itself: `HUSK_PLUGIN_ID`, `HUSK_PLUGIN_TOKEN`, `HUSK_PROTOCOL_VERSION`. Children of a plugin inherit all six unless the plugin removes some. A shell opened inside a plugin's surface therefore resolves to the instance that hosts it, which is intended: commands typed there act on the right instance without flags. A plugin that spawns a user shell is expected to remove `HUSK_PLUGIN_TOKEN` first, so that shell acts as the user and not as the plugin; this is a convention Husk cannot enforce.

The CLI reads `HUSK_SOCKET_PATH`, `HUSK_INSTANCE` and `HUSK_PLUGIN_TOKEN`. It reads no other `HUSK_*` variable.

### Invocation shape

```text
husk [start flags]                          start mode: become an instance
husk <noun> [<verb>] [args] [global flags]  command mode: talk to one instance
husk help | husk <noun> --help              usage on stdout, exit 0
```

Rules:

1. `argv[1]` decides the mode. If it exists and does not start with `-`, it is a subcommand noun and the binary is in command mode. Otherwise the binary is in start mode and every argument is a start flag. This rule exists because start mode and command mode both have an `--instance` flag with different meanings: `husk --instance notes` starts an instance named `notes`; `husk ping --instance notes` pings it.
2. In command mode, global flags may appear anywhere after the noun, before or between positional arguments. `--` ends flag parsing. Flags are spelled `--flag value` or `--flag=value`.
3. A noun not in the command table prints the usage on stderr and exits 2. A verb not in that noun's table does the same.
4. Start mode keeps today's flags and gains `--instance <name>` (this spec), `--profile <name>` ([Services](services.md)) and `--distribution <dir>` ([Store](store.md)). An invalid name is exit 2 before the filesystem is touched.

### Global flags

Accepted by every command that talks to an instance. `husk instances` and `husk store ...` do not talk to one instance and reject `--instance`, `--socket` and `--timeout` with exit 2; they accept `--format`.

| Flag | Meaning |
|---|---|
| `--instance <id>` | Target the instance with this id (resolution rule 1). |
| `--socket <path>` | Target this socket path directly (resolution rule 1). Giving both target flags is exit 2. |
| `--format json\|table` | Output format. Default: `table` when stdout is a terminal, `json` otherwise. Any other value is exit 2. |
| `--timeout <duration>` | Bound on the command from connect to final reply (Go duration syntax: `5s`, `500ms`). Default per command (table below). `0` means the command's default; negative is exit 2. Streaming commands default to no bound. |

### Caller identity

A request carries a `token` member when and only when `HUSK_PLUGIN_TOKEN` is set and non-empty in the CLI's environment ([Protocol](protocol.md), [Pods](pods.md)). Identity is the token, not the binary:

- From a bare shell the CLI is the user. Methods marked CLI-only in [Protocol](protocol.md) (`state.clear`, `settings.list`, `secrets.set`, `plugin.*`, `config.reload` and the others in that table) are served.
- From inside a plugin process (token set) the same command is that plugin, with that plugin's permissions. `husk state clear` run by a plugin is answered `denied`, exit 1.

The CLI enforces none of these rules itself. It validates syntax (flags, JSON well-formedness, method name shape) and forwards everything else; every permission, namespace and ownership rule is the instance's and comes back as an error reply. There is one copy of each rule, server side.

### Resolution

Every command that talks to an instance resolves exactly one target, using the first rule that applies:

| Rule | Input | Target |
|---|---|---|
| 1. Flag | `--socket <path>` | That path. No entry. |
| | `--instance <id>` | The socket of entry `<id>.json`. The id must be valid (else exit 2) and the entry must exist (else exit 3). |
| 2. Environment | `HUSK_SOCKET_PATH` set and non-empty | That path. No entry. |
| | else `HUSK_INSTANCE` set and non-empty | Looked up like `--instance`. |
| 3. Discovery | neither flag nor variable | `Survey` the registry. Exactly one live entry: its socket. Zero live and zero unresponsive: exit 3. Zero live and one or more unresponsive: exit 5, naming them. Two or more live: exit 4 with the list. Unresponsive entries alongside one live one are skipped with one warning each on stderr. |

Invariants:

1. The first rule that applies decides, even when it then fails. A dead `HUSK_SOCKET_PATH` (the shell outlived its instance) is exit 3, never a silent switch to discovery.
2. Discovery never picks between two live instances, whatever their age or name.
3. Stale entries met during resolution (rule 1 by id, rule 2 by id, rule 3) go through stale recovery. A bare socket path has no entry; a refused or missing socket there is exit 3 with nothing removed.
4. Resolution reads the registry on every invocation. The CLI caches nothing between runs.
5. `husk instances` never resolves: it lists.

### Connecting and the version check

1. The CLI opens one connection to the target, with a 2 s connect deadline. `ENOENT` or `ECONNREFUSED` here (the instance went away between resolution and connect) is exit 3 and, for a target found by entry, runs stale recovery. Any other connect failure is exit 5.
2. On that connection it first sends `session.ping` and reads the reply, under `--timeout`. The reply's `protocol_version` major must equal the CLI's; otherwise exit 6 before anything else is sent. A missing or unparsable version is also exit 6.
3. It then sends the command's request on the same connection, with the token rule above, and waits for the reply under `--timeout`. A streaming command then keeps reading notifications.
4. It closes the connection when the command ends. One command, one connection. Discovery may reuse the connection whose probe succeeded as the command's connection; the probe reply then counts as step 2.

The entry's `protocol_version` is informational (`husk instances` shows it); the check uses the live reply, so a target named by a bare socket path is checked the same way as one found by entry.

### Timeouts

| Bound | Value | Where it applies |
|---|---|---|
| Probe | 500 ms, all entries in parallel | Discovery, `husk instances`, stale recovery. A live local instance answers in milliseconds; discovery must not feel slow. |
| Connect | 2 s | Step 1 of connecting. A local connect that takes longer is a failure (exit 5). |
| Command (`--timeout`) | per command, see the table | From the start of step 2 to the final reply. Expiry is exit 5 with `instance did not answer within <timeout>`. |
| Streaming | none by default | `state watch` and `event watch` run until interrupted or the connection ends. |

### Command table

The complete v1 command set. The synopsis column fixes the spelling of every subcommand and flag. The owner column names the spec that defines the arguments' meaning, the method's params and the reply; this spec adds nothing to that. Every command is a named wrapper over one method, except the local ones marked "none".

| Synopsis | Method | Owner | Default timeout |
|---|---|---|---|
| `husk instances` | none: `Survey` of the registry | this spec | 2 s total |
| `husk ping` | `session.ping` | this spec | 2 s |
| `husk rpc <method> [<params-json>]` | `<method>` | [Protocol](protocol.md) | 30 s |
| `husk state` | `state.snapshot` (without values) | [State](state.md) | 5 s |
| `husk state get <slice> [<pointer>]` | `state.get` | [State](state.md) | 5 s |
| `husk state dump` | `state.snapshot` (with values) | [State](state.md) | 5 s |
| `husk state watch [<slice>...]` | `state.watch`, then `state.changed` stream | [State](state.md) | none |
| `husk state set <json>` | `state.set` | [State](state.md) | 5 s |
| `husk state patch <json> [--if-revision <n>]` | `state.patch` | [State](state.md) | 5 s |
| `husk state clear <namespace> [--force]` | `state.clear` | [State](state.md); the `--force` gate is [Hooks](hooks.md) | 5 s |
| `husk action list [--owner <id>]` | `action.list` | [Message kinds](message-kinds.md) | 5 s |
| `husk action invoke <action-id> [--arg <k>=<v>]... [--args-json <json>]` | `action.invoke` | [Message kinds](message-kinds.md) | 30 s |
| `husk event emit <name> [--data <json>]` | `event.emit` | [Message kinds](message-kinds.md) | 5 s |
| `husk event watch <pattern>` | `event.watch`, then `event.deliver` stream | [Message kinds](message-kinds.md) | none |
| `husk hook list [--owner <id>]` | `hook.list` | [Hooks](hooks.md) | 5 s |
| `husk keys [--action <id>] [--focus <surface>] [--shadowed] [--problems]` | `keys.list` | [Keybindings](keybindings.md) | 5 s |
| `husk settings get <key>` | `settings.get` | [Services](services.md) | 5 s |
| `husk settings list [--source]` | `settings.list` | [Services](services.md) | 5 s |
| `husk settings schema [--owner <id>]` | `settings.schema` | [Services](services.md) | 5 s |
| `husk settings validate` | `settings.validate` | [Services](services.md) | 5 s |
| `husk theme show` | `theme.get` | [Services](services.md) | 5 s |
| `husk theme roles` | `theme.get` | [Services](services.md) | 5 s |
| `husk theme check` | `theme.check` | [Services](services.md) | 5 s |
| `husk profile list` | `profile.list` | [Services](services.md) | 5 s |
| `husk profile current` | `profile.list` | [Services](services.md) | 5 s |
| `husk profile use <name>` | `profile.use` | [Services](services.md) | 5 s |
| `husk secrets list` | `secrets.names` | [Services](services.md) | 5 s |
| `husk secrets set <name>` (value on stdin) | `secrets.set` | [Services](services.md) | 5 s |
| `husk secrets grant <plugin> <name>` | `secrets.grant` | [Services](services.md) | 5 s |
| `husk secrets revoke <plugin> <name>` | `secrets.revoke` | [Services](services.md) | 5 s |
| `husk secrets reveal <name>` | `secrets.reveal` | [Services](services.md) | 5 s |
| `husk config reload` | `config.reload` | [Services](services.md) | 10 s |
| `husk plugin list` | `plugin.list` | [Pods](pods.md) | 5 s |
| `husk plugin log <id>` | `plugin.log` | [Pods](pods.md) | 5 s |
| `husk plugin remove <id>` | `plugin.remove` | [Pods](pods.md) | 10 s |
| `husk store paths` | none: local | [Store](store.md) | not applicable |
| `husk store check` | none: local | [Store](store.md) | not applicable |
| `husk store gc` | none: local | [Store](store.md) | not applicable |
| `husk store reset-secrets` | none: local; refuses while `Survey` finds any live or unresponsive instance | [Store](store.md) | not applicable |

Rules that apply across the table:

1. **JSON arguments.** Wherever a command takes JSON (`<params-json>`, `<json>`, `--args-json`, `--data`), the value must be a JSON object, and the literal `-` reads it from stdin. Anything else is exit 2 before connecting. `--arg k=v` pairs build an object of string values; giving both `--arg` and `--args-json` is exit 2.
2. **Client-side filters.** `--owner` on `action list` and `hook list` filters the reply in the CLI, because those methods take no parameters. It is the only client-side filtering in the table.
3. **Streaming commands** (`state watch`, `event watch`) print the opening reply, then one notification per line, until `SIGINT` or `SIGTERM` (exit 0) or the connection ends (exit 5, `instance went away`). The CLI installs signal handlers for streaming commands only.
4. **`husk rpc`** is the escape hatch: any method name that passes the protocol's shape check (two segments, `[a-z0-9_-]+`), params as an object, the raw `result` printed. The CLI does not know whether the method exists; `unknown_method` comes back from the instance as exit 1.
5. **`husk instances`** prints the survey. Stale entries are removed by it and do not appear. Its `--timeout` is not accepted: the survey is bounded by the probe timeout.
6. **No `husk hook call`.** A hook is fired by its owner inside its own operation ([Hooks](hooks.md)); a CLI firer would make subscribers act on an operation that is not happening. To see a hook fire, invoke the owner's operation (`husk action invoke`).
7. **No `husk keys adopt` and no `husk keys set`.** The CLI never writes a keymap ([Keybindings](keybindings.md)).
8. **No `husk stop`.** `husk action invoke husk.quit` asks an instance to quit; the CLI is always allowed to invoke actions ([Message kinds](message-kinds.md)).
9. **Local commands** (`store ...`) open Store without an instance. They still require a usable runtime directory when they survey it (`reset-secrets`); the same exit 7 applies.

### Output

**Streams.** stdout carries results and nothing else. stderr carries errors, warnings and usage text. This holds in both formats, so `husk ... | jq` never sees a diagnostic.

**Formats.**

| Format | stdout | stderr |
|---|---|---|
| `json` | One JSON document per command: the reply's `result`, or the command's own document for `instances`. Compact when stdout is not a terminal, indented when it is. Streaming commands print one compact JSON object per line: the opening `result`, then each notification's `params`. | One JSON object per line, each either `{"error": {...}}` or `{"warning": {...}}` with the shape below. |
| `table` | Human-readable text. Commands with a specified table (`instances`, `ping`, and those whose owner spec gives one) print it; every other result uses the generic rendering below. Streaming commands print one line per notification in the generic rendering. | One line per message: `husk: <message>`, followed for ambiguity by the candidate list. |

**Generic table rendering** of a `result`:

| Result is | Rendering |
|---|---|
| a string, number or boolean | the bare value, one line |
| `null` or `{}` | an empty line |
| an object | one row per member, keys sorted: `<key>  <value>` |
| an array of objects | a header row of the union of keys, sorted, then one row per element |
| any other array | one element per line |
| a nested object or array inside a cell | compact JSON |

Columns are separated by two or more spaces and aligned.

**`husk instances`** (JSON: an array of entries, each with an added `"status": "live" | "unresponsive"`, sorted by id; `[]` when none):

```text
ID     PID    STARTED               PROTO  STATUS
4242   4242   2026-10-22T06:00:00Z  2.0    live
notes  5120   2026-10-22T06:02:11Z  2.0    live
```

**`husk ping`** (JSON: the `session.ping` result verbatim):

```text
ID     PID   PROTO  HOST   UPTIME
notes  5120  2.0    0.9.0  1m12s
```

**Error object.** Every failure prints exactly one error, after any warnings:

```json
{"error": {"code": "ambiguous", "message": "2 instances are running; choose one with --instance <id>", "data": {"candidates": [{"id": "4242", "pid": 4242, "started_at": "2026-10-22T06:00:00Z"}, {"id": "notes", "pid": 5120, "started_at": "2026-10-22T06:02:11Z"}]}}}
```

| `code` | Source | `data` |
|---|---|---|
| any [Protocol](protocol.md) error code | the instance's error reply, forwarded unchanged | the reply's `data`, unchanged |
| `usage` | the CLI, before connecting | none |
| `no_instance` | the CLI, resolution or connect | none |
| `ambiguous` | the CLI, discovery | `{"candidates": [{id, pid, started_at}...]}` sorted by id |
| `transport` | the CLI, connect, timeout or disconnect | none |
| `protocol` | the CLI, version check or unreadable reply | none |
| `environment` | the CLI, runtime directory unusable | none |

Warnings use `{"warning": {"code": "unresponsive", "message": "..."}}`; `unresponsive` is the only warning code in v1.

In `table` mode the same errors print as `husk: <message>`. The ambiguity error appends the candidates, one per line, so the user can pick:

```text
husk: 2 instances are running; choose one with --instance <id>
  4242   started 2026-10-22T06:00:00Z
  notes  started 2026-10-22T06:02:11Z
```

### Exit codes

| Code | Meaning | Error `code` |
|---|---|---|
| 0 | Success. For a streaming command, ended by `SIGINT` or `SIGTERM`. | |
| 1 | The instance answered with an error reply: not found, denied, invalid params, owner failed, busy, timeout, cancelled, conflict, unavailable, line too long, unknown method, internal. The reply's code is in the error object. | the protocol code |
| 2 | Usage: unknown subcommand, bad flag or argument, both target flags, invalid id, non-object JSON, a request that would exceed the protocol line limit. Nothing was sent. | `usage` |
| 3 | No instance: discovery found none, the named id is not registered, or the target socket is stale. | `no_instance` |
| 4 | Ambiguous: discovery found two or more live instances and no flag or variable chose one. | `ambiguous` |
| 5 | Transport: connect failed for a reason other than stale, no reply within the timeout, the connection dropped, or discovery found only unresponsive instances. | `transport` |
| 6 | Protocol: the instance's major differs from the CLI's, or a reply could not be parsed. Also the error replies `parse_error`, `invalid_message` and `version_mismatch`, which mean the CLI itself sent something the instance could not read. | `protocol` |
| 7 | Environment: the runtime directory is missing permissions, a symlink, foreign-owned, or `XDG_RUNTIME_DIR` is set but unusable. Not transient; `husk` start fails the same check. | `environment` |

A non-streaming command interrupted by `SIGINT` or `SIGTERM` installs no handler and dies by the signal (the shell reports 128 + signal). A script branches on 3, 4, 5 and 7 without parsing text: 3 means start one, 4 means choose, 5 means retry later, 7 means fix the machine.

### Limits

| Limit | Value | Reason |
|---|---|---|
| Id length | 32 bytes | Fits any `sun_path` with margin; readable in a table. |
| Socket path | 103 bytes | `sun_path` on macOS. |
| Entry file | 4 KiB | Larger is corrupt. |
| Probe | 500 ms | See Timeouts. |
| Connect | 2 s | See Timeouts. |
| Request line | the protocol's 4 MiB | A larger request is exit 2 before sending. |
| Streaming backpressure | the Server's per-connection queue ([Pods](pods.md)) | A watcher that stops reading is disconnected like any slow client; it cannot stall the instance. |

### Dependency directions

- The CLI (`internal/cli` today) imports the `instance` package and the protocol package only. It imports no Stem, Services, Substrate or Pods package, and reaches State, the action catalog, the keymap, config and plugins only through the Server. A test pins the import list.
- `instance` imports the standard library only.
- The Server imports `instance` for the registry protocol; the Wrapper imports it for the variable names; Store imports it for `RuntimeDir` and the liveness probe its log GC uses.
- `cmd/husk` dispatches: `argv[1]` to the CLI or to start mode. It carries no command logic.

### Failure modes

| Condition | Detected by | Behaviour | Message | Exit |
|---|---|---|---|---|
| Invalid name at start | start-mode flag parsing | Refuse before touching the filesystem | `husk: invalid instance name "Foo_1": use [a-z0-9][a-z0-9-]*, at most 32 bytes, not digits only` | 2 |
| Name held by a live instance | Server, claim step 3 | Start fails; nothing registered or removed | `husk: instance "notes" is already running (pid 5120)` | start fails |
| Name held by an unresponsive instance | Server, claim step 3 | Start fails; nothing removed | `husk: instance "notes" did not answer; it may be busy` | start fails |
| Name held by a dead instance | Server, claim step 3 | Stale recovery, bind again, continue | nothing | |
| Runtime directory unusable | `RuntimeDir` | Instance refuses to start; CLI refuses to read | `husk: runtime dir <path> is not private to uid <uid>` (or `is a symlink`, `is not a directory`) | 7 |
| Socket path too long | Server, claim step 1 | Start fails | names the length, the limit and the directory | start fails |
| Entry write fails | Server, claim step 5 | Listener closed, socket unlinked, start fails | names the registration failure | start fails |
| Discovery: no entries | CLI | | `husk: no instance is running` | 3 |
| Discovery: only unresponsive | CLI | Nothing removed | `husk: 1 instance is registered but none answered within 500ms: notes` | 5 |
| Discovery: several live | CLI | Prints the list | as in Output | 4 |
| Discovery: one live, others unresponsive | CLI | Uses the live one | warning per skipped entry: `husk: instance "hung" did not answer within 500ms; skipped` | 0 |
| `--instance` or `HUSK_INSTANCE` not registered | CLI | | `husk: instance "notes" is not running` | 3 |
| `--instance` or `HUSK_INSTANCE` entry stale | CLI | Stale recovery | `husk: instance "notes" is not running (stale entry removed)` | 3 |
| `--socket` or `HUSK_SOCKET_PATH` refused or missing | CLI | Nothing removed | `husk: nobody is listening on <path>` (`HUSK_SOCKET_PATH` variant adds `; unset it or pass --instance`) | 3 |
| Target does not answer `session.ping` | CLI, connecting step 2 | | `husk: instance did not answer within <timeout>` | 5 |
| Protocol major differs | CLI, connecting step 2 | Nothing else sent | `husk: instance speaks protocol 3.x, this CLI speaks 2.x` | 6 |
| Peer uid differs | Server, accept | Connection closed | `husk: connection closed by host` | 5 |
| Instance exits during a stream | CLI, EOF | | `husk: instance went away` | 5 |
| Error reply | instance | Forwarded | the reply's `message` | 1 (6 for the three codec codes) |
| Reply not parsable | CLI | | `husk: unreadable reply: <detail>` | 6 |
| Request over the line limit | CLI, before sending | Nothing sent | `husk: request exceeds the 4 MiB line limit` | 2 |
| Two starters race for one name | `bind` | One wins; the other takes the taken-name path | as for a live holder | start fails |

### Invariants

1. One instance has exactly one socket and at most one entry; the id is the key of both.
2. An entry on disk means its socket was listening when the entry was written.
3. An instance removes its own files on a clean exit. Stale files are removed only by stale recovery, under the directory lock, after a probe under that lock confirmed nothing listens.
4. Stale recovery never removes an entry whose `pid` or `started_at` differ from the entry that was probed, and never removes a path that is not a socket.
5. The Wrapper sets `HUSK_SOCKET_PATH`, `HUSK_INSTANCE` and `HUSK_BIN_PATH` in every plugin process. A plugin never sees another instance's value.
6. The CLI never talks to an instance that a flag, a variable or a unique discovery did not select.
7. The CLI checks the protocol major on every connection before sending the command's request.
8. The CLI writes results to stdout and everything else to stderr, in both formats.
9. The CLI keeps no state between invocations and caches nothing.
10. The CLI enforces no permission or namespace rule; those are the instance's and arrive as error replies.
11. The runtime directory and every file in it are readable and writable by the owning uid only.

## Level 2: Migration from today

What `main` has and what this spec changes. Rows without a "today" are new.

| Today | Target |
|---|---|
| `internal/instance` exists with `Entry`, `Register`, `Unregister`, `List`, `RemoveIfStale`, `Survey`, `Resolve`, `DefaultPing`, two probe timeouts (500 ms discovery, 2 s lookup). | One probe timeout (500 ms). `RemoveIfStale` becomes `RecoverStale` with the directory lock and socket removal. `Resolve` does not probe explicit targets; the command's own connection is the check. |
| `Entry` has `name` and `husk_version`. | `name` dropped (the id's spelling says whether it is a name). `husk_version` renamed `host_version`, matching `session.ping`. |
| The Server (`internal/pods/server`) runs the claim with its own `recoverStale` and `gofrs/flock`. | The Server calls `instance.RecoverStale`; one implementation of the protocol. |
| The Server's `session.ping` reads `HostVersion` from `Entry.HuskVersion`; `cmd/husk` passes no version. | `host_version` set from the build version in both places. |
| `cmd/husk/main.go` is start mode only; the id is always the pid. | `argv[1]` dispatch to `internal/cli`; start mode gains `--instance <name>`, `--profile`, `--distribution`. |
| No CLI. | `internal/cli` with the shell contract above; commands land per owner spec. |
| `cmd/husk/socket_security.go` `createSocketDir` (fresh `husk-*` directory per run) | Done: the Server binds at `<id>.sock` in the shared runtime directory. |
| `pluginEnv` removes `HUSK_SOCKET_PATH` for stdio plugins | Done in the Wrapper: all variables set for every plugin. |
| Plugin logs under the socket directory | Done: `$XDG_STATE_HOME/husk/logs/<id>/<plugin>.log` through Store. |
| `docs/docs/architecture.md`, "Socket security" and "Plugin processes" | Updated when the CLI lands: path layout, environment variables, command list. |

## Decisions made here

1. **Pid ids and names cannot collide.** Digit-only names are rejected, so `--instance 4242` is unambiguous and no field is needed to say which form an id is.
2. **Claim by `bind`, register after listen.** The bind at a deterministic path is the atomic name claim; the entry is written after listen, so an entry always means ready and discovery cannot see a half-started instance.
3. **One stale recovery protocol, under the directory lock, for the Server and the CLI.** Previously the CLI removed only the entry and left the socket to "whoever bound it", so a crashed pid instance left a dead socket forever. Under the lock the race that rule guarded against cannot happen, and one implementation serves both sides. The first bind stays lock-free.
4. **Stale means refused or missing, not slow.** A dial that succeeds without a reply is unresponsive: kept, skipped, never deleted, and a reason for a newcomer with the same name to fail rather than evict it.
5. **No fallback after a rule applies.** A set but dead `HUSK_SOCKET_PATH` is exit 3, not a drop to discovery. This is the literal reading of "never guess".
6. **Only unresponsive instances found by discovery is exit 5, not 3.** A script that treats 3 as "start one" must not start a second instance beside a hung one; 5 says retry.
7. **`argv[1]` selects the mode.** Start mode and command mode share the `--instance` spelling with different meanings, so the subcommand must come first and global flags after it.
8. **One connection per command, `session.ping` first.** The ping is the liveness check for explicit targets, the version check for every target, and costs one local round trip. It replaces a separate pre-connection probe with its own timeout.
9. **The CLI validates syntax only.** Permission, namespace and ownership rules live in the instance and come back as error replies. No rule is duplicated client side.
10. **Identity is the token, not the binary.** `husk` from a plugin process is that plugin; `husk` from a bare shell is the user.
11. **`host_version` in the entry.** The same fact had two names (`husk_version` in the entry, `host_version` in `session.ping`). One name.
12. **Exit 7 for an unusable runtime directory.** It is neither usage nor transport nor "no instance"; folding it into any of those would make a script do the wrong thing (retry, or start an instance that fails the same check).
13. **Output default by TTY, override by flag.** JSON when stdout is not a terminal, table when it is. The error object and the local error codes are a closed set so that scripts branch on `code`.
14. **The command table lists synopses only.** Meaning, params and reply belong to the owner spec, so a command's behaviour is written in one place. The spelling is written here, in one place, so the owner specs can say "as in Instances".
15. **`event emit` is acknowledged.** `event.emit` is a request whose `{}` reply means the event is on the bus ([Protocol](protocol.md)); a CLI that cannot report acceptance is useless in scripts.
16. **No `husk hook call`, no `husk keys adopt`, no `husk stop`.** Each has an existing path or an owner that forbids it; see the command table rules.

## Open questions

Each is written into the contract above with the recommended answer; the operator may reverse any.

1. **Should the CLI remove stale sockets (decision 3)?** The code on `main` deliberately removes only the entry. Recommended: yes, under the lock, as specified; the lock closes the race and dead sockets should not accumulate. If reversed, keep the lock protocol for the Server and restore "entry only" for the CLI.
2. **Exit 7 (decision 12).** Alternative: fold into 5. Recommended: keep 7; the failure is not transient.
3. **Drop `name` from the entry (decision 1).** Store's `InstanceRef.Named` becomes a derivation from the id. Recommended: drop; a derivable field is a place for inconsistency and the Server has three validation branches only because of it.
4. **`husk hook call` for diagnostics.** The decisions log (D73) allows the CLI to fire any hook for diagnostics; the Hooks spec denies the CLI. This spec follows the Hooks spec. Recommended: no CLI firer; a hook fired outside its operation misleads every subscriber.
5. **Where the CLI and `instance` live.** `internal/cli` and `internal/instance` for now (D72). A public client package for tools that drive a running instance is part of the open import-root question; nothing here depends on the answer.
