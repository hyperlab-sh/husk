---
type: Design Spec
title: "Services: what configuration means"
description: "Profile, Settings, Theme, Keybinding and Secrets: the five modules that turn Store documents and plugin declarations into typed values, who may read each value, how a reload replaces them, and what the CLI and plugins see."
status: planned
generated: { by: claude/fable-5-1, at: 2026-10-22T20:00:00Z }
tags: [architecture-v2, services]
---

# Services: what configuration means

## Level 0: What and why

[Store](store.md) merges configuration files and moves bytes. [State](state.md) holds what is true right now. Neither knows what `theme.palette.ink` or `git.token` means. The five **Services** do:

| Service | Question it answers | Who asks |
| :--- | :--- | :--- |
| Profile | Which profile is active, and which exist? | the reload (to pick the profile layer), the CLI |
| Settings | What is the validated value of a settings key, and where did it come from? | the Stem, Theme, plugins (their own keys), the CLI |
| Theme | What palette does Husk draw with? | the renderer, plugins (through `theme.get` and `theme.changed`), the CLI |
| Keybinding | Which action does this key sequence run in this focus context? | Input, the footer and palette, the CLI (`husk keys`) |
| Secrets | May this plugin read this secret, and what is its value? | plugins (`secrets.get`), the CLI |

