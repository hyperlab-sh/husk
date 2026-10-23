---
type: Design Spec
title: "Keybinding policy and config layers"
description: "Who may bind keys in Husk, which layers a keymap comes from, which binding wins, the keymap file shape, and what husk keys reports."
status: planned
generated: { by: claude/fable-5-1, at: 2026-10-22T18:00:00Z }
tags: [architecture-v2, services]
---

# Keybinding policy and config layers

## Level 0: What and why

A key press reaches Husk and one of three things happens: a binding runs an action, the key is the first part of a longer sequence and Husk waits, or no binding claims the key and it goes to the plugin occupying the focused surface. This spec fixes the rules behind that decision: who may declare a binding, where the effective keymap comes from, which binding wins when several claim the same key, how a plugin that is taking text input keeps plain typing, and what the user sees when they ask `husk keys`.

The policy in six lines:

1. A plugin declares **actions**. It never declares a global binding.
2. A plugin may declare **surface-scoped** bindings in its manifest. They apply only while one of its own surfaces has focus and that plugin occupies it. This is the `focus-local` layer.
3. **Global** bindings come only from keymap files: the built-in `husk` layer, then `distribution`, `user` and `profile`.
4. **The focused surface wins over the rest of the screen.** A binding scoped to the focused surface beats a global binding on the same key, whichever layer each comes from. Within one scope, the stronger layer wins. (Operator ruling D64.)
5. **A surface taking text input keeps plain typing.** While the focused occupant has declared capture, unmodified text keys skip every global binding. Modified keys and `esc` always resolve.
6. The host hard-codes no workbench key. Today's keys live in a built-in `husk` keymap that sits below every other layer, so a distribution can replace or drop them. Husk never writes a keymap file and never copies a plugin's suggestions into the user layer (`husk keys adopt` is rejected on purpose).

What this spec is not:

- Not the Keybinding service mechanics (rebuild, candidate, atomic swap, reload coordination). Those are in [Services](services.md). This spec is the policy that service implements.
- Not config layer reading. [Store](store.md) reads the layer documents and the declared documents; it does not merge keymaps.
- Not key decoding. Terminal escape handling stays in the frontend; this spec starts at the normalized key spelling.
- Not the action catalog. Which actions exist, and who owns them, comes from manifests via [Pods](pods.md) and is routed by the Dispatcher ([Message kinds](message-kinds.md)).

Dependencies:

- Depends on [Store](store.md) for the unmerged `keymap` layer documents and the declared per-plugin `keymap` documents, on [Pods](pods.md) (manifest conversion) for the `focus-local` layer, on [Services](services.md) for the service that hosts this policy, and on [State](state.md) for the focus, overlay and capture facts the frontend reads into `KeyContext`.
- Depended on by the frontend's Input (resolves a key press to an action id), by the footer, rail and palette (key labels), and by the CLI through the Server (`husk keys`, [Instances and the CLI](instances.md)).

## Level 1: Contract

### Terms

| Term | Meaning |
|---|---|
| **Key** | One normalized key spelling, for example `n`, `ctrl+g`, `f4`, `esc`, `" "`. The spelling rules are under "Key spelling". |
| **Sequence** | One to four keys pressed one after another. A binding with one key is a sequence of length one. |
| **Binding** | One entry from any layer: a sequence, a scope, and either an action to run or `disable`. |
| **Scope** | Where a binding applies: **global** (no `focus`) or **surface** (`focus` names a surface id). A surface-scoped binding may carry `plugin`, which is a match condition, not a scope of its own. |
| **Layer** | One source of bindings, with a fixed rank. The five layers are listed below. |
| **Context** | What Input knows when a key arrives: the focused surface, its occupant, whether a modal or the palette is open, whether the occupant captures text, whether the terminal is too small. |
| **Candidate** | A binding whose sequence begins with the keys typed so far and whose scope and conditions match the context. A tombstone is a candidate only when its sequence equals the keys typed so far (D92). |
| **Winner** | The candidate that ranks highest for the keys typed so far in one context. `Resolve` returns the winner's outcome. |
| **Effective** | A listing state, with no context: an entry that no other entry statically shadows (see "`husk keys`"). `Hints`, `KeysFor` and the loader use this word only in that sense. |
| **Tombstone** | A `disable` binding. When it is the winner, the key is unbound: nothing runs and the key goes to the occupant. |
| **Occupant** | The plugin occupying the focused surface, from `husk.surfaces` ([State](state.md)). |
| **Text key** | An unmodified key that produces or edits text: a single printable rune including `" "`, or one of `enter`, `backspace`, `delete`, `tab`, `up`, `down`, `left`, `right`, `home`, `end`, `pgup`, `pgdown`, `insert`. `esc` is not a text key. A key with any modifier prefix is not a text key. |

### Layers

| Rank | Layer | Source | Scopes it may bind |
|---|---|---|---|
| 0 (weakest) | `husk` | Keymap file embedded in the binary | global and surface |
| 1 | `focus-local` | Plugin manifests (`keybindings`), converted by Pods into one declared `keymap` document per plugin | surface only; `plugin` is implicitly the declaring plugin |
| 2 | `distribution` | The keymap file an application built on Husk registers with Store | global and surface |
| 3 | `user` | `keymap.json` in the user config directory | global and surface |
| 4 (strongest) | `profile` | `keymap.json` in the active profile's directory | global and surface |

The Store `instance` layer (run flags) does not carry a keymap. Store has one distribution layer, in two parts merged in order: an application's embedded files (`Options.Distribution`) and a bundle's files (`Options.Bundle`, [Store](store.md), [Bundles](bundles.md)); since `bind` is an array, a bundle `keymap.json` replaces an embedded one whole, so the layer still yields one distribution keymap. The rank order is fixed; no file changes it.

### Which binding wins

Given the keys typed so far `K` and the context `C`, the winner is found in these steps (D81). Each step is a total order; the first step that separates two candidates decides.

1. **Candidates.** A binding is a candidate when:
   - its sequence equals or begins with `K`; a tombstone only when its sequence equals `K` (D92: a tombstone unbinds the sequence it names, never a prefix of it); and
   - it is global, or its `focus` equals `C.FocusSurface` and (`plugin` is empty or equals `C.FocusOwner`); and
   - the context filters below (frozen, modal, capture) keep it.
2. **Scope first.** A surface-scoped candidate ranks above a global candidate.
3. **Layer second.** Within one scope, the higher-ranked layer wins: `profile` > `user` > `distribution` > `focus-local` > `husk`.
4. **Narrower condition third.** Within one scope and one layer, a candidate with `plugin` ranks above one without.
5. **File order last.** Within one scope, layer and condition, the entry that appears first in its document wins. (Two such entries with the same sequence are a `duplicate`; see "Loader rules".)

The outcome:

| Top candidate | Result |
|---|---|
| none | `NoMatch`: the key is not bound here; Input hands it to the occupant. |
| a tombstone | `NoMatch`. Because a tombstone is a candidate only for its exact sequence, it never produces `Prefix`; a prefix whose only continuations are tombstones has no candidates and is `NoMatch`, not a wait. |
| sequence longer than `K` | `Prefix`: Input waits for the next key. |
| sequence equal to `K` | `Match`: Input runs the binding's action with its args. |

Consequences, stated so they are tested rather than inferred:

