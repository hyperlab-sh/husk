---
type: Design Spec
title: "Services: stateless domain modules"
description: "Profile, Settings, Theme, Keybinding and Secrets as stateless modules over Substrate, with cache, reload and CLI contracts."
status: planned
generated: { by: claude/sonnet-5-5, at: 2026-10-22T06:00:00Z }
tags: [architecture-v2, services]
---

# Services: stateless domain modules

## Level 0: What and why

Services are the five modules that know what a configuration key means: Profile, Settings, Theme, Keybinding and Secrets. [Substrate](state.md) and [Store](store.md) move and persist data without understanding it. Stem and the Pods consume meaning. Services sit between the two: they read the resolved document from Store, check it, turn it into a typed value, and hand that value to whoever asks.

"Stateless" means three things:

- A Service owns no file. It never opens, writes or watches one. All persistence goes through Store.
- A Service holds no truth that cannot be rebuilt. Its only memory is a cache: an immutable snapshot derived from the resolved document, replaced as a whole on `husk config reload` and discarded on failure to rebuild.
- The one value a Service writes is its own State slice (Profile writes the active profile; Secrets writes the grant record). Every other module reads State; none writes a Service's slice.

What Services are not: they are not a settings editor (no writes to config layers in this spec), not a plugin host, and not a message router. They do not know about sockets, Pods or the event loop.

Depends on: [State](state.md) (read, plus own slices), [Store](store.md) (resolved layers, declared documents, encrypted namespace), [Protocol](protocol.md) (the `Palette` wire struct and the request envelope). Services import nothing from [Pods](pods.md); Pods reach Services, not the reverse: manifest conversion supplies focus-local bindings, secret declarations and settings schemas through Store's declared documents, the Pods host asks the coordinator to reload, and the Server supplies a verified caller identity as a plain `Caller` value.

Depended on by: Stem Input (Keybinding answers it; Input hands the resulting action id to the Dispatcher, see [Keybindings](keybindings.md) for the key-handling flow), the Stem renderer (Theme), the Wrapper (Theme reaches plugins through it) and the Server (Settings, Secrets, Theme and Keybinding answer plugin and CLI requests through it). [Calls](calls.md) never touches Services.

## Level 1: Contract

### Shared shape

Every Service has the same lifecycle.

```go
// Snapshot is immutable once published. Generation increases by one per
// successful rebuild of that Service and starts at 1 after first load.
type Generation uint64

// Reloadable is implemented by every Service.
type Reloadable interface {
    // Rebuild derives a new snapshot from in. On error the Service keeps its
    // previous snapshot and returns a Report listing every problem found,
    // not only the first.
    Rebuild(in ReloadInput) (Report, error)
    Generation() Generation
}

// ReloadInput is what the reload coordinator passes to every Service.
type ReloadInput struct {
    Doc      store.Resolved   // the merged "config" document, from Store.Resolve
    Layers   []store.LayerDoc // Store.Layers("keymap", profile): the unmerged keymap documents, for Keybinding ([Store](store.md))
    Declared map[store.DocName]map[string]store.Doc // Store.Declared("plugins") and Store.Declared("keymap"): per-plugin catalog (actions, secrets, settings schema) and focus-local bindings, keyed by plugin id ([Store](store.md))
    Settings settings.Snapshot // set for Theme; zero for the others
}

type Report struct {
    Service  string
    Gen      Generation   // generation now serving, new or unchanged
    Applied  bool         // false when the old snapshot is still serving
    Problems []Problem
}

type Problem struct {
    Severity string // "error" | "warning"
    Layer    string // "husk" | "distribution" | "user" | "profile" | "instance" | "plugin:<id>"
    Key      string // dotted settings key or binding key, when known
    Message  string
}
```

The reload coordinator is a small function in the services package, not a sixth Service. It serializes reloads with a mutex, runs the steps below, and publishes one event.

```go
type Reason string // "cli" | "profile" | "plugin"

// Reload runs on the caller's goroutine, never on the Bubble Tea loop: Store
// re-reads files after Invalidate. Callers on the loop (the husk.profile
// Listener, the Pods host) start it on the coordinator's goroutine and
// receive its completion as a loop message.
func (c *Coordinator) Reload(ctx context.Context, reason Reason) ([]Report, error)
```

