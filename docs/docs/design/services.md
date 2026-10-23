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

What Services are not. They are not a settings editor: the one writing method, `settings.set` (Settings, "Writing one key"), puts one key and one value into the user layer through Store's write and then reloads; everything else about configuration changes by editing a file and reloading. They are not a plugin host, a message router or a renderer: they know nothing of sockets, Pods, the event loop or the terminal.

### Spec ownership

Three specs touch the same ground. The rule that owns a topic is written once, in the spec named here; the other specs point to it.

| Topic | Owner |
| :--- | :--- |
| Keymap layers and their rank, the keymap file shape, key spelling, tombstones, sequences, resolution, special contexts, the built-in `husk` keymap, what `keys.list` and `husk keys` report, per-file and per-plugin binding limits | [Keybindings](keybindings.md) |
| The Keybinding Service mechanics: inputs, rebuild, generation, where loader problems go, the per-caller view of `keys.list`, the resolved-keymap size cap | this spec |
| Merging of `distribution < user < profile < instance`, file locations, the declared-document schema, the encrypted namespace, the `Patch` write | [Store](store.md) |
| The `themes` document's shape and meaning, which theme `theme.name` selects, the theme selector's behaviour | this spec, "Theme" |
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
| `settings.set` | yes | yes | `denied` |
| `theme.get`, `theme.list` | yes | yes | yes |
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
4. A Service always has a snapshot. Before its first load it serves its built-in snapshot (generation 0): no profile, the schema defaults, the built-in palette, the embedded `husk` keymap, no declarations. On a reload after startup, a Service whose rebuild fails keeps the snapshot it was serving, and the failure is reported. On the `startup` flight the same holds for a Service's own validation errors, with two exceptions that abort construction instead: a failed flight, and a Settings error on `husk.plugins.paths` or `husk.plugins.dirs` (Reload, the `startup` flight).
5. Problem messages name layers, keys, roles, ratios and plugin ids. They never contain a secret value or a declaration's `purpose`.
6. `Rebuild` may run on any goroutine and writes no State. Only the posted callbacks named under Profile and Secrets write State.
7. `Code` is the contract; `Message` is prose. Every code a Service can emit is in its table in this spec (Keybinding: the loader table in [Keybindings](keybindings.md)); a new code is a spec change. `Changed` is false whenever `Applied` is false.

Codes raised by the coordinator or by a candidate's `Apply`, in any report; on a reload after startup each leaves the previous snapshot serving (`applied:false`), and on the `startup` flight `store_error`, `timed_out` and `cancelled` abort construction (Reload):

| Code | Raised by | Condition | Severity | Effect |
| :--- | :--- | :--- | :--- | :--- |
| `store_error` | coordinator, step 2 | Store could not resolve or load a layer (parse error, over a limit); the message carries Store's code, the config-relative file, the layer and the line and column when Store knows them, never a value or Store's own text, and the problem's `source` holds the layer, file and line; an error Store returns without a problem gets a coded reason instead (`invalid_path` and so on) | error | no Service rebuilds; every report carries it; `config.reload` answers `unavailable` `store_error` |
| `timed_out` | coordinator, step 4 | the 2 s deadline passed before this Service published | error | this and every later Service keeps its snapshot; candidates already published stay |
| `cancelled` | coordinator | the coordinator closed during the flight | error | the Services not yet published keep their snapshots; `config.reload` answers `cancelled` `shutdown` |
| `settings_unchanged` | coordinator, step 3 | Settings produced no candidate, so Theme was skipped (message `settings unchanged; palette unchanged`) | warning | Theme's report only: no rebuild, `applied:false`, the palette stays; the only `applied:false` report that carries no `error` |
| `candidate_superseded` | the candidate's `Apply` | `Apply` found the candidate stale and swapped nothing: its flight's deadline had passed, the coordinator had closed, or the Service's generation moved since the rebuild | error | that Service's report says `applied:false`; the coordinator never calls `Apply` on a late candidate (step 4), so in a healthy flight this code never appears |

A Service that reads the declared `plugins` documents (Settings, Keybinding, Secrets) raises `invalid_plugin_catalog` (`warning`, layer `plugin:<id>`, `Source.Plugin` set) when a plugin's document does not decode or its `id` is not its Store source; that plugin's declarations are ignored by the reporting Service, as if it had declared nothing (its configuration keys are then `unknown_key`), and every other plugin's stand. Like a bad plugin schema (Settings, "Validation"), it is never an `error`: a plugin cannot block the user's reload by shipping a bad manifest.

