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

Four pieces of process memory are not rebuildable from the inputs and are not persisted or exposed by any method; each is named where its rule is: the Keybinding Service's last-good layer documents (Keybinding rule 3), the Settings values in force for `apply: restart` keys (Validation), the Secrets decline memory (Reading, step 3), and the set of unregistered keys already warned about (Snapshot and getters). All four start empty at process start and die with the process.

What Services are not. They are not a settings editor: no method writes a configuration layer in v1; configuration changes by editing a file and reloading. They are not a plugin host, a message router or a renderer: they know nothing of sockets, Pods, the event loop or the terminal.

### Spec ownership

Three specs touch the same ground. The rule that owns a topic is written once, in the spec named here; the other specs point to it.

| Topic | Owner |
| :--- | :--- |
| Keymap layers and their rank, the keymap file shape, key spelling, tombstones, sequences, resolution, special contexts, the built-in `husk` keymap, what `keys.list` and `husk keys` report, per-file and per-plugin binding limits | [Keybindings](keybindings.md) |
| The Keybinding Service mechanics: inputs, rebuild, generation, where loader problems go, the per-caller view of `keys.list`, the resolved-keymap size cap | this spec |
| Merging of `distribution < user < profile < instance`, file locations, the declared-document schema, the encrypted namespace | [Store](store.md) |
| The `husk.profile` and `husk.grants` slices as slices (scope, retention) | [State](state.md); their contents, this spec |
| Method names, the error-code table, the request envelope, the `Palette`, `Caller`, `Problem` and `Source` wire types | [Protocol](protocol.md); params and results of the `settings.*`, `secrets.*`, `theme.*`, `profile.*` and `config.*` methods, the `Report` type and the problem-code tables, this spec |
| Manifest conversion into declared documents, the launch gate, pushing `theme.changed` down a plugin's stdin | [Pods](pods.md) |
| CLI subcommand syntax, output format, exit codes | [Instances and the CLI](instances.md); which method each `husk settings|theme|profile|secrets|config` subcommand calls, this spec |

Dependencies. Services import [Store](store.md) and [State](state.md) interfaces and the `Palette`, `Caller`, `Problem` and `Source` types from [Protocol](protocol.md). They import nothing from the Stem or Pods: the confirm dialog, the loop post function, the event publisher, the warning sink (one `func(msg string)` the host points at its log) and the distribution schema arrive as values at construction; the caller identity arrives per call. The Stem (Input, renderer, palette, footer) and the Server handlers depend on Services. The Pods host depends only on an interface it defines itself (`Reloader`, [Pods](pods.md)), which the coordinator satisfies; it imports no Services package. [Calls](calls.md) never touches Services.

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
| **problem** | One diagnostic (`error` or `warning`) with a stable code, the layer, the key, a human message and, when known, the source location. A message never contains a secret value. |
| **layer** | One configuration source: `distribution`, `user`, `profile`, `instance` ([Store](store.md)); for keymaps also `husk` and `focus-local` ([Keybindings](keybindings.md)); for declarations `plugin:<id>`. |
| **capture** | The moment a reload starts gathering its inputs (the `Store.Invalidate()` call that opens Reload step 2), before any input is read. Requests arriving after it cannot join that reload. |
| **declared document** | The per-plugin `plugins` and focus-local keymap documents Pods' manifest conversion writes into Store ([Store](store.md), "Declared-document schema"). |
| **caller** | The verified identity of whoever invokes a Service method. See "Callers". |
| **user** | A `cli` caller. Only a user may change grants, secrets and the active profile, or read every setting. |
| **grant** | The user's recorded consent for one `(plugin id, secret name)` pair, bound to the declaration's digest. |

### Callers