Reload order is fixed: Profile, then Settings, then Theme (needs the Settings snapshot), with Keybinding and Secrets built from Store input directly and independent of Settings validity. The coordinator registers a `state.Listener` ([State](state.md), `OnFlush`) for the `husk.profile` slice, so `Profile.Use` triggers a reload without Profile calling the coordinator and without Services importing the Event bus; the Listener only starts the reload off the loop and returns. Reload has three triggers, named in `reason`: `config.reload` (`cli`), a `husk.profile` change (`profile`), and a plugin added or removed at runtime, which the Pods host requests after updating the declared documents (`plugin`). Adding a plugin is a reload, not a State mutation. The Pods host starts a plugin's process only after the `plugin` reload that read its declarations has completed (the launch gate, [Pods](pods.md)), so no plugin is `live` before Keybinding, Settings and Secrets hold its declarations; a reload that fails still completes, and the gate opens with the failure in the report.

After the last Service finishes, the coordinator publishes one event, `services.reloaded`, on the Event bus through a publisher interface the loop injects at construction, the same way State publishes (delivered as `event.deliver`, see [Message kinds](message-kinds.md)); Services import nothing from Stem:

```json
{"method":"event.deliver","params":{"name":"services.reloaded","source":"husk","data":{"reason":"cli","reports":[
  {"service":"profile","gen":3,"applied":true},
  {"service":"settings","gen":3,"applied":true},
  {"service":"theme","gen":2,"applied":false,"problems":[{"severity":"error","layer":"user","key":"theme.palette.ink","message":"ink on slot is 2.1:1, need 4.5:1"}]},
  {"service":"keybinding","gen":3,"applied":true},
  {"service":"secrets","gen":3,"applied":true}]}}}
```

Subscribers: the renderer re-renders on a new Theme generation; the Pods host pushes a `theme.changed` notification ([Protocol](protocol.md)) down each plugin's stdin ([Pods](pods.md)) when the Theme generation changes; nobody else needs the event.

### Profile

Inputs: the list of profile names Store reports (`Store.Profiles()`, [Store](store.md)), the `--profile` instance flag, and the user setting `profile.default`. Output: the active profile name.

```go
type Profile interface {
    Reloadable
    Active() string                   // "" means no profile layer is merged
    List() []ProfileInfo              // sorted by name
    // Use sets the active profile for this instance and writes the State
    // slice. It does not reload; the coordinator does, on the slice change.
    Use(name string) error            // ErrUnknownProfile if no such layer
}

type ProfileInfo struct {
    Name   string
    Source string // "user" | "distribution"
    Active bool
}
```

State slice, registered by Profile at start, named and persisted as [State](state.md) fixes it (`husk.profile`, instance scope, persistent, `keep-latest`; a named instance therefore reopens with its last profile, a pid instance does not):

```json
{"slice":"husk.profile","scope":"instance","persistent":true,"retention":{"policy":"keep-latest"},"value":{"active":"work"}}
```

Initial value: `--profile` flag if given, else the hydrated value for a named instance, else `profile.default` from the user layer, else `""`. A name that no layer defines is rejected at start with a warning in the report and falls back to `""`.

Rebuild after a reload re-validates the active name against the layer list. If the active profile's layer was deleted, Profile reports a warning and posts a reset of the slice to `""` to the loop (invariant 13); Rebuild itself runs off the loop and writes no State.

CLI: `husk profile list`, `husk profile current`, `husk profile use <name>` (this instance only).

### Settings

Inputs: the merged document from Store, plus schemas. Output: validated typed access.

The schema is the only place a settings key is defined. Owners register schema entries; there are three owners: Husk itself (keys under `husk.*`, `theme.*`, `profile.*`, the keymap is a separate `keymap` document, not a settings key, see Keybinding), distributions (keys under `dist.*`), and plugins (keys under `plugins.<id>.*`, from the manifest).

