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
| **socket identity** | The `(dev, ino)` pair `lstat` reports for `<id>.sock`, taken right after `bind`. It tells a Server's own socket file from a replacement bound at the same path. |
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

1. Start mode creates the directory with mode 0700 if absent. No command creates it, `store` commands included: an absent directory means no instance is running (`husk instances` prints `[]` or an empty table; resolution by discovery is exit 3; resolution by id is exit 3). Either side uses an existing directory only if `lstat` shows a real directory (not a symlink) owned by the current uid with no group or world permission bits. Otherwise the instance refuses to start and the CLI refuses to read it (exit 7).
2. A set but unusable `XDG_RUNTIME_DIR` is an error, never a fallback to `/tmp`. The `/tmp` form is used only when the variable is empty or unset.
3. The path is `filepath.Clean`ed before use (a trailing slash in `XDG_RUNTIME_DIR` does not change it), and every path written into an entry or compared against one is built from that cleaned directory.
4. The directory is shared by all of the user's instances and is never removed. It holds sockets, entries and the temporary files `Register` writes (`.<id>.json.tmp-*`, see entry rule 1), nothing else; the files of a stale instance are removed by the stale recovery protocol below.
5. The socket path is at most 103 bytes (the `sun_path` limit on macOS is 104 including the terminator; Linux allows 107). A longer path fails start with a message naming the length, the limit and the directory. This is why the fallback is `/tmp/husk-<uid>/` and not the system temp directory, whose macOS value is long.

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
| `host_version` | string | The Husk build version, informational. Always present; `""` when the build carries no version. Same name and same rule as the `host_version` member of the `session.ping` result, which is also always present and `""` when unknown. |

Rules:

1. The entry is written atomically: `Register` first removes any leftover `.<id>.json.tmp-*` of the same id, writes a new `.<id>.json.tmp-<random>` in the runtime directory with mode 0600, syncs it and renames it onto `<id>.json`. This happens only after the socket listens, so an entry on disk means the instance was ready when the entry was written. A crash between write and rename leaves a temporary file that the next `Register` for that id removes; nothing else touches temporary files.
2. `List` reads only files named `<stem>.json` whose stem is a valid id. An entry file over 4 KiB, not valid JSON, missing `id`, `pid` or `socket`, whose `id` differs from its file stem, or whose `socket` is not the cleaned `<runtime dir>/<id>.sock`, is corrupt. Readers skip corrupt entries; the next `Register` for the same id overwrites them. A corrupt entry is never listed, probed or removed by the CLI, so a live instance with a corrupt entry is reachable only with `--socket` until it restarts.
3. There is one `Entry` type, in the `instance` package, used by the Server (writes it), the CLI (reads it) and Store (reads the id). No module keeps its own copy.

### Registry protocol

The Server ([Pods](pods.md)) runs these steps at start and exit. The CLI runs the stale recovery part whenever it meets a stale entry. The protocol is defined here once so that the two never disagree about a file.

**Claim and register (Server start):**

1. Validate the id. Resolve and check the runtime directory. Check the socket path length.
2. `bind` and listen at `<id>.sock`. A successful bind is the claim; this fast path takes no lock. Immediately after a successful bind, `lstat` the path and keep its socket identity `(dev, ino)` for Unregister.
3. On `EADDRINUSE`: probe the socket.
   - Reply: the name is held. Start fails with `instance "<id>" is already running`. Nothing is registered or removed.
   - Timeout: the holder is unresponsive. Start fails with `instance "<id>" did not answer; it may be busy`. Nothing is removed. A busy instance never loses its name to a newcomer.
   - Refused or missing: run stale recovery (below), then bind once more. Step 2 applies to this bind as well: the socket identity is taken from the socket this bind created, never from the one recovery unlinked. A second `EADDRINUSE` fails start.
4. `chmod 0600` the socket.
5. Write the entry with `Register`. If this fails, close the listener, unlink the socket just bound, and fail start: no instance may listen without being discoverable.
6. Hand the socket path and id to the Wrapper for plugin environments ([Pods](pods.md)).

