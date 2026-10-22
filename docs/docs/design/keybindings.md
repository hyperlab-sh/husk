---
type: Design Spec
title: "Keybinding policy and config layers"
description: "Who may bind keys in Husk, which layers a keymap comes from, how conflicts resolve, the keymap file shape, and what husk keys reports."
status: planned
generated: { by: claude/sonnet-5-5, at: 2026-10-22T06:00:00Z }
tags: [architecture-v2, services]
---

# Keybinding policy and config layers

## Level 0: What and why

This spec fixes the rules for key bindings: who may declare one, where the effective keymap comes from, which binding wins when two claim the same key, and what the user sees when they ask. The Keybinding service mechanics (the Go type that caches and answers lookups, reload, invalidation) belong to [Services](services.md). This spec is the policy that service implements.

The policy in five lines:

1. A plugin declares **actions**. It never declares a global binding.
2. A plugin may declare **focus-local** bindings in its manifest. They are active only while one of its surfaces has focus, and they sit below the distribution, user and profile layers.
3. **Global** bindings come only from config layers: `distribution`, `user`, `profile`.
4. The host hard-codes no workbench key. Today's keys move to a built-in `husk` keymap file that sits below every other layer, so a real distribution replaces them.
5. Husk resolves no plugin-versus-plugin conflict, because plugins cannot create one. Users are deliberate about bindings; the extra work of writing a binding is the point. A command that copies a plugin-suggested keymap into the user layer (`husk keys adopt`) is rejected on purpose.

What this is not:

- Not the Keybinding service implementation. Caching, reload wiring and the method set are in [Services](services.md).
- Not config layer merging. [Store](store.md) merges `distribution < user < profile` generically; this spec says what a keymap layer contains and how its entries compete.
- Not key decoding. Terminal escape handling stays in the Input module of the Stem; this spec starts at the normalized key spelling.
- Not the action catalog. Which actions exist, and who owns them, comes from manifests via [Pods](pods.md) and is routed by the Dispatcher in [Message kinds](message-kinds.md).

Dependencies:

- Depends on [Store](store.md) for the resolved `distribution`, `user` and `profile` documents, on [Pods](pods.md) (manifest conversion) for the focus-local layer, and on [Services](services.md) for the service that hosts this policy.
- Depended on by Input (resolves a key press to an action id), by the footer and rail (hint text), by the palette (shortcut column), and by the CLI through the Server (`husk keys`, see [Instances and the CLI](instances.md)).

## Level 1: Contract

### Layers and precedence

Five sources feed one resolved keymap. Rank 4 is strongest.

| Rank | Layer | Source | Scope of its bindings |
|---|---|---|---|
| 0 | `husk` | Built-in keymap file embedded in the binary | global and focus |
| 1 | `focus-local` | Plugin manifests, converted by the Pod | focus only, implicitly bound to the declaring plugin |
| 2 | `distribution` | Keymap file shipped by an application or bundle built on Husk | global and focus |
| 3 | `user` | User config directory | global and focus |
| 4 | `profile` | Active profile directory | global and focus |

Rules:

- **Layer first.** When several bindings match a key press, the highest layer wins. Within one layer, the more specific scope wins (plugin-qualified focus, then focus, then global). Within one layer and one scope, the later entry in the file wins and the loader reports a `duplicate` problem.
- **Configuration always beats a plugin.** A distribution, user or profile binding beats a focus-local binding on the same key, whatever the scope. A plugin can never take a key a person or an application chose.
- **The `husk` layer is the weakest default.** A plugin's focus-local binding beats a built-in host key while its surface has focus. A distribution replaces the host keymap by overriding or disabling entries, or by dropping the layer (see `builtin` below).
- **Disable is exact.** A `disable` entry removes the lower-layer entries with the same `(key, focus, plugin)` triple and nothing else. A global disable of `[` does not remove a plugin's focus-local `[`.
- **A disabled or unbound key falls through.** If the winning candidate is a disable, or no candidate exists, the key goes to the focused surface's occupant as `slot_key`, exactly as an unbound key does today.
- **No instance layer.** The Store `instance` layer (run flags) does not feed keymaps in v1.
- **One distribution layer in v1.** Store takes a single distribution source (`Options.Distribution`, [Store](store.md)); an application that bundles plugins ships one merged distribution keymap. Several distribution layers ranked by registration order would be an additive change to Store and to this spec, not to the rank table.