```go
type Type string // "string" | "int" | "bool" | "duration" | "enum" | "strings"

type Entry struct {
    Key      string   // dotted, lowercase, [a-z0-9_.-]
    Type     Type
    Default  any      // required; zero value is allowed but must be written out
    Enum     []string // for TypeEnum
    Min, Max *int64   // for int and duration (milliseconds)
    Doc      string   // one line, shown by `husk settings schema`
    Owner    string   // "husk" | "dist" | "plugin:<id>"
    Apply    string   // "live" (takes effect on reload) | "restart" (read once at start)
}

type Settings interface {
    Reloadable
    Snapshot() Snapshot // current immutable snapshot; never nil after first load
    Schema() []Entry
}

// Snapshot getters never fail. A key missing from the document returns the
// schema default. A key missing from the schema is a programming error: the
// getter returns the zero value and records a "unregistered key" warning once.
type Snapshot interface {
    Generation() Generation
    String(key string) string
    Int(key string) int64
    Bool(key string) bool
    Duration(key string) time.Duration
    Strings(key string) []string
    Raw(key string) (any, bool)   // for `husk settings get`; value as written
    Source(key string) string     // layer that supplied the value, or "default"
}
```

Validation rules, applied to the whole merged document on every rebuild:

1. Unknown key outside `plugins.<id>.*`: error.
2. Unknown key under `plugins.<id>.*` for a plugin that declares no schema: warning, value kept as raw.
3. Wrong type, value outside `Min`/`Max`, or value not in `Enum`: error.
4. A key whose `Apply` is `restart` and whose value changed since start: warning "restart required", the old value stays in the snapshot.
5. Secrets never appear here. A document key whose name matches a registered secret name is an error ("secrets are not settings").

Rebuild is atomic: any error keeps the whole previous snapshot. A bad user value therefore never half-applies.

Plugins reach settings through the Server: `settings.get` returns the plugin's own `plugins.<id>.*` subtree and any `husk.*` key; reading another plugin's subtree is refused. Plugins cannot write settings.

```json
{"id":7,"method":"settings.get","params":{"key":"plugins.files.show_hidden"}}
{"id":7,"result":{"key":"plugins.files.show_hidden","value":false,"source":"user"}}
```

CLI: `husk settings get <key>`, `husk settings list [--source]`, `husk settings schema [--owner X]`, `husk settings validate` (runs a dry rebuild against disk, does not swap).

### Theme

Theme is a pure function: built-in palette plus overrides from the Settings snapshot, checked against the contrast rule, producing one `Palette`.

Today `pkg/theme.Default()` returns the single CharmTone palette, and `TestDefaultPaletteContrast` proves every pairing the chrome draws. This spec keeps exactly one built-in palette. There is no theme registry and no second named theme. A distribution or user changes colours by overriding roles.

```go
type Theme interface {
    Reloadable
    Palette() theme.Palette           // resolved, hex only, contrast-checked
    Wire() protocol.Palette           // same values, wire shape for plugins
    Overrides() map[string]string     // role -> hex, for `husk theme show`
}
```

Override keys live in the settings document under `theme.palette.<role>`, where `<role>` is the JSON tag of a `Palette` field (`canvas`, `ink_muted`, `border_focused`, `slot_background` and so on), plus `theme.name` (string). Values are `#RRGGBB` only. ANSI indices and `#RGB` are rejected, because contrast cannot be computed for a terminal-dependent colour.

```json
{
  "theme": {
    "name": "Evening",
    "palette": { "canvas": "#101018", "accent": "#8fa8ff" }
  }
}
```

Build steps:

1. Start from `theme.Default()`.
2. Apply overrides in layer order (the Settings snapshot already merged them: distribution, user, profile, instance).
3. If `slot_background` was not overridden, recompute it with `SlotBackground(canvas)` so a new canvas drags the slot panel with it.
4. Run `theme.CheckContrast(p) []Violation`.
5. No violations: publish. Violations: the build fails, the previous palette keeps serving, and every violation becomes an error Problem naming the pair, the ratio and the minimum.

Acceptance rule: the contrast table currently inside `TestDefaultPaletteContrast` moves into non-test code as `theme.ContrastPairs()` and `theme.CheckContrast`. The test calls `CheckContrast(Default())` and still fails if the built-in palette regresses. The runtime check uses the same function, so a user theme and the built-in palette are held to one standard: text roles 4.5:1 on every surface they land on, borders and non-text 3:1.

The opt-out `theme.allow_low_contrast` (bool, default false) turns violations into warnings. It is honoured only from the `user`, `profile` and `instance` layers. A distribution layer that sets it is an error: a distribution ships a theme it has checked.

Cache: one resolved palette per Settings generation. Plugins receive the `protocol.Palette` in the `theme.changed` notification that opens their stdin at launch and again after a Theme generation change. The values Theme emits are always `#rrggbb`, a subset of what the wire struct accepts.

