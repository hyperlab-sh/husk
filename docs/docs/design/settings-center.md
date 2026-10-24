---
type: Design Spec
title: "Settings center"
description: "One host-owned modal with tabs on the left (General, Keys, Theme, Plugins with Store and Installed) and the current profile in the top-right corner; forms built with huh over the existing Services and Pods methods; the catalog document the Store tab reads."
status: planned
generated: { by: claude/fable-5-1, at: 2026-10-23T22:00:00Z }
tags: [architecture-v2, frontend, services, pods]
---

# Settings center

**Landed:** the center shell, host actions, bottom-bar entry point, all five tabs (General, Keys, Theme, Store and Installed), profile corner, host seams and catalog reader.

## Level 0: What and why

Husk has, or has specified, every method a settings screen needs: `settings.schema` says which keys exist and of what type, `settings.get` and `settings.set` read and write one key, `keys.list` tells which key does what, `theme.list` names the themes, `profile.list` and `profile.use` pick a profile, `husk.pods` says which plugins are loaded and `plugin.remove` unloads one ([Services](services.md), [Keybindings](keybindings.md), [Pods](pods.md)). What it does not have is one place in the TUI where a user sees them. Today a user who wants to change a setting edits a file; the theme selector of [Services](services.md) ("The theme selector") was the first screen to change one from inside Husk, and it was specified as a modal of its own.

The **Settings center** is that one place. It is core frontend chrome, a host-owned modal like the command palette, drawn on the modal surface, with **tabs on the left**: General, Keys, Theme, Plugins (with the sub-tabs Store and Installed). The **current profile is shown in the top-right corner of every tab** and can be switched there. The theme selector **becomes the Theme tab**: same list, same `settings.set` of `theme.name`, same failure surface, and no second modal. Forms are built with `charmbracelet/huh` so the screen stays in the Charm realm the rest of the frontend lives in; huh is used inside the center and nowhere else in core.

Operator ruling (2026-10-23, final): one surface, tabs on the left (General, Keys, Theme, Plugins with Store and Installed), the current profile top-right and always visible, forms built with huh.

What the center is **not**:

- Not a new write path. Every change it makes goes through a method that already exists and keeps that method's rules: `settings.set` writes one key into the user layer and reloads; `profile.use` switches the instance's profile without saving it; `plugin.remove` unloads without uninstalling. The center adds no method and no wire type; it adds one host action, one Store document and a handful of frontend seams.
- Not a store. Husk ships no catalog of plugins and downloads nothing. The Plugins → Store tab is a **catalog view** over a `catalog` document the distribution or the user supplies; the built-in catalog is empty and the tab says so, with the file to fill. The mechanism is Husk's, the catalog is the consumer's.
- Not a keymap editor. The Keys tab is read-only in v1 because no keymap write method exists; the follow-up is named under Open questions.
- Not a plugin. Like the palette and the theme selector before it, it is drawn by the frontend from host seams; nothing in it is reachable by a plugin, and no plugin can open it.

Who depends on it: nobody in the host. It depends on the Services methods above, on the Pods seams for the plugin list and removal, on the Store `catalog` document this spec defines, and on the modal placement and key rules the palette already follows.

## Level 1: Contract

### Terms

| Term | Meaning |
| :--- | :--- |
| **center** | The Settings center modal. Exactly one exists per frontend; it is open or closed. |
| **tab** | One of the five content panes: `general`, `keys`, `theme`, `store`, `installed`. `store` and `installed` are drawn as sub-tabs of a `Plugins` heading in the tab list; everywhere else (the action argument, State, this spec) they are tabs like the other three. |
| **tab list** | The column on the left that names the tabs. |
| **content** | The pane to the right of the tab list showing the active tab. |
| **profile corner** | The top-right cell group of the modal showing the active profile's name. |
| **field** | One editable setting in the General tab, bound to one settings key. |
| **dirty** | A field whose value differs from the value the snapshot holds and has not been saved. |
| **catalog** | The layered Store document `catalog` ("The catalog document"). |
| **catalog entry** | One plugin a catalog describes: id, name, description, surfaces, source, tags. |

### Opening and closing

The center is opened by the host action **`husk.settings.open`** ([Keybindings](keybindings.md), host actions), which takes one optional argument, `tab`, one of `general`, `keys`, `theme`, `store`, `installed`; any other value is `invalid_params`. Without `tab` the center opens on the tab it was last on in this process, `general` the first time. The action is reachable from the palette (catalog entry title `Open settings`), from `husk action invoke husk.settings.open --arg tab=theme`, and from any key a keymap layer binds to it; the built-in keymap binds **no key** to it, for the reason [Keybindings](keybindings.md) gives for `husk.theme.select`: every unmodified spelling is spoken for or deliberately left to plugins, and the center is one palette entry away. A user writes `{"key": "alt+,", "action": "husk.settings.open"}` (or any spelling, with `"args": {"tab": "keys"}` to land on a tab) in a keymap layer.

Two more entry points open it on the Theme tab. **`husk.theme.select`** stays a host action with its catalog entry (`Select theme`) and no built-in key; it is an alias that runs `husk.settings.open` with `tab: theme`, kept so a keymap entry written for the selector keeps working. **The theme name in the bottom bar** (the right slot that shows the served palette's name; its content, clipping and hit rect are the frontend layout's) runs the same action with `tab: theme` on a left click.

The action does nothing while the terminal is frozen or while the palette or the secret prompt is open: its handler (registered through `Frontend.RegisterHostAction`) returns `contracts.ErrDeclined`, so a bound key goes to the occupant like any declined key and a `cli` `action.invoke` gets an empty success reply ([Keybindings](keybindings.md), the decline mechanism), exactly as `husk.theme.select` does. While the center is **already open**, `husk.settings.open` with a `tab` switches to that tab and without one does nothing; it never closes the center (so a bound key is not a toggle; `esc` is the close). Changing tabs dismisses any Remove confirmation or surface Select belonging to the previous tab, so no hidden dialog can consume keys or authorize removal. Opening writes `overlays.settings_center` in `husk.surfaces` ([State](state.md)) with the active tab's id, and every tab switch rewrites it.

Modals replace one another; they never stack. An explicit `action.invoke` of a rail item with modal placement, from the CLI or any other caller, first closes the center and then opens that modal. Closing this way has the same semantics as `esc`: unsaved edits are discarded without a prompt and pending replies are ignored. Conversely, `husk.settings.open` (including its `husk.theme.select` alias) closes any open plugin or runtime modal before opening the center. The resulting State view contains only the new modal overlay, and subsequent keys go only to it.

Closing: `esc`, `ctrl+c` (closes the center, never quits, as for the palette), a click outside the modal (except the theme pill, which switches to Theme even while the center is open), and the frozen-terminal transition each close it and write `overlays.settings_center: ""`. Closing **discards dirty fields with no prompt** in v1 (operator ruling); the footer says `esc close (discards edits)` while any field is dirty. A pending reply (a `settings.set`, `profile.use` or `plugin.remove` the center is waiting on) is dropped when it arrives; the write or removal it may have made stands, exactly as for the theme selector. A `profile`, `plugin` or `settings` reload while the center is open does not close it; the center invalidates and refreshes tab sources as described in "Reloads while open".