**Unregister (on Server `Close`, which the host calls on a normal exit and on `SIGINT` / `SIGTERM`; the Server itself handles no signal):** stop accepting, `lstat` `<id>.sock`, then one of three branches. Its `(dev, ino)` equal the socket identity taken in claim step 2: remove `<id>.json`, then unlink the socket. It is absent (stale recovery unlinked it after this Server stopped accepting and its probe was refused): remove `<id>.json` and unlink nothing, since there is no socket of ours at the path, and whatever appears there next belongs to someone else. Any other identity: a replacement bound at the same path; it belongs to its owner, and so does the entry that owner wrote, so both are left alone. The absent branch removes the entry without an ownership check, which leaves one race: a replacement that binds at the path and writes its entry between the `lstat` and the removal of `<id>.json` loses its entry while its socket stays bound, so it listens but is undiscoverable until it restarts. The window is two syscalls wide and is accepted; closing it would mean comparing the entry's `pid` and `started_at` against this Server's own, as stale recovery step 3 does. The absent branch must never unlink by path: that would remove the replacement's socket, leaving it unreachable and the path free for a third bind. A missing file is not an error. A crash or `SIGKILL` leaves both files behind for stale recovery. The listener's own file descriptor is not the comparison: `fstat` on a socket descriptor reports the socket filesystem, never the path's inode.

**Stale recovery (Server on a refused claim; CLI on a stale entry):**

