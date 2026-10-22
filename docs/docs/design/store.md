---
type: Design Spec
title: "Store: persistence mechanics"
description: "How Husk merges config layers, places every file it reads or writes, encrypts secrets, and hydrates and persists State slices without knowing what any key means."
status: planned
generated: { by: claude/claude-opus-5-5, at: 2026-10-22T06:00:00Z }
tags: [architecture-v2, substrate]
---

# Store: persistence mechanics

## Level 0: What and why

Store is the half of Substrate that touches disk. [State](state.md) is the live
in-memory tree; Store is how parts of that tree, and every config document,
survive a restart. It has five submodules:

- **layers** merges the `distribution`, `user`, `profile` and `instance`
  versions of one config document into a resolved document.
- **sources** maps documents and slices to storage. The local-files source
  ships first; a cloud source is a later implementation of the same interface.
- **sync** reconciles two sources. Interface only in v1; conflict policy is
  last writer wins.
- **crypto** encrypts secret namespaces at rest.
- **retention** hydrates persistent State slices on start, persists them on
  change (debounced) and on exit, garbage-collects per policy, enforces size
  caps and deletes a plugin's slices on uninstall.

The problem it solves: today the host writes files from two places
(`cmd/husk/socket_security.go`, `cmd/husk/lifecycle.go`), keeps no config at
all, and has no rule for what a plugin may persist. Store makes one module
own every path Husk writes and every config or state file Husk reads, so
accumulation, corruption and multi-instance collisions have one place to be
handled.

What Store is NOT:

- It does not know what a key means. "Theme", "keymap", "active profile" are
  [Services](services.md) concepts. Store merges trees and moves bytes.
- It is not State. It never answers "what is true now"; it fills State on start
  and copies State to disk afterwards. Owners never call Store for slice data;
  they find their slice already filled.
- It does not validate settings. [Services](services.md) validate the resolved
  document Store hands them.
- It does not read plugin packages (executables, assets). The
  [Pods](pods.md) manifest module reads a plugin's own `husk-plugin.json`
  read-only; that file is the plugin's, not Husk's.

Who depends on it: [Services](services.md) (resolved config documents, secret
namespaces), [Pods](pods.md) (runtime directory, socket path, plugin log
files, declared documents from manifests, uninstall), and `cmd/husk` (opens
Store first, passes the instance layer from flags, closes it last). Store depends on
[State](state.md) and on the `instance` leaf package ([Instances](instances.md))
for runtime-directory rules and instance liveness, nothing else.

V1 targets Unix: storage uses Unix-only syscall flags such as `O_NOFOLLOW`;
Windows is out of scope.

## Level 1: Contract

### Dependency directions

1. Store imports State. State never imports Store; it defines the `Hydrator`
   interface and the `Listener` hook Store implements.
2. Services import Store. Store never imports Services.
3. Pods import Store for paths, log files, declared documents and uninstall.
   Store never imports Pods.
4. Stem never imports Store. Stem sees persisted data only as State slices and
   resolved config only through Services.
5. Store never imports Stem, including the event bus. It learns of slice
   changes through a State `Listener` and reports problems by writing its
   own State slice, whose change event State puts on the bus.
6. Store never runs on the Bubble Tea loop. All disk I/O happens on Store's
   goroutines; calls from the loop return without waiting on disk, except
   `Hydrate`, which runs before owners start (see invariant 4).

### Package shape

```go
package store // substrate/store

// Open resolves every directory, creates the ones Store writes to, loads the
// crypto identity lazily, and starts the persistence goroutine. It touches no
// slice until State asks it to hydrate one. It preloads every persisted slice
// of this instance's scopes into memory so Hydrate is a map lookup.
func Open(opts Options) (*Store, error)

type Options struct {
	Instance     InstanceRef  // who this process is
	Distribution fs.FS        // read-only distribution layer; nil: none
	InstanceDocs map[DocName]Doc // instance layer built by cmd/husk from flags
	Env          Env          // XDG lookups; injectable for tests
	Clock        Clock        // injectable for debounce and GC tests
	Keyring      Keyring      // nil: OS keyring; injectable for tests
	InstanceDeps *instance.Deps // nil: real liveness pings; injectable for log GC tests
	Post         func(func()) bool // runs f on the Bubble Tea loop; false after shutdown.
	                               // Store writes its husk.store slice only through it
	                               // (State writes are loop-only, [State](state.md) invariant 8).
}

type InstanceRef struct {
	ID    string // the name if named, else the decimal pid
	Named bool   // only named instances persist instance and workspace scope
}

// Attach is called once State exists (State is created with the Store as its
// Hydrator, so Store cannot take State in Options). It registers Store's own
// husk.store slice and its Listener.
func (s *Store) Attach(st *state.State) error

// Close flushes every dirty slice within the context deadline (cmd/husk
// gives it 2s) and stops the persistence goroutine. The socket and the
// registration entry are the Server's to remove. Idempotent.
func (s *Store) Close(ctx context.Context) error
```