### Key spelling and sequences

Keys use the existing `slot_key` spelling and match exactly: `f4`, `ctrl+g`, `alt+2`, `n`, `esc`, `tab`. The loader normalizes `space` to the single space the wire uses and rejects any key it cannot normalize with an `invalid_key` problem.

A binding names one key with `key` or a sequence of up to 4 keys with `keys: [...]` (`"keys": ["ctrl+k", "ctrl+s"]`); exactly one of the two is present. A sequence is never written as one space-separated string, because `" "` is itself a key. Input keeps the chord state and asks the service after each key. Rules:

- A sequence times out after 1500 ms without the next key. The partial input is dropped and the pending keys are not replayed to the occupant.
- If a binding's sequence is a strict prefix of another binding's sequence in the same scope and both survive precedence, the longer one is unreachable. The loader drops it and reports `unreachable`.
- `esc` while a sequence is pending cancels it and is consumed.

### Keymap file shape

Format: **HuJSON** (JSON with comments and trailing commas), one file per layer, named `keymap.json`. It is the Store document `keymap`, read with the same parser as every other layer document ([Store](store.md), decision 1), so a keymap can carry hand-written comments without a second config format in the binary. The plugin manifest uses the same syntax.

```jsonc
// keymap.json
{
  "version": 1,            // required; the only accepted value is 1
  // "builtin": false,     // distribution layers only: drop the husk layer entirely
  "bind": [
    {
      "key": "ctrl+g",       // one key; or "keys": [...] for a sequence
      "action": "notes.open", // required unless "disable": true
      "hint": "Notes",        // optional footer text, at most 40 runes
      // "focus": "...",      // omitted: global
      // "plugin": "...",     // optional, with focus: only while this plugin occupies the surface
      // "args": { ... },     // optional object passed to the action as params
      // "disable": true,     // optional: remove the same (keys, focus, plugin) from lower layers
    },
  ],
}
```

Fields:

| Field | Type | Required | Meaning |
|---|---|---|---|
| `version` | integer | yes | Schema version. Unknown values reject the file. |
| `builtin` | bool | no | `false` in a distribution layer removes the `husk` layer. Ignored with a `problem` in any other layer. |
| `bind` | array | yes | The entries, in order. |
| `key` | string | one of `key`/`keys` | A single key. |
| `keys` | array of string | one of `key`/`keys` | A sequence of 2 to 4 keys. |
| `action` | string | unless `disable` | An action id, `<plugin_id>.<name>` or a `husk.*` host action. |
| `focus` | string | no | A surface id. Omitted means global. |
| `plugin` | string | no | Narrows `focus` to the surface while this plugin occupies it. Invalid without `focus`. |
| `args` | object | no | Params for the action. Strings, numbers and booleans only. |
| `hint` | string | no | Short label the footer shows. Without it the binding is not advertised. |
| `disable` | bool | no | Tombstone. `action`, `args` and `hint` must be absent. |

Examples (the surrounding `{"version": 1, "bind": [...]}` omitted).

A global binding in the user layer:

```json
{"key": "ctrl+g", "action": "notes.open", "hint": "Notes"}
```

A focus-local override. The plugin `files` declared `n` for `files.new` on its `sidebar-list` surface; the user rebinds that surface's `n` to a different action and gives `files.new` a new key:

```json
{"key": "n", "focus": "sidebar-list", "plugin": "files", "action": "files.new_dir"},
{"key": "ctrl+n", "focus": "sidebar-list", "plugin": "files", "action": "files.new"}
```

A disable. The distribution binds `[` globally; this user wants `[` to reach plugins:

```json
{"key": "[", "disable": true}
```

A focus-local disable, which removes only the plugin's own binding:

```json
{"key": "r", "focus": "sidebar-list", "plugin": "files", "disable": true}
```

A chord with parameters:

```json
{"keys": ["ctrl+k", "ctrl+o"], "action": "notes.open", "args": {"path": "todo.md"}}
```

### Layer locations

| Layer | Where | Registered by |
|---|---|---|
| `husk` | Embedded in the binary (`keymap.json` beside the Keybinding service source) | Husk |
| `distribution` | Whatever file or embedded filesystem the embedding application registers with Store | The application |
| `user` | `keymap.json` in the user config directory (`$XDG_CONFIG_HOME/husk`, default `~/.config/husk`) | The user |
| `profile` | `keymap.json` in the active profile's directory (`profiles/<name>/`) under the user config directory | The user, selected by the Profile service |
| `focus-local` | `keybindings` array in the plugin manifest | The plugin author |