- A user global binding on `r` does not fire while a surface is focused whose occupant declared a surface-scoped `r`. The user takes that key back with a surface-scoped entry: `{"key":"r","focus":"sidebar-list","action":"..."}` or `{"key":"r","focus":"sidebar-list","disable":true}`.
- A global tombstone unbinds global bindings only. It does not silence a plugin's surface-scoped binding on the same key.
- A surface-scoped tombstone without `plugin` in the user layer unbinds every lower-layer binding on that surface, including every plugin's `focus-local` entry for it. With `plugin`, it unbinds them only while that plugin occupies the surface.
- A `focus-local` entry beats a `husk`-layer global key while its surface has focus (so a plugin's `[` on its surface wins over the host's `[`), and loses to a `distribution`, `user` or `profile` entry scoped to the same surface.
- A user tombstone on `["ctrl+k","ctrl+s"]` over a `husk`-layer binding on `ctrl+k`: for `K = ["ctrl+k"]` the tombstone is not a candidate, so the plain `ctrl+k` binding fires. The tombstone unbinds only `ctrl+k ctrl+s` where a lower layer binds that sequence.
- A user tombstone on `["ctrl+k","ctrl+s"]` and a user binding on `["ctrl+k","ctrl+o"]`, whatever their file order: `ctrl+k` is `Prefix` (the binding is the only candidate), `ctrl+k ctrl+o` is `Match`, `ctrl+k ctrl+s` is `NoMatch`.
- A user tombstone on `["ctrl+k"]` over a `husk`-layer binding on `["ctrl+k","ctrl+s"]`: for `K = ["ctrl+k"]` the tombstone equals `K`, outranks, and the result is `NoMatch`; `ctrl+k` reaches the occupant and `ctrl+k ctrl+s` can never be typed. This is what any shorter higher-ranked binding does to a longer lower one, and `husk keys` lists the longer entry as shadowed by the tombstone. A tombstone is a binding to nothing, not a hole that lower layers show through.

### Context filters

`KeyContext` carries five facts. Each one removes candidates before ranking.

| Fact | Source | Effect on candidates |
|---|---|---|
| `Frozen` (terminal below the minimum size) | frontend geometry | Only candidates whose action is `husk.quit`, and tombstones, remain. Nothing is forwarded to any plugin. |
| `ModalOpen`, `ModalAction` (a modal surface is open; the rail item id that opened it) | `husk.surfaces.overlays.modal` | Only candidates scoped to `modal-surface`, global candidates whose action is `husk.modal.close` or equals `ModalAction`, and tombstones remain. Keys that then yield `NoMatch` go to the modal's plugin. `FocusSurface` is `modal-surface` and `FocusOwner` the modal's plugin while a modal is open. |
| `Capture` (the focused occupant has declared text capture for the focused surface) | `husk.surfaces.capture[FocusSurface]`, see "Text capture" | Global candidates whose first key is a text key are removed, from every layer, except, while `ModalOpen`, candidates whose action is `husk.modal.close` or equals `ModalAction`, which this filter never removes (with no modal open there is no exemption; `ModalAction` is empty and is not compared). Surface-scoped candidates, candidates on modified keys, on function keys and on `esc` remain. |
| Palette open | frontend | `Resolve` is not called. The palette consumes every key (Invariant 9). |
| Settings center open (`husk.surfaces.overlays.settings_center` non-empty) | frontend | `Resolve` is not called. The center consumes every key: its closed set acts and every other key is ignored ([Settings center](settings-center.md), "Focus and keys"; its Theme tab is the theme selector of [Services](services.md), "The theme selector"). Unlike the palette, no opener key closes it; only `esc` and `ctrl+c` do. **Landed.** |

The filters compose: a frozen terminal with a modal open still resolves only `husk.quit`; a modal in `text` capture still closes on the key that opened it. While capture is active (`text` or `all`), `husk.capture.release` is exempt from the modal filter; in `all` capture every `husk.modal.close` candidate is suppressed regardless of scope or source until release suspends capture (see "Capture modes"). Tombstones survive every filter so that a user can unbind `q` while frozen or unbind a modal's close key; the Frozen rule that nothing is forwarded still holds.

### Text capture

Problem: `q`, `h`, `v`, `?`, `tab` and the other unmodified host keys fire while a person types into a plugin's text field, because a binding on any key wins before the occupant sees it. Vim-style modes (normal/insert) are not the fix: Husk has no editor and the plugin, not the host, knows when it is editing. The fix is one bit of context, set by the occupant (D82).

- An occupant sets capture per surface through the optional `capture` field of `surface.update` ([Protocol](protocol.md), minor 2.1): `true` means "this surface is taking text input now", `false` turns it off, and an absent field leaves the current value unchanged (D93). A surface starts with capture off. A `surface.update` the engine refuses (not the occupant, unknown surface) sets nothing.
- The Surfaces engine records the bit as `capture[surface]` in `husk.surfaces` ([State](state.md)) and clears it when the surface's occupant changes, when the occupant releases or closes the surface, and when the occupant's Pod leaves `live` (D93). An unoccupied surface never captures.
- Input copies `husk.surfaces.capture[FocusSurface]` into `KeyContext.Capture` on every key press.
- While `Capture` is true, a text key that no surface-scoped binding claims goes to the occupant unchanged, whatever any global layer binds. A person who wants a plain letter to stay a shortcut even while typing writes it as a surface-scoped entry.
- `esc` and every modified key (`ctrl+`, `alt+`, `super+`, `meta+`, `hyper+`) resolve as usual, so quit (`ctrl+c`), the palette (`ctrl+p`), home (`alt+1`, `f1`) and focus cycling (`shift+tab`) stay reachable through the built-in keymap while typing.
- Capture is not focus: it changes which keys resolve, not which surface receives them.
- Closing the modal overlay is not a release, so it does not clear `capture` for `modal-surface`; a modal the same occupant reopens starts with the old value until that occupant sends `capture: false` (another plugin taking `modal-surface` clears it as any occupant change does). An occupant that captures in the modal sends `capture: false` before it closes.

### Capture modes

**Landed in protocol 2.5.** The capture bit above becomes a three-valued mode ([Protocol](protocol.md), "Runtime and terminal surfaces"); everything in "Text capture" is the `text` mode and is unchanged.

| Mode | Set by | Effect on candidates |
|---|---|---|
| `none` | default; `capture: false` | No filter. |
| `text` | `capture: true` or `"text"` | "Text capture" above: global candidates on text keys are removed. |
| `all` | `capture: "all"`; implied by `terminal.attach` | Every global candidate is removed, from every layer, modified keys, function keys and `esc` included, except candidates whose action is `husk.capture.release`, and tombstones. Every `husk.modal.close` candidate is also removed, including surface-scoped candidates from any source. Other surface-scoped candidates remain, so an occupant can still declare its own keys. Keys that then yield `NoMatch` go to the occupant: for a terminal surface as encoded bytes (`terminal.input`), for a line surface as `input.key`. |

An application running inside a terminal surface expects the whole keyboard (`esc`, `ctrl+c`, `ctrl+p`, `tab`), so `text` is not enough for it and a plugin must be able to say "all of it". The cost is that the host's own keys vanish while such a surface has focus, which is why one key is held back: the **release key**.

- `husk.capture.release` is a host action bound in the built-in keymap to `ctrl+]` (the one key no terminal application needs often; it is `GS`, which `telnet` chose for the same reason). Under `all` it is the only global binding that fires. It suspends capture for the **next key only**: `ctrl+] ctrl+p` opens the palette from inside a terminal, `ctrl+] tab` cycles focus, `ctrl+] ctrl+]` sends a literal `ctrl+]` to the occupant, and any key with no global binding is forwarded as it would have been. The suspension lasts until that next keypress or 1500 ms (the sequence timeout), whichever comes first, and the footer shows `release` while it is pending. A multi-key binding's prefix consumes the suspension too: its continuation is resolved under normal capture, so release does not reserve an entire key sequence. Under `text` and `none` the action fires and does nothing beyond the one-key suspension, which changes nothing there.
- Mouse events on a surface in `all` capture go to the occupant whole (click, release, motion, wheel) with no host handling except divider drags on the frame ring and clicks outside the surface, which change focus as usual; a wheel event on a terminal surface whose application has not enabled mouse reporting scrolls the host's scrollback instead. Any key, paste or new program output returns a scrolled-back view to the live screen, and that key still reaches the program. Paste goes to the occupant (bracketed when the application asked). The palette, when open, still consumes everything (Invariant 9). While a terminal has focus, `input.key_state` broadcasts are suppressed for every plugin to avoid observing captured input, and resume after focus leaves or detachment.
- `KeyContext.Capture` becomes `Capture CaptureMode` (`none`, `text`, `all`); the frontend reads `husk.surfaces.capture[FocusSurface]`, whose values become `"text"` or `"all"` ([State](state.md)). `Resolve` applies the `all` filter before the `text` filter; both compose with `Frozen` (which still leaves only `husk.quit`). While capture is active, `husk.capture.release` is exempt from the modal filter, so a captured modal cannot trap the user. Under `all`, every `husk.modal.close` candidate is suppressed, whatever its layer or scope; other surface-scoped candidates remain. A modal closes through that action only after release suspends capture: the built-in Escape and a user-bound modal-close key reach the program until then.
- The Surfaces engine clears the mode as it clears the bit today (occupant change, release, close, Pod leaving `live`), and `terminal.detach` returns an attached surface to `none`, including when attach explicitly requested a different mode.