### Layers

```go
type Doc = map[string]any // decoded JSON object; values are Doc, []any, string, float64, bool, nil
type DocName string       // "config", "keymap"; [a-z0-9_-] segments joined by "/"

type Layer string
const (
	LayerDistribution Layer = "distribution"
	LayerUser         Layer = "user"
	LayerProfile      Layer = "profile"
	LayerInstance     Layer = "instance"
)

type Resolved struct {
	Doc      Doc
	Origins  map[string]Layer // dotted leaf path -> layer that set it
	Problems []Problem        // skipped layers, type clashes; never fatal
}

// Resolve merges distribution < user < profile < instance for one document.
// profile is the active profile name, passed by the caller (the Profile
// service reads it from State); "" skips the profile layer. Results are
// cached per (name, profile) until Invalidate.
func (s *Store) Resolve(name DocName, profile string) (Resolved, error)

// Layers returns one document per layer, unmerged, tagged with its layer and
// file path. The Keybinding service resolves keymaps across layers itself,
// because rank, scope and tombstones are not a document merge
// ([Keybindings](keybindings.md)).
func (s *Store) Layers(name DocName, profile string) ([]LayerDoc, error)

type LayerDoc struct {
	Layer Layer
	File  string // path for problems and `husk keys`; "" for the distribution fs
	Doc   Doc
}

// Profiles lists the profile names that have a directory in the user or
// distribution layer. The Profile service validates the active name against it.
func (s *Store) Profiles() []string

// Invalidate drops every cached layer and resolution. `husk config reload`
// and a plugin added at runtime call it through Services.
func (s *Store) Invalidate()

// Patch applies a merge patch (same rules as layer merge, null deletes) to
// one writable layer's file: user, profile (needs profile name) or the named
// instance's file. Comments and formatting of untouched parts survive.
// A hidden per-file .<doc>.json.lock covers the complete read-modify-write.
func (s *Store) Patch(name DocName, layer Layer, profile string, patch Doc) error

// Declared documents: in-memory only, written by Pods' manifest conversion,
// read by Services during a reload and by the action catalog adapter that
// cmd/husk hands the Dispatcher (Stem never imports Store, invariant 4).
// Safe for concurrent use: the loop declares while a reload reads.
// Two names exist in v1:
// "plugins" (per-plugin catalog: actions, surfaces, subscriptions, hooks,
// secret requests, settings schema) and "focus-keymap" (focus-local bindings).
// source is the plugin id. Never persisted, never merged into layers.
func (s *Store) Declare(name DocName, source string, d Doc)
func (s *Store) Undeclare(source string)
func (s *Store) Declared(name DocName) map[string]Doc // source -> doc
```

Merge rules, applied layer by layer from lowest to highest:

| Lower value | Higher value | Result |
| :--- | :--- | :--- |
| object | object | Recursive merge key by key |
| any | `null` | Key removed from the result; later higher layers may set it again |
| absent | `null` | No-op |
| any | array | Higher array replaces the lower one whole; no element merge, no append |
| any | scalar | Higher scalar wins |
| object | non-object, or non-object to object | Higher value wins whole; a `type_clash` Problem names the path and both layers |

Consequence for every Service: a collection that users must be able to extend
or partly override is modelled as an object keyed by id, never as an array.

Example. Distribution `config.json`, user `config.json`, result:

```json
{"theme": {"name": "Evening", "palette": {"canvas": "#101018", "accent": "#8fa8ff"}}, "husk": {"plugins": {"dirs": ["./plugins"]}}}
{"theme": {"palette": {"accent": null, "ink": "#f0f0f0"}}, "husk": {"plugins": {"dirs": ["~/plugins"]}}}
{"theme": {"name": "Evening", "palette": {"canvas": "#101018", "ink": "#f0f0f0"}}, "husk": {"plugins": {"dirs": ["~/plugins"]}}}
```

Instance layer: two parts merged in order, the named instance's file under the
config directory, then the documents `cmd/husk` builds from flags. Flags map to
settings keys under the host's `husk.*` namespace in document `config`
([Services](services.md)), for example:

```json
{"husk": {"plugins": {"paths": ["./plugins/reference"], "dirs": ["./plugins"]},
          "layout": {"rail": "stage"},
          "input": {"hold_alt": true}}}
```

Store does not know these paths mean anything; `cmd/husk` owns the flag table
and Services own the schema.

File format: config documents are HuJSON (JSON plus comments and trailing
commas, `github.com/tailscale/hujson`), extension `.json`. HuJSON keeps `null`,
which the delete rule needs, and its patch support keeps user comments when
`Patch` writes. Slice and secret files are plain JSON written by Store.

### Sources

```go
type Scope string // "user" | "instance" | "workspace"; same values as state.Scope

type Key struct {
	Scope    Scope
	Instance string // set for Scope instance and Scope workspace: the instance name
	Path     []string
}

type Entry struct {
	Key      Key
	Size     int64
	Modified time.Time
}

var ErrNotFound = errors.New("store: not found")

type Source interface {
	ID() string // "local", later "cloud:<account>"
	Get(ctx context.Context, k Key) ([]byte, Entry, error)
	Put(ctx context.Context, k Key, data []byte) error // atomic, see invariant 1
	Delete(ctx context.Context, k Key) error           // missing is not an error
	List(ctx context.Context, prefix Key) ([]Entry, error)
}
```

v1 ships `LocalSource`. `CloudSource` is the name reserved for the later
implementation; no part of it is specified here.

### Sync

```go
type Syncer interface {
	// Reconcile makes a and b agree for every key under prefix. v1 policy:
	// the entry with the later Modified wins; on equal times the source whose
	// ID sorts first wins. Deletions propagate as tombstones the Syncer keeps.
	Reconcile(ctx context.Context, a, b Source, prefix Key) (SyncReport, error)
}
```

v1 has one source, so the only implementation is `NoopSyncer`. Multiple
instances on one machine share `LocalSource` directly; atomic rename gives
last writer wins without a Syncer.

### Crypto

```go
type Sealer interface {
	Seal(plain []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// Secrets returns one encrypted namespace, user scope, stored as one file.
// The Secrets service owns grant policy; Store only stores bytes.
func (s *Store) Secrets(namespace string) (SecretDoc, error)

type SecretDoc interface {
	Get(key string) ([]byte, bool, error)
	Put(key string, value []byte) error
	Delete(key string) error
	Keys() ([]string, error)
}
```

- Library: `filippo.io/age`, X25519 recipient, one age file per secret
  namespace.
- Key source: an age identity in the OS keyring through
  `github.com/zalando/go-keyring` (macOS Keychain, Secret Service on Linux),
  service `husk`, account `store-identity`. Generated on first secret write.
- Only definite absence permits identity creation: keyring `ErrNotFound`,
  `ErrUnsupportedPlatform`, D-Bus `ServiceUnknown` or `NameHasNoOwner`, or no
  session bus (no discoverable address, missing `dbus-launch`, or a missing
  Unix bus socket). This allows headless Linux without Secret Service to use
  secrets. These are narrow classifications, not a general unavailable fallback.
- Locked collections, timeouts, `NoReply`, permission failures and other I/O
  uncertainty return `unavailable`, emit a diagnostic and never create a new
  identity or `identity.txt`: a transient outage must not shadow an existing
  key and strand its encrypted namespaces. An unclassified launcher exit error
  is uncertainty, not proof of absence.
- If that definite absence permits creation but the subsequent keyring write
  fails, the new identity falls back to
  `$XDG_DATA_HOME/husk/keys/identity.txt`, mode 0600. An existing private fallback
  identity raises `keyring_fallback` at every `Open`, before any secret access;
  key material remains lazily loaded.