Store owns file reading; the Keybinding service asks it for the unmerged `keymap` documents (`Store.Layers("keymap", profile)`, [Store](store.md)) and receives each one tagged with its layer and file path.

### Plugin manifest: focus-local bindings

The manifest gains a `keybindings` array, taking over from today's `hello.keybindings`. It means focus-local only.

```json
{
  "keybindings": [
    { "key": "n", "action": "files.new", "focus": "sidebar-list", "hint": "new file" },
    { "key": "ctrl+r", "action": "files.refresh", "focus": "sidebar-list" }
  ]
}
```

Rules:

- `focus` is required and must name a surface the plugin declares in `slots` (or one it publishes). A binding with no `focus`, or for a surface the plugin does not own, is dropped and reported as a `global_not_allowed` or `foreign_surface` problem.
- `action` must be one of the plugin's own actions or a `husk.*` host action. A focus-local binding to another plugin's action is dropped (`foreign_action`): a plugin does not wire users into someone else's code.
- The `plugin` field is implicit (the declaring plugin) and not accepted.
- At most 128 focus-local bindings per plugin. The rest are dropped with `too_many`.
- The Pod converts the array into the `focus-local` layer document and hands it to Store as config, so a reload and a plugin install use the same path.

### Resolution

```go
// Package keybinding: the policy view. Services owns the concrete service type.

type Layer string

const (
	LayerHusk         Layer = "husk"
	LayerFocusLocal   Layer = "focus-local"
	LayerDistribution Layer = "distribution"
	LayerUser         Layer = "user"
	LayerProfile      Layer = "profile"
)

// Binding is one parsed entry from any layer.
type Binding struct {
	Keys    []string       // one to four normalized keys
	Action  string         // empty when Disable
	Args    map[string]any // optional
	Hint    string         // optional
	Focus   string         // surface id; empty means global
	Plugin  string         // empty unless narrowed; implicit for focus-local
	Disable bool
	Source  Source
}

type Source struct {
	Layer  Layer
	File   string // repo-relative or config-relative path; empty for focus-local
	Line   int    // 1-based; 0 when unknown
	Plugin string // declaring plugin, focus-local only
	Order  int    // position within the layer, for tie-breaking
}

// KeyContext is what Input knows when a key arrives. All of it comes from State.
type KeyContext struct {
	FocusSurface string // focused surface id; empty when none
	FocusOwner   string // plugin occupying it; empty when unoccupied
	ModalOpen    bool   // a modal surface is open
	Frozen       bool   // terminal too small; only husk.quit may resolve
}

type Outcome int

const (
	NoMatch Outcome = iota // key is not bound: hand it to the occupant
	Prefix                 // a sequence is in progress: wait for the next key
	Match                  // run Binding
)

type Result struct {
	Outcome Outcome
	Binding *Binding // set when Match
}

type Resolver interface {
	// Resolve takes the keys pressed since the last Match or reset, in order.
	Resolve(ctx KeyContext, keys []string) Result
	// Hints returns the footer hints for the focused surface, at most four.
	Hints(ctx KeyContext) []Hint
	// KeysFor returns the effective keys for an action, strongest first, for
	// palette and rail labels.
	KeysFor(action string) []string
	// Listing returns every entry, effective and shadowed, plus load problems.
	Listing() Listing
}
```

`Resolve` is a pure function of the loaded layers and the context. It reads no State and writes none.

Candidate selection for a key sequence `K` in context `C`:

1. A binding is a **candidate** if its keys equal or begin with `K` and its scope matches `C`: global, or `Focus == C.FocusSurface` and (`Plugin` empty or `Plugin == C.FocusOwner`).
2. Tombstones apply: for each `Disable` entry, remove every lower-layer entry with the same `(Keys, Focus, Plugin)`. The disable entry itself stays as a candidate for ranking purposes only and yields `NoMatch` if it wins.
3. Order the rest by layer rank, then scope specificity, then `Source.Order` descending.
4. If the top candidate's keys equal `K` exactly, return `Match` (or `NoMatch` for a disable). If every candidate is longer than `K`, return `Prefix`.
5. All other candidates are **shadowed** and listed as such by `Listing`.