### Layout

The modal follows the placement rules of the frontend layout for a host modal (centred on the frame, over the default layout, the layout label reads `Modal`). Its size is the smaller of the terminal less two cells each side and 100 by 32 cells; at the 84 by 24 minimum it is 80 by 20. Inside the border:

```
┌ Settings ─────────────────────────────────────── profile: work ▾ ┐
│ General    │ husk                                                │
│ Keys       │   bundle               [                           ]│
│ Theme      │ husk.plugins                                        │
│ Plugins    │   paths                [/home/u/plugins/alpha      ]│
│   Store    │                        restart required             │
│ ▸ Installed│   dirs                 [                           ]│
│            │                        restart required             │
│            │ profile                                             │
│            │   default              [work                       ]│
│            │ theme                                               │
│            │   name                 [evening                    ]│
│            │   allow_low_contrast   [ ] no                       │
│            │   palette.accent       [#7d56f4                    ]│
│ tab/shift+tab focus · 1-5 tabs · ctrl+s save · esc close         │
└──────────────────────────────────────────────────────────────────┘
```

- **Title row.** The word `Settings` at the left of the top border; the **profile corner** at the right: `profile: <name> ▾`, where `<name>` is the entry of `profile.list` with `active: true`, or `default` when none is active (an empty active profile is what [Services](services.md) calls "no profile"; the corner shows `default` because that is what a user reads it as). The corner is always drawn, on every tab, clipped to 24 cells with a `…` tail.
- **Tab list.** 13 cells wide (one cell of margin, the marker, a space, the longest name `Installed` at 9, then its divider), one row per tab: `General`, `Keys`, `Theme`, `Plugins`, then `Store` and `Installed` indented under it. `Plugins` is a heading, not a tab: moving onto it lands on `Store`. The active tab is drawn with `surface_selected` and a `▸` marker; the list has focus when its row text is `accent`.
- **Content.** The rest of the width, the tab's pane ("Tabs"). It scrolls when taller than the modal; the scroll position is per tab and resets on close.
- **Footer.** The last inner row: the key hints for the focused part, key in `ink` bold, description in `ink_muted` (keys are never accent-coloured: [Workbench chrome](chrome.md), "Colour discipline", rule 2).

Colours are palette roles ([Protocol](protocol.md) `Palette`): the modal is `surface_elevated` with `border_focused`, rows on `surface_elevated`, text `ink`, muted text `ink_muted`, the cursor row `surface_selected`, a problem or warning line in `ink` after a status glyph, `✕` in `danger` or `⚠` in `warning` ([Workbench chrome](chrome.md), "Colour discipline", rule 4). One-word status cells (`live`, `installed`, `restart required`, `taken`) are status badges; glyphs and badges are drawn by the frontend's one shared status helper, the same one the secret prompt uses; `accent` marks only the focused tab label, the focused form field or plugin group heading, the focused profile corner and the Theme tab's current-theme marker. huh fields take a huh theme built from the same roles ("huh"), so a theme change recolours the center with the rest of the frame.

### Focus and keys

The center is a modal in the palette's key model: while it is open `Resolve` is not called and the center consumes every key ([Keybindings](keybindings.md), context filters). Three parts can hold focus: the **tab list**, the **content** and the **profile corner**. `tab` moves focus tab list → content → profile corner → tab list; `shift+tab` the other way. General uses individual huh fields, not a `huh.Form`. Its traversal targets are, in display order, each plugin group heading and each visible field (the fields of a collapsed plugin group are skipped). The center moves between them with `tab` and `shift+tab`, from the **last** target to the profile corner and from the **first** target to the tab list, without forwarding traversal or submission to huh. Entering General from the tab list starts at its first target; entering backwards from the profile corner starts at its last target, so fields and center parts form one tab order. Fields stay live until a reload changes the schema, a save finishes, or the palette/ANSI theme changes; rebuilding keeps the active field by key when it still exists. No completion event submits or rebuilds them.

The closed key set, by focused part:

| Part | Key | Effect |
| :--- | :--- | :--- |
| any | `esc`, `ctrl+c` | close, discarding dirty fields (while a profile Select, a Remove confirm or the Store surface Select is open, `esc` closes only that dialog; `ctrl+c` always closes the whole center) |
| any | `tab`, `shift+tab` | move focus between parts, as above; while one of those dialogs is open, first dismiss it without answering (the profile Select makes no call, the Remove confirm removes nothing, the surface Select restores its previous surface), then move focus |
| tab list | `up`, `down`, `ctrl+k`, `ctrl+j` | move the marker; `Plugins` is skipped |
| tab list | `enter`, `right` | activate the marked tab and focus the content |
| tab list | `1`, `2`, `3`, `4`, `5` | activate General, Keys, Theme, Store, Installed; digits jump only while the tab list has focus, so a digit typed into a field stays a digit |
| content | everything else | the tab's own keys ("Tabs"); in the General tab the individual huh field's keys |
| Installed or Store content | `up`, `down`, `ctrl+k`, `ctrl+j` | move the cursor one row (Installed: the table row; Store: the highlighted entry, whose detail pane follows) |
| Installed or Store content | `home`, `end` | move the cursor to the first or last row |
| Installed or Store content | `pgup`, `pgdown` | move the cursor by half the content height |
| Installed content | `enter`, `x` | open the Remove confirm for the cursor row ("Plugins → Installed") |
| Store content | `enter` | open the surface Select of the filter row; it never acts on the highlighted entry (the Store installs nothing) |
| Store content | a printable rune, `backspace` | append it to the text query, or remove the query's last rune; every printable rune is text here, digits and `x` included; a bracketed paste appends its text, line breaks and tabs as spaces and other control characters dropped (a paste does nothing while a dialog is open) |
| Remove confirm open | `left`, `right`, `h`, `l`, `y`, `n`, `up`, `down` | move between `yes` and `no` (huh `Confirm`); `enter` answers with the chosen button; `esc` returns to the table |
| surface Select open | `up`, `down`, `ctrl+k`, `ctrl+j`, `home`, `end` | move the highlighted option and apply it to the list at once; `enter` commits it; `esc` restores the surface chosen before the Select opened |
| General content | `ctrl+s` | save dirty fields ("General") |
| General content | `enter`, `space` on a plugin group heading | expand or collapse that group |
| General content | `enter` on any field but a `strings` `Text` | move to the next target, as `tab` (from the last target, to the profile corner); a `Text` field keeps huh's own new-line keys |
| profile corner | `enter`, `space` | open the profile Select ("The profile corner") |

Mouse: a click on a tab row activates it; a click in the content focuses it and is handed to the tab's list rows. General field clicks focus only (they do not toggle a Confirm or pick a Select option); its plugin-heading clicks expand or collapse the group, and its wheel steps between field/group targets, bringing the active target into view. Other tabs scroll their focused part with the wheel. In the Store tab a click on the filter row opens the surface Select and a click on a list row highlights that entry. While a Remove confirm or the surface Select is open, the vertical wheel acts as `up`/`down` on it and a horizontal wheel does nothing; a click on the confirm's `yes` or `no` answers it, and any other click inside it does nothing; a click inside the open surface Select dismisses it, restoring its previous surface, without picking the option under the pointer (options are picked with the keys). A click on the profile corner opens the Select; a click outside the modal closes the center, except on the theme pill ("Opening and closing"). Dismissing a plugin confirmation or surface Select with an outside click lets that same click activate the visible tab or control underneath. Every other key is ignored; nothing reaches `Resolve` or a plugin.