- What is secret: secret namespaces, and only those. Config documents are
  never secret; credentials do not belong in config. State slices are never
  secret either: every slice is readable by every reader ([State](state.md)
  invariant 14), so a secret in a slice would leak through `state.get`.
- Out of scope for v1: key rotation (only `husk store reset-secrets`, which
  deletes every sealed file), passphrase unlock, sharing the key across
  devices (the cloud source must solve it before syncing secrets), memory
  locking, protection from malware running as the same user, and integrity
  checks on non-secret files.

### Retention

Store implements State's `Hydrator` interface and registers a `Listener`;
both are defined in [State](state.md), with `state.Spec` as the registration
shape (namespace, owner, scope, `Persistent`, `Retention{Policy, N, TTL}`,
`MaxBytes`). Store adds nothing to it.

Registration is not a persistence write when its normalized value equals the
value Store hydrated or last persisted. It creates no new generation, touches
no mtime and does not renew TTL. The same no-op check applies to workspace
batch peers; `ttl` and `keep-last-n` count real value changes only. Expired TTL
values are not equality baselines. Deleting a persisted current record clears
its cached value and baseline, so a later identical committed value is written
rather than silently lost. A newer
value queued during an in-flight write keeps its own debounce/failure state.
On unregistration, previously committed pending changes remain queued for an
immediate background flush (and exit retry); teardown never silently drops them.

```go
// state.Hydrator, implemented by Store. Called synchronously inside
// state.Register for a slice with Persistent true. Store preloaded the
// values in Open, so this is a map lookup and does no I/O.
func (s *Store) Hydrate(spec state.Spec) (value json.RawMessage, ok bool)

// The state.Listener Store registers in Attach. Runs on the loop after each
// Flush; marks the persistent slices among changes dirty and returns.
func (s *Store) onFlush(changes []state.Change, snap *state.Snapshot)

// Called by Pods when a plugin is uninstalled: deletes every persisted copy
// of <pluginID>.state in every scope.
func (s *Store) RemovePlugin(pluginID string) error

// Called by state.clear: deletes shared user copies and this instance's own
// instance/workspace copies only, then resets the owner's slice to Initial.
// Other named instances are untouched; only RemovePlugin spans every instance.
func (s *Store) ClearSlice(namespace string) error

// Logs and runtime, handed to Pods so they never build paths. RuntimeDir
// and SocketPath delegate to the instance package, which owns the rule.
func (s *Store) RuntimeDir() string
func (s *Store) SocketPath() string // instance.SocketPath(RuntimeDir(), instance id)
func (s *Store) OpenPluginLog(pluginID string) (*os.File, error)

func (s *Store) GC(ctx context.Context) (GCReport, error)
func (s *Store) Check(ctx context.Context) (CheckReport, error)
func (s *Store) Paths() Paths // every resolved directory, for `husk store paths`
```

Policy semantics on disk, by `Retention.Policy`:

| Policy | Files kept | Hydrates from | GC removes |
| :--- | :--- | :--- | :--- |
| `keep-latest` | `current.json` | `current.json` | nothing |
| `keep-last-n` | `gen-<unix-nanos>-<instance>.json`, newest N | newest generation | generations beyond N |
| `ttl` | `current.json` | `current.json` if mtime within TTL, else nothing | the file once expired |
| `manual` | `current.json` | `current.json` | nothing; only `husk state clear <ns>` or uninstall |

Slice file envelope:

```json
{"husk_store": 1, "namespace": "example.notes.state", "scope": "instance",
 "instance": "notes", "written_at": "2026-10-22T06:00:00Z",
 "retention": {"policy": "keep-latest"},
 "value": {"open_tabs": 3}}
```

The optional `retention` object contains `policy`, `n` and `ttl` (TTL is a
Go duration encoded as integer nanoseconds). It lets startup GC enforce a known
policy before owners register, without bumping `husk_store` above 1. A file
without it has unknown policy: startup GC skips it until a registration supplies
the policy. An optional `registered_at` timestamp records the registration
associated with the last real write for the 90-day orphan report; current
in-memory registrations also count. No-op starts do not rewrite that metadata.
Legacy files fall back to `written_at` or mtime.

Scope rules:

- `user`: shared by every instance; last writer wins.
- `instance`: persisted only for named instances, keyed by the instance name.
  An anonymous (pid) instance keeps instance-scope slices in memory only, so
  nothing accumulates per pid.
- `workspace`: the named instance's workspace snapshot, keyed by the instance
  name like `instance` scope and persisted only for named instances. The
  difference from `instance` scope is how Store writes it: every workspace
  slice of the instance is written and restored together as one overwritten
  snapshot directory, never appended to ([State](state.md) decision 18). In v1,
  "together" means one batch from one State snapshot, with per-file atomic
  overwrites in the directory layout below; there is no cross-file atomicity
  guarantee. Transactional directory publication is a possible later version.
  Two instances never share a workspace; sharing is an application's concern.

### Directory tree

All bases follow XDG on every Unix, macOS included. Unset variables fall back
to the XDG defaults below. Windows is out of scope (Unix sockets).

```text
$XDG_CONFIG_HOME/husk/                  ~/.config/husk          0700
  <doc>.json                            user layer, e.g. config.json, keymap.json
  profiles/<profile>/<doc>.json         profile layer
  instances/<name>/<doc>.json           instance layer file part, named instances

$XDG_STATE_HOME/husk/                   ~/.local/state/husk     0700
  user/slices/<namespace>/              user scope slices
    current.json | gen-*.json
  instances/<name>/meta.json            last start, last clean exit
  instances/<name>/slices/<namespace>/  instance scope, named instances only
  instances/<name>/workspace/<namespace>/current.json
                                        workspace scope, named instances only, overwritten as one snapshot
  logs/<instance id>/<plugin id>.log    plugin stderr, 0600

$XDG_DATA_HOME/husk/                    ~/.local/share/husk     0700
  secrets/<namespace>.age               secret namespaces, user scope
  keys/identity.txt                     definite absence + keyring write failed, 0600

$XDG_RUNTIME_DIR/husk/                  fallback /tmp/husk-<uid>/   0700, rule owned by Instances
  <instance id>.sock                    the instance socket, 0600, written by the Server
  <instance id>.json                    registration entry, written by the Server

<distribution fs.FS or dir>/            read-only, supplied by a distribution
  <doc>.json, profiles/<profile>/<doc>.json
```

A slice namespace is one path segment and follows exactly State's rule
(`[A-Za-z0-9_.-]`, at most 128 bytes, `<owner>.<name>`), which contains no
separator and no `..`. Store rejects any other namespace at hydrate time
rather than sanitizing it, so two namespaces never map to one directory.
Instance names follow [Instances](instances.md): `[a-z0-9][a-z0-9-]*`, at most
32 bytes, never all digits.

Every directory Store creates is checked after creation with `Lstat`: it must
not be a symlink, must be owned by the current uid, and must have mode 0700.
The runtime directory is resolved and checked by `instance.RuntimeDir`
([Instances](instances.md)); Store calls it and fails `Open` when it fails.
An existing runtime root follows that module's rule: current-uid ownership and
no group/other permission bits, not an exact requirement on the owner's bits.
Today's per-run `mkdtemp` directory is replaced by that check.

### Invariants

1. Every write is atomic: write to `.<name>.tmp-<random>` in the target
   directory with mode 0600, `fsync` the file, `rename` over the target,
   `fsync` the directory. No reader ever sees a partial file.
2. Only Store opens, creates, renames or deletes files Husk owns. Services and
   Stem never touch a file. Pods receive file descriptors and paths from Store
   (`OpenPluginLog`, `SocketPath`) or from the `instance` leaf package, which
   owns the runtime directory rule, and bind or pass them on, never build them.
3. Store never interprets a key. Merge, retention and size checks are
   identical for every document and slice.
4. A persistent slice is hydrated before `state.Register` returns, so its
   owner, a Stem module or a plugin launched by its Wrapper after
   registration, never sees an empty slice that has a stored value.
5. A never-registered slice is never written. Pending changes from a previously
   registered persistent slice are flushed on removal, even after it leaves
   State. A registration with `Persistent: false` is never written.
6. Persistence never blocks the event loop. `onFlush` marks dirty and
   returns; the debounce and the write happen on Store's goroutine.
7. A config file written by the user is never moved, rewritten or deleted
   except by an explicit `Patch` or CLI command.