Special contexts:

- **Frozen.** While the terminal is below the minimum size, only bindings whose action is `husk.quit` resolve. All other keys give `NoMatch` and are not forwarded.
- **Modal open.** Only candidates scoped to `modal-surface`, plus global candidates whose action is `husk.modal.close` or equals the open modal's rail item id, are considered. Everything else gives `NoMatch` and goes to the modal's plugin. This preserves today's grab behavior and lets the rail item's own key close the modal.
- **Palette open.** The palette consumes every key (see Invariant 9). `Resolve` is not called.

What happens on `Match`: Input sends the binding's `Action` and `Args` to the Dispatcher ([Message kinds](message-kinds.md)). Host actions (`husk.*`) are owned by the host and run in the Stem. Rail item ids resolve to the rail item's activation. Any other id goes to its single owner.

### Built-in `husk` keymap

The host hard-codes no workbench key. The built-in file reproduces today's bindings so the first release changes no behavior except the rank change described in Level 2.

```json
{
  "version": 1,
  "bind": [
    {"key": "q",      "action": "husk.quit"},
    {"key": "ctrl+c", "action": "husk.quit"},
    {"key": "esc",    "action": "husk.home"},
    {"key": "f1",     "action": "husk.home"},
    {"key": "alt+1",  "action": "husk.home"},
    {"key": "ctrl+p", "action": "husk.palette.open"},
    {"key": ":",      "action": "husk.palette.open"},
    {"key": "alt+p",  "action": "husk.palette.open"},
    {"key": "ctrl+i", "action": "husk.hotspots.toggle_stage"},
    {"key": "v",      "action": "husk.hotspots.toggle_stage"},
    {"key": "alt+v",  "action": "husk.hotspots.toggle_stage"},
    {"key": "h",      "action": "husk.hotspots.badges"},
    {"key": "[",      "action": "husk.sidebar.toggle"},
    {"key": "]",      "action": "husk.sidepanel.toggle"},
    {"key": "/",      "action": "husk.docks.cycle"},
    {"key": "=",      "action": "husk.layout.reset"},
    {"key": "<",      "action": "husk.sidebar.narrower"},
    {"key": ",",      "action": "husk.sidebar.narrower"},
    {"key": ">",      "action": "husk.sidebar.wider"},
    {"key": ".",      "action": "husk.sidebar.wider"},
    {"key": "tab",    "action": "husk.focus.next"},
    {"key": "?",      "action": "husk.modifiers.toggle_alt_latch"},
    {"key": "esc",    "focus": "modal-surface", "action": "husk.modal.close"}
  ]
}
```

Host actions this file introduces that exist only as inline code in `cmd/husk/input.go` today: `husk.quit`, `husk.palette.open`, `husk.layout.reset`, `husk.sidebar.narrower`, `husk.sidebar.wider`, `husk.focus.next`, `husk.modifiers.toggle_alt_latch`, `husk.modal.close`. They join the existing `husk.home`, `husk.hotspots.toggle_stage`, `husk.hotspots.badges`, `husk.sidebar.toggle`, `husk.sidepanel.toggle` and `husk.docks.cycle`. Each has a catalog entry owned by the host, so it appears in the palette and in `husk action list`.

Widget-local keys are not keymap entries. The command palette and the slot inspector (hotspots stage) are host-owned surfaces. Like a plugin's surface, each receives the keys that reach it and handles them itself: the palette's `up`, `down`, `ctrl+j`, `ctrl+k`, `tab`, `enter`, `backspace`, `esc`, `ctrl+p`, `ctrl+c` and text entry; the inspector's `j`, `k`, `up`, `down`. The set is closed and listed in `docs/docs/architecture.md` under Input routing after this spec lands.

### Rail items, palette and footer

- A rail item's `shortcut` field and a command's `shortcut` field stop being bindings. They remain only as the manifest's non-binding suggestion, are ignored for routing, and trigger a `suggestion_ignored` problem so the author sees it. The text a rail button, palette row or top bar shows is `KeysFor(action)[0]`: the effective key, or nothing.
- A rail item is addressable as an action by its id. Binding a key to a rail item id activates that item; pressing it while the item is active returns home (modal items close), as today.
- The footer always shows the effective keys of `husk.home`, `husk.sidebar.toggle`/`husk.docks.cycle`, `husk.palette.open` and `husk.quit`, then `Hints(ctx)`: the winning bindings that have a `hint`, in scope for the focused surface (focus-local, or global with a hint), at most 4, first by layer rank descending then file order. A shadowed binding is never advertised.