1. Take an exclusive advisory lock (`flock`, `LOCK_EX`) on a file descriptor opened read-only on the runtime directory itself. No lock file is created. `flock` has no timeout, so acquire with `LOCK_NB`, retrying every 50 ms until a 2.5 s deadline (the connect bound plus the probe bound; `flock.TryLockContext` in the existing `gofrs/flock`). Never leave a blocking `flock` running after the deadline: an abandoned blocking call can acquire the lock later and hold it until the process exits. On expiry nothing is removed; the Server fails start with `runtime dir <path> is locked`, and the CLI leaves the entry as `unresponsive` (not a fourth class) with a warning that names the lock, not the instance (failure table below). The lock is released when the descriptor closes, so a holder that dies releases it without any cleanup step.
2. Probe the socket again under the lock. A reply ends recovery with nothing removed and the entry is live: the Server fails start as for a held name; the CLI reports exit 3 with `instance "<id>" started while this command ran; retry` and never retries the command itself against a socket it did not resolve; `Survey` lists the entry as `live`. A timeout ends recovery with nothing removed and the entry is unresponsive.
3. If the probe in step 2 did not find `<id>.sock` missing and `lstat` says it exists and is not a socket, leave both it and `<id>.json` and fail (Server: start fails naming the path; CLI: the entry is skipped and a warning names the path). Nothing is removed for that entry, so every later survey warns again.
4. If `<id>.json` exists and still holds the same `pid` and `started_at` as the entry that was probed, remove it. An entry passed with `pid` 0 and empty `started_at` (the Server's case: it has only the id) matches any file of that id, so the Server removes `<id>.json` if present.
5. If `<id>.sock` exists and `lstat` says it is a socket, unlink it, unless the probe in step 2 found the path missing: a claimant that unlinked it earlier may have bound a new socket since, outside the lock, and only a path that existed and refused at the probe is certainly still the dead file (no bind can succeed over it while the lock is held). Step 4 has already removed the entry, so if this unlink fails (`EACCES`, `EBUSY`) the CLI warns once, naming the path, and the orphan socket surfaces again only when a start with that id fails; this residual is accepted.
6. Release the lock.

`Survey` takes the lock once and runs steps 2 to 5 for every stale entry under it, so a survey after several crashes costs one lock wait, not one per entry.

Removal is silent: the CLI prints no message for a stale entry it removed, except the `(stale entry removed)` suffix when that entry was the explicit target.

Why the lock: two starters probing one dead socket could otherwise both unlink and both bind, and a CLI could unlink a socket a starter bound between the probe and the unlink. Under the lock, a starter's first bind either precedes the probe (and answers it) or hits `EADDRINUSE` and waits for the lock. The `pid` and `started_at` comparison in step 3 is defence in depth, not the mechanism.

**Liveness classification**, used by every probe:

| Probe outcome | Class | Consequence |
|---|---|---|
| Reply within 500 ms | live | Candidate for discovery; listed as `live`. |
| `ENOENT`, `ECONNREFUSED` or `ENOTSOCK` on connect | stale | Stale recovery runs; the entry is not listed and not a candidate. |
| Connect succeeds, no complete reply line within 500 ms; any other error | unresponsive | Nothing removed; not a candidate; listed as `unresponsive`; a warning on stderr when discovery skips it. |

A dead pid is never evidence of staleness (pid reuse makes that check unsafe). The failed connect is.

### The `instance` package

A leaf package (`internal/instance`, D72; whether a public client package joins it is open question 1) that imports the standard library only. The Server, Store and the CLI import it; it imports none of them. It owns:

| Responsibility | Exported surface |
|---|---|
| Id validation | `ValidName(s) error` (names only), `ValidID(s) error` (pid id or name). |
| Runtime directory rule | `RuntimeDir() (string, error)` resolves the cleaned path, creates it and checks it (start mode); `LookupRuntimeDir() (string, bool, error)` resolves and checks without creating, reporting absence (the CLI); `CheckRuntimeDir(dir) error` checks a given directory without fallback. |
| Paths | `SocketPath(dir, id)`, `EntryPath(dir, id)`, `CheckSocketPath(path) error`. |
| The shared type | `Entry` as in the table above. |
| Socket identity | `SocketIdentity(path) (Identity, error)`: the `(dev, ino)` of `<id>.sock`, taken after `bind`. |
| Entry files | `Register(dir, Entry) error` (atomic write, own leftovers removed first), `Unregister(dir, id string, ident Identity) error` (the entry when the socket is absent or matches `ident`; then the socket only when its `(dev, ino)` match `ident`, never when it is absent; idempotent), `List(dir) ([]Entry, error)` (valid-id `*.json` only, corrupt skipped, no probing). |
| Probing and classification | `Probe(socket) error` (one `session.ping`, 500 ms deadline) and the three-way classification. |
| Stale recovery | `RecoverStale(dir string, entries []*Entry) ([]Class, error)` implementing steps 1 to 5 above once for all given entries under one lock; the CLI passes one entry or `Survey`'s stale set, the Server passes a single `&Entry{ID: id}`, whose zero `pid` and empty `started_at` match any entry file of that id (step 3). It returns the class the re-probe found per entry so callers act on live and unresponsive as step 2 says. A lock deadline expiry is the error `ErrLocked`: the classes are then nil and the caller treats every entry passed as not recovered; the function never returns a partial slice. |
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
2. In command mode, global flags may appear anywhere after the noun, before or between positional arguments. `--` ends flag parsing. Flags are spelled `--flag value` or `--flag=value`. A flag given twice: the last value wins. A positional argument that is exactly `-` is an argument (stdin, rule 1 of the command table), not a flag. A flag that takes a number (`--if-revision`) with a value that is not a non-negative integer is exit 2. `--arg` takes `<k>=<v>` split at the first `=`; a value with no `=` or an empty key is exit 2, and a repeated key keeps the last value.
3. A noun not in the command table prints the usage on stderr and exits 2. A verb not in that noun's table does the same.
4. Start mode keeps today's flags and gains `--instance <name>` (this spec), `--profile <name>` ([Services](services.md)) and `--distribution <dir>` ([Store](store.md)). An invalid name is exit 2 before the filesystem is touched.

### Global flags

Accepted by every command that talks to an instance. `husk instances` and `husk store ...` do not talk to one instance and reject `--instance`, `--socket` and `--timeout` with exit 2; they accept `--format`. Streaming commands (`state watch`, `event watch`) reject `--timeout` with exit 2: they end only by signal or disconnect.

| Flag | Meaning |
|---|---|
| `--instance <id>` | Target the instance with this id (resolution rule 1). |
| `--socket <path>` | Target this socket path directly (resolution rule 1). Giving both target flags is exit 2. |
| `--format json\|table` | Output format for stdout and stderr alike. Default: `table` when stdout is a terminal, `json` otherwise; stderr's own terminal-ness plays no part. Any other value is exit 2. |
| `--timeout <duration>` | Bound on the command from the first byte sent (connecting step 2) to the final reply (Go duration syntax: `5s`, `500ms`). Connect has its own 2 s bound that does not overlap, so the worst case is 2 s plus the timeout. Default per command (table below). `0` means the command's default; negative is exit 2. |

### Caller identity

A request carries a `token` member when and only when `HUSK_PLUGIN_TOKEN` is set and non-empty in the CLI's environment ([Protocol](protocol.md), [Pods](pods.md)). Identity is the token, not the binary:

- From a bare shell the CLI is the user. Methods marked CLI-only in [Protocol](protocol.md) (`state.clear`, `settings.list`, `secrets.set`, `plugin.*`, `config.reload` and the others in that table) are served.
- From inside a plugin process (token set) the same command is that plugin, with that plugin's permissions. `husk state clear` run by a plugin is answered `denied`, exit 1.
- The token is sent whatever the target. `husk --instance other ping` from inside a plugin sends that plugin's token to an instance that does not know it, which answers `denied` ([Pods](pods.md): never downgraded to the user). The CLI does not drop the token because a flag chose another instance.

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
2. On that connection it first sends `session.ping` and reads the reply, under `--timeout` (for a streaming command, under the fixed 5 s opening bound in Timeouts). The reply's `protocol_version` major must equal the CLI's; otherwise exit 6 before anything else is sent. A missing or unparsable version is also exit 6.
3. It then sends the command's request on the same connection, with the token rule above, and waits for the reply under `--timeout` (streaming: the same 5 s opening bound, which covers steps 2 and 3 together). A streaming command then keeps reading notifications with no bound.
4. It closes the connection when the command ends. One command, one connection. Discovery's probe connections are closed by `Survey`; the command then opens its own connection in step 1.

What the CLI does with lines that are not the awaited reply:

| While | Line | Action |
|---|---|---|
| waiting for a reply | a `protocol.error` notification | exit 6; the error object carries that notification's `code` and `message` |
| waiting for a reply | any other notification | ignored |
| waiting for a reply | a reply whose `id` the CLI did not send | exit 6, `unreadable reply: unexpected id` |
| streaming | the stream's own method (`state.changed`, `event.deliver`) | printed |
| streaming | a `protocol.error` notification | exit 6, as above. The CLI has sent nothing on this connection after the opening request, so the error is about its own request; no "was it ours?" check is needed. |
| streaming | any other notification or any reply | ignored |
| either | a line that is not a valid message | exit 6, `unreadable reply: <detail>` |

The entry's `protocol_version` is informational (`husk instances` shows it); the check uses the live reply, so a target named by a bare socket path is checked the same way as one found by entry.

### Timeouts

| Bound | Value | Where it applies |
|---|---|---|
| Probe | 500 ms, all entries in parallel | Discovery, `husk instances`, stale recovery. A live local instance answers in milliseconds; discovery must not feel slow. |
| Directory lock wait | 2.5 s, `LOCK_NB` retried every 50 ms | Stale recovery step 1. Not a flag. |
| Survey total | 3.5 s: one probe round, one lock wait, one re-probe round under the lock | `husk instances` and discovery; not a flag. The lock is taken once for all stale entries, so the total does not grow with their number. |
| Connect | 2 s | Step 1 of connecting. A local connect that takes longer is a failure (exit 5). Does not overlap `--timeout`. |
| Command (`--timeout`) | per command, see the table | From the first byte sent (step 2) to the final reply. Expiry is exit 5 with `instance did not answer within <timeout>`. |
| Streaming opening | 5 s, not a flag | `state watch` and `event watch`: connecting steps 2 and 3 together (the `session.ping` and the opening reply). Expiry is exit 5 with `instance did not answer within 5s`. |
| Streaming | none; `--timeout` rejected | After the opening reply, the notification stream runs until a signal or the connection ends. |

### Command table

The complete v1 command set. The synopsis column fixes the spelling of every subcommand and flag. The owner column names the spec that defines the arguments' meaning, the method's params and the reply; this spec adds nothing to that. Every command is a named wrapper over one method, except the local ones marked "none".

| Synopsis | Method | Owner | Default timeout |
|---|---|---|---|
| `husk instances` | none: `Survey` of the registry | this spec | not a flag: the survey total above |
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
| `husk hook list [--owner <id>]` | `hook.list` (protocol minor 2.1; the CLI's version check compares majors only) | [Hooks](hooks.md) | 5 s |
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
| `husk plugin remove <id>` | `plugin.remove` (protocol minor 2.1) | [Pods](pods.md), "Removing a plugin" | 10 s |
| `husk store paths` | none: local, the store command package | [Store](store.md) | not applicable |
| `husk store check` | none: local, the store command package | [Store](store.md) | not applicable |
| `husk store gc` | none: local, the store command package | [Store](store.md) | not applicable |
| `husk store reset-secrets` | none: local, the store command package; refuses while `Survey` finds any live or unresponsive instance | [Store](store.md) | not applicable |

Rules that apply across the table:

1. **JSON arguments.** Wherever a command takes JSON (`<params-json>`, `<json>`, `--args-json`, `--data`), the value must be a JSON object, and the literal `-` reads it from stdin. Anything else is exit 2 before connecting. `--arg k=v` pairs build an object of string values; giving both `--arg` and `--args-json` is exit 2. When a command has no JSON argument to send, it sends `"params": {}`; the CLI never omits `params` from a request.
2. **Client-side projections.** `--owner` on `action list` and `hook list` filters the reply in the CLI, because those methods take no parameters; it is the only client-side filter by flag. `theme roles` (the keys of `theme.get`) and `profile current` (the `active` entry of `profile.list`) are fixed projections [Services](services.md) defines. Nothing else is reshaped client side.
3. **Streaming commands** (`state watch`, `event watch`) print the opening reply, then one notification per line, until the connection ends (exit 5, `instance went away`) or the process is killed by a signal. The CLI installs no signal handler for any command: `SIGINT` and `SIGTERM` end the process by the signal (the shell reports 128 + signal), and a closed stdout (`husk event watch | head -1`) ends it by `SIGPIPE`, exactly as `tail -f | head` does. No handler is to be added for this.
4. **`husk rpc`** is the escape hatch: any method name that passes the protocol's shape check (two segments, `[a-z0-9_-]+`), params as an object, the raw `result` printed. The CLI does not know whether the method exists; `unknown_method` comes back from the instance as exit 1.
5. **`husk instances`** prints the survey. Stale entries are removed by it, silently, and do not appear. Its `--timeout` is not accepted: the survey is bounded by the survey total in Timeouts.
6. **No `husk hook call`.** A hook is fired by its owner inside its own operation ([Hooks](hooks.md)); a CLI firer would make subscribers act on an operation that is not happening. To see a hook fire, invoke the owner's operation (`husk action invoke`).
7. **No `husk keys adopt` and no `husk keys set`.** The CLI never writes a keymap ([Keybindings](keybindings.md)).
8. **No `husk stop`.** `husk action invoke husk.quit` asks an instance to quit; the CLI is always allowed to invoke actions ([Message kinds](message-kinds.md)).
9. **Local commands** (`store ...`) open Store without an instance. They live in their own package, the store command package (`internal/cli/storecmd`), which imports Store, the `instance` package and the output package, never `internal/cli`; it exports `storecmd.Command() output.Command`, and `cmd/husk` wires it with `cli.Register("store", storecmd.Command())` before dispatch (Dependency directions). They pass Store the directory `LookupRuntimeDir` found (or none), so opening Store creates no runtime directory (its config, state and data directories, yes), and they open it with `Options.Passive`, so `Open` itself collects, quarantines and writes nothing ([Store](store.md)). A local command that fails is exit 1 with error code `store` and Store's reason in `message`; `reset-secrets` refused because an instance is live or unresponsive is exit 1 `store` with a message naming those instances. A survey needs a usable runtime directory; the same exit 7 applies.

### Output

**Streams.** stdout carries results and nothing else. stderr carries errors, warnings and usage text. This holds in both formats, so `husk ... | jq` never sees a diagnostic.

**Formats.**

| Format | stdout | stderr |
|---|---|---|
| `json` | One JSON document per command: the reply's `result`, or the command's own document for `instances`. Compact when stdout is not a terminal, indented when it is. Streaming commands print one compact JSON object per line: the opening `result`, then each notification's `params`. | One JSON object per line, each either `{"error": {...}}` or `{"warning": {...}}` with the shape below. |
| `table` | Human-readable text. Commands with a specified table (`instances`, `ping`, and those whose owner spec gives one) print it; every other result uses the generic rendering below. Streaming commands print the opening reply in the generic rendering, then one compact JSON object per notification, exactly as in `json` mode, so each notification is one line. | One line per message: `husk: <message>`, followed for ambiguity by the candidate list. |

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

A string in a row or cell, whether a value, an object key or a header, that contains a control character is shown in its JSON form (quoted, escaped), so no cell can break a row or carry a terminal sequence; sorting uses the raw key. A lone string result is printed bare. Alignment counts display columns (wide characters take two, combining and format characters none, East Asian ambiguous characters one) whatever the locale.

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

`UPTIME` is `uptime_ms` rounded to whole seconds; `HOST` is blank when `host_version` is `""`.

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
| `store` | a local `store` command, Store's reason in `message` | none |

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
| 0 | Success. A streaming command never exits 0: it ends by signal or by exit 5. | |
| 1 | The instance answered with an error reply: not found, denied, invalid params, owner failed, busy, timeout, cancelled, conflict, unavailable, unknown method, internal. The reply's code is in the error object. Also: a local `store` command failed. | the protocol code, or `store` |
| 2 | Usage: unknown subcommand, bad flag or argument, both target flags, invalid id, non-object JSON, a request that would exceed the protocol line limit. Nothing was sent. | `usage` |
| 3 | No instance: discovery found none, the named id is not registered, or the target socket is stale. | `no_instance` |
| 4 | Ambiguous: discovery found two or more live instances and no flag or variable chose one. | `ambiguous` |
| 5 | Transport: connect failed for a reason other than stale, no reply within the timeout, the connection dropped, or discovery found only unresponsive instances. | `transport` |
| 6 | Protocol: the instance's major differs from the CLI's, a reply could not be parsed, a `protocol.error` notification arrived, or a reply carried an unknown `id`. Also the four reader error replies `parse_error`, `invalid_message`, `line_too_long` and `version_mismatch`, which mean the CLI itself sent something the instance could not read (the CLI checks the line limit before sending, so receiving `line_too_long` is a CLI bug, not user input). | `protocol` |
| 7 | Environment: the runtime directory is missing permissions, a symlink, foreign-owned, or `XDG_RUNTIME_DIR` is set but unusable. Not transient; `husk` start fails the same check. | `environment` |

Any command ended by `SIGINT`, `SIGTERM` or `SIGPIPE` dies by the signal; the CLI installs no handler and the shell reports 128 + signal. A script branches on 3, 4, 5 and 7 without parsing text: 3 means start one, 4 means choose, 5 means retry later, 7 means fix the machine.

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

Imports go one way; no two of these packages import each other.

- The output package (`internal/cli/output`) is a leaf: `--format` handling, the error object and warning lines, exit codes, the generic table rendering, and the `Command` type (noun, usage, `Run(ctx, args, output.Options) error`), the shape every command package produces. It imports the standard library, the protocol package and `github.com/mattn/go-runewidth` (display width of table cells) only.
- The socket client and every command that talks to an instance live in `internal/cli` (D72), which imports the output package, the `instance` package, the protocol package and `golang.org/x/term` (terminal detection for the `--format` default) only. It imports no Stem, Services, Substrate or Pods package, and reaches State, the action catalog, the keymap, config and plugins only through the Server. It exports the command table with one registration seam, `cli.Register(noun string, cmd output.Command)`, and `cli.Main(args)`. A test pins that import list.
- The `store` noun lives in the store command package (`internal/cli/storecmd`), which imports the output package, the `instance` package and Store (a Substrate package), and never `internal/cli`. It exports `Command() output.Command` and registers nothing itself. It never talks to an instance; its only socket use is the probes `Survey` makes for `reset-secrets`. A test pins its import list too (Store, `instance`, output, standard library), so neither package grows a socket client or a Substrate import by accident.
- `instance` imports the standard library only.
- The Server imports `instance` for the registry protocol; the Wrapper imports it for the variable names; Store imports it for the liveness probe its log GC uses and for `SocketPath`. Store does not resolve the runtime directory itself: its opener passes it (`Options.RuntimeDir`): start mode resolves it once in `cmd/husk` with `RuntimeDir()` and passes the same string to Store and to the Server ([Pods](pods.md) `Options.RuntimeDir`), so no process resolves the directory twice; the store command package passes what `LookupRuntimeDir()` found or `""` for absent, which Store treats as "no instance registered" in its liveness probes ([Store](store.md)).
- `cmd/husk` dispatches: `argv[1]` to `cli.Main` or to start mode, after the one wiring line `cli.Register("store", storecmd.Command())`. Registration is wiring, not command logic; `cmd/husk` carries none.

### Failure modes

| Condition | Detected by | Behaviour | Message | Exit |
|---|---|---|---|---|
| Invalid name at start | start-mode flag parsing | Refuse before touching the filesystem | `husk: invalid instance name "Foo_1": use [a-z0-9][a-z0-9-]*, at most 32 bytes, not digits only` | 2 |
| Name held by a live instance | Server, claim step 3 | Start fails; nothing registered or removed | `husk: instance "notes" is already running` | start fails |
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
| `--instance` or `HUSK_INSTANCE` entry stale, re-probe under the lock answered | CLI | Nothing removed, command not retried | `husk: instance "notes" started while this command ran; retry` | 3 |
| Stale recovery cannot remove a file (`EACCES`, `EBUSY`) | CLI | Entry skipped, survey continues | warning `husk: cannot remove <path>: <err>; instance "notes" skipped` | 0 for `husk instances`; discovery per the remaining entries |
| Directory lock not acquired within 2.5 s, explicit target | CLI stale recovery step 1 | Nothing removed | `husk: instance "notes" is not answering and the runtime dir is locked; retry` | 5 |
| Directory lock not acquired within 2.5 s, discovery | CLI stale recovery step 1 | Entry left as is, nothing removed | warning `husk: runtime dir <path> is locked; instance "notes" left as is`; then the discovery rule as if the entry were unresponsive (0 when another instance is live, 5 when none is) | 0 or 5 |
| Directory lock not acquired within 2.5 s, `husk instances` | CLI stale recovery step 1 | Entry listed `unresponsive`, nothing removed | the same warning | 0 |
| Directory lock not acquired within 2.5 s | Server stale recovery step 1 | Start fails | `husk: runtime dir <path> is locked` | start fails |
| `reset-secrets` while an instance is live or unresponsive | store command package | Nothing deleted | `husk: 2 instances are registered (4242, notes); stop them first` | 1 |
| Other local `store` failure | store command package | | Store's reason | 1 |
| `--socket` or `HUSK_SOCKET_PATH` refused or missing | CLI | Nothing removed | `husk: nobody is listening on <path>` (`HUSK_SOCKET_PATH` variant adds `; unset it or pass --instance`) | 3 |
| Target does not answer `session.ping` | CLI, connecting step 2 | | `husk: instance did not answer within <timeout>` | 5 |
| Protocol major differs | CLI, connecting step 2 | Nothing else sent | `husk: instance speaks protocol 3.x, this CLI speaks 2.x` | 6 |
| Peer uid differs | Server, accept | Connection closed | `husk: connection closed by host` | 5 |
| Instance exits during a stream | CLI, EOF | | `husk: instance went away` | 5 |
| Error reply | instance | Forwarded | the reply's `message` | 1 (6 for the four reader codes) |
| Reply not parsable, unknown `id`, or `protocol.error` received | CLI | | `husk: unreadable reply: <detail>` or the notification's message | 6 |
| Request over the line limit | CLI, before sending | Nothing sent | `husk: request exceeds the 4 MiB line limit` | 2 |
| Two starters race for one name | `bind` | One wins; the other takes the taken-name path | as for a live holder | start fails |

### Invariants

1. One instance has exactly one socket and at most one entry; the id is the key of both.
2. An entry on disk means its socket was listening when the entry was written.
3. An instance removes its own files on a clean exit, recognising its socket by the `(dev, ino)` identity taken after `bind`; it never unlinks a socket path whose identity it did not match. Stale files are removed only by stale recovery, under the directory lock, after a probe under that lock confirmed nothing listens.
4. Stale recovery never removes an entry whose `pid` or `started_at` differ from the entry that was probed, never removes a path that is not a socket, and never removes an entry whose socket path is not a socket.
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
| `internal/instance` exists with `Entry`, `Register`, `Unregister`, `List`, `RemoveIfStale`, `Survey`, `Resolve`, `DefaultPing`, two probe timeouts (500 ms discovery, 2 s lookup). | **Landed:** one probe timeout (`ProbeTimeout`, 500 ms) and `Probe`; `RemoveIfStale` is `RecoverStale` (directory `flock`, 2.5 s wait, entry and socket removal, `ErrLocked`); `Survey` runs it once for its stale set; `SocketIdentity` and the identity argument of `Unregister`; `Resolve` does not probe explicit targets. `RuntimeDir` gained a non-creating lookup for the CLI. |
| No `store` command package; output and error helpers live inside `internal/cli`. | `internal/cli/output` (leaf), `internal/cli` (socket commands, `Register`), `internal/cli/storecmd` (the only command package that imports Store). Third-party imports: `golang.org/x/term` in `internal/cli`, `github.com/mattn/go-runewidth` in `internal/cli/output`, nothing else. **Landed:** all three; `plugin list`, `plugin log`, `plugin remove` and `store paths`, `check`, `gc`, `reset-secrets` are implemented. |
| Store resolves the runtime directory itself with `instance.RuntimeDir`. | **Landed:** Store takes it from its opener (`Options.RuntimeDir`); only start mode creates it; `""` means no instance is registered. `InstanceRef.Named` is derived from the id. |
| `Entry` has `name` and `husk_version`. | **Landed:** `name` dropped (the id's spelling says whether it is a name). `husk_version` renamed `host_version`, matching `session.ping`. |
| The Server (`internal/pods/server`) runs the claim with its own `recoverStale` and `gofrs/flock`; `Close` compares `os.SameFile` of the path against an `Lstat` taken after bind. | **Landed:** the Server calls `instance.RecoverStale`; one implementation of the protocol. The after-bind `Lstat` is `instance.SocketIdentity`, taken after each successful bind and passed to `Unregister`. |
| The Server's `session.ping` reads `HostVersion` from `Entry.HuskVersion`; `cmd/husk` passes no version. | **Landed:** `host_version` set from the build version (`host.WithVersion`) in both places. |
| `cmd/husk/main.go` is start mode only; the id is always the pid. | `argv[1]` dispatch to `internal/cli` (landed). **Landed:** start mode `--instance <name>`. Open: `--profile`, `--distribution`. |
| No CLI. | `internal/cli` with the shell contract above; commands land per owner spec. **Landed:** `instances`, `ping`, `rpc`, `state` (`get`, `dump`, `watch`, `set`, `patch`, `clear` without `--force`), `action` (`list`, `invoke`), `event` (`emit`, `watch`), `plugin list`, `plugin log`, `plugin remove` (verb table, own 10 s default timeout), `store paths`, `store check`, `store gc`, `store reset-secrets`. Still to land: `state clear --force`, `hook list`, `keys`, and the Services nouns. |
| `cmd/husk/socket_security.go` `createSocketDir` (fresh `husk-*` directory per run) | Done: the Server binds at `<id>.sock` in the shared runtime directory. |
| `pluginEnv` removes `HUSK_SOCKET_PATH` for stdio plugins | Done in the Wrapper: all variables set for every plugin. |
| Plugin logs under the socket directory | Done: `$XDG_STATE_HOME/husk/logs/<id>/<plugin>.log` through Store. |
| `docs/docs/architecture.md`, "Socket security" and "Plugin processes" | Updated when the CLI lands: path layout, environment variables, command list. |

## Decisions made here

1. **Pid ids and names cannot collide.** Digit-only names are rejected, so `--instance 4242` is unambiguous and no field is needed to say which form an id is.
2. **Claim by `bind`, register after listen.** The bind at a deterministic path is the atomic name claim; the entry is written after listen, so an entry always means ready and discovery cannot see a half-started instance.
3. **One stale recovery protocol, under the directory lock, for the Server and the CLI** (D78). Previously the CLI removed only the entry and left the socket to "whoever bound it", so a crashed pid instance left a dead socket forever. Under the lock the race that rule guarded against cannot happen, and one implementation serves both sides. The first bind stays lock-free. The lock wait is bounded (2.5 s) because a blocking lock with no bound hangs behind a suspended holder and a non-blocking one fails under ordinary contention.
4. **Stale means refused or missing, not slow.** A dial that succeeds without a reply is unresponsive: kept, skipped, never deleted, and a reason for a newcomer with the same name to fail rather than evict it.
5. **No fallback after a rule applies.** A set but dead `HUSK_SOCKET_PATH` is exit 3, not a drop to discovery. This is the literal reading of "never guess".
6. **Only unresponsive instances found by discovery is exit 5, not 3.** A script that treats 3 as "start one" must not start a second instance beside a hung one; 5 says retry.
7. **`argv[1]` selects the mode.** Start mode and command mode share the `--instance` spelling with different meanings, so the subcommand must come first and global flags after it.
8. **One connection per command, `session.ping` first.** The ping is the liveness check for explicit targets, the version check for every target, and costs one local round trip. It replaces a separate pre-connection probe with its own timeout. Discovery's probe connection is not reused: reuse would be a second code path with a different connect bound and ping deadline, for the price of one local connect.
9. **The CLI validates syntax only.** Permission, namespace and ownership rules live in the instance and come back as error replies. No rule is duplicated client side.
10. **Identity is the token, not the binary.** `husk` from a plugin process is that plugin; `husk` from a bare shell is the user.
11. **`host_version` in the entry** (D80). The same fact had two names (`husk_version` in the entry, `host_version` in `session.ping`). One name, one rule: always present, `""` when unknown.
12. **Exit 7 for an unusable runtime directory** (D79). It is neither usage nor transport nor "no instance"; folding it into any of those would make a script do the wrong thing (retry, or start an instance that fails the same check).
13. **Output default by TTY, override by flag.** JSON when stdout is not a terminal, table when it is. The error object and the local error codes are a closed set so that scripts branch on `code`.
14. **The command table lists synopses only.** Meaning, params and reply belong to the owner spec, so a command's behaviour is written in one place. The spelling is written here, in one place, so the owner specs can say "as in Instances".
15. **`event emit` is acknowledged.** `event.emit` is a request whose `{}` reply means the event is on the bus ([Protocol](protocol.md)); a CLI that cannot report acceptance is useless in scripts.
16. **No `husk hook call`, no `husk keys adopt`, no `husk stop`.** Each has an existing path or an owner that forbids it; see the command table rules.
17. **No signal handlers, streams included.** A watch has no natural end, so "interrupted means success" was tempting, but it made `husk` inconsistent with itself (every other command dies by the signal) and with `tail -f` and its kin. Dying by the signal everywhere is one rule and less code.
18. **The `store` noun is its own package, and the output helpers are a leaf below both.** The socket client stays free of Substrate, which is the point of the import rule; the one noun that needs Store imports it from a sibling package. Sharing the output helpers through `internal/cli` would make the two packages import each other, so the helpers are a third package neither depends on the other for. Store takes the runtime directory from its opener for the same reason: `husk store paths` on a fresh machine must not leave a runtime directory behind.
19. **Socket identity is `(dev, ino)` from `lstat` after `bind`.** The listener descriptor cannot identify the path's inode, so the Server records the identity when it binds and compares it when it unregisters.

## Rulings applied

- **D72.** The CLI and the socket client live in `internal/cli`; the `instance` package in `internal/instance`; no subcommand starts an instance.
- **D74** (amending D73). The CLI fires no hook, not even for diagnostics; `husk hook list` is the CLI's only hook command.
- **D78.** Stale recovery is one protocol under the runtime-directory `flock`, run by the Server and the CLI, removing the entry and the socket. Supersedes "entry only, never the socket".
- **D79.** An unusable runtime directory is exit 7 `environment`.
- **D80.** The entry has no `name` field; `husk_version` is `host_version`.

## Open questions

1. **A public client package.** Whether the socket client in `internal/cli` becomes a public package for tools that drive a running instance is part of the open import-root question; nothing in this spec depends on the answer.