8. A file Store cannot read is never overwritten. Read errors are `unavailable`,
   not evidence of corruption; only readable malformed slice/metadata bytes are
   quarantined. Config and encrypted secrets stay untouched until repaired.
9. Higher layers always win; layer order is fixed and not configurable.

### Limits and timings

| Limit | Value | Reason |
| :--- | :--- | :--- |
| Debounce quiet window | 500 ms | Coalesces a burst of changes into one write |
| Debounce maximum delay | 5 s | A slice that changes constantly still reaches disk; bounds loss on crash |
| Exit flush deadline | 2 s | Matches the plugin stop grace so shutdown stays under about 4 s |
| Slice caps | State's: 64 KiB core, 256 KiB plugin, 1 MiB workspace and hard ceiling | [State](state.md) refuses the write; Store re-checks the encoded size at persist and never writes a larger file |
| `keep-last-n` maximum N | 20 | Bounds disk use per slice to 20 times its cap |
| Config file maximum size | 1 MiB | A larger hand-edited file is a mistake, not config |
| Secret value / namespace plaintext JSON | 64 KiB per value / 64 MiB per namespace | Bounds decryption memory; ciphertext reads allow additional age framing overhead, so every accepted write can be read back |
| Merge maximum depth | 64 | Stops pathological documents before recursion cost matters |
| GC interval | at start, then every 1 h | Off the loop; cheap directory listings |
| Stale temp file age | 10 min | Only Store's `.<name>.tmp-<random decimal suffix>` pattern is eligible; similar hidden user files are not |
| Quarantined copies per file | 3, deleted 30 days after quarantine | Quarantine refreshes mtime; an old corrupt file gets a full evidence window |
| Plugin log retention | 14 days; target 20 instance log directories | Live/unresponsive instances and recent logs (including empty ones) are protected; only eligible empty directories are pruned to the soft count target |

### Corrupt and unreadable files

| Condition | Who detects | What happens | What the user or plugin sees |
| :--- | :--- | :--- | :--- |
| Readable slice file fails to parse or has no envelope | retention, at hydrate | Renamed to `<file>.corrupt-<UTC stamp>` with a fresh quarantine mtime; slice hydrates empty; next change writes fresh | `store` diagnostic entry, warn toast "Slice reset: <namespace>"; plugin finds its slice empty |
| Slice file cannot be read (permissions/mode) | retention, at preload | File untouched, not quarantined; persistence disabled this run until explicit clear/restart | `unavailable` diagnostic; owner starts at Initial |
| Readable instance `meta.json` is malformed | Store, at Open | Quarantined with fresh mtime; fresh metadata written; startup continues | `metadata_reset` diagnostic; evidence available through Check |
| Slice envelope `husk_store` newer than known | retention, at hydrate | File left untouched; slice hydrates empty; persistence of that slice disabled this run | Diagnostic "written by a newer Husk"; plugin finds slice empty |
| Config file fails to parse | layers, at resolve | That layer skipped for that document; previous good resolution kept after a reload | `Resolved.Problems` with file, line, column; Settings surfaces it; `husk config reload` reports it |
| Config file over 1 MiB or depth over 64 | layers | Layer skipped | Same as parse failure |
| Secret file fails to decrypt (missing or wrong key) | crypto | File untouched; namespace returns an error; writes to it refused | Secrets service error; diagnostic suggests `husk store reset-secrets` |
| Keyring lookup is transient/uncertain (locked, timeout, NoReply, I/O) | crypto | `unavailable`; no new identity or fallback file; ciphertext untouched | Diagnostic; retry after keyring recovers |
| Keyring identity/platform/service/session bus is definitely absent, then keyring write fails | crypto | New identity saved to private fallback file; headless Linux remains usable | `keyring_fallback` diagnostic at creation and every later Open |
| Write fails (disk full, permission) | retention | In-memory value kept, slice marked dirty, retried on next change and at exit | Diagnostic with errno; exit flush failures printed on stderr after the TUI ends |
| Slice value over its cap at persist | retention | Not written; last persisted value kept on disk | Diagnostic; State is assumed to have refused the write already and replied to the plugin |
| Runtime directory fails owner or mode check | sources, at `Open` | `Open` fails; `cmd/husk` exits 1 | stderr: "runtime directory <path> is not private: <reason>" |
| Flush deadline passes at exit | retention | Remaining slices abandoned | stderr line naming each unflushed namespace |
| Process killed (SIGKILL, power loss) | none | Up to 5 s of changes lost; atomic rename keeps old files intact | Previous values on next start |