### Reload

`husk config reload` invalidates the cached keymap. The service rebuilds from the latest layer documents and swaps the result atomically; an in-flight chord is cancelled. Adding a plugin at runtime changes the `focus-local` layer and takes the same path; the plugin's process starts only after that reload completes ([Pods](pods.md), launch gate), so its keys exist before its surface can take focus. Removing a plugin from the instance (Pod teardown) reloads likewise. A plugin whose process merely exits keeps its focus-local entries in the keymap, flagged `unavailable` in the listing, and they never match because no surface of that plugin has focus.

### `husk keys`

`husk keys` is read-only. It maps to the `keys.list` method served by the Server ([Instances and the CLI](instances.md) owns the method and exit codes). The Server handler joins `Listing()` with State (is the owner live) and the action catalog (does the action exist). The Keybinding service itself stays free of State.

Flags: `--action <id>` and `--focus <surface>` filter as in [Instances and the CLI](instances.md); this spec adds `--shadowed` (include shadowed entries) and `--problems` (print only load problems).

Each entry has a `state`:

| State | Meaning |
|---|---|
| `effective` | Wins for its key and scope. |
| `shadowed` | Beaten by a higher candidate; `shadowed_by` names the winner. |
| `disabled` | A tombstone that is currently winning. |
| `unavailable` | Effective, but the owning plugin is not live or its surface is not present. |
| `unknown_action` | Effective, but the action is not in the catalog. |

Table output (default on a TTY, with `--shadowed`):

```
KEY      SCOPE                    ACTION               LAYER         SOURCE                     STATE
ctrl+g   global                   notes.open           user          keymap.json:3              effective
n        sidebar-list (files)     files.new_dir        user          keymap.json:8              effective
n        sidebar-list (files)     files.new            focus-local   plugin files               shadowed by user keymap.json:8
[        global                   (disabled)           user          keymap.json:14             disabled
[        global                   husk.sidebar.toggle  distribution  app keymap.json:21         shadowed by user keymap.json:14
q        global                   husk.quit            husk          builtin                    effective
```

JSON output (one document, the reply `result`):

```json
{
  "entries": [
    {
      "keys": ["n"],
      "focus": "sidebar-list",
      "plugin": "files",
      "action": "files.new_dir",
      "layer": "user",
      "source": { "file": "keymap.json", "line": 8 },
      "state": "effective"
    },
    {
      "keys": ["n"],
      "focus": "sidebar-list",
      "plugin": "files",
      "action": "files.new",
      "layer": "focus-local",
      "source": { "plugin": "files" },
      "state": "shadowed",
      "shadowed_by": { "layer": "user", "file": "keymap.json", "line": 8 }
    }
  ],
  "problems": [
    {
      "layer": "focus-local",
      "plugin": "notes",
      "code": "global_not_allowed",
      "message": "binding \"ctrl+n\" has no focus; plugins cannot declare global bindings"
    }
  ]
}
```

`source.file` is relative to the config directory of its layer. `husk keys` never writes a keymap and never offers to copy bindings between layers.

### Invariants

1. **Plugins create no global binding.** A manifest entry without a `focus` has no effect on any key press.
2. **Configuration outranks plugins.** Any distribution, user or profile entry beats any focus-local entry on the same key, in every context.
3. **Rank order is fixed.** `husk` < `focus-local` < `distribution` < `user` < `profile`. No file changes it.
4. **Disable removes exactly its triple.** A disable never affects an entry with a different `(key, focus, plugin)`.
5. **Resolve is pure.** The same layers and context give the same result. It reads no State, does no I/O and takes no lock held across a call out of the service.
6. **The host binds no workbench key outside the `husk` layer.** Input contains no key literal other than the closed widget-local sets in the palette and the inspector.
7. **A plugin's focus-local binding fires only while its surface has focus** and (for a plugin-qualified entry) while that plugin occupies it. A key never reaches a plugin that does not hold focus through a binding.
8. **Every dropped or ignored entry is reported.** No entry disappears silently; each produces a problem visible in `husk keys --problems`.
9. **The palette consumes every key while open**, as today. No binding fires and no `slot_key` is sent to plugins.
10. **An unbound key reaches the occupant unchanged.** Dropping a binding or disabling it returns the key to the focused surface's plugin.
11. **A reload swaps atomically.** A lookup sees the old keymap or the new one, never a mix.
12. **Husk never writes a keymap file.** No command copies, adopts or generates bindings into the user layer.