### Tabs

#### General

**Landed:** schema-driven huh fields, ordered groups with collapsed plugin schemas, explicit sequential saves, per-field outcomes and dirty-preserving reloads. The center traverses individual huh fields without a `huh.Form` and drops submission commands; the external-editor path is disabled. Schema changes, completed saves and palette/ANSI changes rebuild fields while retaining the active key; ordinary edits and cursor changes only refresh cached field views on Update.

Every settings key with a schema entry, as `settings.schema` lists them, edited with an individual huh field. The rows are **grouped by prefix**: the group of a key is the key minus its last segment (`husk`, `husk.plugins`, `theme`, `theme.palette`, `profile`, `dist`, `dist.<x>`, ...), groups sorted by name and keys sorted inside each; the group name is a heading and the field label is the last segment, with the entry's `Doc` line in `ink_muted` under it. Host keys (`husk.*`, `theme.*`, `profile.*`) come first, then the distribution's `dist.*` groups (same mechanics; the user may write them and `settings.set` accepts them, so leaving them out would hide keys the user owns), then one **collapsible group per plugin**, `plugins.<id>`, collapsed by default and showing only the keys that plugin's schema declares (a plugin whose schema was not registered has no group; its raw keys are `settings.list`'s business). `theme.name` is listed like any string key; the Theme tab is the comfortable way to change it, the field is the exact one.

The huh field for each schema type:

| Schema `Type` | huh field | Value shown | Value written |
| :--- | :--- | :--- | :--- |
| `string` | `Input` | the string | the string as typed |
| `bool` | `Confirm` | `yes`/`no` | `true`/`false` |
| `enum` | `Select` | one option per `Enum` value, in schema order | the chosen string |
| `int` | `Input` | the number | the integer parsed from the text; non-integer text or a value outside `Min`/`Max` is refused inline before any write (huh `Validate`) |
| `duration` | `Input` | the integral milliseconds, label suffixed `(ms)` | the integer; `Min`/`Max` as for `int` |
| `strings` | `Text` | one entry per line | the lines, trimmed, empty lines dropped |

A field starts from the snapshot value `settings.get` returns for the key (the value in force for a `restart` key), with the layer that supplied it after the label when it is not `user` or `default` (`from profile`, `from distribution`, `from instance`), because writing the user layer will not change what a higher layer sets ([Services](services.md), "Writing one key"); the field is still editable and the reply's reports say what happened.

**Saving.** Editing a field makes it dirty (marked `*`); nothing is written until the user saves. `ctrl+s` saves the dirty fields of the General tab: one `settings.set` per key through the frontend seam (`SetSetting`, [Pods](pods.md) "Frontend seams"), **sequentially in key order**, each call waiting for its reply before the next starts, so each reload answers one write and its reports are attributable to one key; the form stays usable but a second `ctrl+s` while a save runs is ignored. Per field, when its reply arrives:

- A result whose Settings report has no `error` problem for this key: the field is clean, its value is the reply's `value`; when the report carries a `restart_required` problem for the key, the line `restart required` is drawn under the field in `warning` and stays until the process restarts (the value in force did not change; [Services](services.md), "Validation").
- A result whose Settings report has an `error` problem for this key (a value the rebuild refused that pre-validation did not catch), or an error reply (`invalid_params` `invalid_value`, `not_found` `unknown_setting`, `unavailable` `store_error`, `timeout`, `cancelled`): the field stays dirty and shows `<code>: <message>` under it in `ink` after a `✕` in `danger`, from the problem (its `Message`) or from the error. The user edits and saves again, or closes and discards.

The save continues past a failed key to the next dirty key: each write is independent, and stopping would hide the outcome of the others. A reply that arrives after the center closed is dropped. Only dirty fields are written: a save with none does nothing. The Theme report's problems are not shown here (they are the Theme tab's); the Settings report's `invalid_value` for another key than the one written cannot happen, since `settings.set` writes only a value that validates in isolation and the rest of the document did not change.

Not in v1, said so the implementer does not add it: a Save button (the key is enough and huh has no button field), per-field revert (close and reopen), a "reset to default" (there is no removal in `settings.set`; [Services](services.md) open question 3), batch writes (one reload per key is the cost of one-key semantics; the Services open question on batching is where that changes).

#### Keys

**Landed:** the full read-only listing, muted shadowed rows, palette-compatible query input, scrolling and bounded diagnostics.