### Key spelling

Keys use the frontend's wire spelling, the one plugins receive in `input.key`. The loader normalizes each spelling with the shared `keyspelling.Normalize` and rejects what cannot match a real key press.

| Rule | Examples |
|---|---|
| Modifier prefixes, in any order, each at most once: `alt+`, `ctrl+`, `shift+`, `super+`, `meta+`, `hyper+`. The normalized form puts `alt+` first; the order of the remaining prefixes is the frontend's wire order (`keyspelling.Format`, which the frontend uses for `input.key`), and `husk keys` prints that form. Bindings match the wire spelling by string equality. | `ctrl+g`, `alt+2`, `alt+ctrl+x` |
| `meta+` and `hyper+` are accepted in files and spelled on the wire, but the `modifiers` object of `input.key` and `input.key_state` reports only `alt`, `ctrl`, `shift` and `super`. | `meta+x` binds; `modifiers.meta` does not exist |
| `shift+` plus a named key is kept as written. | `shift+tab`, `shift+enter`, `shift+up` |
| `ctrl+space` normalizes to `ctrl+@`, the wire name of NUL. | `"key": "ctrl+space"` → `ctrl+@` |
| Named keys: `enter`, `tab`, `backspace`, `esc`, `up`, `down`, `left`, `right`, `home`, `end`, `pgup`, `pgdown`, `insert`, `delete`, `space`, `f1` to `f63`. | `f4`, `pgdown` |
| `space` normalizes to the single wire space `" "`. | `"key": "space"` and `"key": " "` are the same key |
| Any other key is exactly one printable rune and keeps its case. | `n`, `N`, `?`, `ö`, `日` |
| A letter after `ctrl+`, `super+`, `meta+` or `hyper+` is lowercased. | `ctrl+X` → `ctrl+x` |
| `shift+` plus a letter means the uppercase letter. | `shift+x` → `X` |
| `shift+` plus a digit or symbol is rejected (`invalid_key`); the message names the produced character to bind instead. | `shift+1` → "bind `!`" |
| Anything else (empty, two or more runes that are not a named key, an unknown name, a repeated modifier) is rejected (`invalid_key`). | `ctrl+ctrl+a`, `ab`, `f0` |

Layout-specific characters pass through: on a German layout, Option+o arrives as `alt+ö` and is bound as `alt+ö`. Layout translation is a terminal problem, not Husk's.

### Sequences

A binding names one key with `key` or two to four keys with `keys`; exactly one of the two is present. A sequence is never written as one space-separated string, because `" "` is itself a key. Input, not the service, owns sequence state:

| Event | Input does |
|---|---|
| `Prefix` returned | Remembers the keys typed so far and waits. |
| 1500 ms pass without another key | Discards the pending keys. Nothing is replayed to the occupant; nothing is dispatched. |
| `esc` arrives while keys are pending | Discards the pending keys and consumes the `esc`. |
| `Match` returned | Clears the pending keys and dispatches the action. |
| `NoMatch` returned with keys pending | Discards the pending keys and resolves the key that just arrived on its own, as if nothing had been pending. That lookup may return `Prefix`, which starts a new sequence. |
| Focus moves, the focused surface's occupant changes, a modal opens or closes, or the palette opens while keys are pending (a click, `surface.close`, a host action, or a plugin exit, which Input sees as an occupant change in `refresh`) | Discards the pending keys. A prefix typed on one surface never completes on another. |
| A paste arrives while keys are pending | Discards the pending keys. A paste is delivered as one bracket-wrapped key ([Palette text input](palette-textinput.md)); it is not a text key and matches no binding, so it is `NoMatch` for the occupant. |
| The terminal becomes frozen while keys are pending | Nothing special. The next key is resolved under the Frozen filter. |
| A reload swaps the keymap while keys are pending | Nothing special. The next key is resolved against the keymap current at that moment. |

If, within one scope, layer and plugin condition, an effective binding's sequence is a strict prefix of another effective binding's sequence, the longer one can never be typed. The loader drops the longer one and reports `unreachable`. (The same prefix in two different scopes or layers is not a conflict: ranking decides at run time.) A tombstone is not the shorter sequence here: a same-layer tombstone that precedes a longer binding on its prefix makes that binding untypeable, so order the tombstone after it. The same order dependence holds between a statically shadowed shorter entry and a longer effective one in one layer, scope and condition: under a Frozen or modal filter that removes the shadower, file order decides which of the two resolves.

### Keymap file shape

Format: HuJSON (JSON with comments and trailing commas), one file per layer, named `keymap.json`. It is the Store document `keymap`, read with the same parser as every other layer document ([Store](store.md), decision 1).

```jsonc
// keymap.json
{
  "version": 1,            // required; the only accepted value is 1
  // "builtin": false,     // distribution layer only: drop the husk layer entirely
  "bind": [
    {
      "key": "ctrl+g",        // one key; or "keys": ["ctrl+k", "ctrl+s"] for a sequence
      "action": "notes.open", // required unless "disable": true
      "hint": "Notes",        // optional footer text, at most 40 runes
      // "focus": "...",      // omitted: global; a surface id: surface-scoped
      // "plugin": "...",     // optional with focus: only while this plugin occupies the surface
      // "args": { ... },     // optional object passed to the action as params
      // "disable": true,     // optional tombstone; excludes action, args and hint
    },
  ],
}
```

| Field | Type | Required | Rule |
|---|---|---|---|
| `version` | integer | yes | Must be `1`. Any other value rejects the file (`unsupported_version`). |
| `builtin` | bool | no | `false` in the `distribution` layer removes the `husk` layer, including its `modal-surface` binding of `esc` to `husk.modal.close`; a distribution that drops the layer binds its own close key or its modals close only by mouse or by the key that opened them. In any other layer the field is reported (`invalid_entry`, key `builtin`) and ignored. |
| `bind` | array | yes | The entries, in order. A missing or non-array `bind` rejects the file (`invalid_entry`, key `bind`). |
| `key` | string | one of `key`/`keys` | One key. |
| `keys` | array of 2 to 4 strings | one of `key`/`keys` | A sequence. |
| `action` | string | unless `disable` | An action id containing at least one `.`: `<plugin_id>.<name>`, a `husk.*` host action, or a rail item id. Existence is not checked at load. |
| `focus` | string | no | A surface id. Omitted means global. Existence is not checked at load. |
| `plugin` | string | no | Only with `focus`. The entry applies only while this plugin occupies the surface. |
| `args` | object | no | Params for the action. Values are strings, numbers and booleans only; at most 1 KiB serialized. |
| `hint` | string | no | Footer label, at most 40 runes. Without it the binding is not advertised. |
| `disable` | bool | no | Tombstone. `action`, `args` and `hint` must be absent. |

Any field of the wrong JSON type, or any violation above, drops that entry with `invalid_entry` and keeps the rest of the file.

Examples (the surrounding `{"version": 1, "bind": [...]}` omitted):