### Limits

| Limit | Value | Reason |
|---|---|---|
| Keymap file size | 256 KiB | A hand-edited keymap is a few KiB; the cap bounds parse time and memory. |
| Bindings per file | 2048 | Same. Extra entries are dropped with `too_many`. |
| Focus-local bindings per plugin | 128 | A plugin is a surface, not a keyboard. |
| Keys per sequence | 4 | Longer sequences are unusable by hand. |
| Sequence timeout | 1500 ms | Long enough for a deliberate chord, short enough that a stray prefix does not trap input. |
| `hint` length | 40 runes | The footer has fixed room for four hints. |
| Footer hints from plugins | 4 | Unchanged from today. |
| `args` size | 1 KiB serialized | Params for an action, not a payload. |
| `Resolve` budget | 1 ms | It runs on the UI loop for every key. The index is a map keyed by first key. |

### Failure modes

| Condition | Who detects | What happens | What the user or plugin sees |
|---|---|---|---|
| Keymap file has a syntax error, first load | Store/Keybinding loader | The file's layer is dropped. Other layers load. | A toast at startup naming the file and line; `husk keys --problems` lists `parse_error`. |
| Same error on reload | Loader | The previous good document for that layer stays active. | `config.reload` returns the problem; the keymap does not change. |
| Unknown `version` | Loader | File rejected as above. | `unsupported_version` problem. |
| Entry has an invalid key | Loader | That entry is dropped. | `invalid_key` problem with file and line. |
| Entry has both `key` and `keys`, neither, or a `keys` array outside 2 to 4 entries | Loader | That entry is dropped. | `invalid_entry` problem. |
| Entry missing `action` without `disable`, or `disable` with an action | Loader | That entry is dropped. | `invalid_entry` problem. |
| `plugin` given without `focus` | Loader | Entry dropped. | `invalid_entry` problem. |
| Two entries with the same `(key, focus, plugin)` in one layer | Loader | The later entry wins. | `duplicate` problem naming both lines. |
| Sequence is a strict prefix of another in the same scope | Loader | The longer binding is dropped. | `unreachable` problem. |
| Bound action is not in the catalog | `keys.list` handler | The binding stays. Pressing it makes the Dispatcher return `not_found`. | State `unknown_action`; on press, a toast with the action id. |
| Bound action's owner is not live | Dispatcher (State liveness) | No call is made. | State `unavailable`; on press, a toast "owner not running". |
| Manifest binding without `focus` | Pod manifest conversion | Binding dropped; plugin still loads. | `global_not_allowed` problem naming the plugin. |
| Manifest binding for a surface the plugin does not own | Pod manifest conversion | Binding dropped. | `foreign_surface` problem. |
| Manifest binding to another plugin's action | Pod manifest conversion | Binding dropped. | `foreign_action` problem. |
| Manifest rail item or command carries `shortcut` | Pod manifest conversion | Field ignored for routing. | `suggestion_ignored` problem; the rail shows no key until a layer binds one. |
| Resolved keymap has no binding for `husk.quit` or `husk.palette.open` | Keybinding service after each load | The keymap is used as written. | A startup toast and a `no_exit_binding` problem. Recovery: `husk action invoke husk.quit`, or SIGTERM. |
| A plugin binds a focus-local key that traps its surface (for example `esc`) | Nobody | Honored: that is focus-local semantics. | Fix with a user-layer disable or override. |
| `args` exceeds 1 KiB or holds a non-scalar value | Loader | Entry dropped. | `invalid_entry` problem. |
| Reload while a chord is pending | Input | The pending chord is cancelled. | The pending keys are discarded; nothing is sent to the occupant. |

### Dependency directions