CLI: `husk theme show`, `husk theme check` (dry build with the contrast report), `husk theme roles`. Output format follows the global `--format` flag ([Instances and the CLI](instances.md)).

### Keybinding

Keybinding turns the keymap layer documents and the manifest focus-local declarations into one resolved keymap and answers lookups from Input. It does not hold chord state: Input keeps the typed prefix and passes it in on each lookup. The policy it implements (layers and their rank, the keymap document shape, tombstones, sequences, special contexts, what `husk keys` reports) is fixed in [Keybindings](keybindings.md); this section fixes only the service mechanics.

Key spelling is not redefined. A key is a string in the host's `slot_key` spelling produced by `legacyKeyString` (`cmd/husk/input.go`): Space is `" "`, NUL is `"ctrl+@"`, Alt precedes other modifiers (`alt+ctrl+x`), shifted Alt printables use their text (`alt+X`), Num and Scroll Lock are ignored, Caps Lock only affects printable casing. The loader normalizes a written key by parsing it against this spelling and rejects anything it cannot round-trip (`invalid_key` problem).

Inputs, per rebuild:

| Layer | Source |
| :--- | :--- |
| `husk` | the keymap document embedded in the binary |
| `focus-local` | the plugin manifests' `keybindings` arrays, as Pods' manifest conversion declared them in Store |
| `distribution`, `user`, `profile` | the `keymap` document of each layer, unmerged, from `Store.Layers` ([Store](store.md)); Store does not merge keymaps because rank, scope and tombstones are not a document merge |

```go
type Keybinding interface {
    Reloadable
    keybinding.Resolver // Resolve, Hints, KeysFor, Listing: the policy view in Keybindings
}
```

- `Resolve` is pure and allocation-light; Input calls it on every key press with the keys typed since the last match. `Prefix` means Input waits for more keys and owns the sequence timeout (1500 ms, a value Input defines; Keybinding has no clock).
- A rebuild parses every layer, applies the loader rules in Keybindings (drop invalid entries, report duplicates and unreachable sequences, cap entries), and builds the index keyed by first key. It reports every dropped entry as a `Problem`; nothing disappears silently.
- Plugins never declare global bindings. A manifest entry without `focus` is dropped with `global_not_allowed`, a rail item or command `shortcut` with `suggestion_ignored`; the declarations stay legal in the manifest so older plugins load.
- Lookup errors never reach the user. An action id that the catalog does not know still resolves; the Dispatcher reports `not_found` at dispatch, so a typo in config is visible where it matters.

CLI: `husk keys` and its flags are specified in Keybindings; the Server's `keys.list` handler joins `Listing()` with State liveness and the action catalog. There is no `adopt` subcommand.

### Secrets

Secrets decides which plugin may read which secret. It does not store values; Store's encrypted namespace does.

Plugin manifests declare what they want:

```json
{"secrets": [{"name": "git.token", "purpose": "Push and pull over HTTPS", "required": true}]}
```

Grant policy:

- A plugin can read a secret only if its manifest declares the name and the user has confirmed that exact `(plugin id, name)` pair. Confirmation happens once and persists.
- A grant records a digest of the declaration (`name` plus `purpose`). If the manifest later changes the purpose, the grant is void and the next read asks again.
- Only the user can grant, set, revoke or reveal. "User" is a CLI call from a caller with no plugin identity (a bare shell) or a keypress in the host's confirm dialog. A call arriving with a plugin identity can never grant.

```go
type Secrets interface {
    Reloadable
    // Read returns the value if caller holds a grant. ErrNeedsGrant means the
    // manifest declares the name but the user has not confirmed it.
    Read(caller Caller, name string) ([]byte, error)
    Names(caller Caller) []SecretInfo   // never values
    Grant(plugin, name string) error    // user only; Stem confirm dialog or CLI
    Revoke(plugin, name string) error
    Grants() []Grant
}

type Caller struct {
    PluginID string // "" means the user's own CLI
}

type SecretInfo struct {
    Name     string
    Declared []string // plugin ids that declare it
    Granted  []string
    Set      bool     // a value exists in the encrypted namespace
}

type Grant struct {
    Plugin  string
    Name    string
    Digest  string
    At      time.Time
}
```

State slice, registered by Secrets, holding grants only (never values). The namespace follows [State](state.md): owner `husk`, one dot-free name, and a JSON object value:

```json
{"slice":"husk.grants","scope":"user","persistent":true,"retention":{"policy":"manual"},"value":{"grants":[{"plugin":"git","name":"git.token","digest":"sha256:9d2e...","at":"2026-10-22T05:12:00Z"}]}}
```

Reads from a plugin (error codes from the [Protocol](protocol.md) registry, the case in `data.reason`):

```json
{"id":3,"method":"secrets.get","token":"9f2c...e1","params":{"name":"git.token"}}
{"id":3,"result":{"name":"git.token","value":"..."}}
{"id":4,"method":"secrets.get","token":"9f2c...e1","params":{"name":"other.key"}}
{"id":4,"error":{"code":"not_found","message":"plugin git does not declare secret other.key","data":{"reason":"not_declared"}}}
```

A first read with no grant returns `denied` with `data.reason` `needs_grant` and, as a side effect, asks Stem to open the confirm dialog showing plugin name, secret name and the declared `purpose`. The plugin may retry; it gets the value once the user accepts. The dialog times out after 60 s and counts as a denial that is not persisted (`denied`, `data.reason` `user_denied`).

Rules for the value: it exists only in the reply to the one read. Secrets never writes it to State, a log, the event bus or a toast. `husk state` shows the grant slice and nothing from the encrypted namespace.

CLI: `husk secrets list` (names, who declares, who is granted, whether set), `husk secrets set <name>` (value from stdin, never argv), `husk secrets grant <plugin> <name>`, `husk secrets revoke <plugin> <name>`, `husk secrets reveal <name>` (user only, prints value, writes an audit line).

### Limits

| Limit | Value | Reason |
| :--- | :--- | :--- |
| Settings document size | 1 MiB | Bounds reload cost; a config larger than this is a mistake |
| Reload wall time | 2 s | Reload runs while the UI is live; slower means a source problem, abort and keep old snapshots |
| Bindings in the resolved keymap | 4096 | Bounds Lookup memory and `husk keys` output |
| Keys per sequence | 4 | Longer sequences are not typable by memory |
| Secret value | 64 KiB | Credentials and tokens; blocks using Secrets as a blob store |
| Secret name | 1 to 64 chars, `[a-z0-9_.-]` | Same alphabet as settings keys, safe in a CLI |
| Grant dialog timeout | 60 s | An unattended prompt must not hold a plugin forever; counts as denied |
| Plugin `settings.get` / `secrets.get` rate | 20 per second per plugin | A plugin polling in a loop cannot starve the loop |

### Invariants

1. A Service holds no file handle and imports no file-system package for config. Persistence is Store's alone.
2. Services read Store; Store never imports Services. Services read State; the only State slices a Service writes are `husk.profile` (Profile) and `husk.grants` (Secrets).
3. Every snapshot is immutable after publication. Readers never lock; a reader holds the pointer it loaded.
4. A failed rebuild leaves the previous snapshot serving. There is no state in which a Service has no snapshot after first successful load. If the first load fails, Husk starts with the built-in defaults (palette, builtin keymap, empty settings) and shows the error.
5. Rebuild is all-or-nothing per Service.
6. The Theme palette always passes `theme.CheckContrast`, unless `theme.allow_low_contrast` is set by a non-distribution layer.
7. Plugins declare no global bindings. An unscoped declaration never enters the resolved keymap.
8. Any config-layer entry (`distribution`, `user`, `profile`) beats any manifest-declared entry for the same keys. Rank is `husk` < `focus-local` < `distribution` < `user` < `profile` ([Keybindings](keybindings.md)).
9. A secret value never appears in State, settings, events, logs, toasts, `husk state` or an error message.
10. A plugin can never grant, revoke, set or reveal a secret, and can never write settings.
11. Reloads are serialized. A request that arrives during a reload waits and shares its result.
12. Services do not publish events except `services.reloaded` (coordinator) and `secrets.read` (name, plugin id and outcome, no value).
13. `Profile.Use`, `Secrets.Grant` and `Secrets.Revoke` write State and therefore run on the loop; a Server handler posts the call there ([State](state.md) invariant 8). Profile's reset of `husk.profile` after a deleted layer, and the grant that follows an accepted confirm dialog, are posted to the loop the same way through the loop-post function injected at construction (the shape of Store's `Options.Post`). Every other Service method, `Rebuild` and the coordinator's `Reload` included, may run on any goroutine and writes no State.

### Dependency directions

