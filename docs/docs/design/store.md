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
// of this instance's scopes into memory so Hydrate is a map lookup. With
// Options.Passive, Open writes nothing on its own beyond creating its config,
// state and data directories: no GC, no quarantine, no metadata write or reset.
func Open(opts Options) (*Store, error)

type Options struct {
	Instance     InstanceRef  // who this process is
	Distribution fs.FS        // read-only distribution layer, first part: an embedding application's files; nil: none
	Bundle       fs.FS        // read-only distribution layer, second part: a bundle's config files ([Bundles](bundles.md)); nil: none
	InstanceDocs map[DocName]Doc // instance layer built by cmd/husk from flags
	Env          Env          // XDG lookups; injectable for tests
	Clock        Clock        // injectable for debounce and GC tests
	Keyring      Keyring      // nil: OS keyring; injectable for tests
	InstanceDeps *instance.Deps // nil: real liveness pings; injectable for log GC tests
	Passive      bool         // Open writes nothing on its own (local CLI commands); explicit GC and ResetSecrets still act
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
type DocName string       // "config", "keymap", "themes"; [a-z0-9_-] segments joined by "/"

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
// ([Keybindings](keybindings.md)); the Theme service reads the "themes"
// document the same way, because a named theme is replaced whole by a higher
// layer and keeps its source layer ([Services](services.md), "Named themes").
// Store knows neither document's meaning; both are files named <doc>.json in
// the directory tree below.
func (s *Store) Layers(name DocName, profile string) ([]LayerDoc, error)

type LayerDoc struct {
	Layer Layer
	File  string // config-relative, slash-separated; distribution relative to its root;
	             // "" for a layer with no single file (flag-folded instance)
	Doc   Doc
}

// Profiles lists the profile names that have a directory in the user or
// distribution layer. The Profile service validates the active name against it.
func (s *Store) Profiles() []string
// ProfileSources returns user/distribution provenance off-loop. User directories
// override equal distribution names; the returned map is detached.
func (s *Store) ProfileSources() map[string]Layer

// Invalidate drops every cached layer and resolution. `husk config reload`
// and a plugin added at runtime call it through Services.
func (s *Store) Invalidate()

// Patch applies a merge patch (same rules as layer merge, null deletes) to
// one writable layer's file: user, profile (needs profile name) or the named
// instance's file. Comments and formatting of untouched parts survive.
// A hidden per-file .<doc>.json.lock covers the complete read-modify-write.
// A missing file is created with the patch as its content; a file that does
// not parse is left untouched and the error says so (invariant 8). Its one
// caller in v1 is `settings.set` ([Services](services.md), "Writing one key"),
// which patches a single key of "config" in the user layer.
func (s *Store) Patch(name DocName, layer Layer, profile string, patch Doc) error

// Declared documents: in-memory only, written by Pods' manifest conversion,
// read by Services during a reload and by the action catalog adapter that
// cmd/husk hands the Dispatcher (Stem never imports Store, invariant 4).
// Safe for concurrent use: the loop declares while a reload reads.
// Two names exist in v1:
// "plugins" (per-plugin catalog: actions, surfaces, subscriptions, hooks,
// secret requests, settings schema) and "keymap" (focus-local bindings, D64;
// the same DocName as the layer document, read through a different method).
// source is the plugin id. Never persisted, never merged into layers.
func (s *Store) Declare(name DocName, source string, d Doc)
func (s *Store) Undeclare(source string)
func (s *Store) Declared(name DocName) map[string]Doc // source -> doc
```

`store.Problem` is Store's own diagnostic type (it shapes the `husk.store` slice
and `husk store check` output), not `protocol.Problem`; Services convert load
problems into `protocol.Problem` in one place, `services/settings`.

#### Declared-document schema (Services / Pods boundary)

This is the normalized JSON contract, not a manifest file format. Pods publish
one `plugins` document and one `keymap` document per plugin, keyed by the
same plugin id passed as `source` to `Declare`. Fields mirror the declaration
types in [Pods](pods.md)'s `Spec`, in snake_case. Services import no Pod types.
Pods finish declaring both documents before requesting a `plugin` reload.
`Undeclare(source)` removes both documents on teardown, not on process exit;
its one caller in v1 is `plugin.remove` ([Pods](pods.md), "Removing a plugin",
step 6). It is idempotent: a source with no declared documents changes nothing
and returns normally. Every `Declare` and `Undeclare` advances
`DeclaredRevision`, which is how the readers that cache a view of the declared
documents (the action catalog, the frontend's rail items, slots and frames)
learn to refresh.

A `plugins` document is an object with these fields. Absent arrays mean empty;
unknown fields are retained by Store and ignored by Services for forward growth.

| Field | Normalized value |
| :--- | :--- |
| `id`, `name`, `version`, `description` | Plugin identity/display strings; `id` equals the Store source id. |
| `actions` | Array of ActionDecl objects: `id`, `title`, `description`, `args_schema` (object), `public` (bool). IDs are fully qualified. |
| `surfaces` | Array of SurfaceDecl objects: `id` (surface id), optional `frame` (object). Additional surface declaration fields remain opaque to Services. |
| `keybindings` | Array of FocusBinding objects, identical to the declared `keymap` document's `bind` below. |
| `subscriptions` | Array of event-pattern strings. |
| `events_declared`, `hooks_declared`, `hook_registrations`, `published_slots` | Arrays of the corresponding Pod declaration objects, with snake_case field names; opaque to Services. |
| `slice` | Optional SliceDecl object; opaque to Services. |
| `secret_requests` | Array of SecretRequest objects: `name` (string), `purpose` (string), `required` (bool, default false). Name/purpose form the grant digest; required does not. |
| `settings_declared` | Array of SettingDecl objects: `key`, `type`, `default`, optional `enum`, `min`, `max`, `doc`, `apply`. `default` must be present, including false/zero/empty defaults; null is invalid for the supported types. Types and limits follow [Services](services.md). |

Setting keys at this boundary are **fully qualified**: Pods prefix a manifest's
local key with `plugins.<exact-source>.` once. The plugin-id segment is opaque,
case-sensitive and follows the Pod id regex (uppercase and dots are allowed).
The local key is a single lowercase `[a-z0-9_-]+` segment: conversion rejects
local dots. Settings exposure uses the exact source's declared keys, never a
textual subtree prefix; host keys reach a plugin only when their schema entry is marked `public` ([Services](services.md)). An already-qualified key must match
that same source; a foreign prefix is rejected. Services derive the owner as
`plugin:<source>` rather than trusting an owner supplied by a plugin. `apply`
defaults to `live`; duration values and min/max are integer milliseconds.
A plugin with no `settings_declared` entries has no schema (unknown keys in its
own subtree are retained raw with a warning). A malformed declaration produces
a problem naming its source, never an import of a Pod implementation.

A declared `keymap` document is `{"version":1,"bind":[...]}`. Entries use exactly
one of `key` (string) or `keys` (2–4 strings), plus `action`, required `focus`,
and optional `hint` and scalar-object `args`. The declaring plugin is implicit:
`plugin` and `disable` are not accepted. Focus ownership is checked against
`surfaces[].id`; action ownership follows [Keybindings](keybindings.md).
Focus-local actions may target the declaring plugin's actions or `husk.*`.
Pods conversion drops foreign, unscoped and over-limit (`too_many`) bindings. It passes key spellings through
unchanged: Keybinding is the single key parser and drops invalid spellings with
`unparsable_key` in layer `plugin:<source>`. Optional `problems` is an array of
`{index,code,message}` objects, where index is the original zero-based manifest
position and code is `foreign_action`, `global_not_allowed` or `too_many`. Keybinding
imports these into its problems with layer `plugin:<source>`; conversion
diagnostics are never lost.
Ignored legacy shortcut suggestions may be carried as `suggestions` (an array
of objects with `key` and `action`) for `suggestion_ignored` diagnostics; they
never enter the key index.

Example (source `files`):

```json
{"id":"files","name":"Files","actions":[{"id":"files.new","title":"New","public":true}],"surfaces":[{"id":"sidebar-list"}],"keybindings":[{"key":"n","action":"files.new","focus":"sidebar-list"}],"secret_requests":[{"name":"git.token","purpose":"Push over HTTPS","required":false}],"settings_declared":[{"key":"plugins.files.show_hidden","type":"bool","default":false,"apply":"live"}]}
```

The matching declared `keymap` document:

```json
{"version":1,"bind":[{"key":"n","action":"files.new","focus":"sidebar-list"}]}
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
{"theme": {"palette": {"accent": null, "ink": "#f0f0f0"}}, "husk": {"plugins": {"dirs": ["./my-plugins"]}}}
{"theme": {"name": "Evening", "palette": {"canvas": "#101018", "ink": "#f0f0f0"}}, "husk": {"plugins": {"dirs": ["./my-plugins"]}}}
```

Instance layer: two parts merged in order, the named instance's file under the
config directory, then the documents `cmd/husk` builds from flags. Flags map to
settings keys under the host's `husk.*` namespace in document `config`
([Services](services.md)), for example:

```json
{"husk": {"plugins": {"paths": ["./plugins/reference"], "dirs": ["./plugins"]}}}
```

Store does not know these paths mean anything; `cmd/husk` owns the flag table
and Services own the schema.

Distribution layer: two parts merged in order with the same rules, the
embedding application's `Options.Distribution` files, then the bundle's
`Options.Bundle` files ([Bundles](bundles.md)). Both are layer `distribution`
in `Origins`, `Problems` and `LayerDoc`; a `LayerDoc.File` for a document both
parts supply is `""`, as for the flag-folded instance layer. `Profiles` and
`ProfileSources` list profile directories from both parts as `distribution`.
Either part may be nil.

One setting must be known before Store opens, because it names what the
distribution layer is made of: `husk.bundle`. `cmd/husk` reads it with
`ReadBootstrap`, which is the one Store read that happens before `Open`:

```go
// ReadBootstrap reads the layers that can be known before Open: the user
// config.json, then the named instance's instances/<name>/config.json, then the
// flag-folded instance documents, merged in that order with the layer rules. It
// returns the value of each bootstrap key (v1: "husk.bundle") and the layer
// that set it. A file it cannot parse counts as absent for this read; Resolve
// reports the parse problem later as for any layer. It opens no directory,
// creates nothing and caches nothing.
func ReadBootstrap(env Env, inst InstanceRef, instanceDocs map[DocName]Doc) (Bootstrap, error)

type Bootstrap struct {
	Bundle      string // "" when no layer names one
	BundleLayer Layer  // user or instance; "" when Bundle is ""
}
```

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
- Before generating any new identity, Store checks for existing `secrets/*.age`
  namespaces. Any sealed namespace means an identity already exists somewhere:
  even `ServiceUnknown` may be a provider that has not started yet. Creation is
  refused with `unavailable` and a diagnostic explaining the sealed namespaces;
  neither the keyring identity nor `identity.txt` is replaced. An unreadable or
  unsafe namespace directory also fails closed. Restore the provider/key, or
  run `husk store reset-secrets` (`ResetSecrets`) before creating a different
  identity.
- If definite absence and no sealed namespaces permit creation but the subsequent
  keyring write fails, the new identity falls back to
  `$XDG_DATA_HOME/husk/keys/identity.txt`, mode 0600. An existing private fallback
  identity raises `keyring_fallback` at every `Open`, before any secret access;
  key material remains lazily loaded.
- What is secret: secret namespaces, and only those. Config documents are
  never secret; credentials do not belong in config. State slices are never
  secret either: every slice is readable by every reader ([State](state.md)
  invariant 14), so a secret in a slice would leak through `state.get`.
- Out of scope for v1: key rotation (only `husk store reset-secrets`, which
  deletes every sealed namespace and both identity copies, the keyring entry
  and `identity.txt`), passphrase unlock, sharing the key across
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

// Deletes every persisted copy of <pluginID>.state in every scope. Reserved
// for an uninstall; no v1 command calls it. `plugin.remove` ([Pods](pods.md))
// is an unload and keeps every persisted copy.
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

// ResetSecrets deletes every sealed secrets/<ns>.age file (and the
// .<ns>.age.tmp-* crash temps of their writes), the keyring identity and
// keys/identity.txt, and nothing else: lock files, config, slices and logs
// stay. The caller must have established that no instance runs. An absent
// keyring, directory or file is not an error; any other failure stops the
// reset and is returned, so a half-reset is reported, never hidden. The cached
// identity is dropped and keyring_fallback cleared.
func (s *Store) ResetSecrets() error
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
  <doc>.json                            user layer, e.g. config.json, keymap.json, themes.json
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

<distribution fs.FS>/                   read-only, embedded by an application (first part)
  <doc>.json, profiles/<profile>/<doc>.json
<bundle dir>/                           read-only, named by --bundle or husk.bundle (second part, [Bundles](bundles.md))
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
The runtime directory is resolved and checked by the `instance` package
([Instances](instances.md)) and handed to Store by its opener as
`Options.RuntimeDir`: start mode passes `instance.RuntimeDir()` (creates), the
store commands pass what `instance.LookupRuntimeDir()` found, or `""` when the
directory is absent, which Store treats as "no instance registered" for its
liveness probes. Store never creates the runtime directory.
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
   quarantined, and never by a passive `Open` (`Options.Passive`): the store
   commands leave them in place and report them (`Check`, `unavailable`).
   Config and encrypted secrets stay untouched until repaired.
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
| GC interval | at start and then every 1 h in start mode; never with `Options.Passive` (only an explicit `GC`) | Off the loop; cheap directory listings |
| Stale temp file age | 10 min | Only Store's `.<name>.tmp-<random decimal suffix>` pattern is eligible; similar hidden user files are not |
| Quarantined copies per file | 3, deleted 30 days after quarantine | Quarantine refreshes mtime; an old corrupt file gets a full evidence window |
| Plugin log retention | 14 days; target 20 instance log directories | Live/unresponsive instances and recent logs (including empty ones) are protected; only eligible empty directories are pruned to the soft count target |

### Corrupt and unreadable files

| Condition | Who detects | What happens | What the user or plugin sees |
| :--- | :--- | :--- | :--- |
| Readable slice file fails to parse or has no envelope | retention, at preload | Renamed to `<file>.corrupt-<UTC stamp>` with a fresh quarantine mtime; slice hydrates empty; next change writes fresh. With `Options.Passive`: left in place, slice blocked as in the row below, listed by `Check` as `invalid_slice` | `store` diagnostic entry, warn toast "Slice reset: <namespace>"; plugin finds its slice empty. Passive: `unavailable` diagnostic |
| Slice file cannot be read (permissions/mode) | retention, at preload | File untouched, not quarantined; persistence disabled this run until explicit clear/restart | `unavailable` diagnostic; owner starts at Initial |
| Readable instance `meta.json` is malformed | Store, at Open | Quarantined with fresh mtime; fresh metadata written; startup continues. With `Options.Passive`: left in place, nothing written, not reported (the store commands open anonymously, so they never read it) | `metadata_reset` diagnostic; evidence available through Check. Passive: nothing |
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
| `husk store check` | no | `Check`: parses every file, lists corrupt, quarantined and orphan slice directories (namespaces not registered in 90 days); deletes nothing, and the passive `Open` before it moves nothing |
| `husk store gc` | no | `GC` |
| `husk store reset-secrets` | no, refuses while `Survey` finds any live or unresponsive instance | `ResetSecrets`: every sealed namespace (and its crash temps), the keyring identity and `identity.txt`; nothing else |
| `husk state clear <namespace>` | yes, through the instance | `ClearSlice` deletes shared user and this instance's copies only, then the handler calls `State.Clear` ([State](state.md)); other instances are untouched |
| `husk config reload` | yes | `Invalidate`, through Services |
| `husk plugin remove <id>` | yes, through the instance | `Undeclare` of the plugin's two declared documents, then a Services reload with reason `plugin` ([Pods](pods.md)); the plugin's slice is unregistered and its pending persistent writes flushed (invariant 5); no file is deleted and `RemovePlugin` is not called |
| `husk plugin add <path>` | yes, through the instance | `Declare` of the plugin's two declared documents and `Register` of its slice (hydrated from the persisted copies a removal kept), then a Services reload with reason `plugin` ([Pods](pods.md), "Adding a plugin"); no layer file is written, so `husk.plugins.paths` and `husk.plugins.dirs` are unchanged and the plugin is not loaded by the next start unless a layer or a flag names it |

Every `husk store` verb opens Store with `Options.Passive`: `Open` itself
collects, quarantines and writes nothing (no startup or periodic GC, no preload
quarantine, no `meta.json` write or reset; it still creates its config, state and
data directories, never the runtime directory). So `check` moves nothing, `gc`'s
report is the whole collection of that run, and `reset-secrets` touches only
secrets. Explicit `GC` and `ResetSecrets` still do what they say.

### Startup and shutdown order

1. `cmd/husk` parses flags into `InstanceDocs`; the composition root
   (`internal/host.New`) calls `ReadBootstrap`, loads the bundle it names if
   any (a bad bundle stops the start here, [Bundles](bundles.md)), and calls
   `store.Open` with the bundle's config as `Options.Bundle`.
2. State is created with Store as `Hydrator`; `Store.Attach` registers the
   `husk.store` slice and the `Listener`.
3. Services register their slices; each persistent one is hydrated inside
   `Register`.
4. The composition root runs the synchronous `startup` flight, reading no
   declared plugin documents ([Services](services.md), "Reload"). A failed
   flight aborts the start.
5. The root reads `husk.plugins.*` from that flight's Settings snapshot and
   converts manifests (an invalid value of either key, or an entry it cannot
   honour, aborts the start), then builds Stem and Pods, which register their
   slices (hydrated the same way). Pods `Declare` the plugin documents,
   register each plugin's slices and request one `plugin` flight for the
   batch. Only after it completes do the Wrappers launch the processes (the
   launch gate, [Pods](pods.md)).
6. GC runs once in the background (start mode only; a passive `Open`
   schedules no GC).
7. Exit: the Calls table closes ([Calls](calls.md)), the Server stops
   accepting and unregisters the instance (entry and socket,
   [Instances](instances.md)), Pods stop plugins (2 s grace), then
   `Store.Close` flushes (2 s).

## Level 2: Migration from today

Plugin settings: landed. The root passes explicit flag documents to Store,
loads Services before manifest conversion, and reads plugin arguments from the
startup snapshot. Configured arrays survive when their flag is absent.

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
- `cmd/husk/main.go`: `--plugin` and `--plugin-dir` become instance-layer
  paths (`husk.plugins.paths`, `husk.plugins.dirs`) in `Options.InstanceDocs`,
  set only when given. The composition root reads them from the startup
  flight's Settings snapshot. `--dump-frame`, `--demo-frame`, `--demo`,
  `--rail` and `--hold-alt` stay process flags, not config. New flags:
  `--instance <name>`, `--bundle <dir>` (folded into `husk.bundle`; the
  directory form of the distribution layer is a bundle, [Bundles](bundles.md),
  so no separate `--distribution <dir>` flag is added), `--profile <name>`
  ([Services](services.md)). `main` opens Store before the listener and closes
  it after `model.cleanup`.
- `pkg/manifest`: deleted in the retirement; `internal/pods/manifest` reads
  `husk-plugin.json` read-only, and the catalog it yields reaches Store through
  `Declare`, called by Pods.
- `pkg/huskipc`: became `pkg/protocol` ([Protocol](protocol.md)) and was deleted; its
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
20. The distribution layer has two parts, an application's embedded files then
    a bundle's files, merged in order like the instance layer's two parts; a
    bundle therefore wins over embedded defaults and loses to the user without
    a fifth layer ([Bundles](bundles.md)). The one setting that names the
    second part, `husk.bundle`, is read by `ReadBootstrap` before `Open`.

## Open questions

1. Does "only Store touches files" cover reading plugin packages? This spec
   lets the Pods manifest module read `husk-plugin.json` and lets the Wrapper
   execute plugin binaries, since those files belong to the plugin.
   Recommended: yes, keep that exception and state it in the Pods spec.
2. Should a plugin get a private data directory (for caches, databases) in
   addition to its slice? The notes say a plugin persists only through its
   slice. Recommended: no for v1; revisit when a plugin needs more than its 256 KiB slice.
3. **Resolved:** the distribution layer is loadable both as an `fs.FS` from an
   application that embeds Husk (`Options.Distribution`) and from a directory
   (`Options.Bundle`); the directory form is a bundle, named by `--bundle` or
   `husk.bundle`, and the two are the two parts of one layer
   ([Bundles](bundles.md)). No `--distribution <dir>` flag exists.
4. **Resolved:** a fallback identity is allowed after definite absence of the
   identity, supported keyring platform, Secret Service or session bus, followed
   by a failed keyring write. This supports headless Linux without treating a
   locked collection, timeout, NoReply or I/O failure as absence. Uncertain
   lookups never replace a possibly existing identity. Existing sealed namespaces
   also forbid creating a new identity, even when the provider reports absence.
   Existing fallback use is
   diagnosed at Open, without waiting for the first secret request.
5. **Resolved:** `husk.layout.rail` and `husk.input.hold_alt` are deleted;
   `--rail` and `--hold-alt` remain process flags.
6. Should plugin paths resolve relative to their layer file? Today every entry
   resolves against the process cwd, without `~` expansion, like the flags.
   Layer-relative resolution is deferred.