Store's own State slice is `husk.store` (owner `husk`, not persistent): the
last 50 diagnostics, the quarantined files of this run, and whether the
keyring fallback is active. Store's goroutines never write it directly; they
hand the write to the loop through `Options.Post`. That makes Store's health
visible to `husk state get husk.store` and to any subscriber of its change event.

A core slice write must never fail because of a recoverable condition. Store
keeps the last 50 diagnostics internally, but bounds its published health JSON
to State's 64 KiB core cap. Only when it exceeds that cap, the summary removes
oldest quarantine paths first, then oldest diagnostics, and shortens an oversized
final diagnostic if needed. It marks such a summary `truncated: true`; `Check`
remains the full file inventory. Trimming a published summary never deletes
files or discards the internal diagnostic history.

### Exposed to the CLI

| Command | Needs a running instance | Calls |
| :--- | :--- | :--- |
| `husk store paths` | no | `Paths` |
| `husk store check` | no | `Check`: parses every file, lists corrupt, quarantined and orphan slice directories (namespaces not registered in 90 days); deletes nothing |
| `husk store gc` | no | `GC` |
| `husk store reset-secrets` | no, refuses while any instance runs | deletes every sealed file and the identity |
| `husk state clear <namespace>` | yes, through the instance | `ClearSlice` deletes shared user and this instance's copies only, then the handler calls `State.Clear` ([State](state.md)); other instances are untouched |
| `husk config reload` | yes | `Invalidate`, through Services |

### Startup and shutdown order

1. `cmd/husk` parses flags into `InstanceDocs` and calls `store.Open`.
2. State is created with Store as `Hydrator`; `Store.Attach` registers the
   `husk.store` slice and the `Listener`.
3. Stem, Services and Pods register their slices; each persistent one is
   hydrated inside `Register`.
4. Pods convert every manifest, `Declare` the plugin documents and register
   each plugin's slice. Then the Services first load runs, reading the
   declared documents; only after it completes do the Wrappers launch the
   processes (the launch gate, [Pods](pods.md)).
5. GC runs once in the background.
6. Exit: the Calls table closes ([Calls](calls.md)), the Server stops
   accepting and unregisters the instance (entry and socket,
   [Instances](instances.md)), Pods stop plugins (2 s grace), then
   `Store.Close` flushes (2 s).

## Level 2: Migration from today

- `cmd/husk/socket_security.go`: `createSocketDir` and its per-run `mkdtemp`
  directory under `$XDG_RUNTIME_DIR` or the temp dir are replaced by
  `instance.RuntimeDir` (fixed `husk/` directory with the ownership check);
  Store's paths come from it. `socketName` (`husk.sock`) is replaced by
  `<instance id>.sock`. `listenPrivate` keeps binding and `chmod 0600` but
  takes the path from `Store.SocketPath`; it moves to the Pods Server. Peer
  uid checks stay where they are.
- `cmd/husk/lifecycle.go`: `openPluginLog`, `pluginLogDir` and
  `sanitizeLogName` move into Store as `OpenPluginLog`; logs move from
  `<socket dir>/logs/` to `$XDG_STATE_HOME/husk/logs/<instance id>/`, so they
  survive reboot and no longer block removal of the runtime directory.
  `removeRuntimeFiles` splits: expired-log cleanup becomes Store GC, socket
  removal becomes `Store.Close`. `tailLines` stays with the Wrapper; it reads a
  path Store returned.
- `cmd/husk/transport.go`: no file access; unchanged by Store.
- `cmd/husk/main.go`: `--plugin`, `--plugin-dir`, `--rail` and `--hold-alt`
  become instance-layer paths (`husk.plugins.paths`, `husk.plugins.dirs`,
  `husk.layout.rail`, `husk.input.hold_alt`) passed in `Options.InstanceDocs`; their
  consumers read the resolved document through Services instead of `main`
  locals. `--dump-frame`, `--demo-frame` and `--demo` stay process flags, not
  config. New flags: `--instance <name>`, `--distribution <dir>`,
  `--profile <name>` ([Services](services.md)). `main`
  opens Store before the listener and closes it after `model.cleanup`.