- Services read Store; Store never imports Services. The Keybinding service receives the unmerged layer documents through `Store.Layers("keymap", profile)` and the focus-local layer through `Store.Declared("keymap")`.
- The Keybinding service reads the `focus-local` layer from the Pod's manifest conversion output (stored as config); Pods never import the Keybinding service.
- Input reads the Keybinding service; the service never imports Input, the Dispatcher, State or the Server.
- Input sends `Match` results to the Dispatcher as an action id plus params. The Dispatcher does not know a key caused it.
- The Server's `keys.list` handler imports the Keybinding service, State and the action catalog and joins them. The CLI imports none of them and reaches the keymap only through the Server.
- Hint text consumers (renderer, palette, rail) call `KeysFor` and `Hints`; none reads a layer document.

### What this exposes to the CLI

- `keys.list` (backing `husk keys`): the entries, their layer, source and state, and the load problems.
- `config.reload` rebuilds the keymap along with every other service cache.
- No write path. A keymap changes by editing a file and reloading.

## Level 2: Migration from today

| Today | Change |
|---|---|
| `cmd/husk/input.go` (the `switch key` block around the `q`, `?`, `esc`, `ctrl+p`, `[`, `]`, `/`, `=`, `<`, `>`, `tab` cases) | Replaced by a `Resolve` call and a dispatch of the resulting action. Each case body moves into a host action (see the built-in keymap list). `layoutFrozen` handling becomes `KeyContext.Frozen`. The palette and slot-inspector key handling stays as widget-local code. |
| `cmd/husk/input.go` (`railShortcutItem` use, rail shortcut activation) | Removed. Rail activation is an action bound by a layer. |
| `cmd/husk/commands.go` `keyBinding`, `isChordKey`, `keyOwner` use for plugin bindings | Removed. `isChordKey` is dropped with the "plugin globals only on chords" rule, which no longer has a case to guard. `keyOwner` remains for `slot_key` routing. |
| `cmd/husk/commands.go` `builtinCommands`, `executeCommand` | Kept as the host action catalog and handler. Gains the eight new `husk.*` actions. The `Shortcut` field on host commands is deleted; palette rows show `KeysFor`. |
| `cmd/husk/commands.go` `footerHints`, `maxPluginFooterHints` | Reads `Hints` and `KeysFor` instead of `Info.Keybindings` and fixed host strings. |
| `cmd/husk/keybinding_test.go` | Rewritten against `Resolve` with fixtures for each layer, disable, shadowing and sequences. Behavior tests for the built-in keymap assert today's key to action table. |
| `pkg/huskipc/protocol.go` `KeybindSpec`, `Hello.Keybindings` | Removed with `hello` ([Protocol](protocol.md): there is no handshake in v2). The shape moves to the manifest as the focus-local entry above (`command_id` becomes `action`, `when_slot` becomes `focus`). `RailItemSpec.Shortcut` and `CommandSpec.Shortcut` become non-binding suggestions. |
| `pkg/manifest/manifest.go` | Gains a `Keybindings` field with the focus-local shape above. Validation of `focus`, `action` and the 128 cap lives in the conversion step. |
| `docs/docs/architecture.md` Input routing | Steps 2 to 4 collapse into "resolve against the layered keymap". The statement that plugins cannot override host shortcuts is replaced by the rank rules above. Updated when the implementation lands. |
| New | Keymap loader and `Resolve` under the Keybinding service ([Services](services.md)); embedded `keymap.json`; `keys.list` handler; `husk keys` subcommand. |

Behavior changes users can notice:

1. A plugin's focus-local binding on a key the host also binds now wins while that surface has focus. Before, host shortcuts always won.
2. Plugin bindings without a focus surface stop working. Distributions that relied on them add a `distribution` keymap.
3. Rail and palette shortcut labels follow the effective keymap, so they change when a user rebinds.

## Decisions made here