Read-only. One row per entry of `keys.list` (the full listing, as a `cli` caller sees it, through the seam `KeyListing()`), in the listing's order, with the columns `KEY` (a sequence space-joined, `ctrl+k ctrl+s`, as `husk keys` prints it), `SCOPE`, `ACTION`, `LAYER` (the owner: `husk`, `distribution`, `user`, `profile`, `instance`, `focus-local` with the plugin id) and `STATE` (`effective`, `shadowed by <source>`, `disabled`, `unavailable`, `unknown_action`; the handler's states, [Keybindings](keybindings.md) "`husk keys`"). Shadowed entries are **shown** (the center is where a user wonders why a key does nothing), in `ink_muted`. A **filter box** above the table (the palette's query line component, not a huh field, because it filters as the user types) keeps rows whose key spelling, action or plugin contains the text, case-insensitive. The listing's problems are shown under the table, `severity: code: message`, at most 20 with `and <n> more`.

No editing: no method writes a keymap layer in v1. A user who wants a different key edits `keymap.json` and reloads; the tab's footer says so (`edit keymap.json to change a key`). The follow-up is a `keymap.set` sibling of `settings.set` (Open questions).

#### Theme

The theme selector of [Services](services.md) ("The theme selector"), as a tab. Its list, selection, failure surface and tests are specified there and are not restated; what changes by being a tab:

- It opens as the center on `tab: theme`, by `husk.theme.select`, by the bottom-bar click and by `husk.settings.open` with the argument. There is one modal, not two.
- Its keys are the content keys of this center while the Theme tab is active: `up`, `down`, `ctrl+k`, `ctrl+j`, `pgup`, `pgdown`, `home`, `end` move, `enter` selects; `esc` and `ctrl+c` close the whole center, as "Focus and keys" says, and `tab` moves focus to the profile corner rather than being ignored.
- A successful selection (the Theme report `applied: true`, no `unknown_theme`) **closes the center**, as it closed the selector: the user came to change the theme and is done. A refused or unapplied write, or one a higher layer shadows (`set in <layer> layer, which wins`), keeps the center open on the Theme tab with the selector's lines.
- The rows: the cursor row is drawn on `surface_selected`; the **current theme** (the entry `theme.list` marks `current`) carries a `●` marker in `accent` before its name, as the selected item among its siblings, whether or not the cursor is on it. Other rows carry a blank cell in the marker's place so names align.
- `overlays.theme_selector` no longer exists; `overlays.settings_center` is `theme` while this tab is active ([State](state.md)).

#### Plugins → Installed

**Landed:** State-driven rows, claimed versus held surfaces, source directories, status badges and confirmed instance-only removal with pending and error feedback. The pane caches its derived content until its State, input, size or theme changes.

The live plugins of this instance: one row per entry of `husk.pods.plugins` in the State snapshot, sorted by id, with `id`, `status` (coloured by value: `live` in `success`, `starting` in `ink`, `exited` in `ink_muted`, `failed` in `danger`), the **claimed surfaces** (the keys of the plugin's `<id>.occupants` declaration slice, with the ones it currently holds, present in `husk.surfaces.occupants` with this plugin, in `ink` and the refused or pending ones in `ink_muted`; `none` when it claims nothing), and the **source path**: the plugin's directory as the converter resolved it, from the new `dir` field of the `husk.pods` entry ("Changes to other specs"), or `(command line)` when the plugin was given as an executable with arguments and has no directory. `last_error` is shown under a `failed` row. The table refreshes from every `husk.pods` and `husk.surfaces` change while the tab is open.

**Remove.** `enter` or `x` on a row (or a click on its `Remove` cell) opens a huh `Confirm` in the content: `Remove <id> from this instance? It is unloaded, not uninstalled: its files, settings and saved state stay, and the next start loads it again.` Confirming calls `plugin.remove` through the seam `RemovePlugin(id, reply)` with a `host` caller; the row shows `removing…` until the reply or until the `husk.pods` entry disappears, whichever comes first. The reply's `status` and `log` are not shown (the row is gone); an error reply (`not_found` because the plugin went away meanwhile, `conflict`, `unavailable` `shutdown`, `cancelled`) is shown under the table as `✕ <code>: <message>` (`✕` in `danger`, the rest in `ink`) until the next key. A second Remove of the same id while one is pending joins it, as the method does. `esc` or `no` on the Confirm returns to the table. The confirmation is bound to the original plugin id, never to the table cursor; resize and palette rebuilds preserve that id. If its Pod disappears while the confirmation is open, the confirmation is dismissed rather than retargeted to a neighbouring row.

**No Add.** `plugin.add` is CLI-only by ruling; the tab's last row shows the exact command to run, `husk plugin add <path>` and `husk plugin add --dir <dir>`, with `<path>` and `<dir>` literal, in a selectable-looking box; there is no clipboard integration (Open questions). After the user runs it, the new Pod appears in the table through `husk.pods`.

#### Plugins → Store

**Landed:** the optional catalog list and detail pane, surface Select and text query, installed and taken markers, path and URL command instructions, empty-catalog example and bounded diagnostics. The pane uses `ServicesRevision()` and `CatalogRevision()` tokens to catch reloads and catalog workers that finish later without copying the catalog on idle ticks. State-derived rows are invalidated by slice revisions, checked only after the snapshot identity or sequence changes, and render keys are comparable values; idle updates neither scan namespaces nor allocate a revision map. Content is derived only when inputs change. It performs no installation or filesystem/network action.

A **catalog view**: what plugins exist that the user could add, from the `catalog` document ("The catalog document"), read through the seam `Catalog()`. It never downloads, builds or runs anything.

Layout: a **filter row** with a huh `Select` over `any surface` plus the twelve surface ids ([Protocol](protocol.md) `SurfaceID`, in registry order) and the palette's query line for text. The text query keeps entries whose id, name, description or one of whose tags contains it, case-insensitively (an empty query keeps every entry); the surface Select keeps entries whose `surfaces` include the chosen id (`any surface` keeps every entry). The Select opens on `enter` in the Store content or a click on the filter row, and its keys and dismissal are in "Focus and keys". A **list** of the entries that pass both filters, sorted by name then id, each row `name` in `ink`, `id` and the claimed surfaces in `ink_muted`, tags after; and a **detail pane** under the list for the highlighted entry: the description (wrapped), the surfaces, the tags, the source, and the **install command**:

- a `source` that is an absolute path or starts with `~/`: `husk plugin add <source>` (`~/` is shown as written; the shell expands it);
- a `source` that is a URL: the URL, then the line `fetch it, then: husk plugin add <directory>`, since `plugin.add` takes a local path only ([Pods](pods.md), "Adding a plugin").

An entry whose id is already a Pod of this instance (present in `husk.pods`) is marked `installed` in `success` and its command line reads `already loaded`. An entry's surfaces that a different loaded plugin currently occupies are marked `taken` in `warning`, because a claim on them would be refused ([State](state.md), first live claim wins); this is information, not a filter.

**Empty catalog.** When no layer defines an entry, the list says `No catalog. This Husk ships none; the distribution or you supply one.` followed by the user-layer file to fill, `<config dir>/catalog.json` (the config directory from Store's resolved paths, as `husk store paths` prints it), and a three-entry example of the document. When layers define entries but the filters exclude them all, the list says `no entry matches`.

**Problems.** Entries a layer defines that do not validate are dropped and listed under the list as `warning: <code>: <message>`, each line `ink` after a `⚠` in `warning` ("The catalog document", codes), at most 10 with `and <n> more`, so a typo in the file is visible where the user looks for its effect. A `catalog.json` that does not parse is reported as `<file> (<layer>): <parse error>` the same way, `<file>` config-relative.

### The profile corner

The corner shows the active profile. `enter`, `space` or a click opens a huh `Select` anchored under it, one option per entry of `profile.list` (`<name>  <source>`, sorted by name, the active one pre-selected) plus `(none)` for no profile. Choosing calls `profile.use` with the name (`""` for `(none)`) through the seam `UseProfile(name, reply)` with a `host` caller; the corner shows `switching…` until the reply. On a result, the corner shows the reply's `active` (or `default` for `""`) and the center invalidates its sources, because a profile change is a reload ("Reloads while open"). On an error reply (`not_found` `unknown_profile` if the directory vanished, `unavailable` `store_error`, `timeout`, `cancelled`), the corner keeps the previous name and the content pane's last row shows `✕ <code>: <message>` (`✕` in `danger`, the rest in `ink`) until the next key. `esc` on the Select returns to the corner without a call.

The choice is **instance-only**: `profile.use` does not write `profile.default`, so the next start of a pid instance is back to the default (a named instance keeps its `husk.profile` slice, [Services](services.md), "Profile"). The Select's footer says `applies now; persisting is husk profile use --save` so the user is not surprised; `--save` is [Services](services.md) open question 2 and this spec does not resolve it.

### Reloads while open

A `services.reloaded` event advances the host's `ServicesRevision()` cache-invalidation token. The General pane re-reads its schema and snapshots when that token changes, not on idle modifier ticks. Keys reads its listing each time its tab is entered and when it is visible after a reload; a hidden Keys pane defers its re-read until entry. A reload refreshes sources without closing or moving focus: the General tab re-reads the schema and the snapshot and refreshes every **clean** field (a dirty field keeps its edit; the group list is rebuilt when a plugin's schema appeared or vanished, keeping the active field when its key still exists; a changed schema type replaces that field from the new snapshot rather than retaining an edit/baseline of the old type); the Keys tab re-reads the listing and re-applies the filter; the Theme tab re-reads `theme.list` as the selector did (the cursor stays on the same name when it still exists, else on the current theme); the Store tab re-reads the catalog and keeps the highlighted id when it still exists; the profile corner re-reads `profile.list`. The Installed tab is driven by State changes, not by reloads. A reload the center itself caused (a save, a profile switch) is handled by the same path, after the reply.

### The catalog document

**Landed:** the layered reader and off-loop host cache, exposed by `Frontend.Catalog()`, and the read-only Store tab UI.

The catalog is a layered Store document named **`catalog`**, file `catalog.json` in each layer ([Store](store.md): the distribution's root, `catalog.json` in the user layer, `profiles/<name>/catalog.json`, a named instance's file), read **unmerged** through `Store.Layers("catalog", profile)` so that every entry has one source layer, exactly as `themes` is read. Store knows nothing of its meaning.

```json
{"version": 1, "entries": {
  "notes": {
    "name": "Notes",
    "description": "A notebook: an editor in the stage, a tree in the sidebar list.",
    "surfaces": ["sidebar-list", "main-stage"],
    "source": "~/plugins/notes",
    "tags": ["editor", "files"]
  },
  "clock": {
    "name": "Clock",
    "description": "The time in the sidebar footer.",
    "surfaces": ["sidebar-footer"],
    "source": "https://example.invalid/plugins/clock"
  }
}}
```

| Field | Type | Required | Rule |
| :--- | :--- | :--- | :--- |
| `version` | integer | yes | Must be `1`; anything else drops the layer with `invalid_value` on `catalog.version` |
| `entries` | object | yes | Keys are entry ids; `{}` is a valid empty catalog; absent or not an object drops the layer with `invalid_value` on `catalog.entries` |
| `<id>` (key) | string | | The plugin id rule, `[A-Za-z0-9][A-Za-z0-9._-]{0,63}` ([Pods](pods.md) rule 3), matched exactly; the id of the plugin the entry describes, so `installed` can be computed |
| `name` | string | no | At most 128 runes; defaults to the id |
| `description` | string | no | At most 1 KiB |
| `surfaces` | array of strings | no | Each one of the twelve `SurfaceID`s; at most 12, no duplicates; `[]` or absent means "claims nothing or unknown" |
| `source` | string | yes | 1 to 1024 bytes; an absolute path, a `~/` path, or a URL with scheme `http`, `https`, `git`, `git+ssh` or `ssh` |
| `tags` | array of strings | no | At most 16, each `[a-z0-9_-]{1,32}` |

Rules:

1. An entry is a **unit** across layers: a higher layer defining the same id replaces the lower definition whole; the center shows the winning layer as the entry's `source_layer`. Unknown fields in an entry are ignored (forward growth, as for a bundle manifest).
2. An entry that does not validate (bad id, a field of the wrong type or over its limit, an unknown surface, a `source` of another shape) is **dropped** with one `warning` `invalid_value` (layer: the defining layer; `Key`: `catalog.<id>` for a bad id, `catalog.<id>.<field>` for a bad field; message names the first reason; `Source` names the file). The layer's other entries stand. A dropped entry does not shadow a valid definition of the same id in a lower layer.
3. At most **256** entries per layer; the 257th and later in document order are dropped with one `invalid_value` on `catalog.entries`.
4. A `catalog.json` that does not parse, is over a Store limit, whose `version` is not `1` or whose `entries` is not an object drops that layer alone; other layers stand. The reader never gives up on the whole catalog.
5. The catalog is **not** a Service input and no reload report carries its problems: nothing in the host changes because of it. It is read **off the loop**, never on it ([Store](store.md) invariant 6: Store never runs on the Bubble Tea loop): the host warms a catalog cache on a worker at startup, after the `startup` flight, and again after every `services.reloaded` event (a reload is the only thing that changes the layers, and `Store.Invalidate()` ran at its capture), posting the result to the loop. The worker mirrors the coordinator's `themes` gathering ([Services](services.md), Reload step 2): it calls `Store.Resolve("catalog", profile)` **for its problems only**, discarding the merged document, because `Store.Layers` drops a layer that fails to parse without saying so and the reader could otherwise never report that failure; then `Store.Layers("catalog", profile)` for the unmerged layers it validates. Unlike `themes`, a catalog parse failure never fails a reload, because no reload reads the catalog; the `Catalog()` seam returns that cache and performs no I/O, so the center on the loop sees the catalog as of the last reload, exactly as it sees `Themes()`. Its problems are returned beside its entries for the tab to show. `husk store check` does not inspect it beyond parsing, as for any layer file.
6. Husk never acts on a `source`: no fetch, no clone, no build, no `plugin.add`. The tab prints a command; the user runs it.

Problems are `protocol.Problem` values produced through the **one** Store-to-Problem conversion in `services/settings` ([Store](store.md): Services convert load problems into `protocol.Problem` in one place), and they use **existing codes only**; this spec adds no code. A layer file that does not parse or is over a Store limit is `store_error` (the message names the config-relative file, as the coordinator's `store_error` does). Everything the catalog rules above reject is `invalid_value`, with `Key` the path in the document (`catalog.version` for a wrong version; `catalog.<id>` for a bad id; `catalog.<id>.<field>` for a bad field; `catalog.entries` for an absent or non-object `entries`, and for the 257th entry with message `more than 256 entries; the rest ignored`), `Layer` the defining layer and `Source` the file. Every catalog problem is `warning` severity, whatever the code's severity means in a reload report, so a broken catalog can never be more than a line in the Store tab.

The built-in catalog is empty: the host embeds no `catalog.json`. A distribution supplies one in its distribution layer (an embedding application's `fs.FS`, or a bundle's files, which are the same layer, [Bundles](bundles.md)), a user in `catalog.json` of the user layer.

### huh

**Landed:** `charm.land/huh/v2` v2.0.3 and its Bubbles v2 dependency build with Bubble Tea v2.1.0 and the existing Ultraviolet pin unchanged. Select and Confirm render in the workbench compositor under teatest/v2; no fallback or pin bump was needed. Production huh use is confined to the center's files.

Forms are built with **`charm.land/huh/v2`** (the canonical vanity path, like `charm.land/bubbletea/v2`; not the old GitHub module path), the line of huh that targets Bubble Tea v2, because Husk is on Bubble Tea v2 (`charmbracelet/x/exp/teatest/v2`, `charmbracelet/ultraviolet` in `go.mod`); the v1 module targets Bubble Tea v1 and cannot be mixed in.

**The compatibility check comes first.** Before writing a field, the implementer adds the dependency and builds `pkg/workbench` with the **pinned** `ultraviolet` and Bubble Tea v2 versions in `go.mod`: huh v2 must build against them without moving either pin, and a `tea.Program` with huh fields inside the workbench model must render under `teatest/v2`. huh v2 brings `charm.land/bubbles/v2` (its text inputs and lists) into `go.mod` as a new direct-use dependency, which core does not use today; the check records that the bubbles module builds against the same pins and that nothing outside the center imports it (the same import test as for huh). If it builds only by bumping a pin, the bump is a separate commit that the golden frames pass on; if it does not build at all, or the bump breaks the compositor, huh is **not** used and the fallback below is.

**Fallback.** The same forms rendered with Husk's own field components, bounded to exactly the fields the tables above need: `Input` (one line of text with a cursor, which is the palette's query line component), `Text` (several lines; `Input` rows with `enter` adding a line), `Select` (a vertical list with a marker, which is the theme list component), `Confirm` (a two-option `Select`). No other widget is needed by any tab. Whichever path is taken, the spec's behaviour is the same; the tests do not depend on huh types.

Scope: huh is used **only inside the center**. Nothing else in core imports it; the palette, the slot explorer and the bars stay as they are. A test pins that `pkg/workbench` imports huh only from the center's files.

Styling: a `huh.Theme` is built from the served `Palette` and its index map (`PaletteANSI()` seam, one read) once per change of either, mapping `accent` to the focused field, `ink` and `ink_muted` to text and descriptions, `danger` to the validation error indicator and `ink` to its message, `surface_elevated` to the field background. A role the served palette carries in its `ansi` map ([Built-in themes](themes.md), "Role values") maps to the ANSI basic colour of its index, not to its hex, as everywhere else in the frontend. huh's own key hints are hidden; the center's footer shows the keys.

### Frontend seams

**Landed:** the center's host seams, including `Themes` and `SetSetting`, are available through `contracts.Frontend`. The shell, all five tabs and profile corner are landed.

The center drives the host through `contracts.Frontend` ([Pods](pods.md), "Frontend seams") and nothing else. It uses `Snapshot`, `Palette`, `Themes`, `SetSetting` and the `PaletteANSI` seam that [Built-in themes](themes.md) adds. The seams below use a `host` caller with the authorization that caller has in [Services](services.md) and [Pods](pods.md), except `KeyListing`, which explicitly requests the full `cli` view:

| Seam | Backing method | Goroutine |
| :--- | :--- | :--- |
| `ServicesRevision() uint64` | host counter of delivered `services.reloaded` events, including unchanged and failed reloads; cache-invalidation only, no wire method | any; atomic read, never I/O |
| `SettingsSchema() []SettingEntry` | `settings.schema` with no owner (every entry) | any |
| `Setting(key) (value any, source string, err error)` | `settings.get` for the host: `{value, source}` | any |
| `KeyListing() KeyListing` | `keys.list` as the Server answers a `cli` caller, states included (the existing Keybinding port is shared by the handler and this seam) | any |
| `Profiles() []ProfileInfo` | `profile.list` | any |
| `UseProfile(name, reply)` | `profile.use`; answers on the loop exactly once with `{active, reports}` or a `CodedError` | call from the loop; reply on the loop |
| `RemovePlugin(id, reply)` | `plugin.remove` with a `host` caller ("Changes to other specs"); answers on the loop exactly once with `{plugin, status, log, reports}` or a `CodedError` | call from the loop; reply on the loop |
| `Catalog()` | the catalog cache: `Store.Resolve("catalog", profile)` for its problems only, then `Store.Layers("catalog", profile)` validated by the rules above, on a host worker, at startup and after every `services.reloaded`, posted to the loop; `{entries, problems}` | any; a cache read, never I/O |
| `CatalogRevision() uint64` | advances after each accepted catalog-cache publication, including a worker finishing after a reload event; stale rejected workers do not advance it | any; atomic read, never I/O |
| `ConfigDir() string` | the user layer's config directory, for the empty-catalog message | any |

None of them blocks the loop: the writers run their method off the loop and post the reply, like `SetSetting`, including its shutdown rule (a `cancelled` reply with `data.reason` `shutdown`, or none once the post gate has closed; [Pods](pods.md), "Frontend seams"). The frontend never imports Services, Pods or Store.

### Invariants

1. The center writes configuration only through `settings.set`, switches profiles only through `profile.use`, and unloads only through `plugin.remove`; it holds no state that those methods do not also show. Closing it loses only unsaved edits.
2. While the center is open, no key reaches `Resolve` or a plugin, and `overlays.settings_center` names the active tab; it is `""` whenever the center is closed, by any path.
3. A save writes exactly the dirty fields, one `settings.set` each, sequentially; a field is clean only after a reply with no `error` problem for its key.
4. The Store tab never performs a network or filesystem action; the catalog is read through Store on a host worker, never on the loop, and `plugin.add` is never called by the center. Nothing the center does on the loop touches Store.
5. The profile corner shows the name `profile.list` marks active (or `default`), on every tab, at every size the modal can have.
6. huh is imported by the center's files only.
7. The Theme tab's selection semantics are those of [Services](services.md), "The theme selector", unchanged: same `theme.list` order, same `settings.set theme.name`, same close-on-applied and stay-open-on-problem rules.

### Failure modes

| Condition | Who detects | What happens | What the user sees |
| :--- | :--- | :--- | :--- |
| `husk.settings.open` while a plugin modal or runtime modal is open | frontend | Closes the modal, then opens the center | Only Settings is open and receives keys |
| A modal rail action is invoked while the center is open | frontend | Closes the center, discarding unsaved edits without a prompt, then opens the modal | Only the new modal is open and receives keys |
| `husk.settings.open` while frozen | frontend | Nothing opens; the handler declines | A bound key reaches the occupant; `husk action invoke husk.settings.open` succeeds empty |
| `husk.settings.open` with an unknown `tab` | frontend | Nothing opens | `invalid_params` naming the value |
| `settings.set` refuses a value (`invalid_params` `invalid_value`) | Settings, before the write | Nothing written; the field stays dirty | `invalid_value: <message>` under the field |
| `settings.set` cannot write the user layer | Store `Patch` | Nothing written; no reload | `store_error: <message>` under the field; the save continues with the next key |
| A `restart` key is saved | Settings rebuild | Written; value in force unchanged | `restart required` under the field |
| Theme write unapplied or unknown theme | Theme rebuild | As the selector: file written, palette unchanged | The selector's error lines; the center stays open on Theme |
| `profile.use` of a profile whose directory vanished | Profile | No switch | The corner keeps the previous name; the content pane's last row shows `✕ unknown_profile: <message>` until the next key |
| `plugin.remove` for a plugin that already left | Pods | Nothing | `✕ not_found: <message>` under the table; the row is already gone |
| Shutdown during a pending call | host | The reply is `cancelled` `shutdown` or never arrives | The TUI is exiting; nothing is drawn |
| `catalog.json` does not parse or is over a Store limit | the catalog reader, off the loop (seen through `Store.Resolve`'s problems; `Store.Layers` drops it silently) | That layer dropped; no reload is affected | `warning: store_error: <file> (<layer>): <reason>` under the Store list |
| `catalog.json` has a wrong `version` | the catalog reader | That layer dropped | `warning: invalid_value: catalog.version: want 1` |
| A catalog entry is invalid, or past the 256th | the catalog reader | That entry dropped | `warning: invalid_value: catalog.<id>...: <reason>` |
| No catalog anywhere | the `Catalog()` seam | Empty list | `No catalog ...` with the file path and an example |
| A reload publishes while a field is dirty | frontend | Clean fields refresh; dirty ones keep their edit | The dirty marker stays; a later save writes the edit |
| Terminal becomes frozen (below 84x24) | frontend | The center closes, edits discarded | The too-small screen |
| huh v2 does not build with the pinned Bubble Tea v2 and ultraviolet | implementer, first task | The fallback field components are used | Nothing different |

### Limits

| Limit | Value | Reason |
| :--- | :--- | :--- |
| Catalog entries per layer | 256 | A list a user scrolls; a distribution with more has an index, not a catalog |
| Catalog `name` / `description` / `source` | 128 runes / 1 KiB / 1024 bytes | Fits a row, a detail pane and a command line |
| Catalog `tags` | 16 of `[a-z0-9_-]{1,32}` | Filter vocabulary, not prose |
| Catalog `surfaces` | at most 12, each a `SurfaceID` | The registry has twelve |
| Problems shown per tab | 20 (Keys), 10 (Store) | The rest is `husk keys --problems` and the file |
| Profile corner | 24 cells | Fits beside the title at the minimum width |
| Modal size | at most 100 by 32, at least 80 by 20 | Fits the 84 by 24 minimum with the frame's bars |
| Save concurrency | 1 write in flight | One reload per write, attributable reports |

### Tests and goldens

Each named for the behaviour it pins, in `pkg/workbench` on the recording fake frontend:

- opens on `general` the first time, on the last tab the next time, and on the `tab` argument when given; an unknown `tab` is `invalid_params` and opens nothing;
- `husk.theme.select` and the bottom-bar click open on `theme`; invoking `husk.settings.open` or its alias while a plugin modal is open closes that modal first, commits only the Settings overlay and isolates subsequent keys;
- invoking a modal rail action while Settings is open closes the center without saving dirty fields, opens the modal, commits only that modal overlay and routes subsequent keys to its plugin; reopening Settings restores the saved values;
- `overlays.settings_center` holds the active tab while open, follows every tab switch, and is `""` after each close path (`esc`, `ctrl+c`, click outside, frozen);
- `tab` cycles tab list → content → profile corner → tab list; digits switch tabs only while the tab list has focus; a digit in a focused Input is typed;
- General groups keys by prefix in the stated order, with one collapsed group per plugin schema and none for an unregistered plugin schema;
- the field type per schema type (table-driven over the six types), including `strings` as lines;
- an `int` outside `Min`/`Max` is refused inline and `SetSetting` is not called;
- `ctrl+s` writes exactly the dirty keys, in key order, one at a time, each after the previous reply; a second `ctrl+s` during a save is ignored;
- a reply with `restart_required` shows `restart required`; an `invalid_value` reply keeps the field dirty with the code and message; a `store_error` on one key does not stop the next;
- `esc` with dirty fields closes without a prompt and writes nothing;
- a reload while open refreshes clean fields and keeps dirty ones;
- Keys lists the listing's entries with shadowed rows included and filters by key, action and plugin; no key reaches `SetSetting` or any writer from this tab;
- Theme: the selector's tests ([Services](services.md)) pass unchanged with the selector opened as this tab; a successful selection closes the center;
- Installed lists `husk.pods` entries with claimed and held surfaces and the `dir`; Remove asks, calls `RemovePlugin` once on `yes` and not on `no`; the row leaves when `husk.pods` drops it;
- Store with no catalog shows the empty message with the config path; with a fixture shows entries filtered by surface and text; an entry whose id is a Pod is `installed`; a path source shows `husk plugin add <source>`, a URL source shows the fetch line; invalid entries are dropped and listed;
- the catalog reader: a table over every field rule and limit, layer replacement by id, the 257th entry dropped with `invalid_value` on `catalog.entries`, a layer with `version: 2` dropped alone with `invalid_value` on `catalog.version`, an unparsable layer dropped alone with `store_error` from the shared conversion;
- the center does no Store I/O on the loop: a Store fake that fails any call made on the loop goroutine, with the catalog warmed by the worker at startup and after a `services.reloaded`, and `Catalog()` answering from the cache;
- the profile corner shows the active name or `default`, opens the Select, calls `UseProfile` once with the chosen name (`""` for `(none)`), and shows the error for a failed switch;
- huh is imported only from the center's files (an import test like `TestImportsStayWithinAllowList`);
- golden frames: the center open on General (empty workbench, 144x40), on Keys with a filter, on Store with the empty catalog, on Store with a fixture catalog (two entries, one `installed`, the detail pane showing a path source's command), on Installed with one plugin and the Remove confirm, the General tab with one `restart required` and one error line, and the profile Select open over the corner; regenerated once with `go test ./pkg/workbench -run TestGoldenFrame -update` in the commit that lands them.

## Level 2: Migration from today

The shell, all five tabs and profile corner are landed, along with the host seams and catalog reader. The Theme tab is the only selector; no standalone selector was introduced. Implementation order:

1. **huh compatibility check — landed**, as "huh" says: huh v2 builds and renders with the existing pins. One commit, no workbench behaviour.
2. **Seams — landed.** `contracts.Frontend` exposes `Themes`, `SetSetting`, `SettingsSchema`, `Setting`, `KeyListing`, `Profiles`, `UseProfile`, `RemovePlugin` and `ConfigDir` (`Catalog` landed with the reader, step 3). The root implements them over the existing Services ports and Pods; `KeyListing` calls the same Keybinding port as the handler with a `cli` caller. Writes run on tracked workers and reply only through the loop. `plugin.remove` admits a `host` caller in process ("Changes to other specs"). `husk.pods` entries and `plugin.list` carry `dir`.
3. **The catalog reader — landed**, a small package beside Services (`pkg/services/catalog`), with the rules of "The catalog document", importing `pkg/protocol`, Store's layer types and the shared Store-to-Problem conversion in `services/settings`, and nothing else; tested on fixtures. The host warms its cache on a worker after the startup flight and after each `services.reloaded` and posts the result to the loop. Construction drains the initial worker's post before handing ownership to the frontend; later reloads never wait for the cache. Late workers cannot replace a newer refresh. The cache uses the serving Profile snapshot's active name.
4. **The center shell, Theme and profile corner — landed** in `pkg/workbench`: the modal, five-tab list, focus and input isolation, profile Select, both host actions, `overlays.settings_center`, bottom-bar click and golden frames. The theme selector is implemented **as the Theme tab**, never as a standalone modal. Store and Installed content and goldens landed in step 6.
5. **General and Keys — landed** in `pkg/workbench`: the six schema field types, plugin-group collapse, single-flight key-ordered saves, persistent restart warnings, failure continuation and dirty-preserving snapshot refresh; the read-only Keys table with filtering and at most 20 problems. Three frame goldens cover General, its restart/error state and filtered Keys.
6. **Installed and Store — landed** in `pkg/workbench`: State-derived Installed rows with unload confirmation and asynchronous removal, and the cached read-only catalog with surface/text filters, details, command instructions, occupancy markers and diagnostics. Three frame goldens cover an empty catalog, a fixture catalog and Installed with the removal confirmation.

The specs this one amends, each with the minimal consistent edit: [Services](services.md) ("The theme selector" becomes the Theme tab's contract; the toast rule names the center), [State](state.md) (`overlays.settings_center` replaces `overlays.theme_selector`; `husk.pods` gains `dir`), [Keybindings](keybindings.md) (`husk.settings.open` joins the host actions; the context filter row), [Store](store.md) (the `catalog` document in the document list and the directory tree), [Protocol](protocol.md) (`plugin.remove` admits the host; `plugin.list` gains `dir` in minor 2.3), [Pods](pods.md) (the same, and the seams), [Workbench chrome](chrome.md) (the theme pill's click runs `husk.settings.open` with `tab: theme`), [Bundles](bundles.md) (`themes` and `catalog` join the document names a bundle may ship).

### Changes to other specs

- **`plugin.remove` admits a `host` caller — landed.** Previously the four `plugin.*` methods were CLI-only. The Installed tab is the user acting through the frontend, the same trust as the CLI, so `plugin.remove` admits `cli` and `host` (an `input` caller cannot reach it: no key binds a method) and stays `denied` for `plugin`; `plugin.list`, `plugin.log` and `plugin.add` stay CLI-only, because the center reads the Pod set from State and never adds. The frontend reaches `PluginPort.Remove` through the `RemovePlugin` seam, not through the socket adapter, so on the wire `plugin.remove` stays homed `cliOnly` in `MethodHomes`, as `settings.set` does; the port itself admits `cli` and `host`. The sequence under [Pods](pods.md), "Removing a plugin", is unchanged: step 1 admits the two kinds and everything after it is caller-blind.
- **`husk.pods` entries and `plugin.list` carry `dir` — landed.** The plugin's directory as the converter resolved it (the directory holding `husk-plugin.json`, or the directory a bare executable or manifest file was found in), `""` for a plugin given as an implicit command line. Written by the Pod at `starting` and never changed; additive to a State slice and to a result, so a reader that ignores it is unaffected. It is listed with minor 2.3 but, being an additive field, it does not move `protocol.Version`: minor 2.3 is landed now that both `theme.list` and `settings.set` and minor 2.2's `plugin.add` are served ([Protocol](protocol.md), rule 5 of Versioning). It answers "where did this plugin come from" for every load path, bundle or not.
- **`overlays.settings_center` — landed** (string: `""` or a tab id) replaces the specified-not-landed `overlays.theme_selector` (bool) in `husk.surfaces`; Input's context filter reads the new field.
- **`catalog`** joins `config`, `keymap` and `themes` as a layered Store document name.

## Decisions taken by this spec

1. **One modal, the selector folded in.** The theme selector's text in [Services](services.md) is kept as the Theme tab's contract and amended only where being a tab changes something (opening, the whole-center close keys, `tab` to the corner, the State field); its tests stand. `husk.theme.select` stays as an alias so a keymap written for it keeps working.
2. **`tab` argument values** are `general`, `keys`, `theme`, `store`, `installed`; `plugins` is a heading, not a tab, so it is not a value. Digits `1`..`5` map to the same five in that order, and work only while the tab list has focus so they never steal a digit from a field.
3. **No built-in key** for `husk.settings.open`, for the reason already recorded for `husk.theme.select`; the palette and the bottom bar reach it.
4. **Save is explicit (`ctrl+s`), per tab, dirty keys only, sequential.** A write-on-commit per field would turn every `enter` into a reload and make "unsaved edits are discarded" meaningless; a batch method does not exist and would need partial-failure semantics ([Services](services.md), "Writing one key"). Sequential writes keep each reply's reports attributable to one key. A failed key does not stop the others.
5. **Groups are "key minus last segment"**, sorted, host first, then `dist.*`, then one collapsed group per plugin. `dist.*` is included because the user owns those values as much as the host's and `settings.set` accepts them; leaving them out would make the center lie about what can be set.
6. **The Keys filter is the palette's query line, not a huh Input**, because it must filter on every keystroke; huh fields commit on `enter`. The same component serves the Store text filter.
7. **Installed reads State, not `plugin.list`**: `husk.pods` is already in the snapshot and updates on every change, so the tab needs no polling and no new read seam; `dir` is added to the slice (and to `plugin.list`, so the two stay one shape) rather than reading the Pod map through a new seam.
8. **`plugin.remove` admits `host`**, the smallest change that lets the Installed tab remove; `plugin.add` stays CLI-only by ruling and the tab prints the command.
9. **The catalog is a layered document keyed by id, replaced whole per entry**, modelled on `themes`: a user can override one entry of a distribution's catalog without copying the rest, and `source_layer` tells where an entry came from. It is read by a seam, not a Service: no reload depends on it, so making it one would add a sixth report for nothing. It is still read off the loop, warmed after each reload, because Store never runs on the loop; and its problems reuse the existing codes through the one shared conversion, because a catalog typo is the same kind of thing as a config typo and deserves no vocabulary of its own.
10. **`source` is a path or a URL, and Husk acts on neither.** A path becomes a copyable `husk plugin add`; a URL is shown with a line saying to fetch it first. Rejected: a `plugin.add` button (CLI-only by ruling), any fetch (framework rule: the distribution decides how plugins arrive).
11. **Catalog problems are warnings shown in the tab**, never in a reload report or a toast: nothing in the host changes because of a catalog, so nothing should alarm.
12. **The profile corner uses `profile.use` without saving**, and says so in its footer. Persisting is `profile.use --save`, left open in [Services](services.md).
13. **Unsaved edits are discarded on close with no prompt** (ruling), with the footer saying so while anything is dirty.
14. **huh v2 with a bounded fallback.** The fallback names exactly four field kinds, all of which the frontend has or nearly has, so a failed compatibility check costs a bounded amount and changes no behaviour.
15. **The bottom-bar click runs `husk.settings.open` with `tab: theme`**, not `husk.theme.select`: one action opens the center and the alias exists only for keymaps already written; [Workbench chrome](chrome.md) is amended accordingly.

## Spec gaps resolved

- A duration default that is not a whole millisecond is displayed as integer milliseconds, truncated toward zero, with `(ms)` after its label. Saves still send an integer number of milliseconds.
- A catalog entry whose id is present in the Pod set is `installed` even when that Pod is `failed` or `exited`. Remove then add is the relaunch path; process liveness is not installation state.

## Open questions

1. **`keymap.set`.** A sibling of `settings.set` that writes one `bind` entry (or a tombstone) into the user layer's `keymap.json` through `Store.Patch` and reloads, so the Keys tab can rebind. It needs a rule for arrays under merge-patch (a `bind` array replaces whole; the method would read, edit and write the array) and a `keymap.unset`. Recommended: its own spec after the center lands; the tab's layout already leaves room for an `edit` key.
2. **Clipboard.** The Installed and Store tabs show commands to run. OSC 52 would copy them to the terminal clipboard where the terminal allows it; terminals differ and some disable it. Recommended: a `y` key that emits OSC 52 and says `copied (if your terminal allows it)`, in a later change, once the frontend has a terminal-capability story.
3. **Live theme preview** stays [Services](services.md) open question 4; the Theme tab changes nothing there.
4. **Batch save.** One reload per saved key is the cost of `settings.set`'s one-key contract. If users save many keys at once, a `settings.set_many` with partial-failure semantics is the fix, in [Services](services.md); the center would then save one request per tab.
5. **A `husk plugin catalog` CLI** listing the catalog as the Store tab sees it, for scripting. Recommended: when a user asks; the reader package is ready for it.
6. **Catalog `version` growth.** `version: 2` would be the place for per-entry `protocol` majors or checksums if a distribution wants them; nothing here depends on it.