- `pkg/manifest`: unchanged. It still reads `husk-plugin.json` read-only; the
  catalog it yields reaches Store through `Declare`, called by Pods.
- `pkg/huskipc`: becomes `pkg/protocol` ([Protocol](protocol.md)); its
  `MaxLineBytes` is the line limit State's hard ceiling is derived from.
- New dependencies: `filippo.io/age`, `github.com/zalando/go-keyring`,
  `github.com/tailscale/hujson`.

## Decisions made here

1. Config documents are HuJSON files with a `.json` extension: `null` survives
   for delete, users get comments, `Patch` preserves comments. TOML rejected
   because it has no null.
2. Arrays replace whole on merge; no append syntax. Services model extensible
   collections as objects keyed by id.
3. A type clash between layers is not an error: the higher layer wins and a
   Problem is recorded.
4. XDG directories on every Unix, macOS included, instead of
   `~/Library/Application Support`.
5. Persistent slices live under `$XDG_STATE_HOME`, secrets and the fallback
   key under `$XDG_DATA_HOME`, so a dotfiles repository of `~/.config/husk`
   never carries state or ciphertext.
6. The runtime directory is a fixed `husk/` under `$XDG_RUNTIME_DIR` (fallback
   `/tmp/husk-<uid>/`) with an owner, mode and symlink check, replacing
   today's per-run `mkdtemp` directory, because discovery needs a known place.
   The rule lives in the `instance` package so the CLI can apply it without
   opening Store; Store calls it.
7. Plugin logs move to the state directory, per instance, retained 14 days.
8. Anonymous instances persist no instance-scope and no workspace-scope slices.
9. Workspace scope is keyed by the instance name and written as one
   overwritten snapshot-directory batch, with per-file atomicity only in v1;
   no root hash, no sharing between instances.
10. Instance layer has two parts: a per-named-instance file, then flags.
11. Manifest-derived documents (catalog, focus-local bindings) are in-memory
    "declared" documents in Store, not a fifth layer.
12. Crypto: `filippo.io/age` X25519, identity in the OS keyring via
    `go-keyring`, file fallback with a visible diagnostic.
13. Config and State slices are never secret; only secret namespaces are
    sealed.
14. Corrupt slice files are quarantined beside the original; corrupt config
    and undecryptable secrets are left in place.
15. Orphan plugin slice directories are reported by `husk store check`, never
    deleted automatically; only uninstall deletes them.
16. Store learns of changes through a State `Listener`, not the Stem event
    bus, so Substrate never imports Stem.
17. Store reports its own health in a non-persistent `husk.store` State slice.
18. No file watching in v1; config changes take effect on `husk config reload`.
19. Slice namespaces and instance names are validated, never sanitized.

## Open questions

1. Does "only Store touches files" cover reading plugin packages? This spec
   lets the Pods manifest module read `husk-plugin.json` and lets the Wrapper
   execute plugin binaries, since those files belong to the plugin.
   Recommended: yes, keep that exception and state it in the Pods spec.
2. Should a plugin get a private data directory (for caches, databases) in
   addition to its slice? The notes say a plugin persists only through its
   slice. Recommended: no for v1; revisit when a plugin needs more than its 256 KiB slice.
3. Should the distribution layer also be loadable from a directory flag in
   `cmd/husk`, or only as an `fs.FS` from an application that embeds Husk?
   This spec allows both because a bundle is plain files. Recommended: keep
   both.
4. **Resolved:** a fallback identity is allowed after definite absence of the
   identity, supported keyring platform, Secret Service or session bus, followed
   by a failed keyring write. This supports headless Linux without treating a
   locked collection, timeout, NoReply or I/O failure as absence. Uncertain
   lookups never replace a possibly existing identity. Existing fallback use is
   diagnosed at Open, without waiting for the first secret request.
5. Should the instance-layer flag mapping (`husk.layout.rail`, `husk.input.hold_alt`)
   keep `--rail` and `--hold-alt` at all, given they exist for frame dumps?
   Recommended: keep them as config paths so a named instance file can set
   them; tests keep using the flags.