1. **HuJSON for keymap files, like every other layer document.** Keymaps are hand-written and need comments; HuJSON gives comments and trailing commas without a second config parser in the binary ([Store](store.md), decision 1). An earlier draft chose TOML; one format for all documents wins.
2. **Rank order `husk` < `focus-local` < `distribution` < `user` < `profile`.** The notes place focus-local below the user layer; this puts it above the built-in host keys and below every real config layer.
3. **Layer first, specificity second.** A user global binding beats a plugin's focus-local binding on the same key. This is the strongest reading of "configuration beats plugins" and the easiest to explain; the user restores a plugin key with a focus-scoped entry.
4. **Disable is a tombstone on an exact `(key, focus, plugin)` triple.** Narrow on purpose, so one line cannot silently kill unrelated bindings.
5. **A disabled or unbound key falls through to the occupant.** It does not swallow the key.
6. **The built-in `husk` keymap reproduces today's keys, including bare letters such as `q`, `h`, `v`.** The migration changes no default. Distributions are the lever for changing them.
7. **A distribution may drop the built-in layer with `builtin = false`.** Overriding entry by entry is not enough for an application that wants none of the host keys.
8. **One distribution layer in v1**, matching Store's single `Options.Distribution`. Several distribution layers ranked by registration order (a bundle nested in an application) are deferred until Store accepts more than one source.
9. **No keymap in the `instance` layer in v1.** Run flags do not carry bindings.
10. **Key sequences up to four keys, written as a `keys` array, 1500 ms timeout, no replay of a dropped prefix.** Input needs chord state anyway, and replay would send stray keys to a plugin. The array form exists because `" "` is a key and a space-joined string could not spell it.
11. **Palette and slot-inspector keys stay widget-local.** They are the input handling of two host-owned surfaces, not workbench bindings. The set is closed.
12. **Modal grab considers only `modal-surface` scope plus the modal's own close action.** Preserves today's behavior that a modal sees nearly every key.
13. **Rail and command `shortcut` fields become suggestions that are ignored for routing and reported.** Otherwise a plugin could still declare a global key through the rail.
14. **A foreign action in a focus-local binding is dropped.** A plugin may bind its own actions and `husk.*` actions only.
15. **Eight new `husk.*` host actions** extract inline key handling in `input.go` so the built-in keymap has something to bind.
16. **`keys.list` computes availability in the Server handler, not in the service.** Keeps `Resolve` pure and the service free of State.
17. **Parse failure on reload keeps the last good document for that layer.** A typo cannot lock the user out mid-session. At first load the layer is dropped instead, because there is nothing to keep.
18. **`husk keys` gains `--shadowed` and `--problems`.** Required to show where each binding came from and what it beat.
19. **No key is protected from remapping, including quit.** Recovery is `husk action invoke husk.quit` or a signal; a warning fires when no exit binding remains.

## Open questions

1. **Should the built-in keymap keep bare letters (`q`, `h`, `v`, `?`, `.`, `,`) that fire even when a plugin's text input has focus?** They do today, and `q` quits while typing in a plugin. Recommend: keep them in v1 for a behavior-neutral migration, and change the defaults to chords in the first release after, since a distribution can already override them.
2. **Layer-first precedence versus specificity-first.** With layer-first, a user global `r` hides every plugin's focus-local `r` until the user adds focus-scoped entries. Recommend: keep layer-first; the shadowed listing in `husk keys` shows exactly what a binding hides, and the policy goal is that people, not plugins, own the keyboard.
3. **Should a plugin be able to protect a key on its surface from distribution globals?** The notes say no (focus-local sits below config). This spec follows the notes. A text-editing plugin would then lose bare-letter keys to any distribution global on a bare letter. Recommend: leave it to distribution authors, and document that global bare-letter bindings steal typing.
4. **Should the host protect one escape binding?** Decision 19 allows removing quit. An alternative is to refuse a keymap with no `husk.quit` and fall back to the built-in. Recommend: keep the warning only; the CLI and signals are enough, and a framework should not second-guess an explicit config.
5. **Contexts for palette and widget-local keys.** A later spec could make the palette's navigation rebindable through a `context = "palette"` scope. Recommend: not in v1; the closed list is small and stable.
6. **Key spelling of non-US layouts and Option combinations.** The terminal passes layout-specific characters through unchanged, so a binding for `alt+o` on a German layout needs `alt+ö`. Recommend: document it and keep exact matching; layout translation is a terminal problem.
7. **Disagreement with the notes: none on policy.** One gap: the notes say the resolved keymap comes from "Store's generic layer resolution", but precedence here includes rank, scope specificity and tombstones, which a generic document merge does not express. Resolved: [Store](store.md) exposes `Layers(name, profile)`, which returns the unmerged documents tagged with their layer, and the Keybinding service resolves across them. Store stays generic.
8. **Flags in the instances spec.** Resolved: [Instances and the CLI](instances.md) lists `husk keys [--action] [--focus] [--shadowed] [--problems]`.