Every Service works the same way. It reads its inputs (Store documents and the declared documents plugins' manifests were converted into), checks them, and publishes one immutable **snapshot**. Readers hold the snapshot they loaded; a reload builds a new snapshot and swaps it in whole, or keeps the old one when the new inputs are invalid. A Service owns no file and watches no file: all persistence is Store's. A Service holds no truth it cannot rebuild from its inputs, with two exceptions that are State slices, not Service memory: the active profile (`husk.profile`) and the secret grants (`husk.grants`).

What Services are not. They are not a settings editor: no method writes a configuration layer in v1; configuration changes by editing a file and reloading. They are not a plugin host, a message router or a renderer: they know nothing of sockets, Pods, the event loop or the terminal.

### Spec ownership

Three specs touch the same ground. The rule that owns a topic is written once, in the spec named here; the other specs point to it.

| Topic | Owner |
| :--- | :--- |
| Keymap layers and their rank, the keymap file shape, key spelling, tombstones, sequences, resolution, special contexts, the built-in `husk` keymap, what `keys.list` and `husk keys` report, per-file and per-plugin binding limits | [Keybindings](keybindings.md) |
| The Keybinding Service mechanics: inputs, rebuild, generation, where loader problems go, the per-caller view of `keys.list`, the resolved-keymap size cap | this spec |
| Merging of `distribution < user < profile < instance`, file locations, the declared-document schema, the encrypted namespace | [Store](store.md) |
| The `husk.profile` and `husk.grants` slices as slices (scope, retention) | [State](state.md); their contents, this spec |
| Method names, the error-code table, the request envelope, `Palette` wire type | [Protocol](protocol.md); params and results of the `settings.*`, `secrets.*`, `theme.*`, `profile.*` and `config.*` methods, this spec |
| Manifest conversion into declared documents, the launch gate, pushing `theme.changed` down a plugin's stdin | [Pods](pods.md) |
| CLI subcommand syntax, output format, exit codes | [Instances and the CLI](instances.md); which method each `husk settings|theme|profile|secrets|config` subcommand calls, this spec |

Dependencies. Services import [Store](store.md) and [State](state.md) interfaces and the `Palette` type from [Protocol](protocol.md). They import nothing from the Stem or Pods: the confirm dialog, the loop post function, the event publisher and the caller identity arrive as values at construction or per call. The Stem (Input, renderer, palette, footer), the Server handlers and the Pods host depend on Services. [Calls](calls.md) never touches Services.

## Level 1: Contract

### Terms

One meaning each, used throughout.

| Term | Meaning |
| :--- | :--- |
| **snapshot** | The immutable value a Service serves. Replaced whole; never mutated. |
| **generation** | A counter per Service, starting at 0 for the built-in snapshot a Service serves before its first load. It increases by one each time a rebuilt snapshot is published. |
| **rebuild** | One Service deriving a candidate snapshot from a set of inputs and checking it. A rebuild publishes nothing. |
| **candidate** | The unpublished result of a successful rebuild. The coordinator publishes it, or discards it. |
| **reload** | The coordinator running the rebuilds of all five Services on one consistent set of inputs and publishing the candidates. Also called a **flight** when discussing coalescing. |
| **report** | What one Service's rebuild produced: the generation now serving, whether the candidate was published, and every problem found. |
| **problem** | One diagnostic (`error` or `warning`) with the layer, the key and a message. A message never contains a secret value. |
| **layer** | One configuration source: `distribution`, `user`, `profile`, `instance` ([Store](store.md)); for keymaps also `husk` and `focus-local` ([Keybindings](keybindings.md)); for declarations `plugin:<id>`. |
| **declared document** | The per-plugin `plugins` and focus-local keymap documents Pods' manifest conversion writes into Store ([Store](store.md), "Declared-document schema"). |
| **caller** | The verified identity of whoever invokes a Service method. See "Callers". |
| **user** | A `cli` caller. Only a user may change grants, secrets and the active profile, or read every setting. |
| **grant** | The user's recorded consent for one `(plugin id, secret name)` pair, bound to the declaration's digest. |

### Callers

The Server authenticates every socket request and produces the caller ([Pods](pods.md), [Protocol](protocol.md) rule 6). Services never read identity from params and never infer it from a key prefix. The caller type is the one shared caller-identity type used by the Server, the Dispatcher and Services (one definition, no per-layer copies); Services need these two fields:

| Kind | Produced when | `plugin_id` |
| :--- | :--- | :--- |
| `cli` | A socket request carries no `token` (the user's own shell). | empty |
| `plugin` | A socket request carries a valid per-launch token. | the exact plugin id, never case-folded |
| `host` | In-process code of the Stem or frontend calls a Service directly (the renderer, Input, the palette). | empty |

A zero-value caller, an unknown kind, a `plugin` kind with an empty id, or a `cli`/`host` kind with a non-empty id is **invalid**: every method answers `denied` with `data.reason` `invalid_caller`. The table below is the complete authorization matrix. "own" means the caller's exact plugin id, by declaration ownership, never by prefix.

| Operation | `cli` | `host` | `plugin` |
| :--- | :--- | :--- | :--- |
| `settings.get` | any key | any key | own declared keys and host keys marked `public` |
| `settings.list`, `settings.schema`, `settings.validate` | yes | yes | `denied` |
| `theme.get` | yes | yes | yes |
| `theme.check` | yes | yes | `denied` |
| `keys.list` | full listing | full listing | own entries and problems only ([Keybindings](keybindings.md) owns the shape) |
| `profile.list`, `profile.use` | yes | yes | `denied` |
| `config.reload` | yes | yes | `denied` |
| `secrets.get` | any name (audited, see Secrets) | `denied` | declared and granted names |
| `secrets.names` | every name | every name | own declared names |
| `secrets.set`, `secrets.grant`, `secrets.revoke`, `secrets.reveal` | yes | `denied` | `denied` |

The `host` kind is trusted like the host itself for reading and for reload, but it never handles a secret value: the only way the host participates in secrets is the confirm dialog, which answers a question Services asked (see Secrets). A plugin that is denied an operation gets `denied` with `data.reason` `user_only`; a plugin asking for a setting or secret it did not declare gets `not_found` (it must not learn whether another owner's key exists).

### Snapshots, rebuilds and reports

```go
// Generation starts at 0 (built-in snapshot) and increases by one per published rebuild.
type Generation uint64

type Problem struct {
    Severity string `json:"severity"` // "error" | "warning"
    Layer    string `json:"layer"`    // "husk" | "distribution" | "user" | "profile" | "instance" | "focus-local" | "plugin:<id>" | "" when not layer-specific
    Key      string `json:"key,omitempty"`
    Message  string `json:"message"`  // human-readable; never a secret value
}

type Report struct {
    Service  string     `json:"service"`  // "profile" | "settings" | "theme" | "keybinding" | "secrets"
    Gen      Generation `json:"gen"`      // generation now serving
    Applied  bool       `json:"applied"`  // false: the previous snapshot is still serving
    Problems []Problem  `json:"problems,omitempty"`
}

// Every Service implements Reloadable.
type Reloadable interface {
    // Rebuild derives a candidate from in. It publishes nothing. It returns every
    // problem found, not the first. err != nil means no candidate: the Report
    // carries the problems and Applied is false.
    Rebuild(ctx context.Context, in ReloadInput) (Candidate, Report, error)
    Generation() Generation
}

// Candidate publishes at most once. Apply returns the final Report.
type Candidate interface{ Apply() Report }
```

Rules, each testable:

1. A snapshot is immutable after publication. Readers never lock; a reader keeps using the snapshot pointer it loaded even while a reload publishes a new one.
2. A rebuild is all-or-nothing per Service. Any `error` problem means no candidate; the previous snapshot keeps serving; `Applied` is false; the report lists every problem, errors and warnings alike.
3. A rebuild with only `warning` problems produces a candidate. When published, `Applied` is true and the warnings are in the report.
4. A Service always has a snapshot. Before its first load it serves its built-in snapshot (generation 0): no profile, the schema defaults, the built-in palette, the embedded `husk` keymap, no declarations. If the first load fails, the built-in snapshot keeps serving and the failure is reported.
5. Problem messages name layers, keys, roles, ratios and plugin ids. They never contain a secret value, and `Secrets` problems never contain a value or a `purpose` longer than the declaration.
6. `Rebuild` may run on any goroutine and writes no State. Only the posted callbacks named under Profile and Secrets write State.

`ReloadInput` is one consistent set of inputs, gathered once per reload and handed to every Service: the merged `config` document with per-key origins (`Store.Resolve("config", profile)`), the unmerged `config` layers (`Store.Layers("config", profile)`, for Theme's distribution check), the unmerged `keymap` layers (`Store.Layers("keymap", profile)`), the declared documents (`Store.Declared`), the profile the coordinator selected, and, for Theme only, the Settings candidate of the same reload.

### Reload

The **coordinator** is a function of the Services package, not a sixth Service. It owns every reload. Nothing else publishes a candidate.

Triggers, each carried as the reload's `reason`:

| `reason` | Trigger | Who asks |
| :--- | :--- | :--- |
| `cli` | `config.reload` | a `cli` or `host` caller through the Server |
| `profile` | the `husk.profile` slice changed | the coordinator's own State flush listener |
| `plugin` | a plugin's declared documents were added or removed | the Pods host, after `Store.Declare`/`Store.Undeclare` ([Pods](pods.md), launch step 3 and teardown) |

One reload runs these steps in order:

1. Mark the flight as **capturing**. From this moment a new request does not join it (see coalescing).
2. `Store.Invalidate()`, then gather `ReloadInput` on a worker goroutine. Select the profile first: the `husk.profile` slice value, validated against `Store.Profiles()`; an unknown name selects no profile and is reported by Profile. Resolve `config` for that profile. If Store reports a problem other than `type_clash` (a layer that failed to parse or exceeded its limits), the reload **fails as a whole**: no Service rebuilds, every report says `applied:false` with the Store problem, and `config.reload` returns an error.
3. Rebuild in the fixed order Profile, Settings, Theme, Keybinding, Secrets, each on a worker goroutine, each publishing its candidate before the next starts. Theme receives the Settings candidate of this reload; if Settings produced no candidate, Theme is skipped with the problem `settings unchanged; palette unchanged` and `applied:false`. Keybinding and Secrets do not depend on Settings.
4. The whole reload, gathering included, has a **2 s deadline**. When it passes, candidates already published stay; the Service being rebuilt and every later one report `applied:false` with the problem `timed out`; a late candidate is discarded and can never change a generation.
5. Publish one event, `services.reloaded`, with the reason and the five reports, through the publisher the host injected at construction (the same seam State uses; delivered as `event.deliver`, [Message kinds](message-kinds.md)).
6. Answer every waiter with the five reports. `config.reload` returns `{"reports":[...]}`; its reply is an error only when step 2 failed or the deadline passed; a Service that kept its old snapshot is a success reply with `applied:false` in that report.

```json
{"method":"event.deliver","params":{"name":"services.reloaded","source":"husk","data":{"reason":"cli","reports":[
  {"service":"profile","gen":3,"applied":true},
  {"service":"settings","gen":3,"applied":true},
  {"service":"theme","gen":2,"applied":false,"problems":[{"severity":"error","layer":"user","key":"theme.palette.ink","message":"ink on surface is 2.10:1, need 4.5:1"}]},
  {"service":"keybinding","gen":3,"applied":true},
  {"service":"secrets","gen":3,"applied":true}]}}}
```

Coalescing. Reloads are serialized. A request that arrives while no flight runs starts one. A request that arrives while a flight runs and has **not yet captured** joins it. A request that arrives after capture joins the single **queued** follow-up flight (one, whatever the number of requesters); the follow-up starts when the running flight ends and reads fresh inputs, so a `plugin` reload always observes the declarations that preceded it. This is why the launch gate in [Pods](pods.md) can wait on the reload it requested: that reload, or the follow-up it was placed in, read the plugin's documents.

Waiting. A flight belongs to the coordinator, bounded by the 2 s deadline and the coordinator's `Close`. A requester waits with its own context; cancelling it detaches that requester only, never the flight or the queued follow-up. The host closes the coordinator at shutdown; pending waiters get `cancelled`.

What the user sees. `config.reload` prints the reports. A reload the user did not start from the CLI (`profile`, `plugin`, or startup) with at least one `error` problem shows one toast: `<n> configuration problems; run husk config reload` with `<n>` the error count across reports. Warnings alone show no toast. The frontend derives the toast from the `services.reloaded` event; Services show nothing themselves.

Dry runs. `settings.validate` and `theme.check` gather fresh inputs the same way (step 2, same 2 s deadline, `timeout` with `data.reason` `input_deadline` when exceeded) and run the one Service's `Rebuild`, then discard the candidate. `theme.check` first dry-builds Settings to obtain the snapshot Theme would see. They never publish, never change a generation and never emit `services.reloaded`.

### Profile

Inputs: `Store.Profiles()` and `Store.ProfileSources()` (names and whether each comes from the `user` or `distribution` layer), the `--profile` start flag, the `husk.profile` slice, and the user-layer setting `profile.default`. Output: the active profile name; `""` means no profile layer is merged.

State slice, registered by Profile at construction, exactly as [State](state.md) lists it: `husk.profile`, owner `husk`, instance scope, persistent, `keep-latest`, value `{"active":"<name>"}`. A named instance therefore reopens with its last profile; a pid-named instance starts fresh.

Initial value, first match wins:

1. `--profile <name>` was given: that name.
2. The slice hydrated (named instance): the hydrated name.
3. `profile.default` is set in the `user` layer: that name. The `distribution`, `profile` and `instance` layers cannot set it (a profile choosing a profile is circular; a distribution ships a default profile by shipping one profile directory, not by setting this key). A `profile.default` from any other layer is a `warning` problem `profile.default is honoured from the user layer only` and is ignored.
4. Otherwise `""`.

A name that `Store.Profiles()` does not list is not activated: Profile reports a `warning` problem `profile <name> does not exist; using no profile` and the active profile is `""`. On a later reload, if the active profile's directory is gone, Profile reports the same warning and posts a write of `{"active":""}` to the loop; that write triggers one `profile` reload, which finds `""` and runs clean.

Methods:

| Method | Params | Result | Errors |
| :--- | :--- | :--- | :--- |
| `profile.list` | `{}` | `{"profiles":[{"name":"work","source":"user","active":true}]}` sorted by name; `source` is `user` or `distribution` | `denied` |
| `profile.use` | `{"name":"work"}`; `""` selects no profile | `{"active":"work","reports":[...]}`, the reports of the reload this change caused | `denied`; `not_found` with `data.reason` `unknown_profile` when no layer defines the name |

`profile.use` validates the name against the current snapshot, posts the slice write to the loop, waits for the `profile` reload that write triggers (joining it exactly like a `config.reload` requester, with the same deadline) and replies with its reports. It is instance-only: it does not write `profile.default`. Writing a chosen profile back to the user layer (`--save`) is a later spec; Services do not write configuration files.

CLI: `husk profile list`, `husk profile current` (prints the entry with `active:true`, or nothing), `husk profile use <name>`.

### Settings

Settings turns the merged `config` document into typed, validated values. The **schema** is the only place a key is defined; a key without a schema entry has no type, no default and no owner.

#### Namespaces and ownership

| Namespace | Owner | Schema registered by | Readable by | Written by |
| :--- | :--- | :--- | :--- | :--- |
| `husk.*`, `theme.*`, `profile.*` | the host | Husk's built-in schema (compiled in) | `cli` and `host`: all; `plugin`: only entries marked `public` | the user and distribution, in their layer files |
| `dist.*` | the distribution | the embedding application, at Services construction (`Schema []Entry`, owner `dist`) | `cli` and `host`: all; `plugin`: only entries marked `public` | the distribution and the user, in their layer files |
| `plugins.<id>.*` | the plugin `<id>` | that plugin's manifest `settings` section, through the declared `plugins` document | `cli` and `host`: all; the plugin `<id>`: its own entries; other plugins: nothing | the user (and a distribution shipping defaults for a bundled plugin), in their layer files |
| anything else | nobody | cannot be registered | `cli` and `host` see the raw value in `settings.list`; plugins never | |

A plugin's identity in a key is the exact Store source id: `plugins.<exact-plugin-id>.<local-key>`. Plugin ids follow the Pod id regex ([Pods](pods.md) rule 3; uppercase and dots allowed) and are never case-folded. The local key is exactly one segment matching `[a-z0-9_-]+`; conversion rejects a dotted local key ([Store](store.md)). Ownership is by declaration, never by textual prefix: a plugin `a` does not own `plugins.a.b.x`, which belongs to a plugin `a.b` if one declared it, and to nobody otherwise.

No method writes a setting in v1. There is no `settings.set`; plugins cannot change configuration, and a user changes it by editing a layer file and reloading.

`public` host keys. A host or distribution entry is readable by plugins only when its schema entry says `public: true`. The built-in schema marks **no** key public today: the palette reaches plugins through `theme.get`, and the other host keys (plugin search paths, layout and input preferences) are the user's business, not a plugin's. A future key that plugins need is made public by its schema entry, which is the one place that decision is recorded.

#### Schema

```go
type Type string // "string" | "int" | "bool" | "duration" | "enum" | "strings"

type Entry struct {
    Key     string   `json:"key"`     // fully qualified, as above
    Type    Type     `json:"type"`
    Default any      `json:"default"` // required, must validate against Type; a zero value is allowed but must be written
    Enum    []string `json:"enum,omitempty"`    // TypeEnum only; non-empty
    Min     *int64   `json:"min,omitempty"`     // int and duration (milliseconds)
    Max     *int64   `json:"max,omitempty"`
    Doc     string   `json:"doc,omitempty"`     // one line, shown by `husk settings schema`
    Owner   string   `json:"owner"`   // "husk" | "dist" | "plugin:<id>"; set by Services from the source, never trusted from a declaration
    Apply   string   `json:"apply"`   // "live" (default) | "restart"
    Public  bool     `json:"public,omitempty"`  // host and dist entries only; readable by plugins
}
```

| Type | JSON value accepted | Go value readers get |
| :--- | :--- | :--- |
| `string` | string | `string` |
| `int` | integral number within `Min`/`Max` | `int64` |
| `bool` | boolean | `bool` |
| `duration` | integral number of milliseconds within `Min`/`Max` | `time.Duration` |
| `enum` | string present in `Enum` | `string` |
| `strings` | array of strings | `[]string` |

Schema problems are reported against the owner's layer (`husk`, `distribution`, `plugin:<id>`) and are `error` severity: a key that does not belong to its owner's namespace (`foreign schema key`), a duplicate key (`duplicate schema key`), an invalid default (`invalid schema default`), an unknown `type` or `apply` (`invalid schema entry`), a plugin entry whose local key equals one of **that same plugin's** declared secret names (`secrets are not settings`; the comparison is exact and owner-scoped, never a suffix match and never across owners, so a secret named `name` cannot invalidate `theme.name`). A plugin whose declarations contain a schema error has none of its entries registered; the rest of the schema is unaffected.

Built-in host entries (the complete list is `husk settings schema --owner husk`): `profile.default` (string, restart), `theme.name` (string), `theme.allow_low_contrast` (bool, default false), one `theme.palette.<role>` (string) per palette role with the built-in colour as default, `husk.plugins.paths` and `husk.plugins.dirs` (strings, restart), `husk.layout.rail` (enum `""|modal|full-canvas|stage`), `husk.input.hold_alt` (bool). None is `public`.

#### Validation

Applied to the whole merged document on every rebuild. Severity decides the outcome: any `error` means no candidate.

| Condition | Severity | Effect on the candidate |
| :--- | :--- | :--- |
| Known key, value of the wrong type, outside `Min`/`Max`, or not in `Enum` | error | none built; the message names the layer, the key and the expected type |
| Unknown key (no schema entry), in any namespace | warning `unknown key` | the raw value is kept with its layer for `settings.list` and `settings.get` by `cli`/`host`; no plugin ever sees it; typed getters do not know it |
| Known key with `apply: restart` whose value differs from the value at start | warning `restart required` | the value from start stays in the snapshot; removing the key counts as a change back to the default |
| Schema problems listed above | error | none built |

Rationale for unknown keys being warnings: a stale key from a removed plugin or a typo must not freeze every other setting, and every established configuration system (editors, `git config`, service managers) warns and ignores unknown keys. A wrong value of a known key is an error because the user asked for something specific and did not get it; the snapshot stays whole so a half-applied configuration never exists.

#### Snapshot and getters

```go
type Snapshot interface {
    Generation() Generation
    String(key string) string
    Int(key string) int64
    Bool(key string) bool
    Duration(key string) time.Duration
    Strings(key string) []string      // detached copy
    Raw(key string) (any, bool)        // JSON value as written (durations in ms); false when absent
    Source(key string) string          // layer that supplied the value, or "default"
}
```

Typed getters never fail. A key absent from the document returns the schema default. A key absent from the schema is a programming error in the caller: the getter returns the zero value and the Service reports `unregistered key <key>` once per key per process through its warning sink (the host logs it). `host` callers get the full snapshot (`Snapshot()`); a plugin gets a filtered snapshot (`SnapshotFor(caller)`) containing exactly its own declared entries plus `public` host and distribution entries, and nothing else.

#### Methods

| Method | Params | Result | Errors |
| :--- | :--- | :--- | :--- |
| `settings.get` | `{"key":"plugins.files.show_hidden"}` | `{"key":"...","value":false,"source":"user"}`; `value` is the validated value in JSON form (durations in milliseconds); `source` is a layer name or `default` | `not_found` with `data.reason` `unknown_setting` when the key is unknown to this caller (a plugin asking for a key it did not declare gets this, not `denied`); `busy` `rate_limited` |
| `settings.list` | `{}` | `{"entries":[{"key":"...","value":...,"source":"..."}]}`, every known key plus every retained unknown key, sorted by key | `denied` |
| `settings.schema` | `{"owner":"plugin:files"}`; empty or absent means all | `{"entries":[Entry...]}` sorted by key | `denied` |
| `settings.validate` | `{}` | `{"ok":true,"problems":[...]}`; `ok` is whether a reload now would publish a Settings candidate | `denied`; `timeout` `input_deadline`; `unavailable` when Store cannot resolve |

CLI: `husk settings get <key>`, `husk settings list [--source]`, `husk settings schema [--owner <owner>]`, `husk settings validate`.

### Theme

Theme is a function of the Settings snapshot: the one built-in palette, with per-role overrides, checked against the contrast floor, producing one `Palette` ([Protocol](protocol.md) wire type; the same Go type the renderer uses, no copy).

There is exactly one built-in palette and no theme registry. A distribution or user changes colours by overriding roles.

Overrides are the settings keys `theme.palette.<role>`, one per role of `Palette` (the JSON tags: `accent`, `accent_secondary`, `accent_soft`, `border`, `border_focused`, `border_subtle`, `canvas`, `danger`, `info`, `ink`, `ink_muted`, `ink_subtle`, `slot_background`, `success`, `surface`, `surface_elevated`, `surface_hover`, `surface_selected`, `warning`), plus `theme.name` (string). A value is `#RRGGBB`, case-insensitive on input, emitted lowercase. `#RGB`, named colours and ANSI indices are rejected (`role <role>: want #RRGGBB`, error) because contrast cannot be computed for a terminal-dependent colour.

```json
{"theme": {"name": "Evening", "palette": {"canvas": "#101018", "accent": "#8fa8ff"}}}
```

Build, per rebuild:

1. Start from the built-in palette.
2. Apply every `theme.palette.<role>` whose `Source` is not `default` (layer order is already resolved in the Settings snapshot).
3. If `slot_background` was not overridden, derive it from the final `canvas` (`SlotBackground(canvas)`), so a new canvas drags the slot panel with it.
4. Check every pair in the contrast table (`ContrastPairs()`, production code shared with the built-in palette's test): text roles need **4.5:1** on every surface they are drawn on, borders and non-text indicators need **3:1** (WCAG 2.1 SC 1.4.3 and 1.4.11).
5. Each failing pair is a problem naming the foreground role, the background role, the measured ratio and the minimum (`ink on surface is 2.10:1, need 4.5:1`), attributed to the layer that set the foreground role.

**The contrast floor (ruling: the Theme Service enforces it).** When at least one pair fails:

- `theme.allow_low_contrast` is false (the default): the problems are `error`, no candidate is built, the previous palette keeps serving (the built-in palette at first load). The user sees the toast described under Reload and gets the full list from `husk theme check` or `husk config reload`.
- `theme.allow_low_contrast` is true and its `Source` is `user`, `profile` or `instance`: the problems are `warning`, the candidate is built and published.
- `theme.allow_low_contrast` appears in the `distribution` layer, whatever its value and whether or not a stronger layer overrides it: one `error` problem `distribution cannot opt out of contrast` and the opt-out is treated as false. A distribution ships a palette it has checked; the unmerged `config` layers are consulted so a user override cannot hide the distribution's attempt.

There is no partial application: a palette with one failing override does not publish the passing ones. The user fixes the file; `husk theme check` reproduces the exact list.

`theme.changed`. When a reload publishes a Theme candidate whose palette differs from the previously serving one, the Pods host pushes `theme.changed` with the new palette to every live plugin ([Pods](pods.md) owns the push; it learns of the change from `services.reloaded` by comparing the Theme generation and the palette). A reload that republishes an identical palette (generation increases, values equal) pushes nothing. Every plugin also receives `theme.changed` as its first stdin message at launch.

Methods:

| Method | Params | Result | Errors |
| :--- | :--- | :--- | :--- |
| `theme.get` | `{}` | `{"palette":Palette}`, the serving palette; values are lowercase `#rrggbb` | none beyond the Server's |
| `theme.check` | `{}` | `{"ok":true,"problems":[...]}`, a dry build from the files on disk; `ok` is whether a reload now would publish a palette | `denied`; `timeout` `input_deadline`; `unavailable` |

CLI: `husk theme show` (the palette, from `theme.get`), `husk theme roles` (the role names), `husk theme check`.

### Keybinding

The policy (which layers exist and how they rank, what a keymap file contains, how a key is spelled, how a sequence resolves, what `husk keys` prints, the per-file and per-plugin limits) is fixed in [Keybindings](keybindings.md). This section fixes only how the Service is fed and what it exposes.

Inputs, per rebuild:

| Layer | Source |
| :--- | :--- |
| `husk` | the keymap document embedded in the binary beside the Service |
| `focus-local` | `Store.Declared("keymap")`: one declared document per plugin (the manifest's `keybindings`, converted by Pods) |
| `distribution`, `user`, `profile` | `Store.Layers("keymap", profile)`: the unmerged layer documents, each tagged with its layer and file |

Two different documents share the name `keymap`: `Store.Declared("keymap")` is a plugin's declared focus-local document, written by Pods' manifest conversion and never persisted; `Store.Layers("keymap", profile)` is the layered user and distribution document, read from files. Services read the first through `Store.Declared` only, never through a Pod type.

The Service exposes the policy view from [Keybindings](keybindings.md) (`Resolve`, `Hints`, `KeysFor`, `Listing`) plus `Reloadable`. Rules the Service adds:

1. `Resolve` is pure and lock-free: it reads the snapshot pointer, no State, no clock. Input owns the typed prefix and the sequence timeout and passes the prefix in. Which candidate wins is [Keybindings](keybindings.md)'s rule and is not restated here: scope first (a binding scoped to the focused surface beats a global one), then layer (`profile` > `user` > `distribution` > `focus-local` > `husk` within one scope), then the `plugin` condition, then file order. Wherever this spec says "weakest" or "strongest", it means that ranking.
2. A rebuild parses every layer, applies the loader rules of [Keybindings](keybindings.md), and reports every dropped or shadowed entry as a `Problem`; nothing disappears silently. Loader problems carry the file and line (config-relative) for config layers and `plugin:<id>` for focus-local entries. Conversion problems Pods attached to a declared document (`global_not_allowed`, `foreign_action`, `foreign_surface`, `too_many`) are imported as problems of layer `plugin:<id>`; the Service is the only key parser and adds `unparsable_key` for a focus-local spelling it cannot parse.
3. A layer document that fails to load on a reload (parse, version, size) keeps that layer's **last good** document for the same layer and, for `profile`, the same profile name; a different profile never inherits another profile's document. At first load the layer is dropped instead ([Keybindings](keybindings.md), failure modes).
4. The resolved keymap is capped at **4096 effective bindings** (the index the Service keeps in memory and `husk keys` prints). Past the cap the weakest-ranked entries are shadowed and reported `too_many`, so a flood of low-layer entries cannot displace a `profile` or `user` binding.
5. A rebuild that leaves no effective binding for `husk.quit` or `husk.palette.open` still publishes and reports `no_exit_binding` ([Keybindings](keybindings.md) decision 19).
6. Lookup never fails: an unknown action id resolves, and the Dispatcher reports `not_found` at dispatch, where the user can see it.
7. `keys.list` for a `plugin` caller returns only the entries whose declaring plugin is the caller or whose action the caller owns in the catalog, and only the caller's own problems; `cli` and `host` get the full listing. The Server joins the listing with State liveness and the action catalog to produce the `state` column ([Keybindings](keybindings.md)).

### Secrets

Secrets decides which plugin may read which secret. Values live in Store's encrypted namespace `husk-secrets` ([Store](store.md), crypto); Secrets holds declarations (from the declared `plugins` documents) and grants (in State), never values.

A plugin declares what it wants in its manifest:

```json
{"secrets": [{"name": "git.token", "purpose": "Push and pull over HTTPS", "required": true}]}
```

`name` matches `[a-z0-9_.-]{1,64}`. `purpose` is the sentence the user reads in the dialog. `required` is informational in v1 (shown in the dialog and in `husk secrets list`); it changes no behaviour. A declaration with an invalid name or a name declared twice by the same plugin is an `error` problem of layer `plugin:<id>` and that declaration is dropped; the plugin's other declarations stand.

Names are global to the user (`git.token` is one value whichever plugin reads it), grants are per plugin. Two plugins that both declare `git.token` and are both granted share the value; a per-plugin namespace would block that sharing, and the per-pair grant keeps the user in control. This is the opposite of a per-extension secret store with no cross-extension policy (the VS Code model, where any extension can in practice read another's token); here the host mediates every read.

#### Grants

A **grant** is `(plugin id, name, digest, at)` where `digest` is `sha256:` over the JSON array `[name, purpose]` of the declaration. A grant is valid while the plugin's current declaration has the same digest; a changed `purpose` voids it and the next read asks again.

Grants are the `husk.grants` slice, registered by Secrets at construction exactly as [State](state.md) lists it: owner `husk`, user scope, persistent, `manual` retention, value `{"grants":[{"plugin":"git","name":"git.token","digest":"sha256:9d2e...","at":"2026-10-22T05:12:00Z"}]}`. Grants are shared by every instance of the user and survive a plugin's removal (reinstalling does not re-prompt); `husk secrets revoke` removes one. Every slice is readable by every reader ([State](state.md) invariant 14); the slice holds no value, so that is fine.

Only a user grants. A grant is written in two ways and no other:

- `secrets.grant` from a `cli` caller.
- The user accepting the confirm dialog that a plugin's read opened.

Both writes go through the loop post function; at execution the write re-checks that the current declaration's digest is the one the user saw and writes nothing otherwise.

#### Reading

`secrets.get` from a plugin:

1. The name is invalid or the plugin does not declare it: `not_found`, `data.reason` `not_declared`. The reply never confirms whether the secret exists for anyone else.
2. Declared, granted with the current digest: read the value from the encrypted namespace. Missing: `not_found`, `data.reason` `unset`. Namespace unreadable: `unavailable`, `data.reason` `storage`. Otherwise the reply carries the value.
3. Declared, not granted, and this `(plugin, name, digest)` was **declined earlier in this instance's life**: `denied`, `data.reason` `user_denied`, no dialog.
4. Declared, not granted, no earlier decline: the read **waits** while Services ask the host to show the confirm dialog (plugin id and display name, secret name, `purpose`, `required`). The user accepts: the grant is written and the read continues at step 2. The user declines, the dialog is dismissed, 60 s pass with no answer, or no dialog is wired (a headless host): `denied`, `data.reason` `user_denied`, and the decline is remembered for this instance's life. A later `secrets.grant` from the CLI clears the memory.

The read blocks instead of returning a "come back later" code because one request with one answer is simpler for a plugin than polling, and polling costs rate-limit budget; browser permission prompts resolve the same way. The Server's per-connection in-flight cap bounds how many such waits a plugin can hold open.

Dialog rules: at most one dialog is shown at a time per instance; further prompts queue in request order and each one's 60 s clock starts when it is shown. Concurrent reads of the same pending `(plugin, name, digest)` wait on the same dialog; no second dialog opens. A plugin's reads for different names open one dialog each, in order. If the requesting connection closes while its read waits, the dialog closes and the read is dropped; nothing is remembered.

`secrets.get` from a `cli` caller returns the value when it exists (`not_found` `unset` otherwise) and is **audited** exactly like `secrets.reveal`: there is one audited path for a value leaving the store to a human.

The value is UTF-8 text, at most 64 KiB. `secrets.set` rejects a value that is not valid UTF-8 (`invalid_params`, `data.reason` `not_utf8`) or over the cap (`invalid_params`, `data.reason` `secret_limit`); a binary secret is the plugin's problem to encode.

#### Where a secret may and may not appear

| Place | Secret value | Secret name | `purpose` | Plugin id, outcome |
| :--- | :--- | :--- | :--- | :--- |
| The reply to the one `secrets.get` or `secrets.reveal` that was authorized | yes | yes | no | no |
| `secrets.set` params (CLI reads the value from stdin, never argv) | yes, inbound only | yes | | |
| Confirm dialog | never | yes | yes | yes |
| `husk.grants` slice, `husk state` | never | yes | no (digest only) | yes |
| `secrets.read` event | never | yes, if well-formed | never | yes |
| Host log (audit line) | never | yes | never | yes |
| `Problem` messages, error `message` fields | never | yes | never | yes |
| `services.reloaded` reports, `husk secrets list` | never | yes | never | yes |
| Toasts | never | never | never | never |
| Server diagnostics and logs | never: the Server logs codes and counts, never params ([Pods](pods.md)) | never | | |
| Memory after the reply | out of scope: Go gives no secure erase; the process is the user's ([Store](store.md) scope) | | | |

Rules that make the table hold: a Services error is built from a code and a reason, never from a value; the `secrets.read` event omits a requested name that is not a well-formed secret name (plugin-controlled text is not echoed); the CLI never takes a value on argv (`husk rpc secrets.set` is the user putting their own secret in their own shell history, which the CLI does not prevent); `husk store` commands and Store diagnostics name files and namespaces, never entries.

The audit record for a value leaving the store to a human (`secrets.reveal`, or `secrets.get` from a `cli` caller) is the `secrets.read` event with outcome `revealed` plus one host log line `secrets: <name> revealed to cli`.

#### Events

Secrets publish `secrets.read` for every `secrets.get` and `secrets.reveal`: `{"name":"git.token","plugin_id":"git","outcome":"allowed"}`, with `plugin_id` empty for a `cli` caller and `outcome` one of `allowed`, `revealed`, `not_declared`, `unset`, `user_denied`, `unavailable`. It is a core event ([Message kinds](message-kinds.md)) and is delivered to `cli` watchers only, never to a plugin subscriber: one plugin has no business learning which secrets another reads.

#### Methods

| Method | Params | Result | Errors |
| :--- | :--- | :--- | :--- |
| `secrets.get` | `{"name":"git.token"}` | `{"name":"git.token","value":"..."}` | `not_found` (`not_declared`, `unset`), `denied` (`user_denied`, `user_only` for `host`), `unavailable` (`storage`), `busy` (`rate_limited`) |
| `secrets.names` | `{}` | `{"storage":"ok","secrets":[{"name":"git.token","declared":["git"],"granted":["git"],"set":true,"required":true}]}` sorted by name; `storage` is `ok` or `unavailable`; a `cli` caller also sees stored names nobody declares; a plugin sees only names it declares, with `declared` and `granted` reduced to itself | none beyond the Server's |
| `secrets.set` | `{"name":"git.token","value":"..."}` | `{}` | `denied`, `invalid_params` (`invalid_secret_name`, `not_utf8`, `secret_limit`), `unavailable` (`storage`) |
| `secrets.grant` | `{"plugin":"git","name":"git.token"}` | `{}` once the write is queued | `denied`, `not_found` (`not_declared`: the plugin does not currently declare the name) |
| `secrets.revoke` | `{"plugin":"git","name":"git.token"}` | `{}`; revoking a grant that does not exist succeeds | `denied` |
| `secrets.reveal` | `{"name":"git.token"}` | `{"name":"git.token","value":"..."}` | `denied`, `not_found` (`unset`), `unavailable` (`storage`) |

CLI: `husk secrets list`, `husk secrets set <name>` (value from stdin, trailing newline stripped), `husk secrets grant <plugin> <name>`, `husk secrets revoke <plugin> <name>`, `husk secrets reveal <name>`.

### Limits

| Limit | Value | Reason |
| :--- | :--- | :--- |
| Reload wall time, gathering included | 2 s | Reload runs while the UI is live; slower is a source problem; keep the old snapshots |
| Effective bindings in the resolved keymap | 4096 | Bounds the lookup index and `husk keys` output; per-file and per-plugin caps are in [Keybindings](keybindings.md) |
| Secret value | 64 KiB, valid UTF-8 | Tokens and credentials; blocks using Secrets as a blob store; matches [Store](store.md) |
| Secret name | 1 to 64 characters of `[a-z0-9_.-]` | Same alphabet as settings keys; safe on a command line |
| Grant dialog | 60 s from when it is shown; one shown at a time | An unattended prompt must not hold a plugin forever; counts as declined |
| Plugin `settings.get` and `secrets.get` | 20 requests per second per plugin, one shared budget, fixed one-second window | A plugin polling in a loop cannot starve the loop; enforced by the Server before the Service is called; refused with `busy`, `data.reason` `rate_limited`; malformed requests count |
| Configuration document size, merge depth | Store's ([Store](store.md) limits) | Not re-stated here |

### Invariants

1. A Service holds no file handle and imports no file-system package. Persistence is Store's alone.
2. Services import Store and State interfaces; Store, State, the Stem and Pods never import Services' implementations; Pods never import Services at all (they reach the coordinator through an injected interface).
3. Every snapshot is immutable after publication. Readers never lock.
4. A failed rebuild leaves the previous snapshot serving; a Service always has a snapshot.
5. A rebuild is all-or-nothing per Service; an `error` problem means no candidate.
6. Only the coordinator publishes candidates, and only within its deadline. Dry runs publish nothing.
7. The serving palette passes the contrast table, unless `theme.allow_low_contrast` is set by a non-distribution layer.
8. Settings and secret visibility is by exact declaration ownership plus the explicit `public` flag. A key prefix is never authorization; a plugin never reads another plugin's keys or a non-public host key.
9. A secret value appears only in the reply to the one authorized `secrets.get` or `secrets.reveal`, and inbound in `secrets.set`. Never in State, settings, events, logs, problems, error messages, toasts or the dialog.
10. Only a `cli` caller or the accepted confirm dialog writes a grant; only a `cli` caller sets, revokes or reveals. A plugin can do none of these and can never write a setting or a configuration file.
11. Reloads are serialized and coalesced as the Reload section says; a flight never runs under a requester's context.
12. The only State writes Services make are `husk.profile` (Profile) and `husk.grants` (Secrets), both through callbacks posted to the loop. Rebuilds and reads write nothing.
13. Services publish exactly two events: `services.reloaded` (coordinator) and `secrets.read` (Secrets).
14. Services define no second copy of a type another layer owns: the caller identity, `Palette`, the settings `Entry` and the declared-document types are shared, not converted.

### Failure modes

| Condition | Detected by | What happens | What the user or plugin sees |
| :--- | :--- | :--- | :--- |
| A known settings key has an invalid value | Settings rebuild | No Settings candidate; old snapshot serves; Theme is skipped and keeps its palette | `config.reload` prints the problem with layer and key; a toast on a reload the user did not start |
| Unknown settings key | Settings rebuild | Warning; raw value kept for `settings.list`; everything else applies | Listed by `husk settings validate` and in the reload report; no toast |
| Palette override fails contrast, opt-out off | Theme rebuild | No Theme candidate; old palette serves | Every failing pair with ratio and minimum; `husk theme check` reproduces it |
| Palette override fails contrast, opt-out on from `user`/`profile`/`instance` | Theme rebuild | Warnings; palette published | Warnings in the report; no toast |
| `theme.allow_low_contrast` present in the distribution layer | Theme rebuild | Error; opt-out ignored | `distribution cannot opt out of contrast` |
| Override is not `#RRGGBB` | Theme rebuild | No Theme candidate | `role <role>: want #RRGGBB` |
| A keymap layer fails to load on reload | Keybinding rebuild | That layer's last good document is used; rebuild publishes | Problem naming file and line; `husk keys --problems` |
| Keymap entry invalid, duplicate, unreachable, over a cap | Keybinding rebuild | Entry dropped or shadowed; rebuild publishes | Problem per entry ([Keybindings](keybindings.md) codes) |
| Binding names an unknown action | Dispatcher, at dispatch | The key resolves; dispatch fails `not_found` | Toast naming the action; `husk keys` state `unknown_action` |
| `--profile` or `profile.default` names no profile | Profile | No profile; reload proceeds | Warning problem; one startup toast |
| Active profile's directory deleted | Profile rebuild | Slice reset to `""`; one follow-up `profile` reload | Warning toast |
| Store cannot resolve `config` (parse error, over limit) | Coordinator, step 2 | No Service rebuilds; every snapshot stays | `config.reload` returns an error naming the file; toast when the user did not start the reload |
| First load fails | Coordinator at start | Built-in snapshots serve | Startup toast with the error count |
| Reload exceeds 2 s | Coordinator | Published candidates stay; remaining Services report `timed out`; late candidates discarded | Report rows with `applied:false`, `timed out` |
| Plugin reads an undeclared secret | Secrets | `not_found`, `not_declared` | The plugin gets the error; nothing shown to the user |
| Plugin reads a declared, ungranted secret | Secrets | Read waits; dialog opens | Dialog with plugin, name, purpose; the plugin's reply is the value or `denied` |
| Dialog declined, dismissed, 60 s elapsed, or no dialog wired | Secrets | `denied`, `user_denied`; remembered for this instance | The plugin gets the error; later reads get it without a dialog; `husk secrets grant` clears it |
| Declaration `purpose` changed since the grant | Secrets digest check | Grant void | Next read prompts again |
| Encrypted namespace unreadable | Store crypto | `unavailable`, `storage`; other Services unaffected | Plugin gets the error; `husk secrets list` shows `storage: unavailable`; Store's diagnostic suggests `husk store reset-secrets` |
| Plugin exceeds 20 `settings.get`+`secrets.get` per second | Server | `busy`, `rate_limited` | The plugin gets the error |
| Plugin calls a user-only method | Server, Services | `denied`, `user_only` | The plugin gets the error |
| Invalid caller reaches a Service | Services | `denied`, `invalid_caller` | A composition bug; logged |

### Code layout

Services live under one import root as `services/{profile,settings,theme,keybinding,secrets}` plus the coordinator in `services`. Shared value types the children and the coordinator both need (`Generation`, `Problem`, `Report`, `ReloadInput`, `Reloadable`, `Candidate`, the settings `Entry` and the declared-document types) live in one leaf package the children import, so no child imports its parent. The embedded `husk` keymap sits beside the Keybinding Service. `pkg/theme` keeps `Palette`, `Default()`, `Overlay`, `SlotBackground`, `ContrastPairs` and `CheckContrast`, so the runtime check and the built-in palette's test share one rule.

## Level 2: Status against the code

The five Services and the coordinator exist. Items below are where the code on `main` and this spec differ; each is small and self-contained.

- Caller identity: Services define their own `Caller` struct and the host converts to it. The spec requires the one shared caller type. The `host` kind currently counts as a user for secrets; the spec denies it secret operations.
- Settings exposure: every `husk.*` key is readable by every plugin. The spec adds `Entry.Public` and exposes only entries marked public; none is marked today.
- Unknown settings keys outside a declared plugin subtree are errors that block the whole Settings rebuild. The spec makes every unknown key a warning with the raw value retained.
- `settings.get` from a plugin for a foreign key answers `denied`/`undeclared_setting` at the Service and is narrowed to `not_found` by the host wrapper; the spec names `not_found`/`unknown_setting` end to end. `settings.list` returns a bare key-to-value object without sources; `settings.schema` and `profile.list` return bare arrays; `settings.validate` and `theme.check` return `applied` for a dry run. The spec fixes the object shapes in the method tables.
- `secrets.get` with no grant returns `denied`/`needs_grant` immediately and expects the plugin to retry; a decline is forgotten after one `user_denied` reply, so a plugin can reopen the dialog at will; several dialogs can be open at once. The spec blocks the read, remembers the decline, and shows one dialog at a time.
- `secrets.get` from a `cli` caller returns the value without an audit record; the audit sink and the confirm dialog are not wired by the host, so every dialog grant is refused. `secrets.set` accepts arbitrary bytes and encodes them as a JSON string.
- `profile.use` replies as soon as the slice write is queued; the spec replies with the reports of the reload it caused. `--profile` is not wired into Profile.
- `theme.changed` is sent only at plugin launch; nothing pushes it after a Theme change.
- `secrets.read` is published on the bus with no delivery restriction; the spec delivers it to `cli` watchers only.
- `profile.default` is honoured from the user layer only in the code, as specified, but a value in another layer is silently ignored instead of reported.
- The `husk settings|theme|profile|secrets|config` subcommands do not exist yet; the Server handlers do.
- The host built-in keys `husk.layout.rail` and `husk.input.hold_alt` are registered but the frontend still reads the equivalent start flags; `husk.plugins.paths` and `husk.plugins.dirs` are registered and read by nothing.
- The declared focus-local document is still named `focus-keymap` in the code (`store.DeclaredFocusKeymap`); the spec name is `keymap`.
- The Keybinding Service ranks layer before scope; [Keybindings](keybindings.md) ranks scope first.

## Decisions made here

1. Five Services with one shared rebuild shape: immutable generation-numbered snapshots, all-or-nothing per Service, one coordinator that serializes and coalesces reloads and publishes one `services.reloaded`.
2. Reload order Profile, Settings, Theme, Keybinding, Secrets; Theme derives from the Settings candidate of the same reload and is skipped when Settings fails; Keybinding and Secrets are independent of Settings.
3. `Profile.Use` writes State only and the coordinator reloads on the slice change; the reply carries the reload's reports.
4. Settings namespaces have exactly one owner each; visibility is by declaration ownership plus an explicit `public` flag on host and distribution entries; no key is public today; no method writes a setting.
5. Unknown settings keys are warnings and retained raw for the user; invalid values of known keys are errors and keep the whole previous snapshot.
6. One built-in palette, `#RRGGBB` overrides per role, the contrast floor enforced by Theme at 4.5:1 text and 3:1 non-text, `theme.allow_low_contrast` demotes to warnings from non-distribution layers only, no partial application.
7. The contrast table is production code shared by the runtime check and the built-in palette's test.
8. `theme.changed` is pushed when the published palette differs, not on every reload.
9. Keymap policy, ranking included (scope first, then layer, then the `plugin` condition, then file order), lives in [Keybindings](keybindings.md); this spec owns only the Service mechanics, the last-good-layer rule, the 4096 cap and the per-caller `keys.list` view. The declared focus-local document and the layered keymap document share the name `keymap` and are told apart by the Store method that returns them.
10. Secret names are global to the user, grants are per `(plugin, name)` and bound to the declaration digest; grants live in the persistent user-scope `husk.grants` slice; values live only in Store's encrypted namespace.
11. A read of an ungranted secret blocks on one confirm dialog (60 s, one at a time) instead of returning a retry code; a decline is remembered for the instance's life.
12. Only a `cli` caller or the accepted dialog writes a grant; the `host` kind never handles a secret value; every value handed to a human is audited through `secrets.read` and a host log line.
13. Secret values are UTF-8 text up to 64 KiB.
14. `secrets.read` reaches `cli` watchers only.
15. Plugins reach settings, secrets, the palette and their own keys through Server methods under a 20 requests per second per plugin budget; nothing arrives as environment values.
16. **No host settings key is readable by plugins today.** A schema entry marked `public: true` is the mechanism when a need appears; nothing else makes a host key visible. This closes the finding that every `husk.*` key was handed to every plugin.
17. **An unknown settings key is a warning**, kept raw for the user and never shown to plugins; an invalid value of a known key is an error, so nothing half-applies. Errors for unknown keys would let a stale key from a removed plugin or a typo freeze every setting.
18. **An ungranted secret read blocks on the grant dialog** (60 s, one dialog at a time); a decline is remembered for the life of the instance. A retry code would need a polling interval nobody specified and would spend rate budget.
19. **Grants survive `husk plugin remove`.** They are the user's consent record, not plugin state; `husk secrets revoke` is the explicit way to drop one.
20. **The secret audit is the `secrets.read` event plus a host log line; there is no audit file.** A single-user local tool has no consumer for one.
21. **The `host` caller never handles secret values.** The confirm dialog is the host's only secret role; the same least-privilege line that keeps the Stem out of `state.clear`.
22. **The declared focus-local document is named `keymap`**, the same name as the layered keymap document. The two are told apart by the Store method (`Declared` versus `Layers`), and every spec that mentions one says which it means.
23. **Contrast floor, closed.** The Theme Service enforces it; a theme that misses it is not applied at all (no partial application); the user gets one toast; `husk theme check` reports the failing pairs. The opt-out from a non-distribution layer stands as written under Theme.

## Open questions

1. **Live reload on file change.** Not in this spec: reload runs on `config.reload`, a profile change, and a plugin added or removed. File watching, if wanted, belongs to Store sources calling the coordinator, in the Store spec.
2. **Persisting the chosen profile (`husk profile use --save`).** Deferred until Store exposes a write API for the user layer; Services do not write files.