`ReloadInput` is one consistent set of inputs, gathered once per reload and handed to every Service: the merged `config` document with per-key origins (`Store.Resolve("config", profile)`), the unmerged `config` layers (`Store.Layers("config", profile)`, for Theme's distribution check and Profile's `profile.default` rule), the unmerged `keymap` layers (`Store.Layers("keymap", profile)`), the unmerged `themes` layers (`Store.Layers("themes", profile)`, for Theme's named themes), the declared documents (`Store.Declared`), the profile the coordinator selected, and `Settings`: the Settings `Snapshot` this reload published, filled in by the coordinator after Settings' candidate is applied and nil when it was not. Only Theme reads `Settings` and the `themes` layers.

### Reload

The **coordinator** is a function of the Services package, not a sixth Service. It owns every reload. Nothing else publishes a candidate.

Triggers, each carried as the reload's `reason`:

| `reason` | Trigger | Who asks |
| :--- | :--- | :--- |
| `startup` | Store, State and Services are built and State is hydrated (before manifests are converted) | the composition root, once per process, before the first plugin launches |
| `cli` | `config.reload` | a `cli` or `host` caller through the Server |
| `profile` | the `husk.profile` slice changed | the coordinator's own State flush listener |
| `plugin` | a plugin's declared documents were added or removed | the Pods host, after `Store.Declare` (launch step 3, at startup and for each `plugin.add` batch) or `Store.Undeclare` (`plugin.remove` step 7, [Pods](pods.md)); the launch gate, the add and the removal each wait for this flight, and the add and the removal return its five reports in their replies, the same `Report` objects `config.reload` returns; a report with `applied: false` fails neither |
| `settings` | `settings.set` wrote one key into the user layer | Settings, after `Store.Patch` returned (Settings, "Writing one key"); the requester waits for this flight and gets its five reports in the reply, like `profile.use` |

One reload runs these steps in order:

1. Start the flight. Requests that arrive from now until capture join it (see coalescing). The coordinator hands the flight to its worker at the end of the loop tick that created it, so the requests that join a flight are exactly those posted in that tick; nothing is read yet.
2. **Capture**, then gather `ReloadInput` on a worker goroutine. Capture is the `Store.Invalidate()` call, before any input is read, so a request that arrives once gathering has begun can never be absorbed by a flight that already read the value it changed. Then select the profile (see Profile below; it reads the `--profile` flag, the `husk.profile` slice and the unmerged `user` config layer from `Store.Layers("config", "")`, never a merged document), then `Store.Resolve("config", profile)` exactly once, then `Store.Resolve("themes", profile)` for its problems only (the merged `themes` document is discarded: `Store.Layers` drops a layer that fails to parse without saying so, and Theme reads themes unmerged), then `Store.Layers("themes", profile)`, then the `keymap` layers and the declared documents. If either `Resolve` reports a problem other than `type_clash` (a layer that failed to parse or exceeded its limits), the reload **fails as a whole**: no Service rebuilds, every report says `applied:false` with one `store_error` problem naming the config-relative file, and `config.reload` returns `unavailable` with `data.reason` `store_error`. A `type_clash` is passed to Settings, which imports it as a `type_clash` warning naming the key and both layers.
3. Rebuild in the fixed order Profile, Settings, Theme, Keybinding, Secrets, each on a worker goroutine, each publishing its candidate before the next starts. After Settings publishes, the coordinator sets `ReloadInput.Settings` to the published Settings `Snapshot`; if Settings produced no candidate it stays nil and Theme is skipped: no rebuild, `applied:false`, one `warning` problem `settings_unchanged` (message `settings unchanged; palette unchanged`). Keybinding and Secrets do not read `Settings`.
4. The whole reload, gathering included, has a **2 s deadline**. When it passes, candidates already published stay; the Service being rebuilt and every later one report `applied:false` with the problem `timed_out`; the coordinator never calls `Apply` on a late candidate, and `Apply` after the deadline is a no-op (`applied:false`) in any case, so a late candidate can never change a generation.
5. Publish one event, `services.reloaded`, with the reason and the five reports, through the publisher the host injected at construction (the same seam State uses; delivered as `event.deliver`, [Message kinds](message-kinds.md), to `cli` watchers only, like `secrets.read`).
6. Answer every waiter with the five reports.

The `config.reload` reply is `{"reports":[...]}`. It is an error only when the flight itself failed: `unavailable` `store_error` (step 2), `timeout` with `data.reason` `reload_deadline` (step 4 passed before step 6), `cancelled` with `data.reason` `shutdown` (the coordinator closed first). A Service that kept its old snapshot is a success reply with `applied:false` in that report. `profile.use` uses the same three error replies for the flight it waits on.

The `startup` flight is the first reload of the process. The composition root runs it after Store, State and Services are built and State is hydrated, before manifests are converted; the Pods launch gate then requests its own `plugin` flight, so a plugin's declarations are never read by the startup flight. Only the root waits on its reply. A failed startup flight is fatal: when any report carries `store_error`, `timed_out` or `cancelled`, or the flight returns an error with no reports, construction aborts with those problems as its diagnostics and the process does not start. Of the coordinator's codes, exactly those are fatal; husk never starts on built-in snapshots with the configured plugins silently missing. A Settings `invalid_value` on `husk.plugins.paths` or `husk.plugins.dirs` (a non-array, for instance) is fatal the same way, naming key and layer, as is an entry the root cannot honour once it reads the two keys: a `paths` entry that fails to load, or a `dirs` entry that is not a readable directory (a bad plugin inside a directory is refused and logged, as under `--plugin-dir`). Husk never starts with a plugins setting the user wrote that it cannot honour. Every other `error` problem on the startup flight is nonfatal: that Service keeps its built-in snapshot, the process starts, and the error reaches the startup toast. For Settings that means the whole built-in snapshot serves (rule 2), so `husk.plugins.paths` and `husk.plugins.dirs` read their `[]` default and no configured plugin launches until the user fixes the key and restarts. The startup flight's `services.reloaded` reaches no one: the flight runs before the Event bus exists, so the host drops that event, and it is never replayed. Instead, the root retains its five reports and exposes a detached, immutable-across-reloads copy through `Frontend.StartupReports()`, available as soon as `New` returns. The frontend reads that seam for the startup toast when reports carry errors.

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

What the user sees. `config.reload` prints the reports. A reload the user did not start from the CLI (`startup`, `profile` or `plugin`) with at least one `error` problem shows one toast: `<n> configuration problems; run husk config reload` with `<n>` the error count across reports. Warnings alone show no toast. A `settings` reload shows no toast either: its requester holds the reports (the CLI prints them; the theme selector shows the Theme report's problems in the modal, "The theme selector"). The frontend derives the startup toast from `Frontend.StartupReports()` (nonfatal errors only: a fatal startup flight aborts construction before any frontend exists) and later reload toasts from the `services.reloaded` event; Services show nothing themselves.

Dry runs. `settings.validate` and `theme.check` take the coordinator's turn exactly like a flight (they wait for a running flight to end, and a flight requested during a dry run waits for it; they never coalesce with one), run step 2 with the same 2 s deadline (`timeout` with `data.reason` `input_deadline` when exceeded), run the one Service's `Rebuild`, and discard the candidate. `theme.check` first dry-builds Settings to obtain the snapshot Theme would see. They never publish, never change a generation and never emit `services.reloaded`. Serializing them is what lets them call `Store.Invalidate()` safely: a dry run always sees the files as they are on disk now, so `husk theme check` after editing a file reports that edit. A turn ends only when the Store gather and the `Rebuild` calls it started have returned, even when the caller (a flight past its deadline, a dry run past `input_deadline`, a `Close`) was answered earlier, so an abandoned predecessor never overlaps the next turn.

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

`profile.use` validates the name against the current snapshot. It is a choice, so the slice records it even when it changes nothing: the write and the decision happen on the loop, in one posted callback, in this order. First the callback writes `{"active":"<name>"}` to `husk.profile`; a write of the value the slice already holds changes nothing and the listener never sees it. Then, if the coordinator is **settled on that name**, meaning no flight is running or queued, the most recent flight selected `<name>` and it is the active profile, the requester replies at once with `active` and the reports of the most recent completed flight: nothing reloads (if the write did change the slice, from absent to `{"active":"<name>"}`, the listener ignores it as equal to the selection, and the instance now keeps that profile when `profile.default` changes or is removed). Otherwise the same callback requests the `profile` flight that applies the write and registers the requester as its waiter, by the coalescing rule: a new flight when none is pending, the running flight itself when it has not captured yet, or the queued follow-up when it has (has begun gathering), so waiting never hangs. The slice listener, told of that registration, requests no second flight for the write. Correlation is that registration, not a comparison of names or generations, so two `profile.use` calls in one tick, whatever their names, wait on the same flight and each gets its reports, and a `profile.use` during a flight that is switching to another profile waits for the follow-up that applies its own. It is instance-only: it does not write `profile.default`. Writing a chosen profile back to the user layer (`profile.use --save`) is a later change that composes this method with `settings.set` of `profile.default` (Settings, "Writing one key"); Services still touch no file themselves.

CLI: `husk profile list`, `husk profile current` (prints the entry with `active:true`, or nothing), `husk profile use <name>`.

### Settings

Settings turns the merged `config` document into typed, validated values. The **schema** is the only place a key is defined; a key without a schema entry has no type, no default and no owner.

#### Namespaces and ownership

| Namespace | Owner | Schema registered by | Readable by | Written by |
| :--- | :--- | :--- | :--- | :--- |
| `husk.*`, `theme.*`, `profile.*` | the host | Husk's built-in schema (compiled in) | `cli` and `host`: all; `plugin`: only entries marked `public` | the user and distribution, in their layer files; the user also through `settings.set` |
| `dist.*` | the distribution | the embedding application, at Services construction (`Schema []Entry`, owner `dist`) | `cli` and `host`: all; `plugin`: only entries marked `public` | the distribution and the user, in their layer files; the user also through `settings.set` |
| `plugins.<id>.*` | the plugin `<id>` | that plugin's manifest `settings` section, through the declared `plugins` document | `cli` and `host`: all; the plugin `<id>`: its own entries; other plugins: nothing | the user (and a distribution shipping defaults for a bundled plugin), in their layer files; the user also through `settings.set`; never the plugin |
| anything else | nobody | cannot be registered | `cli` and `host` see the raw value in `settings.list`; plugins never | `settings.set` refuses it (`not_found`) |

A plugin's identity in a key is the exact Store source id: `plugins.<exact-plugin-id>.<local-key>`. Plugin ids follow the Pod id regex ([Pods](pods.md) rule 3; uppercase and dots allowed) and are never case-folded. The local key is exactly one segment matching `[a-z0-9_-]+`; conversion rejects a dotted local key ([Store](store.md)). Ownership is by declaration, never by textual prefix: a plugin `a` does not own `plugins.a.b.x`, which belongs to a plugin `a.b` if one declared it, and to nobody otherwise.

One method writes a setting: `settings.set`, one key and one value into the user layer, for `cli` and `host` callers only ("Writing one key" below). Plugins cannot change configuration, and everything the method does not cover (another layer, several keys at once, removing a key) is still done by editing a layer file and reloading.

`public` host keys. A host or distribution entry is readable by plugins only when its schema entry says `public: true`. The built-in schema marks **no** key public today: the palette reaches plugins through `theme.get`, and the other host keys (the plugin search paths) are the user's business, not a plugin's. A future key that plugins need is made public by its schema entry, which is the one place that decision is recorded.

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

Built-in host entries (the complete list is `husk settings schema --owner husk`): `profile.default` (string, live; consulted only while `husk.profile` is absent, see Profile), `theme.name` (string, default the built-in palette's name, `CharmTone`; selects the named theme the palette is built from, and that theme's name becomes the served palette's `name`, see Theme; shown in the header line of `husk theme show` and in the workbench's bottom bar, [Workbench chrome](chrome.md)), `theme.allow_low_contrast` (bool, default false), one `theme.palette.<role>` (string) per palette role with the built-in colour as default, `husk.bundle` (string, default `""`, restart; honoured from the `user` and `instance` layers only, read before Store opens, [Bundles](bundles.md); present in any other layer it is a `warning` problem `bundle_setting_ignored` and ignored), `husk.plugins.converters` (strings, restart; Level 1, not landed: executable manifest converters, [Pods](pods.md) "Executable converters"), `husk.plugins.paths` and `husk.plugins.dirs` (strings, restart): the plugin arguments and directories the composition root converts at start, read once from the startup flight's Settings snapshot. Each entry is interpreted like the matching `--plugin` or `--plugin-dir` value (process cwd, no `~` expansion); an invalid value of either key, or an entry the root cannot honour, is fatal at start (Reload, the `startup` flight); the flags are the instance layer on top (see [Store](store.md), "Instance layer"). None is `public`.

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
| `settings.set` | `{"key":"theme.name","value":"evening"}` | `{"key":"theme.name","value":"evening","source":"user","reports":[...]}`, the value as written, the layer that supplies the key's value in the Settings snapshot serving once the flight ended (`Snapshot.Source`, as `settings.get` reports it; not `user` when a `profile` or `instance` layer sets the same key) and the five reports of the reload the write caused | `denied`; `not_found` `unknown_setting`; `invalid_params` (`invalid_value`, `null_value`); `unavailable` `store_error`; `timeout` `reload_deadline`; `cancelled` `shutdown` |

CLI: `husk settings get <key>`, `husk settings list [--source]`, `husk settings schema [--owner <owner>]`, `husk settings validate`, `husk settings set <key> <value>` (`<value>` is parsed as JSON; text that is not valid JSON is sent as a string, so `husk settings set theme.name evening` and `husk settings set husk.input.hold_alt true` both do what they look like, and a string setting whose text is `true` is written as `'"true"'`).

#### Writing one key

`settings.set` is the user-layer write API earlier versions of this spec left out, specified for exactly one key at a time. It is **general** (any registered key, any owner) and **narrow** (one key, one value, the `user` layer, no batch, no `profile` or `instance` layer, no removal). It exists because two frontend features need a setting to change without a text editor: the theme selector writes `theme.name` ("The theme selector"), and `profile.use --save` will write `profile.default`. A later `plugin.add --save` appending to `husk.plugins.paths` is a third user; it needs no new method, only a value of type `strings`.

Params: `key`, a fully qualified settings key; `value`, the JSON value to write, in the JSON form the schema type accepts (Schema table: a `duration` is an integral number of milliseconds, a `strings` is an array of strings). The sequence, in order, on the Server handler goroutine except where said:

1. **Authorize.** A `plugin` caller is `denied` with `data.reason` `user_only` before anything is read; an invalid caller is `denied` `invalid_caller`.
2. **Look the key up** in the current Settings snapshot's schema. No entry, in any namespace, including a plugin whose schema was not registered: `not_found` with `data.reason` `unknown_setting`. The method writes only keys the schema defines, so an unknown key can never be created through it (a stale or misspelled key is a file edit, and `settings.list` shows it as `unknown_key` afterwards).
3. **Validate the value** against the entry exactly as a rebuild would (type, `Enum`, `Min`/`Max`): a value that would be `invalid_value` at reload is `invalid_params` with `data.reason` `invalid_value` and `data.key`, and nothing is written, so a `settings.set` can never produce a Settings rebuild that fails on the key it wrote. A JSON `null` is `invalid_params` `null_value`: in Store's merge-patch rules `null` deletes, and removal is not in this method (open question 3).
4. **Write** through `Store.Patch("config", LayerUser, "", patch)` where `patch` is the one-key nested object the dotted key denotes, split at every dot (`theme.name` becomes `{"theme":{"name":"evening"}}`; a plugin key `plugins.acme.git.show_hidden` becomes `{"plugins":{"acme":{"git":{"show_hidden":true}}}}`). Settings flattens that nesting back to the same dotted key whatever dots the plugin id has, so the written key is the key the schema registered. Store takes the per-file lock, preserves comments and formatting elsewhere in the file, and writes atomically ([Store](store.md), "Layers" and invariant 1). A write Store refuses or that fails (the file or directory cannot be written, the existing file does not parse so a patch cannot be applied to it, a size limit) is `unavailable` with `data.reason` `store_error` and the message Store returned; nothing in memory changed and no reload runs. There is no partial state: either the file holds the new value or it is untouched.
5. **Reload.** Settings requests one flight with reason `settings` and registers the requester as its waiter by the coalescing rule (a new flight, the running flight if it has not captured, else the queued follow-up), exactly as `profile.use` does, so the flight that answers always read the file after the write. The reply is `{key, value, source, reports}` with the five reports; the three flight errors are the same as `config.reload`'s (`unavailable` `store_error`, `timeout` `reload_deadline`, `cancelled` `shutdown`). The write stands whatever the flight reports: a `settings` flight is an ordinary reload, and `theme.changed`, `services.reloaded`, the restart rule and the all-or-nothing rule apply to it unchanged.

The reply's reports are how the caller learns what the write did: `settings` `changed: true` means the value differs from before; a key with `apply: restart` is written and the report carries `restart_required` (the value in force stays, as for a hand edit); a `theme.name` naming no theme is written and the Theme report carries `unknown_theme`. The merged value a later `settings.get` returns may still differ from what was written when a `profile` or `instance` layer sets the same key: `settings.set` writes the user layer, it does not override the layers above it; the reply's `source` names the layer that wins, and `husk settings get <key>` tells, as it always has.

What is **not** in v1 and why: no batch (one write, one reload, one answer is the whole contract; a batch would need partial-failure semantics); no other layer (`profile` would need the profile name in params and `instance` has no file for a pid instance); no removal (`null` would need a "reset to default" story that `settings.get` can explain); no `settings.changed` event (the `services.reloaded` event already carries the Settings report with `changed`). Each is a minor addition when a user appears.

Test names: a plugin caller is denied before Store is touched; an unknown key writes nothing; an invalid value writes nothing and names the key; `null` is refused; the patch touches only the one key and keeps the file's comments; a Store write failure is `unavailable` `store_error` with no reload; the reply's reports come from a flight that captured after the write; two `settings.set` in one tick wait on one flight and each gets its reports; a `restart` key is written and reported `restart_required`; `theme.changed` is pushed once when `theme.name` changed the palette and not when the value was already in force.

### Theme

Theme is a function of the Settings snapshot it receives in `ReloadInput.Settings` and of the unmerged `themes` layers in `ReloadInput`: the one built-in palette, the named theme `theme.name` selects, then per-role overrides, checked against the contrast floor, producing one `Palette` ([Protocol](protocol.md) wire type; the same Go type the renderer uses, no copy).

There is exactly one built-in palette, named by its `name` field (`CharmTone`). A distribution or user adds colours in two ways: by defining **named themes** in the `themes` document, which the user picks between with `theme.name` (by editing the file, with `husk theme set`, or in the frontend's theme selector), and by overriding single roles with `theme.palette.<role>`, which apply on top of whichever theme is selected.

#### Named themes

A theme is a named set of role colours. Themes live in the layered Store document `themes` ([Store](store.md): `themes.json` in the user layer, `profiles/<name>/themes.json` in a profile, the distribution's `themes.json`, a named instance's file), read unmerged through `Store.Layers("themes", profile)` so that every theme has one source layer:

```json
{"version": 1, "themes": {
  "evening": {"palette": {"canvas": "#101018", "accent": "#8fa8ff", "ink": "#e8e8f0"}},
  "paper":   {"palette": {"canvas": "#f4f1ea", "ink": "#2a2a2a"}}
}}
```

Rules:

1. A theme name matches `[A-Za-z0-9_-]{1,32}` and is matched exactly (case-sensitive). The built-in palette's name is reserved: a layer defining a theme with that name gets `reserved_theme_name` and the definition is ignored.
2. `palette` holds any subset of the roles listed under Overrides, each `#RRGGBB`. A role the theme does not set keeps the built-in colour. A theme sets no `name`: its name is its key.
3. A theme is a **unit** across layers: a higher layer defining the same name replaces the lower definition whole (no per-role merge, so a user's `evening` is exactly what the user wrote). `theme.list` reports the winning layer as the theme's `source`.
4. A definition that does not validate (bad name, unknown role, a colour that is not `#RRGGBB`, `palette` not an object) is dropped with one `warning` problem `invalid_theme` (layer: the defining layer; `Key`: `themes.<name>`; message names the name and the first reason; `Source` names the file). The layer's other themes stand. A themes file that does not parse, or exceeds Store's limits, is a `store_error` like a config layer file: the whole reload fails (Reload step 2), not only the palette. A theme problem is never an `error`: a broken definition of a theme nobody selected must not block the palette.
5. `theme.name` selects. The empty string and the built-in name both select the built-in palette. A name no layer defines (or whose only definitions were dropped) is a `warning` problem `unknown_theme` (message `theme <name> does not exist; using <built-in>`, `Key` `theme.name`, attributed to the layer that set `theme.name`), and the built-in palette is used. The mirror of `unknown_profile`: the user asked for something that is not there, the host says so and keeps working.
6. At most **64** themes per layer; the 65th in document order is dropped with `too_many_themes` (warning).

Overrides are the settings keys `theme.palette.<role>`, one per role of `Palette` (the JSON tags: `accent`, `accent_secondary`, `accent_soft`, `border`, `border_focused`, `border_subtle`, `canvas`, `danger`, `info`, `ink`, `ink_muted`, `ink_subtle`, `slot_background`, `success`, `surface`, `surface_elevated`, `surface_hover`, `surface_selected`, `warning`), plus `theme.name` (string). A value is `#RRGGBB`, case-insensitive on input, emitted lowercase. `#RGB`, named colours and ANSI indices are rejected (`invalid_color`, error, message `role <role>: want #RRGGBB`) because contrast cannot be computed for a terminal-dependent colour.

```json
{"theme": {"name": "evening", "palette": {"accent": "#8fa8ff"}}}
```

Build, per rebuild:

1. Start from the built-in palette.
2. Resolve the named themes from the `themes` layers (rules 1 to 4 and 6 above), then select the theme `theme.name` names (rule 5) and apply its roles through `Overlay`. The built-in palette's `name` becomes the selected theme's name, so `theme.get` and `theme.changed` carry it.
3. Apply every `theme.palette.<role>` whose `Source` is not `default` (layer order is already resolved in the Settings snapshot) through `Overlay`, on top of the theme. An override `Overlay` rejects is the same `invalid_color` problem as above, keyed by that role's settings key; the roles are the fixed list, so `Overlay` has no other failure.
4. If `slot_background` was set neither by the theme nor by an override, derive it from the final `canvas` (`SlotBackground(canvas)`), so a new canvas drags the slot panel with it.
5. Check every pair in the contrast table (`ContrastPairs()`, production code shared with the built-in palette's test): text roles need **4.5:1** on every surface they are drawn on, borders and non-text indicators need **3:1** (WCAG 2.1 SC 1.4.3 and 1.4.11).
6. Each failing pair is a `low_contrast` problem whose message names the foreground role, the background role, the measured ratio and the minimum (`ink on surface is 2.10:1, need 4.5:1`). Its `Key` is the settings key of the attributed role (`theme.palette.ink`) when an override set that role, so the key is something `husk settings get` accepts; when the theme set it, `Key` is `themes.<name>.<role>`, a path in the `themes` document, and `Source` names that document's file. It is attributed (`Layer`, `Source`) to whoever set the foreground role (the override's layer, else the theme's layer); when the foreground is the built-in default, to whoever set the background role; when both are set, to the foreground's. (At least one is set, since the built-in palette passes.) A selected theme that fails the floor is therefore refused like a failing override: no candidate, the previous palette keeps serving, and `husk theme check` names the pairs.

**The contrast floor.** The Theme Service enforces it. When at least one pair fails:

- `theme.allow_low_contrast` is false (the default): the problems are `error`, no candidate is built, the previous palette keeps serving (the built-in palette at first load). The user sees the toast described under Reload and gets the full list from `husk theme check` or `husk config reload`.
- `theme.allow_low_contrast` is true and its `Source` is `user`, `profile` or `instance`: the problems are `warning`, the candidate is built and published.

Independently of whether any pair fails: if the `distribution` layer sets `theme.allow_low_contrast` to `true`, whether or not a stronger layer overrides it, Theme reports one `error` problem `distribution_opt_out` (message `distribution cannot opt out of contrast`), attributed to the distribution layer, and treats the opt-out as false. A distribution ships a palette it has checked; the unmerged `config` layers in `ReloadInput` are consulted so a user override cannot hide the distribution's attempt. A distribution value of `false` is the default and is not a problem.

There is no partial application: a palette with one failing override or theme does not publish the passing ones. The user fixes the file; `husk theme check` reproduces the exact list. The complete list of Theme codes is `invalid_color`, `low_contrast`, `distribution_opt_out`, `invalid_theme`, `reserved_theme_name`, `too_many_themes`, `unknown_theme` and the coordinator's `settings_unchanged`.

`theme.changed`. Theme's report sets `Changed` when its candidate was applied and the new palette differs from the one serving before. The Pods host pushes `theme.changed` with the new palette to every live plugin exactly when a `services.reloaded` event carries a report with `service` `theme` and `changed` true ([Pods](pods.md) owns the push; it reads the palette from `theme.get`'s backing snapshot, not from the event). A reload that republishes an identical palette (generation increases, values equal) pushes nothing. Every plugin also receives `theme.changed` as its first stdin message at launch.

Methods:

| Method | Params | Result | Errors |
| :--- | :--- | :--- | :--- |
| `theme.get` | `{}` | `{"palette":Palette}`, the serving palette; values are lowercase `#rrggbb` | none beyond the Server's |
| `theme.check` | `{}` | `{"ok":true,"problems":[...]}`, a dry build from the files on disk; `ok` is whether a reload now would publish a palette | `denied`; `timeout` `input_deadline`; `unavailable` |
| `theme.list` | `{}` | `{"themes":[{"name":"CharmTone","source":"built-in","current":false},{"name":"evening","source":"user","current":true}]}`: every theme the serving Theme snapshot resolved (the built-in first, then the named themes sorted by name); `source` is `built-in`, `distribution`, `user`, `profile` or `instance` (the winning layer, rule 3); exactly one entry has `current: true`, the theme the serving palette was built from, which is the built-in when `theme.name` was empty, the built-in name or unknown. Dropped definitions are not listed; the problems that dropped them are in the Theme report and `husk theme check` | `busy` `rate_limited` for a plugin |

`theme.list` reads the serving snapshot, like `theme.get`: it reflects the last published reload, not the files as they are now; `husk theme check` is the dry run.

CLI: `husk theme show` (the palette, from `theme.get`), `husk theme roles` (the role names), `husk theme check`, `husk theme list` (one row per theme: name, source, a marker on the current one), `husk theme set <name>` (`settings.set` of `theme.name` with the name as a string; prints the reports like `husk config reload`; exit 1 with the error when the method fails).

#### The theme selector

The selector is **core frontend chrome**, part of the TUI in the same way the command palette is: a host-owned modal, not a plugin and not a plugin surface. It is the one place the TUI changes a setting, and it uses only the two methods above through the frontend seam ([Pods](pods.md), "Frontend seams": `Themes()` is `theme.list`'s backing read and `SetSetting(key, value)` is `settings.set` with a `host` caller).

Opening. Two entry points, both running the host action `husk.theme.select` ([Keybindings](keybindings.md), host actions): a click on the theme pill at the right of the bottom bar ([Workbench chrome](chrome.md), which owns what the pill shows), and the action itself, from the palette or from any key a keymap layer binds to it. The built-in keymap binds no key to it (a new global key would take a spelling away from plugins and users, and the action is reachable from the palette); a user adds `{"key": "...", "action": "husk.theme.select"}` to a keymap layer. The action does nothing while the terminal is frozen, while a plugin's modal or runtime modal is open, or while the palette or the selector itself is open: its handler (registered through `Frontend.RegisterHostAction`) returns `contracts.ErrDeclined`, so a key bound to it goes to the occupant like any declined key and a `cli` `action.invoke` gets an empty success reply ([Keybindings](keybindings.md), the decline mechanism). The handler receives no caller kind, so it cannot answer a `cli` caller differently from a key. Opening records `overlays.theme_selector: true` in `husk.surfaces` ([State](state.md)).

Content. One list row per entry of `theme.list`, in its order, showing the name and the source (`built-in`, `user`, ...); the current theme is highlighted when the modal opens and the cursor starts on it. The modal's title is `Theme`; its footer shows `enter` select, `esc` cancel. Sizes and placement follow the modal placement rules of the frontend layout; the list scrolls when it is taller than the modal. It is a host popup like the palette: drawn after the `app-overlay` layer and the toast, with the same popup shadow ([Cell-buffer compositor](cell-buffer-compositor.md), "Overlay layer"), and never at the same time as the palette.

Keys. While the selector is open, `Resolve` is not called and the selector consumes every key, as the palette does ([Keybindings](keybindings.md), context filters). The closed set: `up`, `down`, `ctrl+k`, `ctrl+j`, `pgup`, `pgdown`, `home`, `end` move the cursor; `enter` selects; `esc` and `ctrl+c` cancel; `ctrl+c` closes the selector rather than quits, like the palette; every other key is ignored. The mouse: a click on a row moves the cursor there and a second click on the highlighted row selects; the wheel scrolls; a click outside the modal cancels.

Selecting. `enter` calls `SetSetting("theme.name", <name>)` and the modal shows the row as pending until the reply arrives (the reply is asynchronous: the write and the reload run off the loop, and the frontend never blocks). Then:

- The method replied with a result, the Theme report has `applied: true` and no problem with code `unknown_theme`, and the entry `Themes()` marks `current` is the chosen name: the modal closes. The palette, the bottom bar's theme name and every plugin (`theme.changed`) update through the ordinary reload path; the selector does nothing to the palette itself.
- The method replied with a result but the Theme report says `applied: false` (the theme fails the contrast floor, or Settings produced no candidate so Theme was skipped) or carries `unknown_theme`: the file now names the theme, but the palette did not change. The modal stays open and shows, under the list, the first `error` problem's message, else the first `warning`'s, prefixed by the code (`low_contrast: ink on surface is 2.10:1, need 4.5:1`); when the Theme report's only problem is `settings_unchanged`, the first problem of the Settings report is shown instead, so the cause (an `invalid_value` elsewhere in the configuration) is visible; then the line `written to the user layer; run husk theme check`. The user can pick another theme or cancel. Cancelling here does not undo the write: the selector writes settings, it does not manage them, and the one line tells the user where the value went.
- The method replied with a result and the Theme report has `applied: true` and no `unknown_theme`, but `Themes()` marks another theme `current`: a higher layer sets `theme.name`, so the write stands and changes nothing on screen. The modal stays open and shows under the list `set in <layer> layer, which wins`, where `<layer>` is the reply's `source` (`profile` or `instance`). The user can pick another theme or cancel; the write is not undone.
- The method replied with an error (`unavailable` `store_error` when the user layer cannot be written, `timeout`, `cancelled`, `invalid_params`, `not_found`): the modal stays open and shows `<code>: <message>` under the list. The highlight stays on the row the user chose so a retry is one `enter`.

While a reply is pending the selector accepts `esc` (cancel: the modal closes, the pending reply is dropped when it arrives, the write it may have made stands) and nothing else.

No live preview. In v1 moving the cursor changes nothing: the palette changes only on `enter`. A preview would publish a Theme generation per key press, and every generation whose palette differs pushes `theme.changed` to every live plugin and redraws every surface; a user scrolling through twenty themes would send twenty palettes to every plugin. A preview that stays inside the frontend (recolouring the chrome from a candidate palette without publishing) is possible later and is an open question.

Closing. `esc`, `ctrl+c`, a click outside, a successful selection, and the frozen-terminal transition each close the selector and clear `overlays.theme_selector`. A reload of any reason (including `cli` and another `settings` write) while the selector is open does not close it; the list is re-read from `Themes()` when the reload publishes, and the cursor stays on the same name when it still exists, else on the current theme.

Tests, each named for the behaviour it pins: the selector opens from the bottom-bar click and from the action, and not while a modal or the palette is open, where the action declines (a bound key reaches the occupant, `husk action invoke husk.theme.select` succeeds empty); the list is `theme.list`'s order with the current theme highlighted; `enter` writes exactly `theme.name` once with the chosen name and closes on an applied Theme report; a report with `applied: false` keeps the modal open and shows the first problem's code and message; an `unknown_theme` warning keeps it open; an applied write shadowed by a `profile` layer keeps it open with `set in profile layer, which wins`; a `store_error` reply keeps it open with the row highlighted; `esc` while pending closes and drops the reply; moving the cursor never calls `SetSetting`; the overlay flag is set while open and cleared on every close path; golden frames for the open selector and the error state.

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
2. A rebuild parses every layer, applies the loader rules of [Keybindings](keybindings.md), and reports every dropped or shadowed entry as a `protocol.Problem` whose `Code` is one of that spec's loader codes (its table is the complete list; this spec adds only `invalid_plugin_catalog`, "Snapshots, rebuilds and reports"); nothing disappears silently. `Source` is filled for every keymap problem: layer, config-relative file and line for config layers; `focus-local`, plugin and order for declared entries, with `Layer` `plugin:<id>`. Conversion problems Pods attached to a declared document (`global_not_allowed`, `foreign_action`, `too_many`, `cli_only_event`) are imported under layer `plugin:<id>` with the code and message conversion wrote; the Service itself raises `suggestion_ignored` for each entry of the document's `suggestions` array and, as the only key parser, `unparsable_key` for a focus-local spelling it cannot parse.
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

Both writes go through the loop post function; at execution the write re-checks that the current declaration's digest is the one the user saw and writes nothing otherwise. `secrets.grant` and `secrets.revoke` reply after their posted write has executed (the grant is in the slice when the CLI exits), not when it is queued. A `secrets.grant` whose re-check fails answers `not_found`, `data.reason` `not_declared`, whether the name is gone or only its digest changed since the request: either way the plugin does not currently declare what the user consented to, and a retry consents to the current declaration. A posted write that fails answers `unavailable`, `data.reason` `storage`, and nothing is granted or revoked; `storage` names any failure of persistence Secrets does not own, the encrypted namespace or the `husk.grants` slice. A write posted while the host is shutting down no longer runs: `cancelled`, `data.reason` `shutdown`.

#### Reading

`secrets.get` from a plugin:

1. The name is invalid or the plugin does not declare it: `not_found`, `data.reason` `not_declared`. The reply never confirms whether the secret exists for anyone else.
2. Declared, granted with the current digest: read the value from the encrypted namespace. Missing: `not_found`, `data.reason` `unset`. Namespace unreadable, or the stored value breaks the value rule below (not valid UTF-8, or over 64 KiB: written before the rule, or by another tool): `unavailable`, `data.reason` `storage`, for `secrets.get` and `secrets.reveal` alike; a new `secrets.set` replaces such a value. Otherwise the reply carries the value.
3. Declared, not granted, and this `(plugin, name, digest)` was **explicitly declined earlier in this instance's life**: `denied`, `data.reason` `user_denied`, no dialog. The decline memory is process memory (Level 0); it is cleared for that triple by a `secrets.grant` from the CLI and by a change of the declaration's digest.
4. Declared, not granted, no remembered decline: the read **waits** while Services ask the host to show the confirm dialog. The dialog leads with the **plugin id** (the verified identity); the plugin's display name, if its manifest has one, follows it and is marked as the plugin's own claim; then the secret name, the `purpose` and `required`. Outcomes:
   - The user accepts: the grant is written and the read continues at step 2. If that write fails, nothing is granted or remembered, the read gets `unavailable`, `data.reason` `storage`, and the next read asks again.
   - The user declines: `denied`, `data.reason` `user_denied`, and the decline is **remembered** for this instance's life.
   - The dialog is dismissed without an answer, 60 s pass, or no dialog is wired (a headless host): `denied`, `data.reason` `user_denied`, and **nothing is remembered**; the next read asks again. An unattended prompt is not a decision.
   - A `secrets.grant` from the CLI for this `(plugin, name)` arrives while the dialog is pending or shown: the dialog closes, the read continues at step 2, any memory for the triple is cleared.
   - The plugin's declaration changes digest while the dialog is pending or shown (a `plugin` reload re-declared it): the dialog closes, the read gets `denied` `user_denied`, nothing is remembered, and the next read prompts with the new `purpose`.
   - The plugin's Pod leaves `live`, or the requesting connection closes, while the read waits: the dialog closes if shown, queued prompts of that plugin are dropped, the read is dropped, nothing is remembered. A read dropped because its Pod left `live` answers `cancelled`, `data.reason` `plugin_exited`, if its connection is still open to receive it. Host shutdown drops a waiting read the same way, with `cancelled`, `data.reason` `shutdown`, and a read that arrives after shutdown began gets the same answer without waiting. A dropped read publishes no `secrets.read` event: the event records an answer, and a dropped read has none.

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

Secrets publish `secrets.read` for every `secrets.get` and `secrets.reveal` that is answered (a dropped read, "Reading" step 4, publishes none): `{"name":"git.token","plugin_id":"git","outcome":"allowed"}`, with `plugin_id` empty for a `cli` caller and `outcome` one of `allowed`, `revealed`, `not_declared`, `unset`, `user_denied`, `dialog_pending`, `unavailable`. It is a core event ([Message kinds](message-kinds.md)) and is delivered to `cli` watchers only, never to a plugin subscriber: one plugin has no business learning which secrets another reads. The same restriction applies to `services.reloaded`, whose reports name other plugins' keys and files. The mechanism is [Message kinds](message-kinds.md)'s: manifest conversion rejects a subscription pattern that matches `secrets.` or `services.` (the plugin's subscriptions are dropped and reported, like a malformed pattern), and `event.watch` from a `plugin` caller answers `denied` for such a pattern. Services only publish.

#### Methods

| Method | Params | Result | Errors |
| :--- | :--- | :--- | :--- |
| `secrets.get` | `{"name":"git.token"}` | `{"name":"git.token","value":"..."}` | plugin: `not_found` (`not_declared`, `unset`), `denied` (`user_denied`), `busy` (`dialog_pending`, `rate_limited`), `unavailable` (`storage`), `cancelled` (`plugin_exited`, `shutdown`); `host`: `denied` (`user_only`); `cli`: as `secrets.reveal` |
| `secrets.names` | `{}` | `{"storage":"ok","secrets":[{"name":"git.token","declared":["git"],"granted":["git"],"set":true,"required":true}]}` sorted by name; `storage` is `ok` or `unavailable`; a `cli` caller also sees stored names nobody declares; a plugin sees only names it declares, with `declared` and `granted` reduced to itself and `set` present only for names it is granted (an ungranted plugin must not learn whether a value exists) | `busy` (`rate_limited`) for a plugin |
| `secrets.set` | `{"name":"git.token","value":"..."}` | `{}` | `denied`, `invalid_params` (`invalid_secret_name`, `not_utf8`, `secret_limit`), `unavailable` (`storage`) |
| `secrets.grant` | `{"plugin":"git","name":"git.token"}` | `{}` after the posted write executed | `denied`, `not_found` (`not_declared`: the plugin does not currently declare the name, or its declaration changed before the posted write ran), `unavailable` (`storage`: the write failed), `cancelled` (`shutdown`) |
| `secrets.revoke` | `{"plugin":"git","name":"git.token"}` | `{}` after the posted write executed; revoking a grant that does not exist succeeds | `denied`, `unavailable` (`storage`: the write failed), `cancelled` (`shutdown`) |
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
| Grant dialog | 60 s from when it is shown; one shown at a time per instance; one pending per plugin | An unattended prompt must not hold a plugin forever; a timeout is not a decline and is not remembered |
| Named themes per layer | 64 | Bounds the selector's list and the per-layer document; a distribution that ships more has a catalog, not a theme set |
| Theme name | 1 to 32 characters of `[A-Za-z0-9_-]` | Fits the bottom bar and a modal row; the same alphabet as a surface name, with case because the built-in palette's name has it |
| Plugin calls to any Services method (`settings.get`, `secrets.get`, `secrets.names`, `theme.get`, `theme.list`, `keys.list`) | 20 requests per second per plugin, one shared budget, fixed one-second window | A plugin polling in a loop cannot starve the loop; enforced by the Server before the Service is called; refused with `busy`, `data.reason` `rate_limited`; malformed requests count |
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
16. The only configuration write Services make is `settings.set`: one registered key, one validated value, the `user` layer, through `Store.Patch`, followed by a reload. It never writes another layer, an unregistered key, an invalid value or `null`, and it never changes a snapshot directly; the reload does.
17. The serving palette's `name` is the name of the theme it was built from; `theme.list` marks exactly that theme `current`.

### Failure modes

| Condition | Detected by | What happens | What the user or plugin sees |
| :--- | :--- | :--- | :--- |
| A known settings key has an invalid value | Settings rebuild | No Settings candidate; old snapshot serves; Theme is skipped (`settings_unchanged`) and keeps its palette. On the `startup` flight, fatal when the key is `husk.plugins.paths` or `husk.plugins.dirs` | `config.reload` prints the problem with layer and key; a toast on a reload after startup the user did not start, or the startup toast |
| Unknown settings key | Settings rebuild | Warning; raw value kept for `settings.list`; everything else applies | Listed by `husk settings validate` and in the reload report; no toast |
| A plugin's manifest `settings` schema is invalid | Settings rebuild | Warning per entry; none of that plugin's entries registered; its keys become `unknown_key`; everything else applies | In the reload report under `plugin:<id>`; the plugin's `settings.get` answers `not_found`; no toast |
| The host or distribution schema is invalid | Services construction | The process does not start | Startup error naming the entry |
| Palette override fails contrast, opt-out off | Theme rebuild | No Theme candidate; old palette serves | Every failing pair with ratio and minimum; `husk theme check` reproduces it |
| Palette override fails contrast, opt-out on from `user`/`profile`/`instance` | Theme rebuild | Warnings; palette published | Warnings in the report; no toast |
| `theme.allow_low_contrast` is `true` in the distribution layer | Theme rebuild | Error whether or not any pair fails; opt-out ignored | `distribution_opt_out` |
| Override is not `#RRGGBB` | Theme rebuild | No Theme candidate | `invalid_color`, `role <role>: want #RRGGBB` |
| A named theme's definition is invalid, uses the built-in name, or is past the 64th | Theme rebuild | That definition dropped; everything else applies | `invalid_theme` / `reserved_theme_name` / `too_many_themes` warning; the theme is missing from `husk theme list`; no toast |
| `theme.name` names no theme | Theme rebuild | Built-in palette used; candidate published | `unknown_theme` warning; the selector shows it and stays open; `husk theme list` marks the built-in current |
| Selected theme fails contrast, opt-out off | Theme rebuild | No Theme candidate; old palette serves; the user-layer value stands | `low_contrast` with `Key` `themes.<name>.<role>`; the selector shows the first problem and stays open; `husk theme check` reproduces it |
| `settings.set` of an unknown key or an invalid value | Settings, before the write | Nothing written; no reload | `not_found` `unknown_setting` / `invalid_params` `invalid_value` naming the key |
| `settings.set` cannot write the user layer (permissions, disk full, unparsable file) | Store `Patch` | Nothing written; no reload | `unavailable` `store_error` with Store's message; the selector shows it and stays open |
| `settings.set` from a plugin | Server, Services | `denied` `user_only` before Store is touched | The plugin gets the error |
| A keymap layer fails to load on reload | Keybinding rebuild | That layer's last good document is used; rebuild publishes | Problem naming file and line; `husk keys --problems` |
| Keymap entry invalid, duplicate, unreachable, over a cap | Keybinding rebuild | Entry dropped or shadowed; rebuild publishes | Problem per entry ([Keybindings](keybindings.md) codes) |
| Binding names an unknown action | Dispatcher, at dispatch | The key resolves; dispatch fails `not_found` | Toast naming the action; `husk keys` state `unknown_action` |
| `--profile` or `profile.default` names no profile | Profile | No profile; reload proceeds | Warning problem; one startup toast |
| Active profile's directory deleted | Profile rebuild | Slice reset to `""`; one follow-up `profile` reload | Warning toast |
| Store cannot resolve `config` (parse error, over limit) | Coordinator, step 2 | No Service rebuilds; every snapshot stays; every report carries `store_error` | After startup: `config.reload` returns `unavailable` `store_error` naming the config-relative file, and a toast when the user did not start the reload. On the `startup` flight: fatal, see the next row |
| The `startup` flight fails: a report carries `store_error`, `timed_out` or `cancelled`, or the flight returns an error with no reports | Coordinator; the composition root | Construction aborts; the process does not start | Startup error carrying the problem diagnostics (code, message, layer, file) |
| Settings reports `invalid_value` on `husk.plugins.paths` or `husk.plugins.dirs` on the `startup` flight, or an entry of either fails to load or is not a readable directory | Settings rebuild; the composition root | Construction aborts; the process does not start | Startup error naming the key and the layer |
| Any other Service `error` on the `startup` flight | That Service's rebuild | That Service keeps its built-in snapshot; the process starts. For Settings every key serves its default, `husk.plugins.paths` and `husk.plugins.dirs` included, so no configured plugin launches | One startup toast with the error count |
| Reload exceeds 2 s | Coordinator | Published candidates stay; remaining Services report `timed_out`; late candidates never applied; on the `startup` flight, fatal | Report rows with `applied:false`, `timed_out`; `config.reload` returns `timeout` `reload_deadline` |
| Shutdown during a flight | Coordinator `Close` | Remaining Services report `cancelled`; waiters released; on the `startup` flight, fatal | `config.reload`/`profile.use` return `cancelled` `shutdown` |
| Plugin reads an undeclared secret | Secrets | `not_found`, `not_declared` | The plugin gets the error; nothing shown to the user |
| Plugin reads a declared, ungranted secret | Secrets | Read waits; dialog opens | Dialog with plugin id, name, purpose; the plugin's reply is the value or `denied` |
| Dialog declined by the user | Secrets | `denied`, `user_denied`; remembered for this instance | Later reads get it without a dialog; `husk secrets grant` clears it |
| Dialog dismissed, 60 s elapsed, or no dialog wired | Secrets | `denied`, `user_denied`; nothing remembered | The next read prompts again |
| Plugin reads a second ungranted name while its dialog is pending | Secrets | `busy`, `dialog_pending` | The plugin retries after the first read answers |
| Plugin declares more than 16 secrets or a bad `purpose` | Secrets rebuild | Declaration dropped with a warning | `too_many_secrets` / `invalid_purpose` in the report; no toast |
| Declaration `purpose` changed since the grant | Secrets digest check | Grant void | Next read prompts again |
| Encrypted namespace unreadable, a stored value that is not UTF-8 text within 64 KiB, or a failed `husk.grants` write | Store crypto, State | `unavailable`, `storage`; other Services unaffected | Plugin gets the error; `husk secrets list` shows `storage: unavailable`; Store's diagnostic suggests `husk store reset-secrets` |
| Plugin exceeds 20 Services calls per second | Server | `busy`, `rate_limited` | The plugin gets the error |
| Plugin calls a user-only method | Server, Services | `denied`, `user_only` | The plugin gets the error |
| Invalid caller reaches a Service | Services | `denied`, `invalid_caller` | A composition bug; logged |
| A plugin's declared document does not decode, or its `id` is not its Store source | Settings, Keybinding, Secrets rebuild | That plugin's declarations are ignored by the reporting Service; everything else applies | `invalid_plugin_catalog` warning under `plugin:<id>`; no toast |
| `Apply` reached after the deadline, after `Close`, or after the generation moved | the candidate's `Apply` | Nothing swapped; old snapshot serves | `applied:false`, `candidate_superseded`; never in a healthy flight |

### Code layout

Services live under one import root as `services/{profile,settings,theme,keybinding,secrets}` plus the coordinator in `services`. Shared value types the children and the coordinator both need (`Generation`, `Report`, `ReloadInput`, `Reloadable`, `Candidate`, the settings `Entry` and the declared-document types) live in one leaf package the children import, so no child imports its parent. The caller identity and the diagnostic are not among them: they are `protocol.Caller`, `protocol.Problem` and `protocol.Source` ([Protocol](protocol.md)), because problems cross the wire (`keys.list`, the reload reports) and the host contracts that carry them must not import Services; the Services `Caller`, `Problem` and `Source` structs are deleted. The embedded `husk` keymap sits beside the Keybinding Service. `pkg/theme` keeps `Palette`, `Default()`, `Overlay`, `SlotBackground`, `ContrastPairs` and `CheckContrast`, so the runtime check and the built-in palette's test share one rule.

## Level 2: Status against the code

The five Services and the coordinator exist. Items below are where the code on `main` and this spec differ; each is small and self-contained.

Landed with the Theme and reload work: `Report.Changed` per Service; the `theme.changed` push to live Pods when the Theme report says `changed` (the palette is read from Theme's serving snapshot, not the event); the startup/profile/plugin error toast (the host records a notice from `services.reloaded` and the workbench polls `Frontend.LatestConfigNotice`, so it stays out of the Surfaces toast queue; `cli` reloads and warnings-only reloads show none); coordinator failures as `unavailable`/`store_error`, `timeout`/`reload_deadline` and `cancelled`/`shutdown`, with a Service that kept its snapshot being a success reply and `Close` reporting `cancelled`; `Apply` after the deadline, after `Close` or after the generation moved being a no-op with one `candidate_superseded`; dry runs taking the coordinator's turn under a 2 s `input_deadline`, with `theme.check` dry-building Settings first; the distribution opt-out for `true` only and `low_contrast` attribution; one 20/s budget over `settings.get`, `secrets.get`, `secrets.names`, `theme.get` and `keys.list`; and `secrets.*`/`services.*` events delivered to `cli` watchers only (a plugin's `event.watch` is `denied`, and a manifest subscription to one is dropped at conversion with a `cli_only_event` problem on the plugin's declared document). Remaining gaps in that area: `Source.Line` is never filled, and `Source.File` is empty for a layer with no file (distribution).

- Settings exposure: landed. `Entry.Public` exists, visibility is exact ownership plus `public`, and the built-ins mark none public.
- Settings problems: landed. Every unknown key is a warning with the raw value retained; a plugin schema problem is a warning against `plugin:<id>` that unregisters that plugin's entries; a bad host or distribution schema makes `settings.New` fail. `Source.File` is the config-relative name Store reports.
- Profile selection: landed. The coordinator selects in step 2 from the `--profile` flag (startup flight only), the `husk.profile` slice and the unmerged `user` layer, resolves once, and writes a choice back to the slice; the host runs the startup flight once before the first plugin launch. An absent slice falls through to `profile.default`; `{"active":""}` is an explicit no profile, and `profile.default` selections are not written back.
- Reply shapes: landed. `settings.get` answers `not_found`/`unknown_setting` at the Service for every caller; `settings.list` is `{"entries":[{key,value,source}]}`, `settings.schema` is `{"entries":[...]}`, `profile.list` is `{"profiles":[...]}`, and `settings.validate` and `theme.check` reply `{"ok","problems"}`.
- Secrets matches the Level 1 rules above: `secrets.get` blocks on one queued confirm dialog (the host wires it through `host.WithPrompt`; a host without one answers `user_denied` and remembers nothing), an explicit decline is remembered per `(plugin, name, digest)`, a `cli` `secrets.get` and `secrets.reveal` share one audited path (`secrets.read` event plus the `secrets: <name> revealed to cli` log line), values are UTF-8 text with the 64 KiB cap, `secrets.names` returns `storage` and `set` as specified, declarations are capped at 16 with `purpose` validated, and `secrets.grant`/`secrets.revoke` reply after the posted write ran. The host tells Secrets when a Pod leaves `live` (`Service.DropPlugin`, from the same `husk.pods` flush listener that tells Surfaces), and a write posted once the loop stopped accepting posts answers `cancelled`/`shutdown`.
- Manifest conversion still rejects a whole manifest for an invalid secret name, so `invalid_secret_name` only fires for a catalog that reaches Services by another route.
- `profile.use`: landed. It writes the slice on the loop and replies `{"active","reports"}` with the reports of the flight that applies the write; when the coordinator is already settled on that name (no flight pending, last selection and active profile both equal to it) it replies at once with the last flight's reports and nothing reloads.
- `profile.default` in a layer other than `user` is reported as `profile_default_ignored`: landed.
- The `husk settings|theme|profile|secrets|config` subcommands do not exist yet; the Server handlers do.
- Named themes, `theme.list`, `settings.set` and the theme selector are specified and not landed: the Theme Service reads no `themes` document, `theme.name` is a free string copied into the palette's `name`, `SettingsPort` has no `Set`, `ThemePort` has no `List`, and the frontend draws the theme name as a pill in the top bar (`renderTopWorkbenchBar`; [Workbench chrome](chrome.md) moves it to the bottom bar) with no selector behind it. Protocol minor 2.3 ([Protocol](protocol.md)) lands with them.
- The Keybinding Service ranks layer before scope; [Keybindings](keybindings.md) ranks scope first.

## Decisions made here

1. Five Services with one shared rebuild shape: immutable generation-numbered snapshots, all-or-nothing per Service, one coordinator that serializes and coalesces reloads and publishes one `services.reloaded`. Dry runs take the coordinator's turn so they may invalidate Store safely.
2. Reload order Profile, Settings, Theme, Keybinding, Secrets; Theme reads the Settings snapshot the same reload published (`ReloadInput.Settings`) and is skipped with a `settings_unchanged` warning when Settings failed; Keybinding and Secrets are independent of Settings. A late candidate is never applied.
3. The diagnostic is `protocol.Problem` ([Protocol](protocol.md), "Problem"): it carries a stable `Code` and, when known, a `protocol.Source`; every code is listed in this spec or in [Keybindings](keybindings.md); `keys.list`'s problem object is this type. Tests and tools match on codes; messages are prose.
4. Profile selection runs in the coordinator before the one `Store.Resolve` and reads `profile.default` from the unmerged `user` layer; a `--profile` choice (and a vanished profile's reset to `""`) is written back to `husk.profile` by the flight that made it, a `profile.default` selection is not, and the slice listener ignores that write. `profile.use` writes State only, waits on the flight its write triggers (correlated by registration at write time) and replies with that flight's reports; it replies at once, without a reload, only when the coordinator is settled on that name (no flight pending, last selection and active profile both equal to it), so a `profile.default` selection is recorded without a reload and a repeated name during a flight waits on it.
5. Settings namespaces have exactly one owner each; visibility is by declaration ownership plus an explicit `public` flag on host and distribution entries. **No host settings key is readable by plugins today**; a schema entry marked `public: true` is the mechanism when a need appears, and nothing else makes a host key visible. One method writes a setting, `settings.set` (decision 19).
6. **An unknown settings key is a warning**, kept raw for the user and never shown to plugins; an invalid value of a known key is an error, so nothing half-applies. Errors for unknown keys would let a stale key from a removed plugin or a typo freeze every setting. A plugin's schema error is a warning that unregisters only that plugin's entries; a host or distribution schema error fails construction, never a reload.
7. One built-in palette, `#RRGGBB` overrides per role, the contrast floor enforced by Theme at 4.5:1 text and 3:1 non-text, no partial application: a theme that misses the floor is not applied at all, the user gets one toast, `husk theme check` reports the failing pairs. `theme.allow_low_contrast` demotes to warnings from non-distribution layers only; `true` in the distribution layer is an error whatever the contrast outcome. Closed.
   Named themes (added with the selector) sit between the built-in palette and the role overrides: a theme is a named subset of roles in the layered `themes` document, replaced whole by a higher layer, selected by `theme.name`, with the overrides applied on top so a user's `theme.palette.accent` survives a theme change. A broken or unknown theme is a warning and falls back to the built-in; a selected theme that fails the floor is refused exactly like a failing override. The built-in palette's name is the reserved name of the built-in theme, so `theme.name`'s default needs no new value.
8. The contrast table is production code shared by the runtime check and the built-in palette's test.
9. `Report.Changed` tells Pods when to push `theme.changed`: on a published palette that differs, not on every reload.
10. Keymap policy, ranking included (scope first, then layer, then the `plugin` condition, then file order), tombstones and context filters live in [Keybindings](keybindings.md); this spec owns only the Service mechanics, the last-good-layer rule, the 4096 cap and the per-caller `keys.list` view. **The declared focus-local document is named `keymap`**, the same name as the layered keymap document; the two are told apart by the Store method (`Declared` versus `Layers`), and every spec that mentions one says which it means.
11. Secret names are global to the user, grants are per `(plugin, name)` and bound to the declaration digest; grants live in the persistent user-scope `husk.grants` slice; values live only in Store's encrypted namespace. A plugin declares at most 16 secrets and its `purpose` text is validated and never echoed in a problem.
12. **An ungranted secret read blocks on the grant dialog** (60 s, one shown at a time per instance, one pending per plugin) instead of returning a retry code, which would need a polling interval nobody specified and would spend rate budget. An explicit decline is remembered for the life of the instance; a timeout, a dismissal or a headless host is not a decision and is not remembered.
13. **Grants survive `husk plugin remove`.** They are the user's consent record, not plugin state; `husk secrets revoke` is the explicit way to drop one.
14. Only a `cli` caller or the accepted dialog writes a grant; **the `host` kind never handles a secret value** (the confirm dialog is its only secret role, the same least-privilege line that keeps the Stem out of `state.clear`); every value handed to a human goes through one handler (`secrets.reveal`, aliased by `secrets.get` from the CLI) and is audited through `secrets.read` and a host log line. There is no audit file: a single-user local tool has no consumer for one.
15. Secret values are UTF-8 text up to 64 KiB; the empty string is a value.
16. `secrets.read` and `services.reloaded` reach `cli` watchers only; conversion and `event.watch` refuse plugin subscriptions to them.
17. Plugins reach settings, secrets, the palette and their own keys through Server methods under one 20 requests per second per plugin budget covering every Services method; nothing arrives as environment values.
18. The caller identity is `protocol.Caller`, one type with four kinds (`cli`, `plugin`, `input`, `host`) and the Server's `ConnID`; Services treat `input` as `host`.
19. **`settings.set` is the user-layer write API, one key at a time.** It is general (any registered key) and narrow (one key, one validated value, the `user` layer, `cli` and `host` callers, plugins denied), goes through `Store.Patch` so the file keeps its comments, and reloads through the coordinator with reason `settings` so `theme.changed` and `services.reloaded` fire exactly as for a hand edit followed by `config.reload`. No batch, no other layer, no removal in v1; `profile.use --save` and a future `plugin.add --save` compose it rather than adding writers.
20. **The theme selector is core frontend chrome**, a host-owned modal like the palette, opened by the bottom-bar click and by `husk.theme.select` (unbound by default), applying on `enter`, cancelling on `esc`, with no live preview in v1 because a preview would publish one Theme generation and one `theme.changed` broadcast per key press. The choice persists because it is a user-layer write; an instance-only pick is out of scope. A refused write or an unapplied palette keeps the modal open with the code and message.
21. **A pick a higher layer shadows keeps the selector open.** `settings.set` writes the user layer only, so when a `profile` or `instance` layer sets `theme.name` the write succeeds and the palette does not change; the selector compares `Themes()`' current entry with the chosen name and says `set in <layer> layer, which wins` (the layer from the reply's `source`) instead of closing as if the pick had worked.
22. **The selector takes the mouse; the palette does not.** A click on a row moves the cursor there, a click on the highlighted row selects, the wheel scrolls the list, and a click outside the modal cancels. A list of named rows is a pointing task; the palette's query-driven list stays keyboard-only.

## Open questions

1. **Live reload on file change.** Not in this spec: reload runs on `config.reload`, a profile change, a plugin added or removed, and a `settings.set`. File watching, if wanted, belongs to Store sources calling the coordinator, in the Store spec.
2. **Persisting the chosen profile (`husk profile use --save`).** The write API now exists (`settings.set`, decision 19); `--save` is a small later change: `profile.use` followed by `settings.set` of `profile.default`, in one CLI command. Not specified here so the selector's spec stays one feature.
3. **Resetting a key through `settings.set`.** `null` is refused in v1. A `settings.reset` (or `value: null` meaning "remove from the user layer") would need to say what `settings.get` reports afterwards (the default, or a lower layer's value) and whether the selector offers "use the default theme" as a removal or as writing the built-in name (this spec: writing the name). Recommended: a separate `settings.reset` method when a user appears, so `set` keeps meaning "this value".
4. **Frontend-only theme preview.** Recolouring the chrome from a candidate palette while the selector's cursor moves, without publishing a generation, would give a preview without the per-keypress broadcast. It needs a Theme dry-build API that returns a palette for a name, and the frontend would draw plugin surfaces in the old palette until `enter`. Deferred until the selector exists.