- Services depend on Store and State interfaces, never on their implementations.
- Stem depends on Services (Input on Keybinding, renderer on Theme, dispatcher via Keybinding results). Services never import Stem; the confirm dialog and the lookup of the focused surface cross the boundary as plain values (`FocusContext`) and a callback `func(Prompt) <-chan bool` that Stem supplies at construction.
- Services never import Pods. Plugin identity arrives as `Caller`; plugin declarations arrive as Store's declared documents (`ReloadInput.Declared`).
- Services do not import each other except Theme importing the Settings snapshot interface, and the coordinator importing all five.

### What Services expose

To the Stem: `Keybinding.Resolve`, `Keybinding.Hints`, `Keybinding.KeysFor`, `Theme.Palette`, `Settings.Snapshot`, `Profile.Active`, plus the `services.reloaded` event.

To plugins, through the Server (all authenticated by the caller token the Server resolves): `settings.get`, `secrets.get`, `secrets.names`, `theme.get` (the same `Palette` as `theme.changed`), `keys.list` (only the plugin's own resolved bindings and their layer). Plugins also receive `theme.changed`, which the Pods host pushes down their stdin ([Pods](pods.md)).

To the CLI, through the Server: `husk profile ...`, `husk settings ...`, `husk theme ...`, `husk keys ...`, `husk secrets ...`, `husk config reload`. Each subcommand wraps one method from the [Protocol](protocol.md) table: `profile list|current` wrap `profile.list` (`current` prints the entry with `active`), `profile use` wraps `profile.use`; `settings get|list|schema|validate` wrap the `settings.*` method of the same name; `theme show|roles` wrap `theme.get` (`roles` prints its keys), `theme check` wraps `theme.check`; `secrets list` wraps `secrets.names`, and `secrets set|grant|revoke|reveal` wrap the `secrets.*` method of the same name, which answer `denied` to any caller with a plugin identity.

```json
{"id":1,"method":"config.reload","params":{}}
{"id":1,"result":{"reports":[{"service":"settings","gen":4,"applied":true},{"service":"theme","gen":3,"applied":true}]}}
```

### Failure modes

| Condition | Detected by | What happens | What the user or plugin sees |
| :--- | :--- | :--- | :--- |
| Settings key has the wrong type | Settings rebuild | Rebuild fails, old snapshot serves, Theme keeps its old palette too (it derives from the Settings snapshot) | `husk config reload` prints the problem with layer and key; a warning toast on a live reload |
| Override palette fails contrast | Theme rebuild | Old palette serves | Error lists each pair and ratio; `husk theme check` reproduces it |
| `#RGB` or ANSI index in `theme.palette.*` | Theme rebuild | Same as a contrast failure | Error "role X: want #RRGGBB" |
| Two bindings, same keys and scope, same layer | Keybinding rebuild | First wins, second recorded as a conflict; rebuild still applies | `husk keys` lists the conflict; no toast |
| Key spelling does not parse | Keybinding rebuild | That entry is dropped, the rest apply | Warning problem naming the entry |
| Plugin declares an unscoped binding | Keybinding rebuild | Ignored | `global_not_allowed` problem in `husk keys --problems` |
| Binding names an unknown action | Dispatcher at dispatch | The key resolves, the dispatch fails with `not_found` | Toast "no such action: X" |
| `profile.default` or `--profile` names no layer | Profile start / rebuild | Falls back to `""` | Warning problem; one startup toast |
| Active profile layer deleted | Profile rebuild | Slice reset to `""`, reload proceeds | Warning toast |
| Store cannot resolve the document (source error, parse error) | Coordinator | No Service rebuilds; every snapshot stays | Error from `config.reload`; toast on a UI-triggered reload |
| First load fails | Coordinator at start | Built-in defaults serve | Startup error toast with the first problem |
| Reload exceeds 2 s | Coordinator | Context cancelled; services that finished keep their new snapshot, the rest keep the old | Report shows `applied:false` with "timed out" |
| Plugin reads undeclared secret | Secrets | `not_found`, `data.reason` `not_declared` | Plugin gets the error; nothing shown to the user |
| Plugin reads declared, ungranted secret | Secrets | `denied`, `data.reason` `needs_grant`, confirm dialog opens | Dialog with plugin, name, purpose; plugin may retry |
| Grant dialog times out or is dismissed | Stem callback | Denied, not persisted | Plugin gets `denied`, `data.reason` `user_denied` |
| Granted secret declaration changed in the manifest | Secrets digest check | Grant void | Next read prompts again |
| Encrypted namespace unreadable (bad key, corrupt) | Store crypto | `Read` returns `unavailable`; other Services unaffected | Plugin gets the error; `husk secrets list` marks the store broken |
| Plugin exceeds 20 requests/s to `settings.get` or `secrets.get` | Server rate check | Request refused `busy`, `data.reason` `rate_limited` | Plugin gets the error |
| Plugin calls `settings.set` or `secrets.grant` | Server, Services | `denied` | Plugin gets the error |

## Level 2: Migration from today

Today the code has a palette, key resolution and a manifest, and none of the five Services as modules. Nothing below needs a protocol change that breaks a plugin.

- `pkg/theme/theme.go`: keeps `Palette` and `Default()`. Gains `Overlay(base Palette, overrides map[string]string) (Palette, error)`.
- `pkg/theme/contrast_test.go`: the pair table and WCAG helpers (`relLuminance`, `contrast`, `textMin`, `uiMin`) move to a non-test file `pkg/theme/contrast.go`, exported as `CheckContrast` and `ContrastPairs`. The test file keeps the known-value tests and calls `CheckContrast(Default())`.
- `pkg/theme/slot.go`: unchanged. `SlotBackground` is called by the overlay.
- `pkg/huskipc/protocol.go`: `Palette` moves to `pkg/protocol` unchanged; Palette conversion from `theme.Palette` stays where it is. `KeybindSpec` leaves the wire for the manifest with the focus-local shape in [Keybindings](keybindings.md) (`key` or `keys`, `action`, `focus`, `hint`). The new `settings.get`, `secrets.get`, `secrets.names`, `keys.list`, `theme.get` and `profile.*` methods are added by [Protocol](protocol.md).
- `pkg/manifest/manifest.go`: `Plugin` gains three optional fields: `Keybindings` (focus-local, shape per Keybindings), `Secrets []SecretDecl`, `Settings []SettingDecl` (schema entries, keys implicitly prefixed `plugins.<id>.`). `CheckHello` goes with `hello`. Manifest conversion in [Pods](pods.md) emits these as Store catalog entries; `pkg/manifest` itself does not import Services.
- `cmd/husk/input.go`: key spelling code (`legacyKeyString`, `normalizeTextEvent`) stays in place; the Keybinding service imports the spelling rules from a small shared package extracted from it (`keyspelling`), so config parsing and Input cannot diverge. The hard-coded `switch key` for `q`, `ctrl+c`, `?`, `esc`/`f1`/`alt+1`, `[`, `]`, `/` and the rest moves into the `husk` layer as `husk.*` actions. The rail shortcut branch (`railShortcutItem`) and the `m.keyBinding(...)` call are replaced by one `Resolve` call.
- `cmd/husk/commands.go`: `keyBinding` (the when_slot / owner / `isChordKey` precedence) is deleted; its precedence is the resolution in Keybindings. `isChordKey` is dropped with the rule it guarded. `builtinCommands` keeps the titles and categories; its `Shortcut` strings are generated from `KeysFor` instead of written by hand. `footerHints` reads `Hints` and `KeysFor`.
- `cmd/husk/registry.go` (and the registry split called for in [State](state.md)): not a Services concern, but the catalog it holds feeds Store's declared documents, which Keybinding, Settings and Secrets read.
- No file today handles settings, profiles or secrets. All three are new modules under `services/`.

Behaviour change for existing plugins: unscoped manifest `keybindings` entries and rail item `shortcut` values stop binding keys. They keep loading. A distribution or user binds the same keys in config.

## Decisions made here

1. Services live under `services/{profile,settings,theme,keybinding,secrets}` plus a coordinator in `services`, beside `stem/`, `substrate/` and `pods/` under the one import root ([Calls](calls.md)). `pkg/theme` stays public to the host because it already exists and the palette type is shared with the wire converter.
2. Services share one `Reloadable` shape with immutable generation-numbered snapshots and per-service all-or-nothing rebuild. Reloads run through one coordinator that serializes them and publishes a single `services.reloaded` event.
3. Reload order is Profile, Settings, Theme, with Keybinding and Secrets independent of Settings validity. A Settings failure freezes Theme (it derives from Settings) but not Keybinding.
4. `Profile.Use` writes State only; the coordinator reloads on the `profile` slice change. Profile never calls the coordinator.
5. The active profile lives in the `husk.profile` slice, instance scope, persistent `keep-latest` as [State](state.md) fixes it, so a named instance reopens with its last profile. The default comes from `profile.default` in the user layer or the `--profile` flag. Writing the default back to the user layer is not part of this spec.
6. Husk compiles in a `husk` keybinding layer for `husk.*` actions only. A blank Husk must be able to quit and open the palette; this is not a distribution layer.
7. Precedence is `profile > user > distribution > focus-local > husk`, per [Keybindings](keybindings.md): configuration always beats a plugin. An earlier draft put focus-local above the distribution; the policy spec rules.
8. `disable: true` is the tombstone that removes a lower layer's binding for the same keys, focus and plugin ([Keybindings](keybindings.md)); there is no reserved `none` action.
9. `focus` is allowed in every config layer, so a user can rebind or disable a plugin's focus-local key for that surface only.
10. Sequences are written as `keys: [...]`, single keys as `key`. A space-joined string is rejected because `" "` is itself a key.
11. Input owns chord state and the 1500 ms sequence timeout; Keybinding is pure and takes the typed prefix as a parameter.
12. The "plain keys stay with the occupant" rule from `keyBinding`/`isChordKey` is dropped with the rule it guarded; an unbound or disabled key falls through to the occupant instead ([Keybindings](keybindings.md)).
13. Theme accepts `#RRGGBB` overrides only, per role, under `theme.palette.<role>` plus `theme.name`. There is still no second built-in theme.
14. The contrast table becomes production code (`theme.CheckContrast`) so the runtime check and the test share one rule. `theme.allow_low_contrast` demotes failures to warnings and is refused in the distribution layer.
15. Overriding `canvas` recomputes `slot_background` unless the override sets it.
16. Settings validation is whole-document and atomic. Unknown keys under a plugin that has no schema are warnings; elsewhere they are errors. Keys named like a registered secret are errors.
17. Plugin settings schemas come from an optional manifest `settings` section, keyed `plugins.<id>.*`.
18. Secret grants live in a persistent user-scope State slice `husk.grants` that Secrets owns. Values stay in Store's encrypted namespace. The grant records a digest of the manifest declaration so a changed purpose re-prompts.
19. Only a caller with no plugin identity (bare-shell CLI) or the host confirm dialog can grant, revoke, set or reveal. The dialog times out at 60 s and counts as a non-persisted denial.
20. Plugins read settings, secrets, theme and their own keys through request methods on the Server with a 20 req/s per-plugin limit; they do not receive these as environment values.

## Open questions

1. **Caller identity on the socket.** Resolved in [Pods](pods.md): a per-launch token in `HUSK_PLUGIN_TOKEN`, sent as each request's `token` member; no token is the user.
2. **Rail `shortcut` and unscoped manifest `keybindings` stop binding.** This follows "plugins never declare globals" but breaks existing plugins' keys until a distribution or the user binds them. Recommended: accept the break and document it; the keys still appear in `husk keys --problems` as ignored declarations so the migration is a copy of lines, without an `adopt` command.
3. **Should the notes' wording "Husk alone ships none" for distribution cover the `husk` layer?** I read it as covering distribution only, and kept `husk` for `husk.*`. If not, a blank Husk has no keys at all. Recommended: keep `husk`.
4. **Focus-local above the distribution layer.** Resolved: [Keybindings](keybindings.md) ranks focus-local below the distribution ("configuration always beats a plugin"), and this spec follows it.
5. **Persisting the profile choice.** `husk profile use` is instance-only here. Recommended: add `--save` once Store exposes a write API for the user layer, as a separate spec; Services must not write files.
6. **Secrets namespace ownership.** Names are global to the user (`git.token`), shared by any plugin that declares them and is granted. A per-plugin namespace would be safer but blocks two plugins sharing a token. Recommended: global names plus per-plugin grants, as written.
7. **`allow_low_contrast` as an escape hatch.** The acceptance rule is the contrast test, and a hatch weakens it. Recommended: keep it, because a low-vision or high-contrast preference can need ratios the table does not model, and it is refused in distributions.
8. **Live reload on file change.** This spec has reload on command, on profile change and on plugin add or remove only. Recommended: leave file watching to Store `sources` and have it call the coordinator, in the Store spec.