```jsonc
// A global binding in the user layer.
{"key": "ctrl+g", "action": "notes.open", "hint": "Notes"}

// The plugin `files` declared `n` for `files.new` on `sidebar-list`. The user gives
// that surface's `n` another action and moves `files.new` to `ctrl+n`.
{"key": "n",      "focus": "sidebar-list", "plugin": "files", "action": "files.new_dir"},
{"key": "ctrl+n", "focus": "sidebar-list", "plugin": "files", "action": "files.new"},

// The distribution binds `[` globally; this user wants `[` to reach plugins.
{"key": "[", "disable": true},

// Unbind `r` on sidebar-list whichever plugin occupies it.
{"key": "r", "focus": "sidebar-list", "disable": true},

// A sequence with parameters.
{"keys": ["ctrl+k", "ctrl+o"], "action": "notes.open", "args": {"path": "todo.md"}},
```

### Layer locations

| Layer | Where | Written by |
|---|---|---|
| `husk` | `keymap.json` embedded in the binary beside the Keybinding service source | Husk |
| `distribution` | The file or embedded filesystem the embedding application registers with Store | The application |
| `user` | `keymap.json` in the user config directory (`$XDG_CONFIG_HOME/husk`, default `~/.config/husk`) | The user |
| `profile` | `profiles/<name>/keymap.json` under the user config directory | The user; selected by the Profile service |
| `focus-local` | The `keybindings` array of each plugin manifest, declared by Pods as the per-plugin `keymap` document (`Store.Declared("keymap")`) | The plugin author |

Store owns file reading. The Keybinding service receives the unmerged layer documents through `Store.Layers("keymap", profile)`, each tagged with its layer and file, and the declared documents through `Store.Declared("keymap")`, keyed by plugin id ([Store](store.md)). The service reads no file itself.

### Plugin manifest: surface-scoped bindings

The manifest field is `keybindings`, an optional array. Every entry is surface-scoped; there is no way to write a global binding in a manifest.

```json
{
  "keybindings": [
    { "key": "n", "action": "files.new", "focus": "sidebar-list", "hint": "new file" },
    { "key": "ctrl+r", "action": "files.refresh", "focus": "sidebar-list" }
  ]
}
```

Manifest conversion ([Pods](pods.md)) owns these checks and is the only place they run. It keeps the entries that pass and records one problem per entry it drops, in the declared document's `problems` array (`{index, code, message, key?}`, `index` being the original zero-based position in the manifest list the problem is about: the `keybindings` entry, or for `cli_only_event` the subscription entry, in `events.subscribe` or `subscriptions`). For a problem not about a binding position, `key` names the manifest member, for example `events.subscribe[1]` (the form `<field>[<i>]`):

| Check | Drops the entry with |
|---|---|
| `focus` is absent, or names a surface the plugin does not list in `surfaces` | `global_not_allowed`; the message says which |
| `plugin` or `disable` is present | `global_not_allowed`; the message names the field |
| `action` is neither one of the plugin's own actions nor `husk.*` | `foreign_action` |
| More than 128 entries | the first 128 in document order are kept; one `too_many` problem for the rest |

Key spellings pass through conversion unparsed. The Keybinding service is the single key parser: a focus-local entry whose key does not normalize is dropped there with `unparsable_key`. The declared document carries no `plugin` field; when the service loads a `focus-local` entry it sets `Binding.Plugin` and `Source.Plugin` to the document's source key (the plugin id, [Store](store.md)) and does not re-check ownership.

A rail item's `shortcut` (and any `shortcut` on a command) is not a binding. Conversion records it under `suggestions` in the declared document and the service reports each one as `suggestion_ignored`, so the author learns that a key for it is bound in a keymap layer, not in the manifest.

### Resolution API

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
	Args    map[string]any // nil when absent
	Hint    string
	Focus   string         // surface id; empty means global
	Plugin  string         // match condition; set by conversion for focus-local
	Disable bool
	Source  protocol.Source // where it was declared; the same type a Problem carries ([Protocol](protocol.md), "Problem"); Source.Layer is the Layer name as a string
}

// KeyContext is what Input knows when a key arrives. Every field comes from the
// frontend's own view of State; the service reads no State.
type KeyContext struct {
	FocusSurface string // focused surface id; "modal-surface" while a modal is open; empty when none
	FocusOwner   string // plugin occupying it; empty when unoccupied
	ModalOpen    bool
	ModalAction  string // rail item id that opened the modal; empty when none
	Capture      bool   // the occupant declared text capture for FocusSurface
	Frozen       bool   // terminal below the minimum size
}

type Outcome int

const (
	NoMatch Outcome = iota // not bound here: hand the key to the occupant
	Prefix                 // wait for the next key
	Match                  // run Binding
)

type Result struct {
	Outcome Outcome
	Binding *Binding // set only for Match; a detached copy
}

type Hint struct {
	Keys   []string
	Action string
	Text   string
}

type Resolver interface {
	// Resolve ranks the candidates for keys in ctx. Pure: no State, no I/O,
	// no lock held across a call out of the service, under 1 ms.
	Resolve(ctx KeyContext, keys []string) Result
	// Hints returns the bindings that carry a hint and would be the winner of
	// their own sequence in ctx (the same filters as Resolve apply, so a hint
	// never advertises a key that would not resolve), at most four:
	// surface-scoped before global, then stronger layer first, then file
	// order. A shadowed or disabled binding is never a hint.
	Hints(ctx KeyContext) []Hint
	// KeysFor returns the display spellings of the effective bindings for
	// action: global entries first, then surface-scoped; within each group
	// stronger layer first, then file order. A sequence is joined with one
	// space ("ctrl+k ctrl+s"). Empty when nothing binds the action.
	KeysFor(action string) []string
	// Listing returns every entry with its static state, plus every load problem.
	Listing() Listing
}
```

What happens on `Match`: Input dispatches `Binding.Action` with `Binding.Args` through the Dispatcher as the `input` caller (`protocol.CallerInput`, carried through `Frontend.InvokeAction`; [Message kinds](message-kinds.md), [Protocol](protocol.md)). `husk.*` actions and rail item ids run in the host; any other id goes to its single owner. The Dispatcher does not know a key caused it.

A host action may **decline** a key (D84). The mechanism: a host handler registered through `RegisterHost` ([Message kinds](message-kinds.md)) returns an `ActionReply` with `Declined: true` (and no `Err`); a handler registered through `Frontend.RegisterHostAction` declines by returning the host-only sentinel `contracts.ErrDeclined` (matched with `errors.Is`), which the root maps to that reply and to nothing else. The flag is host-only and is never serialized: the Dispatcher passes it unchanged to whoever made the request, and the Calls port (`ports/stem.Calls.ActionInvoke`, the only wire path) answers a declined reply with a successful empty result, so `husk action invoke husk.home` on the home layout is a no-op, not an error. The Dispatcher runs host handlers inline on the loop, so for a host action the `Frontend.InvokeAction` reply callback, `reply func(result map[string]any, declined bool, err error)` ([Pods](pods.md), Frontend seams), runs before `InvokeAction` returns; Input relies on this and reads `declined` from that callback on the same key press (for a plugin action the callback is asynchronous and `Declined` is always false). Input treats a declined reply as `NoMatch` for the key that completed the sequence: that key goes to the occupant, never to a lower-ranked candidate; pending keys of a longer sequence were already consumed and are not replayed. The only host action that declines today is `husk.home`, when the default layout is already shown and no rail item or stage mode is active; this is why `esc` reaches a plugin's main stage when there is nothing to go home from. A plugin action cannot decline: `action.invoke` has no such reply, and once dispatched the key is consumed.

### Built-in `husk` keymap

The host hard-codes no workbench key. The embedded file reproduces today's keys plus `shift+tab`, so focus cycling stays reachable while a surface captures text (`tab` is a text key). Every action it names has a catalog entry owned by the host, with a title, so each appears in the palette and in `husk action list`. The order of entries is load-bearing: `KeysFor` returns same-layer keys in file order, so the footer and rail show `f1` for `husk.home` because `f1` comes before `esc` and `alt+1`. Do not reorder the file to tidy it.

```json
{
  "version": 1,
  "bind": [
    {"key": "q",      "action": "husk.quit"},
    {"key": "ctrl+c", "action": "husk.quit"},
    {"key": "f1",     "action": "husk.home"},
    {"key": "esc",    "action": "husk.home"},
    {"key": "alt+1",  "action": "husk.home"},
    {"key": "ctrl+p", "action": "husk.palette.open"},
    {"key": ":",      "action": "husk.palette.open"},
    {"key": "alt+p",  "action": "husk.palette.open"},
    {"key": "v",      "action": "husk.hotspots.toggle_stage"},
    {"key": "ctrl+i", "action": "husk.hotspots.toggle_stage"},
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
    {"key": "tab",       "action": "husk.focus.next"},
    {"key": "shift+tab", "action": "husk.focus.next"},
    {"key": "?",         "action": "husk.modifiers.toggle_alt_latch"},
    {"key": "esc",    "focus": "modal-surface", "action": "husk.modal.close"},
    {"key": "ctrl+]", "action": "husk.capture.release"}
  ]
}
```

Host actions: `husk.quit`, `husk.home`, `husk.palette.open`, `husk.hotspots.toggle_stage`, `husk.hotspots.badges`, `husk.sidebar.toggle`, `husk.sidepanel.toggle`, `husk.docks.cycle`, `husk.layout.reset`, `husk.sidebar.narrower`, `husk.sidebar.wider`, `husk.focus.next`, `husk.modifiers.toggle_alt_latch`, `husk.modal.close`, `husk.capture.release`. The set is closed by this file; adding a host key means adding an entry here and a catalog entry in the frontend.

Landed in 2.5: with "Capture modes" the file gains `{"key": "ctrl+]", "action": "husk.capture.release"}` after the `husk.modal.close` entry, and `husk.capture.release` joins the host actions. It is appended, not inserted, so `KeysFor` order for every existing action is unchanged.

**Landed:** with the Settings center ([Settings center](settings-center.md)) two actions join the host actions, each with a catalog entry so it appears in the palette and in `husk action list`, and **no entry in the built-in keymap**: `husk.settings.open` (title `Open settings`; one optional argument `tab`, one of `general`, `keys`, `theme`, `store`, `installed`) and `husk.theme.select` (title `Select theme`; an alias that opens the center on its Theme tab, the theme selector of [Services](services.md), "The theme selector"). Neither gets a built-in key because every unmodified spelling is already spoken for or deliberately left to plugins, and both are reachable from the palette and, for the Theme tab, from the bottom bar. A user who wants a key writes `{"key": "alt+,", "action": "husk.settings.open", "args": {"tab": "keys"}}` (or any spelling, any tab) in a keymap layer; `husk keys --action husk.settings.open` shows what binds it. These are the first host actions without a built-in key; the rule that every host action has a catalog entry holds, the rule that every catalog entry has a built-in key never existed. The frontend catalog's rows (`builtinCommands`) are therefore the embedded keymap's actions plus `husk.settings.open` and `husk.theme.select`, and the test that pins the rows to the embedded file pins them to that set. Both decline (`contracts.ErrDeclined`) when they cannot open, the second and third host actions that do.

The unmodified letters and symbols (`q`, `h`, `v`, `?`, `[`, `]`, `/`, `=`, `<`, `>`, `,`, `.`, `:`) stay for a behavior-neutral migration (D83). Text capture keeps them out of a plugin's typing; a distribution that wants none of them overrides or drops the layer.

Widget-local keys are not keymap entries. The command palette and the slot inspector (hotspots stage) are host-owned surfaces; each handles the keys that reach it itself, as a plugin surface does. The closed sets: the palette's `up`, `down`, `ctrl+j`, `ctrl+k`, `tab`, `enter`, `backspace`, `ctrl+c`, text entry, and its close keys, which are `esc`, `ctrl+c` (which closes the palette rather than quits, as today), and every single-key, global, non-text-key spelling in `KeysFor("husk.palette.open")`, so `ctrl+p` and `alt+p` close it, `:` is typed into the query, a surface-scoped or multi-key opener never closes it, and the key that opens the palette still closes it after a rebind to a modified key; the inspector's `j`, `k`, `up`, `down`. **Landed** for the shell, the Theme tab and the profile corner (the General tab's keys land with that tab): the Settings center is a third, with a set per focused part ([Settings center](settings-center.md), "Focus and keys"): `tab`, `shift+tab` between its parts, `up`, `down`, `ctrl+k`, `ctrl+j`, `enter`, `right` and the digits `1`..`5` on its tab list, each tab's own keys in its content (the Theme tab's `up`, `down`, `ctrl+k`, `ctrl+j`, `pgup`, `pgdown`, `home`, `end`, `enter`; the General tab's huh form keys and `ctrl+s`), and its close keys `esc` and `ctrl+c` (no opener key closes it); every other key is ignored.

### Rail items, palette and footer

- A rail item is addressable as an action by its id ([Pods](pods.md)). Binding a key to a rail item id activates the item; pressing it while the item is active returns home, and closes the item if it is a modal.
- Rail buttons and palette rows show `KeysFor(action)[0]`, the effective key, or nothing. They never show a manifest `shortcut`.
- The footer shows, in this order, one key per action (`KeysFor(action)[0]`): `husk.home`; `Hints(ctx)`, at most four; `husk.layout.reset`; `husk.sidebar.toggle`, `husk.sidepanel.toggle` and `husk.docks.cycle` together; `husk.palette.open`; `husk.quit`. A group is left out when nothing binds its action. A dock key is shown only when pressing it changes the frame, and hint groups that do not fit are dropped whole from the end ([Workbench chrome](chrome.md)). A hint whose action is neither a `husk.*` action nor a rail item id is shown only while the action is in the frontend's action catalog (`Frontend.Actions`) with an owner that is `live` in `husk.pods`: the frontend filters the list `Hints(ctx)` returned, with the same liveness test the palette already applies to plugin actions; the service itself reads no State. The filter runs on the service's result, after its cap of four, so a dropped hint is not replaced by a fifth. A key bound in a config layer to a plugin that is loaded later thus appears in the footer when the plugin goes live and leaves it when the plugin exits.

### Reload

`husk config reload`, a profile change, and a plugin added or removed at runtime each rebuild the keymap through the Services coordinator ([Services](services.md)); the result swaps atomically. A plugin's process starts only after the reload that read its declarations completes ([Pods](pods.md), launch gate), so its keys exist before its surface can take focus. A plugin whose process merely exits keeps its declared `keymap` document ([Pods](pods.md)), but the Surfaces engine vacates its surfaces when its Pod leaves `live` ([State](state.md)), so `FocusOwner` no longer names it and its `focus-local` entries match nothing; `husk keys` lists them as `unavailable`. Should one still be dispatched (a surface-scoped entry whose `plugin` names a dead plugin cannot match, but a rebind of a dead plugin's action in a config layer can), the Dispatcher answers `unavailable` (see Failure modes). Removing the plugin from the instance undeclares its document and reloads.

### `husk keys`

`husk keys` is read-only and maps to `keys.list`, served by the Server ([Instances and the CLI](instances.md) owns flags, output format and exit codes). The Server handler joins `Listing()` with State (is the owner live) and the action catalog (does the action exist); the Keybinding service itself reads neither.

Callers: the CLI (user identity) receives the whole listing. A plugin receives the entries whose `plugin` is its id or whose `action` it owns, plus the entries in state `effective` (so neither shadowed nor tombstones) whose `action` is a `husk.*` host action or the id of a rail item some plugin declares (the service reads rail item ids from the same declared plugin documents it reads action owners from), and only its own problems. The host and rail-item keys are the ones the footer, the rail and the palette already display to everyone, so a plugin that renders help, a tour or a layout overlay can show the same keys; another plugin's own actions and the user's other bindings stay out of its view. Any other caller is denied.

Flags are client-side projections in both output formats: `--action <id>` and `--focus <surface>` exact-match entry fields and can be combined; focus selects only surface-scoped entries. `--shadowed` includes entries with `state:"shadowed"` (hidden by default); `--problems` prints only the unfiltered problems and rejects `--action` or `--focus` with exit 2. `keys.list` always receives `{}`. Default JSON is `{"entries":[...filtered],"problems":[...]}`; problems-only JSON is `{"problems":[...]}` and empty arrays are `[]`. Problems table columns are `SEVERITY CODE LAYER KEY SOURCE MESSAGE`. An empty selection, or `--problems` with no problems, prints nothing in table format. Unrecognized states are displayed verbatim; the CLI computes no liveness or catalog state.

Each entry has a `state`:

| State | Meaning |
|---|---|
| `effective` | Not shadowed by any entry that would always outrank it. |
| `shadowed` | Another entry outranks it in every context where it could match; `shadowed_by` names that entry's source. |
| `disabled` | A tombstone that is not itself shadowed. |
| `unavailable` | Effective, but the owning plugin is not live or declares no such surface. Computed by the handler. |
| `unknown_action` | Effective, but the action is not in the catalog. Computed by the handler. |

Static shadowing (the listing has no context): entry A shadows entry B when all three hold: A's sequence equals B's, or one is a strict prefix of the other (a shorter A matches first; a longer A turns B's completing key into `Prefix`), except that a tombstone A longer than B does not shadow B, because a tombstone is a candidate only for its own sequence (D92); A ranks above B by steps 2 to 4 of "Which binding wins" (scope, layer, `plugin` condition; never file order, so two entries in the same layer, scope and condition never shadow each other: `duplicate` and `unreachable` handle those); and every context that matches B also matches A, that is: both are global, or `A.focus == B.focus` and (`A.plugin` is empty or equals `B.plugin`). A global entry therefore never shadows a surface-scoped one, and a surface-scoped entry never shadows a global one, because each fires in contexts the other does not.

Table output (default on a TTY, here with `--shadowed`). `SOURCE` is the config-relative file and the entry's index in `bind`:

```
KEY      SCOPE                 ACTION               LAYER         SOURCE                        STATE
ctrl+g   global                notes.open           user          keymap.json#3                 effective
n        sidebar-list (files)  files.new_dir        user          keymap.json#8                 effective
n        sidebar-list (files)  files.new            focus-local   plugin files #0               shadowed by user keymap.json#8
[        global                (disabled)           user          keymap.json#14                disabled
[        global                husk.sidebar.toggle  husk          builtin#12                    shadowed by user keymap.json#14
q        global                husk.quit            husk          builtin#0                     effective
```

JSON output (the reply entries after the client-side filters above):

```json
{
  "entries": [
    {
      "keys": ["n"], "focus": "sidebar-list", "plugin": "files", "action": "files.new_dir",
      "source": {"layer": "user", "file": "keymap.json", "order": 8},
      "state": "effective"
    },
    {
      "keys": ["n"], "focus": "sidebar-list", "plugin": "files", "action": "files.new",
      "source": {"layer": "focus-local", "plugin": "files"},
      "state": "shadowed",
      "shadowed_by": {"layer": "user", "file": "keymap.json", "order": 8}
    },
    {
      "keys": ["["], "disable": true,
      "source": {"layer": "user", "file": "keymap.json", "order": 14},
      "state": "disabled"
    }
  ],
  "problems": [
    {
      "severity": "warning",
      "code": "global_not_allowed",
      "layer": "plugin:notes",
      "key": "[\"ctrl+n\"]",
      "message": "focus must be a surface owned by this plugin",
      "source": {"layer": "focus-local", "plugin": "notes", "order": 2}
    }
  ]
}
```

Entry fields are `keys`, `action`, `args`, `hint`, `focus`, `plugin`, `disable`, `source`, `state`, `shadowed_by`; absent or zero-valued optional fields are omitted. Each problem is a `protocol.Problem` ([Protocol](protocol.md), "Problem"; the Service mechanics are in [Services](services.md), "Snapshots, rebuilds and reports") serialized as is: `severity`, `code`, `layer`, `key`, `message`, `source`; `source` is the same `protocol.Source` struct as an entry's. A problem's `layer` is the layer name, or `plugin:<id>` for a focus-local problem. `source.file` is relative to the config directory of its layer; `husk keys` never prints an absolute path.

### Loader rules and problem codes

The loader runs on every rebuild. It never stops at the first error: every layer that parses contributes, every bad entry is dropped on its own, and every drop is a problem.

Loader order, which decides which problem an entry gets when more than one rule could apply:

1. Per document: `parse_error`, `unsupported_version`, `too_large`, then per entry `invalid_entry`, `invalid_key` or `unparsable_key`, and the per-document `too_many` cap.
2. `duplicate` within one layer.
3. Static shadowing between entries that differ in layer or in whether `plugin` is set, per the rule under "`husk keys`"; shadowed entries take no further part. Two entries in the same layer, scope and condition are never shadowed by each other here: an equal sequence is a `duplicate` (step 2), a prefix is `unreachable` (step 4).
4. `unreachable` among the entries still effective. A statically shadowed entry whose strict prefix is in the resolution set within the same layer, scope and condition also leaves the resolution set, with no problem.
5. The 4096 cap over the entries still effective. The static order for the cap is: scope (surface before global), layer rank, `plugin` set before unset, then `Source.Plugin`, then `Source.Order`; the last entries in that order are dropped.
6. `no_exit_binding`.

This is the complete list of loader codes, plus `invalid_plugin_catalog`, raised by the Service for a plugin's declared document that does not decode ([Services](services.md), "Snapshots, rebuilds and reports").

| Code | Raised by | Condition | Effect |
|---|---|---|---|
| `parse_error` | Store | The layer file is not valid HuJSON. | First load: the layer is absent. Reload: the last good document for that layer (and, for `profile`, that profile name) stays. |
| `unsupported_version` | loader | `version` is not `1`. | Same as `parse_error`. |
| `too_large` | loader | The document exceeds 256 KiB serialized. | Same as `parse_error`. |
| `invalid_entry` | loader | A field has the wrong type; both or neither of `key`/`keys`; `keys` outside 2 to 4; `action` missing without `disable`, or present with it; `action` without a `.`; `plugin` without `focus`; `hint` over 40 runes; `args` not scalar-only or over 1 KiB; `builtin` outside the distribution layer (key `builtin`); `bind` missing or not an array (key `bind`, rejects the file). | The entry (or file) is dropped. |
| `invalid_key` | loader | A key spelling in a layer file does not normalize. The message names the spelling and, for `shift+` symbols, the character to bind. | The entry is dropped. |
| `unparsable_key` | loader | The same, for a `focus-local` entry. | The entry is dropped. |
| `duplicate` | loader | Two entries in one layer have the same `(keys, focus, plugin)`. | The first stays effective; the later one is listed as shadowed by it. |
| `unreachable` | loader | Within one scope, layer and plugin condition, an effective sequence is a strict prefix of another effective sequence (a tombstone is not the shorter one). | The longer one is dropped. |
| `too_many` | loader | More than 2048 entries in one layer file; more than 128 in one manifest (raised by conversion, carried in the document's `problems`); or more than 4096 effective entries after shadowing. | Extra entries are dropped. Past the 4096 cap the last entries in the static order (loader step 5) go first, so a flood of low-layer entries cannot push out `profile` or `user` entries. |
| `global_not_allowed` | conversion | A manifest entry without `focus`, with a `focus` the plugin does not own, or with `plugin` or `disable`. | The entry is dropped. |
| `foreign_action` | conversion | A manifest entry whose `action` is neither the plugin's own nor `husk.*`. | The entry is dropped. |
| `cli_only_event` | conversion | A manifest subscribes to a `secrets.` or `services.` event pattern, which only `cli` watchers receive (carried in the document's `problems`; `index` is the position in the manifest's own subscription list and `key` names the member, for example `events.subscribe[1]`). | That subscription is dropped; the plugin's others stand. |
| `suggestion_ignored` | loader, from conversion's `suggestions` | A rail item or command carried `shortcut`. | Nothing is bound; the rail shows no key until a layer binds one. |
| `no_exit_binding` | loader | After ranking, no effective entry has action `husk.quit` or none has `husk.palette.open`. | The keymap is used as written; a toast at startup. Recovery: `husk action invoke husk.quit`, or a signal. |

### Failure modes at run time

| Condition | Who detects | What happens | What the user sees |
|---|---|---|---|
| Bound action is not in the catalog | `keys.list` handler; Dispatcher on press | The binding stays. The Dispatcher answers `not_found`. | State `unknown_action`; a toast with the action id on press. |
| Bound action's owner is not live | Dispatcher (State liveness) | No call is made. | State `unavailable`; a toast "owner not running" on press. |
| A plugin's surface-scoped binding traps its own surface (for example `esc` on its main stage) | nobody | Honored: that is what surface scope means. | The user adds a surface-scoped override or tombstone. |
| A user global binding seems dead on one surface | nobody | A surface-scoped binding outranks it there. | `husk keys --focus <surface>` shows the surface-scoped winner. |
| The occupant declares capture and a global unmodified key stops working on that surface | nobody | Intended. | `husk keys` is unchanged; the modified spelling still works. |

### Invariants

1. **Plugins create no global binding.** A manifest entry without `focus` has no effect on any key press.
2. **The focused surface wins over the rest of the screen.** A surface-scoped candidate always outranks a global candidate for the same keys, in every context. (D64)
3. **Within one scope, configuration outranks plugins.** `profile` > `user` > `distribution` > `focus-local` > `husk`, and no file changes the order.
4. **A tombstone unbinds exactly the sequence it names, in exactly its own scope** (D81, D92). It shadows lower-ranked entries whose sequence equals or begins with its own and that match in every context it matches, and nothing else: a longer tombstone never reaches a shorter sequence, and no tombstone reaches another scope.
5. **Resolve is pure.** The same layers and context give the same result; it reads no State, does no I/O, and holds no lock across a call out of the service.
6. **The host binds no workbench key outside the `husk` layer.** Input contains no key literal other than the closed widget-local sets of the palette and the inspector.
7. **A surface-scoped binding fires only while its surface has focus** and, when `plugin` is set, only while that plugin occupies it.
8. **Every dropped or ignored entry is reported.** Each produces exactly one problem, visible in `husk keys --problems`.
9. **The palette consumes every key while open.** No binding fires and no `input.key` reaches a plugin.
10. **An unbound, disabled or declined key reaches the occupant unchanged**, except while Frozen (nothing is forwarded) and except the widget-local sets of the palette and the inspector, which consume their own keys first.
11. **A capturing surface keeps plain typing.** No global binding on a text key fires while the focused occupant captures; modified keys and `esc` still resolve.
12. **A reload swaps atomically.** A lookup sees the old keymap or the new one, never a mix.
13. **Husk never writes a keymap file.** No command copies, adopts or generates bindings into any layer.

### Limits

| Limit | Value | Reason |
|---|---|---|
| Keymap document size | 256 KiB | A hand-edited keymap is a few KiB; the cap bounds parse time and memory. |
| Entries per layer file | 2048 | Same. |
| Entries per manifest | 128 | A plugin is a surface, not a keyboard. |
| Effective entries after ranking | 4096 | Bounds `husk keys` output and the effective set; the resolution index also holds statically shadowed entries, which the per-file limit of 2048 bounds. |
| Keys per sequence | 4 | Longer sequences are unusable by hand. |
| Sequence timeout | 1500 ms (Input's clock) | Long enough for a deliberate sequence, short enough that a stray prefix does not trap input. |
| `hint` length | 40 runes | The footer has fixed room for four hints. |
| Footer hints | 4 | Unchanged from today. |
| `args` size | 1 KiB serialized, scalars only | Params for an action, not a payload. |
| `Resolve` budget | 1 ms | It runs on the UI loop for every key. The index is a map keyed by first key. |
| Release suspension (Level 1, "Capture modes") | one key or 1500 ms | The release key frees exactly the next key; a forgotten release must not leave a terminal half-captured. |

### Dependency directions

- The Keybinding service reads Store (`Layers("keymap", profile)`, `Declared("keymap")`) and the declared `plugins` documents for action and surface ownership. Store never imports Services.
- Manifest conversion (Pods) owns the surface-scoped rules and never parses key spellings. The service owns key parsing and never re-checks ownership.
- Input reads the Keybinding service through the frontend contract; the service never imports Input, the Dispatcher, State or the Server.
- Input sends `Match` results to the Dispatcher as an action id plus params.
- The Server's `keys.list` handler imports the Keybinding service, State and the action catalog and joins them. The CLI reaches the keymap only through the Server.
- The footer, palette and rail call `KeysFor` and `Hints`; none reads a layer document.

### What this exposes to the CLI

- `keys.list` (backing `husk keys`): entries, their layer, source and state, and the load problems.
- `config.reload` rebuilds the keymap with every other service cache.
- No write path. A keymap changes by editing a file and reloading.

## Level 2: Migration from today

The read-only `husk keys` command is landed, including filtering, source/state tables and problems-only output. The handler's liveness/action-catalog join for `unavailable` and `unknown_action` remains separate work; the CLI already renders those states when supplied.

| Today (`main`) | Change |
|---|---|
| `pkg/services/keybinding`: ranks layer first, then specificity (`stronger`, `dominates`) | Rank scope first, then layer, then `plugin` condition, then order, per "Which binding wins". Static shadowing per "`husk keys`". **Landed**: `stronger`, `shadows`, `Resolve`, `Hints`, `KeysFor` and the no-host fallback in `cmd/husk/keys.go` follow it; `unreachable` is per layer, scope and plugin condition; a tombstone is not a prefix there. The resolution index keeps statically shadowed entries so the Frozen and modal filters see the spec's candidate set. |
| `KeyContext` has no capture bit; `surface.update` has no `capture` field; `husk.surfaces` has no `capture` map | Add `Capture`; filter global text-key candidates when set. `surface.update` gains `capture` (minor 2.1); the Surfaces engine records and clears it (D93); the frontend reads `husk.surfaces.capture`. **Landed.** |
| `pkg/services/keybinding/keybinding.go` `Resolve`: a tombstone longer than `K` returns `NoMatch`; the Frozen and modal filters drop tombstones | A tombstone is a candidate only for its exact sequence (D92); tombstones survive the Frozen, modal and Capture filters. **Landed**: the exact-sequence rule, and tombstones survive the Frozen, modal and Capture filters. |
| `cmd/husk/input.go`: no sequence timeout; the key that breaks a sequence goes to the occupant without being resolved alone; pending keys survive focus and overlay changes | Add the 1500 ms timer; resolve the breaking key on its own; discard pending keys on focus, modal, palette and paste events. **Landed**: the timer rides a generation counter on an injectable `after` seam, and every discard goes through one `clearPending`; an occupant change seen in `refresh` discards too. |
| `cmd/husk/input.go`: `husk.home` falls through by a special case (`homeApplies`); `ActionReply` has no decline carrier; `contracts.Frontend.InvokeAction` takes no caller kind or focus (every call is the Stem caller with State's focus) and its callback is `reply func(result map[string]any, err error)`, with no promise of a synchronous callback for host actions | `ActionReply` gains `Declined bool`; `Frontend.InvokeAction` becomes `InvokeAction(action, args, kind protocol.CallerKind, focus *protocol.FocusContext, reply)` with Input passing `CallerInput` and its own focus snapshot and host-started actions passing `CallerHost` and nil ([Pods](pods.md), Frontend seams); its callback becomes `reply func(result map[string]any, declined bool, err error)` and carries the flag; the `husk.home` handler sets it; the Dispatcher runs host handlers inline so the `InvokeAction` callback fires before it returns for host actions, and maps `Declined` to an empty success reply for wire callers; Input treats a declined reply as `NoMatch`. **Landed**: `husk.home` declines through `contracts.ErrDeclined`, `Input` has no `husk.home` special case, and the wire mapping lives in the Calls port, not the Dispatcher. |
| `cmd/husk/input.go`: the palette closes on `esc`, `ctrl+p`, `ctrl+c` | The palette closes on `esc`, `ctrl+c` and every single-key, global, non-text-key spelling in `KeysFor("husk.palette.open")`. **Landed**: Input asks `Resolve` with an empty context, which admits only global candidates. |
| Built-in keymap has no `shift+tab` | Add `shift+tab` → `husk.focus.next`. **Landed.** |
| `KeyContext.Capture` is a bool; `husk.surfaces.capture` holds `true`; no release key; mouse on a surface is routed by the frontend's own hit rules | `Capture` becomes `CaptureMode` (`none`, `text`, `all`); `capture` on the wire accepts `"text"`/`"all"` (minor 2.5) and the slice stores the mode; `husk.capture.release` and its one-key suspension; mouse and paste on an `all` surface go whole to the occupant (a terminal surface's emulator encodes them). **Landed in 2.5** ("Capture modes"). |
| `cmd/husk/commands.go`: `builtinCommands` lists six host actions | List every action the built-in keymap names, with titles. **Landed**; a test pins the rows to the embedded file. With the landed Settings center shell the rows are the file's actions plus `husk.settings.open` and `husk.theme.select`, and the test pins that set. |
| Loader reports a document over 256 KiB as `too_many` | Report `too_large`. **Landed**; a missing `bind` also names the key `bind`. |
| Conversion accepts `focus_bindings` as an alias of `keybindings` | Accept `keybindings` only. **Landed**: a manifest that still spells `focus_bindings` gets an `ignored_field` warning and binds nothing. The declared `plugins` catalog field is `keybindings` too. The service no longer re-checks focus-local ownership, scope or the 128-entry cap; conversion owns them. |
| Declared document name `focus-keymap` | `keymap` (D64; rename unit). |
| Resolution index holds effective entries only | **Landed**: it holds every entry except duplicates, `unreachable` and cap-dropped ones, and statically shadowed entries whose strict prefix is in the resolution set within the same layer, scope and condition (no extra problem). The 2048 bound is per file, so the index grows with the plugin count (128 entries each). If the 4096 cap drops a shadower, the entries it shadowed keep their `shadowed` state but resolve in its place. `Hints` reads effective entries and skips any whose proper prefix already matches. |
| `docs/docs/architecture.md`, "Input routing" | Says layer order decides; now scope decides first. **Landed.** |

Behavior changes users can notice, compared with the layer-first draft of this spec:

1. A plugin's surface-scoped binding wins over a user or distribution global binding on the same key while that surface has focus. The user takes a key back with a surface-scoped entry.
2. Unmodified host keys stop firing inside a surface whose occupant declared text capture.
3. A user global tombstone no longer silences a plugin's surface-scoped key; a surface-scoped tombstone does.

## Decisions made here

1. **HuJSON for keymap files**, like every other layer document ([Store](store.md), decision 1). One parser for all documents.
2. **Scope first, layer second** (operator ruling D64, order fixed by D81). This is the Emacs and Vim model: a buffer-local map beats the global map whoever set it. The layer-first alternative (VS Code: user rules appended after defaults, last match wins) made every plugin key on a surface hostage to any global binding and forced users to repeat the plugin's keys per surface to get them back. The shadowed listing in `husk keys` shows what each entry hides.
3. **`plugin` is a match condition, not a rank level.** Two scopes (global, surface) keep the ranking explainable; within one layer and scope a conditioned entry wins over an unconditioned one, so a user can write both without a duplicate.
4. **A tombstone is a candidate that resolves to nothing, for its exact sequence only** (D81, D92), with no separate removal rule. Under scope-first ranking, "removes lower-layer entries in its scope" and "shadows them" are the same statement, so the earlier two-part Disable rule is gone. A global tombstone does not reach a surface-scoped binding: D39 is narrowed by D81 to "a user surface-scoped disable outranks a plugin's focus-local binding; a global one does not". A tombstone never unbinds a prefix: D40's "disabled chords are not prefixes" is narrowed by D92 to exact-leaf removal, as Vim's `:unmap` and VS Code's `-command` remove exactly the sequence named. A tombstone blocks lower layers rather than letting them show through, which is Emacs's `undefined` binding, not its `nil` (a `nil` in a higher map falls through to lower maps).
5. **Text capture is a context bit set by the occupant per `surface.update`** (D82), not a manifest flag and not an input mode. A manifest flag cannot say "only while this text field is active"; a mode would make Husk an editor. The text-key set is closed and listed. Absent means unchanged, `false` turns it off, and the engine clears it on occupant change, release and Pod exit (D93).
6. **The built-in keymap keeps today's keys, including unmodified letters** (D83). Capture solves the typing problem; distributions solve the rest. `shift+tab` is added so focus cycling survives capture.
7. **A distribution may drop the built-in layer with `builtin: false`.** Overriding entry by entry is not enough for an application that wants none of the host keys.
8. **One distribution layer, no instance layer**, matching Store.
9. **Sequences of up to four keys as a `keys` array, 1500 ms timeout owned by Input, no replay.** The array form exists because `" "` is a key. The key that breaks a sequence is resolved on its own so that a bound key fires and an unbound key reaches the occupant; `esc` is the one exception, consumed as the cancel key without being resolved.
10. **No chord cancel on reload.** The next key is resolved against the current keymap; a rule for it would add a wire between the coordinator and Input for no observable gain.
11. **Palette and inspector keys stay widget-local.** They are the input handling of two host-owned surfaces.
12. **Modal grab considers only `modal-surface` scope plus the modal's own close action.**
13. **Manifest `shortcut` fields are suggestions, reported and ignored.** Otherwise a plugin could still declare a global key through the rail.
14. **A foreign action in a manifest binding is dropped.** A plugin binds its own actions and `husk.*` only.
15. **Conversion owns the manifest rules; the service owns key parsing.** One owner per rule; two codes (`global_not_allowed`, `foreign_action`) whose messages say what was wrong (D27).
16. **`keys.list` computes availability in the Server handler**, keeping `Resolve` pure.
17. **Parse failure on reload keeps the last good document for that layer and profile name** (D40). At first load the layer is absent instead.
18. **No key is protected from remapping, including quit.** A warning fires when no exit binding remains; the CLI and signals recover.
19. **Host actions may decline a key** (D84), through the host-only `Declined` flag on `ActionReply`. Needed so `esc` reaches a plugin's surface when there is nothing to go home from; stated as a rule with one seam instead of a special case in Input, and invisible on the wire.
20. **Tombstones survive the Frozen, modal and Capture filters.** A filter narrows which bindings may fire; a user's decision that a key fires nothing must hold under every filter, or the user cannot unbind `q` for the frozen screen or a modal's close key.
21. **The palette's close keys follow the single-key, global, non-text-key spellings in `KeysFor("husk.palette.open")`.** Otherwise a rebind leaves `ctrl+p` closing a palette that `alt+x` opens. Text keys are excluded so the built-in `:` opener can be typed into a query; surface-scoped and multi-key openers are excluded because the palette is global and receives one key at a time.

## Open questions

1. **Modes.** D82 keeps Husk mode-free and puts the editing fact in one capture bit set by the occupant. The operator asked about normal/insert modes (Q21); if Husk should own modes, capture is replaced by a mode fact in `KeyContext` and the text-key filter keys off the mode. Until then "Text capture" is the contract.
2. **The release key's spelling.** `ctrl+]` follows `telnet` and is free in the built-in keymap; `ctrl+\`` is the other common choice (it is `NUL` on some terminals and unreliable under tmux) and a prefix key such as `ctrl+b` would collide with the multiplexers people run Husk inside. Recommended: `ctrl+]`, rebindable like every host key.
3. **Should `all` capture also hold `husk.quit`'s `ctrl+c`?** The spec says yes: a terminal application needs `ctrl+c` for its own interrupt, and `ctrl+] ctrl+c` still quits. Recommended: yes; the alternative, a hard-coded exception, is a second release key by another name.
4. **Focus-local bindings on runtime surfaces.** A manifest `keymap` entry's `focus` names a layout surface id; a runtime surface's id is not known at conversion. Recommended: allow `focus: "<plugin_id>.*"` (any of the plugin's own runtime surfaces) as the one pattern form, when the first plugin needs it.