The Server authenticates every socket request and produces the caller ([Pods](pods.md), [Protocol](protocol.md) rule 6). Services never read identity from params and never infer it from a key prefix. There is **one** caller-identity type, `protocol.Caller` in the `pkg/protocol` leaf package ([Protocol](protocol.md), "Caller"), used unchanged by the Server, the host adapters, the Dispatcher, the hook runner and Services: no per-layer copy, no converter. It lives in Protocol because its kind strings are wire-visible (`params.caller.kind` in `action.invoke`, [Message kinds](message-kinds.md)) and every party already imports that package. Its fields are `Kind` (one of exactly four: `cli`, `plugin`, `input`, `host`), `PluginID` (set only for `plugin`) and `ConnID` (the Server's per-connection number for logs and watch cleanup; zero for in-process callers; never serialized). Services treat `input` exactly as `host` in every rule below: the difference between the two matters only to the Dispatcher's focus handling.

| Kind | Produced when | `plugin_id` |
| :--- | :--- | :--- |
| `cli` | A socket request carries no `token` (the user's own shell). | empty |
| `plugin` | A socket request carries a valid per-launch token. | the exact plugin id, never case-folded |
| `host`, `input` | In-process code of the Stem or frontend calls a Service directly (the renderer, Input, the palette). | empty |

A zero-value caller, an unknown kind, a `plugin` kind with an empty id, or any other kind with a non-empty id is **invalid**: every method answers `denied` with `data.reason` `invalid_caller`. The table below is the complete authorization matrix (`host` stands for `host` and `input`). "own" means the caller's exact plugin id, by declaration ownership, never by prefix.

| Operation | `cli` | `host` | `plugin` |
| :--- | :--- | :--- | :--- |
| `settings.get` | any key | any key | own declared keys and host keys marked `public` |
| `settings.list`, `settings.schema`, `settings.validate` | yes | yes | `denied` |
| `theme.get` | yes | yes | yes |
| `theme.check` | yes | yes | `denied` |
| `keys.list` | full listing | full listing | own entries, the effective `husk.*` and rail-item entries, and own problems only ([Keybindings](keybindings.md) owns the shape) |
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

// The diagnostic type is protocol.Problem, with protocol.Source for its
// location ([Protocol](protocol.md), "Problem"): one type for every Service,
// the coordinator and `keys.list`, defined there because it crosses the wire
// and the host contracts must not import Services. Services define no copy.
// Its `Code` values come from the tables in this spec and the loader table in
// [Keybindings](keybindings.md).

type Report struct {
    Service  string             `json:"service"`  // "profile" | "settings" | "theme" | "keybinding" | "secrets"
    Gen      Generation         `json:"gen"`      // generation now serving
    Applied  bool               `json:"applied"`  // false: the previous snapshot is still serving
    Changed  bool               `json:"changed"`  // true when Applied and the new snapshot's value differs from the previous one
    Problems []protocol.Problem `json:"problems,omitempty"`
}

// Every Service implements Reloadable.
type Reloadable interface {
    // Rebuild derives a candidate from in. It publishes nothing. It returns every
    // problem found, not the first. err != nil means no candidate: cand is nil,
    // the Report carries the problems and Applied is false.
    Rebuild(ctx context.Context, in ReloadInput) (cand Candidate, rep Report, err error)
    Generation() Generation
}

// Candidate publishes at most once. Apply returns the final Report. A
// candidate carries its flight's deadline: Apply after it is a no-op that
// returns Applied false with one `candidate_superseded` problem, so a late
// candidate can never change a generation.
type Candidate interface{ Apply() Report }
```

Rules, each testable:

1. A snapshot is immutable after publication. Readers never lock; a reader keeps using the snapshot pointer it loaded even while a reload publishes a new one.
2. A rebuild is all-or-nothing per Service. Any `error` problem means no candidate; the previous snapshot keeps serving; `Applied` is false; the report lists every problem, errors and warnings alike.
3. A rebuild with only `warning` problems produces a candidate. When published, `Applied` is true and the warnings are in the report. One exception carries `applied:false` with only warnings: Theme skipped because Settings produced no candidate (`settings_unchanged`, Reload step 3).
4. A Service always has a snapshot. Before its first load it serves its built-in snapshot (generation 0): no profile, the schema defaults, the built-in palette, the embedded `husk` keymap, no declarations. If the first load fails, the built-in snapshot keeps serving and the failure is reported.
5. Problem messages name layers, keys, roles, ratios and plugin ids. They never contain a secret value or a declaration's `purpose`.
6. `Rebuild` may run on any goroutine and writes no State. Only the posted callbacks named under Profile and Secrets write State.
7. `Code` is the contract; `Message` is prose. Every code a Service can emit is in its table in this spec (Keybinding: the loader table in [Keybindings](keybindings.md)); a new code is a spec change. `Changed` is false whenever `Applied` is false.

Codes raised by the coordinator or by a candidate's `Apply`, in any report; each leaves the previous snapshot serving (`applied:false`):

| Code | Raised by | Condition | Severity | Effect |
| :--- | :--- | :--- | :--- | :--- |
| `store_error` | coordinator, step 2 | Store could not resolve or load a layer (parse error, over a limit); the message names the config-relative file | error | no Service rebuilds; every report carries it; `config.reload` answers `unavailable` `store_error` |
| `timed_out` | coordinator, step 4 | the 2 s deadline passed before this Service published | error | this and every later Service keeps its snapshot; candidates already published stay |
| `cancelled` | coordinator | the coordinator closed during the flight | error | the Services not yet published keep their snapshots; `config.reload` answers `cancelled` `shutdown` |
| `settings_unchanged` | coordinator, step 3 | Settings produced no candidate, so Theme was skipped (message `settings unchanged; palette unchanged`) | warning | Theme's report only: no rebuild, `applied:false`, the palette stays; the only `applied:false` report that carries no `error` |
| `candidate_superseded` | the candidate's `Apply` | `Apply` found the candidate stale and swapped nothing: its flight's deadline had passed, the coordinator had closed, or the Service's generation moved since the rebuild | error | that Service's report says `applied:false`; the coordinator never calls `Apply` on a late candidate (step 4), so in a healthy flight this code never appears |

A Service that reads the declared `plugins` documents (Settings, Keybinding, Secrets) raises `invalid_plugin_catalog` (`warning`, layer `plugin:<id>`, `Source.Plugin` set) when a plugin's document does not decode or its `id` is not its Store source; that plugin's declarations are ignored by the reporting Service, as if it had declared nothing (its configuration keys are then `unknown_key`), and every other plugin's stand. Like a bad plugin schema (Settings, "Validation"), it is never an `error`: a plugin cannot block the user's reload by shipping a bad manifest.

`ReloadInput` is one consistent set of inputs, gathered once per reload and handed to every Service: the merged `config` document with per-key origins (`Store.Resolve("config", profile)`), the unmerged `config` layers (`Store.Layers("config", profile)`, for Theme's distribution check and Profile's `profile.default` rule), the unmerged `keymap` layers (`Store.Layers("keymap", profile)`), the declared documents (`Store.Declared`), the profile the coordinator selected, and `Settings`: the Settings `Snapshot` this reload published, filled in by the coordinator after Settings' candidate is applied and nil when it was not. Only Theme reads `Settings`.

### Reload

The **coordinator** is a function of the Services package, not a sixth Service. It owns every reload. Nothing else publishes a candidate.

Triggers, each carried as the reload's `reason`:

| `reason` | Trigger | Who asks |
| :--- | :--- | :--- |
| `startup` | the host finished construction and hydrated State | the composition root, once per process, before the first plugin launches |
| `cli` | `config.reload` | a `cli` or `host` caller through the Server |
| `profile` | the `husk.profile` slice changed | the coordinator's own State flush listener |
| `plugin` | a plugin's declared documents were added or removed | the Pods host, after `Store.Declare` (launch step 3, at startup and for each `plugin.add` batch) or `Store.Undeclare` (`plugin.remove` step 7, [Pods](pods.md)); the launch gate, the add and the removal each wait for this flight, and the add and the removal return its five reports in their replies, the same `Report` objects `config.reload` returns; a report with `applied: false` fails neither |

One reload runs these steps in order:

1. Start the flight. Requests that arrive from now until capture join it (see coalescing). The coordinator hands the flight to its worker at the end of the loop tick that created it, so the requests that join a flight are exactly those posted in that tick; nothing is read yet.
2. **Capture**, then gather `ReloadInput` on a worker goroutine. Capture is the `Store.Invalidate()` call, before any input is read, so a request that arrives once gathering has begun can never be absorbed by a flight that already read the value it changed. Then select the profile (see Profile below; it reads the `--profile` flag, the `husk.profile` slice and the unmerged `user` config layer from `Store.Layers("config", "")`, never a merged document), then `Store.Resolve("config", profile)` exactly once, then the `keymap` layers and the declared documents. If Store reports a problem other than `type_clash` (a layer that failed to parse or exceeded its limits), the reload **fails as a whole**: no Service rebuilds, every report says `applied:false` with one `store_error` problem naming the config-relative file, and `config.reload` returns `unavailable` with `data.reason` `store_error`. A `type_clash` is passed to Settings, which imports it as a `type_clash` warning naming the key and both layers.
3. Rebuild in the fixed order Profile, Settings, Theme, Keybinding, Secrets, each on a worker goroutine, each publishing its candidate before the next starts. After Settings publishes, the coordinator sets `ReloadInput.Settings` to the published Settings `Snapshot`; if Settings produced no candidate it stays nil and Theme is skipped: no rebuild, `applied:false`, one `warning` problem `settings_unchanged` (message `settings unchanged; palette unchanged`). Keybinding and Secrets do not read `Settings`.
4. The whole reload, gathering included, has a **2 s deadline**. When it passes, candidates already published stay; the Service being rebuilt and every later one report `applied:false` with the problem `timed_out`; the coordinator never calls `Apply` on a late candidate, and `Apply` after the deadline is a no-op (`applied:false`) in any case, so a late candidate can never change a generation.
5. Publish one event, `services.reloaded`, with the reason and the five reports, through the publisher the host injected at construction (the same seam State uses; delivered as `event.deliver`, [Message kinds](message-kinds.md), to `cli` watchers only, like `secrets.read`).
6. Answer every waiter with the five reports.

The `config.reload` reply is `{"reports":[...]}`. It is an error only when the flight itself failed: `unavailable` `store_error` (step 2), `timeout` with `data.reason` `reload_deadline` (step 4 passed before step 6), `cancelled` with `data.reason` `shutdown` (the coordinator closed first). A Service that kept its old snapshot is a success reply with `applied:false` in that report. `profile.use` uses the same three error replies for the flight it waits on.

The `startup` flight is the first reload of the process. The composition root runs it after State hydration and before the first plugin launch; the Pods launch gate then requests its own `plugin` flight, so a plugin's declarations are never read by the startup flight. Its reports are published as `services.reloaded` like any other and shown as the startup toast when they carry errors; nothing waits on its reply except the root.

```json
{"method":"event.deliver","params":{"name":"services.reloaded","source":"husk","data":{"reason":"cli","reports":[
  {"service":"profile","gen":3,"applied":true,"changed":false},
  {"service":"settings","gen":3,"applied":true,"changed":true},
  {"service":"theme","gen":2,"applied":false,"changed":false,"problems":[{"severity":"error","code":"low_contrast","layer":"user","key":"theme.palette.ink","message":"ink on surface is 2.10:1, need 4.5:1","source":{"layer":"user","file":"config.json","line":12}}]},
  {"service":"keybinding","gen":3,"applied":true,"changed":false},
  {"service":"secrets","gen":3,"applied":true,"changed":false}]}}}
```

Coalescing. Reloads are serialized. A request that arrives while no flight runs starts one. A request that arrives while a flight runs and has **not yet captured** joins it. A request that arrives after capture joins the single **queued** follow-up flight (one, whatever the number of requesters); the follow-up starts when the running flight ends and reads fresh inputs. Because capture precedes every read, a request is always served by a flight whose gathering began after the request: a `profile` request is never answered by a flight that read the old slice value, and a `plugin` reload always observes the declarations that preceded it. This is why the launch gate in [Pods](pods.md) can wait on the reload it requested.

Waiting. A flight belongs to the coordinator, bounded by the 2 s deadline and the coordinator's `Close`. A requester waits with its own context; cancelling it detaches that requester only, never the flight or the queued follow-up. The host closes the coordinator at shutdown; pending waiters get `cancelled` `shutdown`, and the Services that had not published by then report `cancelled`.

What the user sees. `config.reload` prints the reports. A reload the user did not start from the CLI (`startup`, `profile` or `plugin`) with at least one `error` problem shows one toast: `<n> configuration problems; run husk config reload` with `<n>` the error count across reports. Warnings alone show no toast. The frontend derives the toast from the `services.reloaded` event; Services show nothing themselves.

Dry runs. `settings.validate` and `theme.check` take the coordinator's turn exactly like a flight (they wait for a running flight to end, and a flight requested during a dry run waits for it; they never coalesce with one), run step 2 with the same 2 s deadline (`timeout` with `data.reason` `input_deadline` when exceeded), run the one Service's `Rebuild`, and discard the candidate. `theme.check` first dry-builds Settings to obtain the snapshot Theme would see. They never publish, never change a generation and never emit `services.reloaded`. Serializing them is what lets them call `Store.Invalidate()` safely: a dry run always sees the files as they are on disk now, so `husk theme check` after editing a file reports that edit.

### Profile

Inputs: `Store.Profiles()` and `Store.ProfileSources()` (names and whether each comes from the `user` or `distribution` layer), the `--profile` start flag, the `husk.profile` slice, and the user-layer setting `profile.default`. Output: the active profile name; `""` means no profile layer is merged.

State slice, registered by Profile at construction, exactly as [State](state.md) lists it: `husk.profile`, owner `husk`, instance scope, persistent, `keep-latest`, value `{"active":"<name>"}`. Before any write the slice is **absent**: State's empty value with no `active` key (a fresh pid instance, a named instance that never chose, a slice just cleared). `{"active":""}` is an explicit **no profile** and is not absent. A named instance therefore reopens with its last profile; a pid-named instance starts fresh.

Selection runs in the coordinator's step 2, before the one `Store.Resolve`, so the merged document it produces is already the selected profile's. It reads only the unmerged `user` layer of `config` (`Store.Layers("config", "")`) for `profile.default`, never a merged document: the merge needs the profile, and the profile must not come from the merge. First match wins:

1. `--profile <name>` was given: that name, on the `startup` flight only. On every later flight the flag is already in the slice (next step).
2. The `husk.profile` slice is present (hydrated for a named instance, or written by `--profile`, `profile.use` or an earlier flight): its value, `""` included. `{"active":""}` is an explicit no profile and selection stops here; `profile.default` is not consulted.
3. The slice is absent and `profile.default` is set in the `user` layer: that name. It is read fresh on every flight, so a change is live for as long as no profile has been chosen (apply: live; never `restart_required`). The `distribution`, `profile` and `instance` layers cannot set it (a profile choosing a profile is circular; a distribution ships a default profile by shipping one profile directory, not by setting this key). A `profile.default` present in any other layer is a `warning` problem `profile_default_ignored` (message `profile.default is honoured from the user layer only`) and is ignored.
4. Otherwise `""`.

A name that `Store.Profiles()` does not list is not activated: Profile reports a `warning` problem `unknown_profile` (message `profile <name> does not exist; using no profile`) and the active profile is `""`.

The slice records choices, not defaults. Profile posts a write of `{"active":"<selected>"}` to the loop before step 5 in exactly two cases: the `startup` flight with `--profile`, and a later flight whose present slice names a profile whose directory is gone (the slice gets `""`). A selection made by `profile.default`, or no selection at all, writes nothing: the slice stays absent and the default keeps being consulted. `profile.use` writes its own choice (below), whether or not that choice changes the active profile. The coordinator's slice listener ignores a change whose value equals the profile the flight that is running or just ran selected, so this write never triggers a second flight; any other value (a `profile.use`, a hydration from another instance's file) does. A `--profile` name that does not exist is therefore not persisted: the slice gets `""`.

Methods:

| Method | Params | Result | Errors |
| :--- | :--- | :--- | :--- |
| `profile.list` | `{}` | `{"profiles":[{"name":"work","source":"user","active":true}]}` sorted by name; `source` is `user` or `distribution` | `denied` |
| `profile.use` | `{"name":"work"}`; `""` selects no profile | `{"active":"work","reports":[...]}`, the reports of the reload this change caused | `denied`; `not_found` with `data.reason` `unknown_profile` when no layer defines the name; `unavailable` `store_error`, `timeout` `reload_deadline`, `cancelled` `shutdown` from the flight |

`profile.use` validates the name against the current snapshot. It is a choice, so the slice records it even when it changes nothing: the write and the decision happen on the loop, in one posted callback, in this order. First the callback writes `{"active":"<name>"}` to `husk.profile`; a write of the value the slice already holds changes nothing and the listener never sees it. Then, if the coordinator is **settled on that name**, meaning no flight is running or queued, the most recent flight selected `<name>` and it is the active profile, the requester replies at once with `active` and the reports of the most recent completed flight: nothing reloads (if the write did change the slice, from absent to `{"active":"<name>"}`, the listener ignores it as equal to the selection, and the instance now keeps that profile when `profile.default` changes or is removed). Otherwise the same callback requests the `profile` flight that applies the write and registers the requester as its waiter, by the coalescing rule: a new flight when none is pending, the running flight itself when it has not captured yet, or the queued follow-up when it has (has begun gathering), so waiting never hangs. The slice listener, told of that registration, requests no second flight for the write. Correlation is that registration, not a comparison of names or generations, so two `profile.use` calls in one tick, whatever their names, wait on the same flight and each gets its reports, and a `profile.use` during a flight that is switching to another profile waits for the follow-up that applies its own. It is instance-only: it does not write `profile.default`. Writing a chosen profile back to the user layer (`--save`) is a later spec; Services do not write configuration files.

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

Schema problems, with their codes: a key that does not belong to its owner's namespace (`foreign_schema_key`), a duplicate key (`duplicate_schema_key`), an invalid default (`invalid_schema_default`), an unknown `type` or `apply`, an empty `enum`, or `public` on a plugin entry (`invalid_schema_entry`), a plugin entry whose local key equals one of **that same plugin's** declared secret names (`secret_as_setting`; the comparison is exact and owner-scoped, never a suffix match and never across owners, so a secret named `name` cannot invalidate `theme.name`). Who pays depends on who wrote the schema:

- The host's built-in schema and the distribution's `Schema []Entry` are compiled into the application. A problem in either is a programming error: Services construction returns an error naming the entry and the process does not start. It is never reported at reload, because these schemas cannot change at run time.
- A plugin's schema arrives in a declared document and can be wrong without the user having done anything. Its problems are `warning` severity against layer `plugin:<id>`, with `Source.Plugin` set; the plugin's entries are **not** registered (all of them, so a half-declared plugin never exists), the rest of the schema is unaffected, and that plugin's keys in the configuration are then reported as `unknown_key` warnings like any other unregistered key. The plugin's own `settings.get` answers `not_found` `unknown_setting` for them. A plugin cannot break the user's settings by shipping a bad manifest.

Built-in host entries (the complete list is `husk settings schema --owner husk`): `profile.default` (string, live; consulted only while `husk.profile` is absent, see Profile), `theme.name` (string; it becomes the served palette's `name`, shown in the header line of `husk theme show` and in the workbench's bottom bar, [Workbench chrome](chrome.md)), `theme.allow_low_contrast` (bool, default false), one `theme.palette.<role>` (string) per palette role with the built-in colour as default, `husk.plugins.paths` and `husk.plugins.dirs` (strings, restart), `husk.bundle` (string, default `""`, restart; honoured from the `user` and `instance` layers only, read before Store opens, [Bundles](bundles.md); present in any other layer it is a `warning` problem `bundle_setting_ignored` and ignored), `husk.plugins.converters` (strings, restart; Level 1, not landed: executable manifest converters, [Pods](pods.md) "Executable converters"), `husk.layout.rail` (enum `""|modal|full-canvas|stage`), `husk.input.hold_alt` (bool). None is `public`.

#### Validation

Applied to the whole merged document on every rebuild. Severity decides the outcome: any `error` means no candidate. This is the complete list of Settings codes.

| Code | Condition | Severity | Effect on the candidate |
| :--- | :--- | :--- | :--- |
| `invalid_value` | Known key, value of the wrong type, outside `Min`/`Max`, or not in `Enum` | error | none built; the message names the layer, the key and the expected type; `Source` names the layer file |
| `unknown_key` | No schema entry for the key, in any namespace | warning | the raw value is kept with its layer for `settings.list` and `settings.get` by `cli`/`host`; no plugin ever sees it; typed getters do not know it |
| `restart_required` | Known key with `apply: restart` whose value differs from the value in force | warning | the value in force stays in the snapshot; removing the key counts as a change back to the default; the message names both values |
| `type_clash` | Store merged an object over a non-object or the reverse ([Store](store.md)) | warning | the higher layer's value stands, as Store merged it; the message names the key and both layers |
| `foreign_schema_key`, `duplicate_schema_key`, `invalid_schema_default`, `invalid_schema_entry`, `secret_as_setting` | Plugin schema problems above | warning | that plugin's entries are not registered |
| `invalid_plugin_catalog` | A plugin's declared document does not decode or its `id` is not its Store source ("Snapshots, rebuilds and reports") | warning | that plugin's entries are not registered; its keys become `unknown_key` |

The value **in force** for a `restart` key is the value the first successful Settings load of the process produced; the first load therefore never reports `restart_required`. Until that load, nothing is in force and the default serves. `settings.get` and `Snapshot.Source` report the value in force and the layer that supplied it at that load; the pending value appears only in the problem message and in the file.

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

Typed getters never fail. A key absent from the document returns the schema default. A key absent from the schema is a programming error in the caller: the getter returns the zero value and the Service writes `settings: unregistered key <key>` once per key per process to the warning sink it was constructed with (the host logs it). This is not a `Problem` and appears in no report; the set of keys already warned about is process memory. `host` callers get the full snapshot (`Snapshot()`); a plugin gets a filtered snapshot (`SnapshotFor(caller)`) containing exactly its own declared entries plus `public` host and distribution entries, and nothing else.

#### Methods

| Method | Params | Result | Errors |
| :--- | :--- | :--- | :--- |
| `settings.get` | `{"key":"plugins.files.show_hidden"}` | `{"key":"...","value":false,"source":"user"}`; `value` is the validated value in JSON form (durations in milliseconds); `source` is a layer name or `default` | `not_found` with `data.reason` `unknown_setting` when the key is unknown to this caller (a plugin asking for a key it did not declare gets this, not `denied`); `busy` `rate_limited` |
| `settings.list` | `{}` | `{"entries":[{"key":"...","value":...,"source":"..."}]}`, every known key plus every retained unknown key, sorted by key | `denied` |
| `settings.schema` | `{"owner":"plugin:files"}`; empty or absent means all | `{"entries":[Entry...]}` sorted by key | `denied` |
| `settings.validate` | `{}` | `{"ok":true,"problems":[...]}`; `ok` is whether a reload now would publish a Settings candidate | `denied`; `timeout` `input_deadline`; `unavailable` when Store cannot resolve |

CLI: `husk settings get <key>`, `husk settings list [--source]`, `husk settings schema [--owner <owner>]`, `husk settings validate`.

### Theme

Theme is a function of the Settings snapshot it receives in `ReloadInput.Settings`: the one built-in palette, with per-role overrides, checked against the contrast floor, producing one `Palette` ([Protocol](protocol.md) wire type; the same Go type the renderer uses, no copy).

There is exactly one built-in palette and no theme registry. A distribution or user changes colours by overriding roles.

Overrides are the settings keys `theme.palette.<role>`, one per role of `Palette` (the JSON tags: `accent`, `accent_secondary`, `accent_soft`, `border`, `border_focused`, `border_subtle`, `canvas`, `danger`, `info`, `ink`, `ink_muted`, `ink_subtle`, `slot_background`, `success`, `surface`, `surface_elevated`, `surface_hover`, `surface_selected`, `warning`), plus `theme.name` (string). A value is `#RRGGBB`, case-insensitive on input, emitted lowercase. `#RGB`, named colours and ANSI indices are rejected (`invalid_color`, error, message `role <role>: want #RRGGBB`) because contrast cannot be computed for a terminal-dependent colour.

```json
{"theme": {"name": "Evening", "palette": {"canvas": "#101018", "accent": "#8fa8ff"}}}
```

Build, per rebuild:

1. Start from the built-in palette.
2. Apply every `theme.palette.<role>` whose `Source` is not `default` (layer order is already resolved in the Settings snapshot) through `Overlay`. An override `Overlay` rejects is the same `invalid_color` problem as above, keyed by that role's settings key; the roles are the fixed list, so `Overlay` has no other failure.
3. If `slot_background` was not overridden, derive it from the final `canvas` (`SlotBackground(canvas)`), so a new canvas drags the slot panel with it.
4. Check every pair in the contrast table (`ContrastPairs()`, production code shared with the built-in palette's test): text roles need **4.5:1** on every surface they are drawn on, borders and non-text indicators need **3:1** (WCAG 2.1 SC 1.4.3 and 1.4.11).
5. Each failing pair is a `low_contrast` problem whose message names the foreground role, the background role, the measured ratio and the minimum (`ink on surface is 2.10:1, need 4.5:1`) and whose `Key` is the settings key of the attributed role (`theme.palette.ink`), so every Settings and Theme problem's `Key` is something `husk settings get` accepts. It is attributed (`Layer`, `Source`) to the layer that overrode the foreground role; when the foreground is the built-in default, to the layer that overrode the background role; when both are overridden, to the foreground's. (At least one is overridden, since the built-in palette passes.)

**The contrast floor.** The Theme Service enforces it. When at least one pair fails:

- `theme.allow_low_contrast` is false (the default): the problems are `error`, no candidate is built, the previous palette keeps serving (the built-in palette at first load). The user sees the toast described under Reload and gets the full list from `husk theme check` or `husk config reload`.
- `theme.allow_low_contrast` is true and its `Source` is `user`, `profile` or `instance`: the problems are `warning`, the candidate is built and published.

Independently of whether any pair fails: if the `distribution` layer sets `theme.allow_low_contrast` to `true`, whether or not a stronger layer overrides it, Theme reports one `error` problem `distribution_opt_out` (message `distribution cannot opt out of contrast`), attributed to the distribution layer, and treats the opt-out as false. A distribution ships a palette it has checked; the unmerged `config` layers in `ReloadInput` are consulted so a user override cannot hide the distribution's attempt. A distribution value of `false` is the default and is not a problem.

There is no partial application: a palette with one failing override does not publish the passing ones. The user fixes the file; `husk theme check` reproduces the exact list. The complete list of Theme codes is `invalid_color`, `low_contrast`, `distribution_opt_out` and the coordinator's `settings_unchanged`.

`theme.changed`. Theme's report sets `Changed` when its candidate was applied and the new palette differs from the one serving before. The Pods host pushes `theme.changed` with the new palette to every live plugin exactly when a `services.reloaded` event carries a report with `service` `theme` and `changed` true ([Pods](pods.md) owns the push; it reads the palette from `theme.get`'s backing snapshot, not from the event). A reload that republishes an identical palette (generation increases, values equal) pushes nothing. Every plugin also receives `theme.changed` as its first stdin message at launch.

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

1. `Resolve` is pure and lock-free: it reads the snapshot pointer, no State, no clock. Input owns the typed prefix and the sequence timeout and passes the prefix in. Which candidate wins is [Keybindings](keybindings.md)'s rule and is not restated here: scope first (a binding scoped to the focused surface beats a global one), then layer (`profile` > `user` > `distribution` > `focus-local` > `husk` within one scope), then the `plugin` condition, then file order. Wherever this spec says "weakest" or "strongest", it means that ranking. Tombstones (`disable` entries), the context filters `Resolve` applies (frozen terminal, modal, text capture) and what each filter keeps are likewise [Keybindings](keybindings.md)'s, stated once there.
2. A rebuild parses every layer, applies the loader rules of [Keybindings](keybindings.md), and reports every dropped or shadowed entry as a `protocol.Problem` whose `Code` is one of that spec's loader codes (its table is the complete list; this spec adds only `invalid_plugin_catalog`, "Snapshots, rebuilds and reports"); nothing disappears silently. `Source` is filled for every keymap problem: layer, config-relative file and line for config layers; `focus-local`, plugin and order for declared entries, with `Layer` `plugin:<id>`. Conversion problems Pods attached to a declared document (`global_not_allowed`, `foreign_action`, `too_many`) are imported under layer `plugin:<id>` with the code and message conversion wrote; the Service itself raises `suggestion_ignored` for each entry of the document's `suggestions` array and, as the only key parser, `unparsable_key` for a focus-local spelling it cannot parse.
3. A layer document that fails to load on a reload (parse, version, size) keeps that layer's **last good** document for the same layer and, for `profile`, the same profile name; a different profile never inherits another profile's document. At first load the layer is dropped instead ([Keybindings](keybindings.md), failure modes). The last-good documents are process memory, named under Level 0.
4. The resolved keymap is capped at **4096 effective bindings** (the index the Service keeps in memory and `husk keys` prints). Past the cap the weakest-ranked entries are shadowed and reported `too_many`, so a flood of low-layer entries cannot displace a `profile` or `user` binding.
5. A rebuild that leaves no effective binding for `husk.quit` or `husk.palette.open` still publishes and reports `no_exit_binding` ([Keybindings](keybindings.md), loader rules table).
6. Lookup never fails: an unknown action id resolves, and the Dispatcher reports `not_found` at dispatch, where the user can see it. A host action declining a key (`ActionReply.Declined`, host-only and never serialized, defined in [Keybindings](keybindings.md) and [Message kinds](message-kinds.md)) is Input's and the Dispatcher's business; the Service never sees it.
7. `keys.list` for a `plugin` caller returns only the entries whose declaring plugin is the caller or whose action the caller owns in the catalog, plus the entries in state `effective` whose action is a `husk.*` host action or a rail item id declared in the catalog (the `rail_items` of the same declared plugin documents the Service already reads for action owners), and only the caller's own problems; `cli` and `host` get the full listing. The Server joins the listing with State liveness and the action catalog to produce the `state` column ([Keybindings](keybindings.md)). The `problems` array of the result is `[]protocol.Problem` ([Protocol](protocol.md), "Problem"), serialized as is.

### Secrets

Secrets decides which plugin may read which secret. Values live in Store's encrypted namespace `husk-secrets` ([Store](store.md), crypto); Secrets holds declarations (from the declared `plugins` documents) and grants (in State), never values.

A plugin declares what it wants in its manifest:

```json
{"secrets": [{"name": "git.token", "purpose": "Push and pull over HTTPS", "required": true}]}
```

`name` matches `[a-z0-9_.-]{1,64}`. `purpose` is the sentence the user reads in the dialog: plugin-controlled text, 1 to 200 runes, printable, no newline or other control character. `required` is informational in v1 (shown in the dialog and in `husk secrets list`); it changes no behaviour. A plugin may declare at most **16** secrets. A declaration is dropped, with a `warning` problem of layer `plugin:<id>` (`Source.Plugin` and `Source.Order` set), when its name is invalid (`invalid_secret_name`), when the same plugin already declared the name (`duplicate_secret`), when its `purpose` breaks the rule above (`invalid_purpose`; the message says which rule, never the text), or when it is past the sixteenth (`too_many_secrets`). The plugin's other declarations stand. This is the complete list of Secrets codes, with `invalid_plugin_catalog` from "Snapshots, rebuilds and reports"; a problem message never contains a `purpose`.

Names are global to the user (`git.token` is one value whichever plugin reads it), grants are per plugin. Two plugins that both declare `git.token` and are both granted share the value; a per-plugin namespace would block that sharing, and the per-pair grant keeps the user in control. This is the opposite of a per-extension secret store with no cross-extension policy (the VS Code model, where any extension can in practice read another's token); here the host mediates every read.

#### Grants

A **grant** is `(plugin id, name, digest, at)` where `digest` is `sha256:` over the JSON array `[name, purpose]` of the declaration. A grant is valid while the plugin's current declaration has the same digest; a changed `purpose` voids it and the next read asks again.

Grants are the `husk.grants` slice, registered by Secrets at construction exactly as [State](state.md) lists it: owner `husk`, user scope, persistent, `manual` retention, value `{"grants":[{"plugin":"git","name":"git.token","digest":"sha256:9d2e...","at":"2026-10-22T05:12:00Z"}]}`. Grants are shared by every instance of the user and survive a plugin's removal (reinstalling does not re-prompt); `husk secrets revoke` removes one. Every slice is readable by every reader ([State](state.md) invariant 14); the slice holds no value, so that is fine.

Only a user grants. A grant is written in two ways and no other:

- `secrets.grant` from a `cli` caller.
- The user accepting the confirm dialog that a plugin's read opened.

Both writes go through the loop post function; at execution the write re-checks that the current declaration's digest is the one the user saw and writes nothing otherwise. `secrets.grant` and `secrets.revoke` reply after their posted write has executed (the grant is in the slice when the CLI exits), not when it is queued.

#### Reading

`secrets.get` from a plugin:

1. The name is invalid or the plugin does not declare it: `not_found`, `data.reason` `not_declared`. The reply never confirms whether the secret exists for anyone else.
2. Declared, granted with the current digest: read the value from the encrypted namespace. Missing: `not_found`, `data.reason` `unset`. Namespace unreadable: `unavailable`, `data.reason` `storage`. Otherwise the reply carries the value.
3. Declared, not granted, and this `(plugin, name, digest)` was **explicitly declined earlier in this instance's life**: `denied`, `data.reason` `user_denied`, no dialog. The decline memory is process memory (Level 0); it is cleared for that triple by a `secrets.grant` from the CLI and by a change of the declaration's digest.
4. Declared, not granted, no remembered decline: the read **waits** while Services ask the host to show the confirm dialog. The dialog leads with the **plugin id** (the verified identity); the plugin's display name, if its manifest has one, follows it and is marked as the plugin's own claim; then the secret name, the `purpose` and `required`. Outcomes:
   - The user accepts: the grant is written and the read continues at step 2.
   - The user declines: `denied`, `data.reason` `user_denied`, and the decline is **remembered** for this instance's life.
   - The dialog is dismissed without an answer, 60 s pass, or no dialog is wired (a headless host): `denied`, `data.reason` `user_denied`, and **nothing is remembered**; the next read asks again. An unattended prompt is not a decision (D94).
   - A `secrets.grant` from the CLI for this `(plugin, name)` arrives while the dialog is pending or shown: the dialog closes, the read continues at step 2, any memory for the triple is cleared.
   - The plugin's declaration changes digest while the dialog is pending or shown (a `plugin` reload re-declared it): the dialog closes, the read gets `denied` `user_denied`, nothing is remembered, and the next read prompts with the new `purpose`.
   - The plugin's Pod leaves `live`, or the requesting connection closes, while the read waits: the dialog closes if shown, queued prompts of that plugin are dropped, the read is dropped, nothing is remembered.

The read blocks instead of returning a "come back later" code because one request with one answer is simpler for a plugin than polling, and polling costs rate-limit budget; browser permission prompts resolve the same way. The Server's per-connection in-flight cap bounds how many such waits a plugin can hold open.

Dialog rules: at most one dialog is shown at a time per instance; prompts from different plugins queue in request order and each one's 60 s clock starts when it is shown. A plugin has at most one prompt pending or shown at a time: concurrent reads of the same pending `(plugin, name, digest)` wait on that one dialog, and a read of a different ungranted name while one is pending gets `busy`, `data.reason` `dialog_pending`, without waiting (a plugin cannot fan out prompts to hold in-flight slots). A grant, once accepted, is checked at step 2 only: a `secrets.revoke` that lands after the dialog was accepted and before the value is read does not fail that read; the next one it does.

`secrets.get` from a `cli` caller and `secrets.reveal` are **one handler**: the same code path, result, errors and audit record. There is one audited path for a value leaving the store to a human, and the two method names are aliases of it.

The value is UTF-8 text, at most 64 KiB; the empty string is a valid value (`set` is true). `secrets.set` rejects a value that is not valid UTF-8 (`invalid_params`, `data.reason` `not_utf8`) or over the cap (`invalid_params`, `data.reason` `secret_limit`); a binary secret is the plugin's problem to encode.

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

Secrets publish `secrets.read` for every `secrets.get` and `secrets.reveal`: `{"name":"git.token","plugin_id":"git","outcome":"allowed"}`, with `plugin_id` empty for a `cli` caller and `outcome` one of `allowed`, `revealed`, `not_declared`, `unset`, `user_denied`, `dialog_pending`, `unavailable`. It is a core event ([Message kinds](message-kinds.md)) and is delivered to `cli` watchers only, never to a plugin subscriber: one plugin has no business learning which secrets another reads. The same restriction applies to `services.reloaded`, whose reports name other plugins' keys and files. The mechanism is [Message kinds](message-kinds.md)'s: manifest conversion rejects a subscription pattern that matches `secrets.` or `services.` (the plugin's subscriptions are dropped and reported, like a malformed pattern), and `event.watch` from a `plugin` caller answers `denied` for such a pattern. Services only publish.

#### Methods

| Method | Params | Result | Errors |
| :--- | :--- | :--- | :--- |
| `secrets.get` | `{"name":"git.token"}` | `{"name":"git.token","value":"..."}` | plugin: `not_found` (`not_declared`, `unset`), `denied` (`user_denied`), `busy` (`dialog_pending`, `rate_limited`), `unavailable` (`storage`); `host`: `denied` (`user_only`); `cli`: as `secrets.reveal` |
| `secrets.names` | `{}` | `{"storage":"ok","secrets":[{"name":"git.token","declared":["git"],"granted":["git"],"set":true,"required":true}]}` sorted by name; `storage` is `ok` or `unavailable`; a `cli` caller also sees stored names nobody declares; a plugin sees only names it declares, with `declared` and `granted` reduced to itself and `set` present only for names it is granted (an ungranted plugin must not learn whether a value exists) | `busy` (`rate_limited`) for a plugin |
| `secrets.set` | `{"name":"git.token","value":"..."}` | `{}` | `denied`, `invalid_params` (`invalid_secret_name`, `not_utf8`, `secret_limit`), `unavailable` (`storage`) |
| `secrets.grant` | `{"plugin":"git","name":"git.token"}` | `{}` after the posted write executed | `denied`, `not_found` (`not_declared`: the plugin does not currently declare the name) |
| `secrets.revoke` | `{"plugin":"git","name":"git.token"}` | `{}` after the posted write executed; revoking a grant that does not exist succeeds | `denied` |
| `secrets.reveal` | `{"name":"git.token"}` | `{"name":"git.token","value":"..."}` | `denied`, `invalid_params` (`invalid_secret_name`), `not_found` (`unset`), `unavailable` (`storage`) |

CLI: `husk secrets list`, `husk secrets set <name>` (value from stdin, trailing newline stripped), `husk secrets grant <plugin> <name>`, `husk secrets revoke <plugin> <name>`, `husk secrets reveal <name>`.

### Limits

| Limit | Value | Reason |
| :--- | :--- | :--- |
| Reload wall time, gathering included | 2 s | Reload runs while the UI is live; slower is a source problem; keep the old snapshots |
| Effective bindings in the resolved keymap | 4096 | Bounds the lookup index and `husk keys` output; per-file and per-plugin caps are in [Keybindings](keybindings.md) |
| Secret value | 64 KiB, valid UTF-8 | Tokens and credentials; blocks using Secrets as a blob store; matches [Store](store.md) |
| Secret name | 1 to 64 characters of `[a-z0-9_.-]` | Same alphabet as settings keys; safe on a command line |
| Declared secrets per plugin | 16 | Bounds the dialog queue and the declaration list a user has to review |
| Grant dialog | 60 s from when it is shown; one shown at a time per instance; one pending per plugin | An unattended prompt must not hold a plugin forever; a timeout is not a decline and is not remembered (D94) |
| Plugin calls to any Services method (`settings.get`, `secrets.get`, `secrets.names`, `theme.get`, `keys.list`) | 20 requests per second per plugin, one shared budget, fixed one-second window | A plugin polling in a loop cannot starve the loop; enforced by the Server before the Service is called; refused with `busy`, `data.reason` `rate_limited`; malformed requests count |
| Configuration document size, merge depth | Store's ([Store](store.md) limits) | Not re-stated here |

### Invariants

1. A Service holds no file handle and imports no file-system package. Persistence is Store's alone.
2. Services import Store and State interfaces and Protocol's value types; Store, State and Protocol never import Services. The Stem and the Server handlers import Services' interfaces; Pods import no Services package and reach the coordinator through the `Reloader` interface Pods define (Level 0, "Dependencies").
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
13. Services publish exactly two events: `services.reloaded` (coordinator) and `secrets.read` (Secrets); both reach `cli` watchers only.
14. Services define no second copy of a type another layer owns: the caller identity (`protocol.Caller`), `Palette`, the settings `Entry` and the declared-document types are shared, not converted; and `protocol.Problem` is the one diagnostic type every report and `keys.list` carry.
15. Every problem a Service can raise has a code listed in this spec or in [Keybindings](keybindings.md)' loader table; tests match on codes, never on messages.

### Failure modes

| Condition | Detected by | What happens | What the user or plugin sees |
| :--- | :--- | :--- | :--- |
| A known settings key has an invalid value | Settings rebuild | No Settings candidate; old snapshot serves; Theme is skipped (`settings_unchanged`) and keeps its palette | `config.reload` prints the problem with layer and key; a toast on a reload the user did not start |
| Unknown settings key | Settings rebuild | Warning; raw value kept for `settings.list`; everything else applies | Listed by `husk settings validate` and in the reload report; no toast |
| A plugin's manifest `settings` schema is invalid | Settings rebuild | Warning per entry; none of that plugin's entries registered; its keys become `unknown_key`; everything else applies | In the reload report under `plugin:<id>`; the plugin's `settings.get` answers `not_found`; no toast |
| The host or distribution schema is invalid | Services construction | The process does not start | Startup error naming the entry |
| Palette override fails contrast, opt-out off | Theme rebuild | No Theme candidate; old palette serves | Every failing pair with ratio and minimum; `husk theme check` reproduces it |
| Palette override fails contrast, opt-out on from `user`/`profile`/`instance` | Theme rebuild | Warnings; palette published | Warnings in the report; no toast |
| `theme.allow_low_contrast` is `true` in the distribution layer | Theme rebuild | Error whether or not any pair fails; opt-out ignored | `distribution_opt_out` |
| Override is not `#RRGGBB` | Theme rebuild | No Theme candidate | `invalid_color`, `role <role>: want #RRGGBB` |
| A keymap layer fails to load on reload | Keybinding rebuild | That layer's last good document is used; rebuild publishes | Problem naming file and line; `husk keys --problems` |
| Keymap entry invalid, duplicate, unreachable, over a cap | Keybinding rebuild | Entry dropped or shadowed; rebuild publishes | Problem per entry ([Keybindings](keybindings.md) codes) |
| Binding names an unknown action | Dispatcher, at dispatch | The key resolves; dispatch fails `not_found` | Toast naming the action; `husk keys` state `unknown_action` |
| `--profile` or `profile.default` names no profile | Profile | No profile; reload proceeds | Warning problem; one startup toast |
| Active profile's directory deleted | Profile rebuild | Slice reset to `""`; one follow-up `profile` reload | Warning toast |
| Store cannot resolve `config` (parse error, over limit) | Coordinator, step 2 | No Service rebuilds; every snapshot stays; every report carries `store_error` | `config.reload` returns `unavailable` `store_error` naming the config-relative file; toast when the user did not start the reload |
| The `startup` flight fails | Coordinator | Built-in snapshots serve | Startup toast with the error count |
| Reload exceeds 2 s | Coordinator | Published candidates stay; remaining Services report `timed_out`; late candidates never applied | Report rows with `applied:false`, `timed_out`; `config.reload` returns `timeout` `reload_deadline` |
| Shutdown during a flight | Coordinator `Close` | Remaining Services report `cancelled`; waiters released | `config.reload`/`profile.use` return `cancelled` `shutdown` |
| Plugin reads an undeclared secret | Secrets | `not_found`, `not_declared` | The plugin gets the error; nothing shown to the user |
| Plugin reads a declared, ungranted secret | Secrets | Read waits; dialog opens | Dialog with plugin id, name, purpose; the plugin's reply is the value or `denied` |
| Dialog declined by the user | Secrets | `denied`, `user_denied`; remembered for this instance | Later reads get it without a dialog; `husk secrets grant` clears it |
| Dialog dismissed, 60 s elapsed, or no dialog wired | Secrets | `denied`, `user_denied`; nothing remembered | The next read prompts again |
| Plugin reads a second ungranted name while its dialog is pending | Secrets | `busy`, `dialog_pending` | The plugin retries after the first read answers |
| Plugin declares more than 16 secrets or a bad `purpose` | Secrets rebuild | Declaration dropped with a warning | `too_many_secrets` / `invalid_purpose` in the report; no toast |
| Declaration `purpose` changed since the grant | Secrets digest check | Grant void | Next read prompts again |
| Encrypted namespace unreadable | Store crypto | `unavailable`, `storage`; other Services unaffected | Plugin gets the error; `husk secrets list` shows `storage: unavailable`; Store's diagnostic suggests `husk store reset-secrets` |
| Plugin exceeds 20 Services calls per second | Server | `busy`, `rate_limited` | The plugin gets the error |
| Plugin calls a user-only method | Server, Services | `denied`, `user_only` | The plugin gets the error |
| Invalid caller reaches a Service | Services | `denied`, `invalid_caller` | A composition bug; logged |
| A plugin's declared document does not decode, or its `id` is not its Store source | Settings, Keybinding, Secrets rebuild | That plugin's declarations are ignored by the reporting Service; everything else applies | `invalid_plugin_catalog` warning under `plugin:<id>`; no toast |
| `Apply` reached after the deadline, after `Close`, or after the generation moved | the candidate's `Apply` | Nothing swapped; old snapshot serves | `applied:false`, `candidate_superseded`; never in a healthy flight |

### Code layout

Services live under one import root as `services/{profile,settings,theme,keybinding,secrets}` plus the coordinator in `services`. Shared value types the children and the coordinator both need (`Generation`, `Report`, `ReloadInput`, `Reloadable`, `Candidate`, the settings `Entry` and the declared-document types) live in one leaf package the children import, so no child imports its parent. The caller identity and the diagnostic are not among them: they are `protocol.Caller`, `protocol.Problem` and `protocol.Source` ([Protocol](protocol.md)), because problems cross the wire (`keys.list`, the reload reports) and the host contracts that carry them must not import Services; the Services `Caller`, `Problem` and `Source` structs are deleted. The embedded `husk` keymap sits beside the Keybinding Service. `pkg/theme` keeps `Palette`, `Default()`, `Overlay`, `SlotBackground`, `ContrastPairs` and `CheckContrast`, so the runtime check and the built-in palette's test share one rule.

## Level 2: Status against the code

The five Services and the coordinator exist. Items below are where the code on `main` and this spec differ; each is small and self-contained.

- Caller identity: five `Caller` structs exist (Server, host contracts, Dispatcher, hooks, Services) and adapters convert between them. The spec requires the one `protocol.Caller`. The `host` kind currently counts as a user for secrets; the spec denies it secret operations.
- `Problem` is `protocol.Problem` with `Code` and `Source`, and the codes and severities are as specified. One divergence remains: the coordinator folds its own `Close` into the timeout path and reports `timed_out` for the Services not yet published; the spec reports `cancelled`.
- Settings exposure: landed. `Entry.Public` exists, visibility is exact ownership plus `public`, and the built-ins mark none public.
- Settings problems: landed. Every unknown key is a warning with the raw value retained; a plugin schema problem is a warning against `plugin:<id>` that unregisters that plugin's entries; a bad host or distribution schema makes `settings.New` fail. `Source.File` is the config-relative name Store reports.
- `store_error` names no file: the coordinator reports `Store resolution failed` without the config-relative file the coordinator code table and Reload step 2 require.
- Profile selection: landed. The coordinator selects in step 2 from the `--profile` flag (startup flight only), the `husk.profile` slice and the unmerged `user` layer, resolves once, and writes a choice back to the slice; the host runs the startup flight once before the first plugin launch. An absent slice falls through to `profile.default`; `{"active":""}` is an explicit no profile, and `profile.default` selections are not written back.
- The distribution opt-out error fires when the key is present in the distribution layer with any value; the spec fires it for `true` only.
- A reload that times out leaves candidates applicable; the spec makes `Apply` after the deadline a no-op.
- `Report` has no `Changed`; Pods have nothing to key `theme.changed` on.
- Reply shapes: landed. `settings.get` answers `not_found`/`unknown_setting` at the Service for every caller; `settings.list` is `{"entries":[{key,value,source}]}`, `settings.schema` is `{"entries":[...]}`, `profile.list` is `{"profiles":[...]}`, and `settings.validate` and `theme.check` reply `{"ok","problems"}`.
- `secrets.get` with no grant returns `denied`/`needs_grant` immediately and expects the plugin to retry; a decline is forgotten after one `user_denied` reply, so a plugin can reopen the dialog at will; several dialogs can be open at once; declarations are uncapped and `purpose` is unchecked. The spec blocks the read, remembers an explicit decline, shows one dialog at a time, allows one pending prompt per plugin, caps declarations at 16 and validates `purpose`.
- `secrets.get` from a `cli` caller returns the value without an audit record; the audit sink and the confirm dialog are not wired by the host, so every dialog grant is refused. `secrets.set` accepts arbitrary bytes and encodes them as a JSON string. `secrets.grant`/`secrets.revoke` reply when the write is queued, not executed.
- `profile.use`: landed. It writes the slice on the loop and replies `{"active","reports"}` with the reports of the flight that applies the write; when the coordinator is already settled on that name (no flight pending, last selection and active profile both equal to it) it replies at once with the last flight's reports and nothing reloads.
- The Server's 20/s limiter covers `settings.get` and `secrets.get` only; the spec puts every plugin-callable Services method under it.
- `theme.changed` is sent only at plugin launch; nothing pushes it after a Theme change.
- `secrets.read` and `services.reloaded` are published on the bus with no delivery restriction; the spec delivers both to `cli` watchers only.
- `profile.default` in a layer other than `user` is reported as `profile_default_ignored`: landed.
- The `husk settings|theme|profile|secrets|config` subcommands do not exist yet; the Server handlers do.
- The host built-in keys `husk.layout.rail` and `husk.input.hold_alt` are registered but the frontend still reads the equivalent start flags; `husk.plugins.paths` and `husk.plugins.dirs` are registered and read by nothing.
- The Keybinding Service ranks layer before scope; [Keybindings](keybindings.md) ranks scope first.

## Decisions made here

1. Five Services with one shared rebuild shape: immutable generation-numbered snapshots, all-or-nothing per Service, one coordinator that serializes and coalesces reloads and publishes one `services.reloaded`. Dry runs take the coordinator's turn so they may invalidate Store safely.
2. Reload order Profile, Settings, Theme, Keybinding, Secrets; Theme reads the Settings snapshot the same reload published (`ReloadInput.Settings`) and is skipped with a `settings_unchanged` warning when Settings failed; Keybinding and Secrets are independent of Settings. A late candidate is never applied.
3. The diagnostic is `protocol.Problem` ([Protocol](protocol.md), "Problem"): it carries a stable `Code` and, when known, a `protocol.Source`; every code is listed in this spec or in [Keybindings](keybindings.md); `keys.list`'s problem object is this type. Tests and tools match on codes; messages are prose.
4. Profile selection runs in the coordinator before the one `Store.Resolve` and reads `profile.default` from the unmerged `user` layer; a `--profile` choice (and a vanished profile's reset to `""`) is written back to `husk.profile` by the flight that made it, a `profile.default` selection is not, and the slice listener ignores that write. `profile.use` writes State only, waits on the flight its write triggers (correlated by registration at write time) and replies with that flight's reports; it replies at once, without a reload, only when the coordinator is settled on that name (no flight pending, last selection and active profile both equal to it), so a `profile.default` selection is recorded without a reload and a repeated name during a flight waits on it.
5. Settings namespaces have exactly one owner each; visibility is by declaration ownership plus an explicit `public` flag on host and distribution entries. **No host settings key is readable by plugins today**; a schema entry marked `public: true` is the mechanism when a need appears, and nothing else makes a host key visible. No method writes a setting.
6. **An unknown settings key is a warning**, kept raw for the user and never shown to plugins; an invalid value of a known key is an error, so nothing half-applies. Errors for unknown keys would let a stale key from a removed plugin or a typo freeze every setting. A plugin's schema error is a warning that unregisters only that plugin's entries; a host or distribution schema error fails construction, never a reload.
7. One built-in palette, `#RRGGBB` overrides per role, the contrast floor enforced by Theme at 4.5:1 text and 3:1 non-text, no partial application: a theme that misses the floor is not applied at all, the user gets one toast, `husk theme check` reports the failing pairs. `theme.allow_low_contrast` demotes to warnings from non-distribution layers only; `true` in the distribution layer is an error whatever the contrast outcome. Closed.
8. The contrast table is production code shared by the runtime check and the built-in palette's test.
9. `Report.Changed` tells Pods when to push `theme.changed`: on a published palette that differs, not on every reload.
10. Keymap policy, ranking included (scope first, then layer, then the `plugin` condition, then file order), tombstones and context filters live in [Keybindings](keybindings.md); this spec owns only the Service mechanics, the last-good-layer rule, the 4096 cap and the per-caller `keys.list` view. **The declared focus-local document is named `keymap`**, the same name as the layered keymap document; the two are told apart by the Store method (`Declared` versus `Layers`), and every spec that mentions one says which it means.
11. Secret names are global to the user, grants are per `(plugin, name)` and bound to the declaration digest; grants live in the persistent user-scope `husk.grants` slice; values live only in Store's encrypted namespace. A plugin declares at most 16 secrets and its `purpose` text is validated and never echoed in a problem.
12. **An ungranted secret read blocks on the grant dialog** (60 s, one shown at a time per instance, one pending per plugin) instead of returning a retry code, which would need a polling interval nobody specified and would spend rate budget. An explicit decline is remembered for the life of the instance; a timeout, a dismissal or a headless host is not a decision and is not remembered (D94).
13. **Grants survive `husk plugin remove`.** They are the user's consent record, not plugin state; `husk secrets revoke` is the explicit way to drop one.
14. Only a `cli` caller or the accepted dialog writes a grant; **the `host` kind never handles a secret value** (the confirm dialog is its only secret role, the same least-privilege line that keeps the Stem out of `state.clear`); every value handed to a human goes through one handler (`secrets.reveal`, aliased by `secrets.get` from the CLI) and is audited through `secrets.read` and a host log line. There is no audit file: a single-user local tool has no consumer for one.
15. Secret values are UTF-8 text up to 64 KiB; the empty string is a value.
16. `secrets.read` and `services.reloaded` reach `cli` watchers only; conversion and `event.watch` refuse plugin subscriptions to them.
17. Plugins reach settings, secrets, the palette and their own keys through Server methods under one 20 requests per second per plugin budget covering every Services method; nothing arrives as environment values.
18. The caller identity is `protocol.Caller`, one type with four kinds (`cli`, `plugin`, `input`, `host`) and the Server's `ConnID`; Services treat `input` as `host`.

## Open questions

1. **Live reload on file change.** Not in this spec: reload runs on `config.reload`, a profile change, and a plugin added or removed. File watching, if wanted, belongs to Store sources calling the coordinator, in the Store spec.
2. **Persisting the chosen profile (`husk profile use --save`).** Deferred until Store exposes a write API for the user layer; Services do not write files.
