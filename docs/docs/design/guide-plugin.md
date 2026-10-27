---
type: Design Spec
title: "Guide plugin: a live annotated layout"
description: "An in-repo developer plugin that decorates the running layout with numbered callouts, surface names, occupants and keys, drawn on the app-overlay surface."
status: planned
generated: { by: claude/claude-fable-5-1, at: 2026-10-23T18:00:00Z }
tags: [architecture-v2, plugins, developer-tooling]
---

# Guide plugin: a live annotated layout

## Level 0: What and why

The guide is a plugin, `plugins/guide` (plugin id `guide`), that draws over the whole workbench while it is toggled on: a box around every surface on screen, a numbered callout on each box with the surface's humanised name, its id, the plugin occupying it and the key that reaches it, and a legend listing the numbers. It is the terminal version of an annotated screenshot in an application's documentation, except that it is live: open the sidebar and its box appears, let a plugin exit and its surface reads `empty`, rebind a dock key and the callout shows the new key.

It exists for people building on the framework. A developer wiring a first plugin has to learn twelve surface ids, which ones are shown in the default layout, which key toggles which dock, and why a claim did not take effect; today that knowledge lives in the architecture page and in the host's slot explorer. The guide answers it on the running instance. It is developer tooling in the sense of the framework rule: it knows nothing about any application, and it is not an onboarding or product guide. Suggestions, stores, bundles and anything a product would show its users stay in the consumer.

It is **not**:

- loaded by default. `husk` starts with no plugins ([Pods](pods.md)); the guide is loaded like any other, by `--plugin-dir` or `--plugin`, and the project scaffolds (specified separately) include it in what they generate.
- part of the host's UI or services. It is a process speaking the protocol, with a manifest, a surface claim and one action. The only code outside `plugins/guide` that names it is the create command package, which copies its files into scaffolds and provides the hidden local `__guide-plugin` command their launcher runs, and that command's registration in `cmd/husk`. Neither loads the guide into an instance.
- a replacement for the host's slot explorer (`husk.hotspots.toggle_stage`, `husk.hotspots.badges`). The explorer is host UI that describes the fixed surfaces; the guide shows what is true now, through the same wire a third-party plugin would use. In the dogfooding sense it is the proof that a plugin can draw a tour or a debugging overlay without host privileges.
- interactive. v1 has one action, `guide.toggle`, and one view (callouts plus legend). It takes no keys, no mouse and no focus.

Who depends on it: nobody in the host. It depends on three general facts of the framework, two of which this spec adds to the specs that own them: the layout is readable from State (`husk.geometry`, `husk.surfaces`, `husk.focus`, [State](state.md)); an `app-overlay` occupant draws a transparent layer over the frame ([Cell-buffer compositor](cell-buffer-compositor.md), "Overlay layer"); and a plugin may read the host's effective keys with `keys.list` ([Keybindings](keybindings.md), "`husk keys`").

## Level 1: Contract

### Manifest

```json
{
  "manifest_version": 2,
  "protocol": 2,
  "id": "guide",
  "name": "Layout guide",
  "version": "0.1.0",
  "description": "Draws numbered callouts, names, occupants and keys over the live layout; toggle with guide.toggle",
  "command": ["./guide"],
  "surfaces": [{"id": "app-overlay"}],
  "actions": [
    {"id": "guide.toggle", "title": "Toggle layout guide", "description": "Shows or hides the annotated layout overlay", "public": true}
  ],
  "events": {"subscribe": ["state.changed"]}
}
```

Rules:

1. The guide claims **`app-overlay` and nothing else**. It never claims a layout surface, a placement surface or a published slot, declares no rail item, no published slot, no hook and no focus-local binding. Reason: a developer loads it next to the plugins under development, and it must never take a surface one of them could want. `app-overlay` is the one surface whose purpose is to lie over the others ([Cell-buffer compositor](cell-buffer-compositor.md), "Overlay layer").
2. Its one action is `guide.toggle`, in its own id space. It is `public`, so another plugin may toggle it ([Message kinds](message-kinds.md), permission rule); the CLI and the palette can anyway. Args are ignored. The reply is `{"shown": true}` or `{"shown": false}`, the state after the toggle.
3. Nothing in the manifest binds a key: no manifest can ([Keybindings](keybindings.md)). The key comes from a keymap layer. The project scaffolds bind `guide.toggle` globally in their distribution keymap with a hint, so the footer shows it; this spec recommends `f2` with hint `guide`, a key the built-in layer leaves free. A developer running `husk --plugin-dir ./plugins` without such a layer reaches the action from the palette (`husk.palette.open`, `ctrl+p`) or with `husk action invoke guide.toggle`.
4. The footer shows the hint only while the guide is live: the frontend drops a `Hints` entry whose plugin action is not in its action catalog with a `live` owner, the same liveness test the palette applies to plugin actions ([Keybindings](keybindings.md), "Rail items, palette and footer").
5. The plugin's declared `state` block is absent: `guide.state` is the default non-persistent instance slice and holds `{"shown": bool}`, written with `state.set` on every toggle so `husk state get guide.state` answers whether the guide is on. Persistence is deliberately off: a guide that comes back on at the next start would surprise the person who forgot it.

### What the guide reads

The guide learns the layout from State and the keys from the Keybinding service; it computes nothing the host has not already decided. It opens one persistent socket connection at start, replacing it only on failure ([Protocol](protocol.md) decision 9).

| Fact | Source | Shape used |
| :--- | :--- | :--- |
| Rect of every surface on screen | `husk.geometry.rects` ([State](state.md)) | `{x, y, width, height, inner}`; a surface without a rect is not on screen |
| Terminal size, too-small flag | `husk.geometry.terminal`, `husk.geometry.too_small` | |
| Occupant of every surface | `husk.surfaces.occupants` | `plugin`; absent means `empty` |
| Which surfaces are shown | `husk.surfaces.visible` | the list; `modal-surface` appears while a modal is open |
| Active rail item and placement | `husk.surfaces.placement` | `{rail_item, kind}`; `kind` is `full-canvas` or `stage` while one is active |
| The open modal | `husk.surfaces.overlays.modal` | `{plugin, rail_item}` |
| Focused surface | `husk.focus.surface` | |
| Plugin set changes | `husk.pods` | any change is the trigger to re-request `keys.list` (below); nothing in it is drawn, since an occupant is always live ([State](state.md), "Surfaces resolution") and a surface whose claimant is not live is `empty` |
| Keys | `keys.list` ([Keybindings](keybindings.md)) | effective entries whose `action` is a `husk.*` host action, a rail item id or `guide.toggle` |

It reads State with one `state.watch` request for `husk.geometry`, `husk.surfaces`, `husk.focus` and `husk.pods` on its socket connection: the reply is the baseline and every later `state.changed` notification on that connection carries that slice's value at the snapshot the host flushed. Notifications available on one socket read are coalesced into one render, best effort; there is no wire tick boundary, so intermediate cross-slice states may show ([State](state.md), "Plugin requests"). The manifest `state.changed` subscription is the stdin mirror of the same facts; the guide ignores it, neither drawing from it nor starting another reconnect worker. Socket EOF and transport/watch failures drive reconnection; a stale baseline is never used for a new drawing. `keys.list` is requested each time the guide is toggled on, on a new watch connection while shown or without ready keys, and after every `state.changed` for `husk.pods`, which is when a reload may have changed the keymap ([Keybindings](keybindings.md), "Reload"); the keys themselves are not State and have no change event a plugin may subscribe to (`services.reloaded` reaches CLI and in-process `host` watchers only, never a plugin, [Message kinds](message-kinds.md), reserved names). A `husk config reload` or profile change that rebinds keys without touching `husk.pods` therefore shows at the next toggle-on.

Reconnects use cancellable exponential backoff: start at 100 ms, double after each failed attempt and cap at 5 s. Dial failures, refused `state.watch` replies, undecodable baselines and socket-send failures share this sequence. A valid baseline resets it once per connection; a successful dial alone does not. Each distinct outage error is logged once, up to 16 distinct messages, then one `guide: further errors suppressed until recovery` notice bounds both diagnostic history and output until a valid baseline clears them (logging `guide: connection recovered` when any were logged). A socket-send failure does not truncate a host batch: all local mutations and stdout replies are attempted before disconnecting and scheduling the next connection. A failed stdout reply is fatal rather than a reason to reconnect the socket.

The twelve surface ids are a fixed vocabulary of the protocol (`protocol.SurfaceID`, [Protocol](protocol.md)), so the guide carries a static table for them: which action reaches each one. The table is the only host knowledge the guide holds, and every entry of it names a surface id or a `husk.*` action id that the protocol or the built-in keymap already publishes. A surface's name is not in the table: the guide computes its **humanised name** from the id with the frontend's rule ([Workbench chrome](chrome.md), "Terms"), so the guide and the host's empty-surface labels never disagree.

| Surface | Key shown is the effective key of |
| :--- | :--- |
| `activity-rail` | none (the key field is omitted) |
| `sidebar-navigation` | `husk.sidebar.toggle` |
| `sidebar-list` | `husk.sidebar.toggle` |
| `thread-row-status` | `husk.sidebar.toggle` |
| `sidebar-footer` | `husk.sidebar.toggle` |
| `main-header` | `husk.home` |
| `main-stage` | `husk.home` |
| `side-panel` | `husk.sidepanel.toggle` |
| `app-overlay` | `guide.toggle` (the guide's own key, from its own `keys.list` entry) |
| `modal-surface` | the rail item in `overlays.modal.rail_item`, then `husk.modal.close` |
| `full-canvas` | the rail item in `placement.rail_item` |
| `workspace-stage` | the rail item in `placement.rail_item`, then `husk.home` |

The **effective key** of an action, for a surface, is read from the `keys.list` entries in state `effective` whose `action` is that id (a rail item is addressable as an action by its id, [Keybindings](keybindings.md)) and whose `focus` is absent or is that surface; of those the guide takes the strongest in `KeysFor`'s order (global before surface-scoped, then the higher-ranked layer, then `source.plugin` in lexical order, then `source.order`; the entry's `plugin` condition is not a display-order tiebreak) and shows its `keys` joined with a space, so a callout shows what the footer and palette show. That is how `modal-surface` finds `esc` for `husk.modal.close`, which the built-in layer binds only on `modal-surface`. An action with no such entry shows `unbound`, except a rail item, which shows `palette`. Where the table names two actions their keys are joined with `, `. A placement surface with no active rail item (it has no rect then, so it appears only in the legend) shows `rail`. An open modal with a rect but no rail item shows only `husk.modal.close`'s effective key; its hidden legend row shows `rail`.

### What the guide draws

While shown, the guide coalesces the notifications available on one socket read into one `surface.update` for `app-overlay`, best effort, with `lines` covering the whole terminal. Intermediate cross-slice states may show, and separate reads from one host tick may each produce a render; there is no wire or host change. The overlay layer's rules make every cell the guide does not paint transparent ([Cell-buffer compositor](cell-buffer-compositor.md), "Overlay layer"), so what follows is everything the user sees change.

1. **Boxes.** For every surface that has a rect in `husk.geometry.rects` except `app-overlay` itself, the guide draws a single-line box on the surface's outer rect: the ring of cells `(x..x+width-1, y..y+height-1)`. Where the occupant declared a `border`, the ring is the occupant's own border cells and the guide repaints them in the guide's colour; where it did not, the ring is the first and last row and column of the occupant's content, which the box covers for as long as the guide is on. A rect narrower than 2 or shorter than 2 gets no box, only a callout.
2. **Callouts.** On the box's top edge, starting one cell right of the top-left corner (`(x+1, y)`), the guide writes the callout: `① Sidebar list · sidebar-list · example.files · [`, that is the number, the humanised name, the surface id, the occupant (`empty` when none) and the key, separated by ` · `. The callout must end by `x+width-2`, so the corners stay visible; whole fields are dropped from the right until it fits, and the number alone is always written, clipped to the frame. A rect without a box gets the callout at `(x, y)`, clipped to the rect's width. Numbers are `1..n` in **reading order**: surfaces sorted by `y`, then `x` of their rect; the modal, when open, comes last whatever its position, because it is above the others. Numbers are circled digits for 1 to 20 (`①`..`⑳`) and plain digits after that.
3. **The focused surface** gets a double-line box; every other box is single-line. The focused surface is `husk.focus.surface`, or `modal-surface` while a modal is open (the frontend writes it so, [State](state.md)).
4. **Legend.** A single-line box of `n + 3` rows, where `n` is the number of entry rows: the top border, the entry rows, one help row, the bottom border. It is `min(64, terminal width - 4)` cells wide, its bottom border is on the row directly above the footer (`y = height - 2`), its right border is 2 cells from the right edge (`x = width - 3`), and it is clipped to the frame. One entry row per numbered surface in number order: `① Sidebar list   sidebar-list   example.files   [`, five columns (number, humanised name, id, occupant, key), each left-aligned and padded to its widest value plus 2 spaces, the row truncated with `…` at the box's inner width. After the numbered surfaces come the surfaces of the fixed table that have no rect, in the table's order, `app-overlay` excepted: no number, `hidden` in the occupant column, and the key that shows them; `thread-row-status`, which the current layout never places, reads `not placed` in the occupant column and has no key. The help row reads `<focus> next focus · <key> hide guide`, where `<focus>` is the effective key of `husk.focus.next` and `<key>` the guide's own key, each `palette` when unbound. The legend is opaque: every cell inside the box, spaces included, carries the palette's `surface_elevated` background so text under it does not show through.
5. **The modal.** While a modal is open the guide boxes `modal-surface` at its rect (the frontend writes it to `husk.geometry.rects` while open) and the callout's key reads the rail item's key then `esc` (`husk.modal.close`'s effective key): `⑧ Modal surface · modal-surface · example.files · alt+2, esc`. The modal is drawn below the overlay layer, so the box and callout stay visible over it.
6. **Colours** come from the palette the guide received in `theme.changed` ([Services](services.md)): boxes and callouts use `accent` on the background of the cell beneath (an opaque overlay cell without a background of its own inherits it), the focused box `border_focused`, the legend `ink` on `surface_elevated`, and `empty`, `hidden` or `not placed` in `ink_muted`. The guide defines no colour of its own.
7. **Hidden.** When toggled off the guide sends `surface.update` with `"lines": []`, a fully transparent frame, then publishes `guide.state` with `shown: false` and replies to the action. If either socket send fails, the reply still reflects the local hidden state, and both idempotent writes are retried after a new valid baseline, while the guide is still hidden, until both sends succeed; a toggle-on accepted before that baseline supersedes the retry. The old overlay and published state may remain visible during that recovery. After successful delivery it sends no further overlay updates until toggled on. It keeps watching State so that the next toggle renders from the current baseline without a round trip.
8. **Too small.** While `husk.geometry.too_small` is true the host draws nothing of any surface; the guide sends nothing and renders again on the first tick that is not too small.

A frame at 200x60 is at most 60 lines of 200 cells with a few SGR runs each, well inside the 4 MiB line limit.

### What the guide never does

- It never takes focus and never receives `input.key` or `input.mouse`: `app-overlay` is not focusable and not hit-tested ([Cell-buffer compositor](cell-buffer-compositor.md), "Overlay layer"). The keys it shows are information; pressing one does whatever it does without the guide in the way.
- It never writes any slice but `guide.state`, never contributes to a slot, never emits an event, never fires a hook.
- It never names a plugin, an action or a key of its own choosing beyond the table above; every string it shows is read from State, from `keys.list` or from the fixed surface vocabulary.

### Failure modes

| Condition | Who detects | What happens | What the user sees |
| :--- | :--- | :--- | :--- |
| Another plugin claimed `app-overlay` first | Surfaces engine (first live claim wins, [State](state.md)) | The guide's claim is refused and recorded; its `surface.update` lines are refused with `denied` | `guide.toggle` answers `failed` with `app-overlay is occupied by <plugin>`; the guide logs it once and stays live, so it takes the surface when the occupant leaves |
| The watch connection drops, a dial/watch fails, or the baseline cannot be decoded | the guide, on socket EOF, a transport error, a refused watch reply or baseline decode failure; the stdin mirror is ignored | Reconnects and re-watches with 100 ms exponential backoff capped at 5 s, reset once on a valid baseline. Distinct errors are logged once, bounded to 16 messages plus one suppression notice per outage. Host batches finish before reconnecting; drawings use only the new baseline | The last frame can remain stale throughout the outage and retry delays. A host that repeatedly delivers a valid baseline then drops the connection keeps retries at the residual 100 ms floor |
| `guide.toggle` arrives while no socket connection is open, or while an earlier toggle-on still awaits its reply | the guide | Answers `unavailable` (`watch reconnecting`) or `busy` (`toggle already in progress`); the shown state is unchanged | The action fails; invoking it again after recovery works |
| A toggle-off socket send fails | the guide | Completes the host batch and action reply, then retries the empty overlay and hidden state after a valid baseline; clears the pending hide only after both sends succeed | The action reports local `shown: false`; the overlay and published state converge after recovery |
| A stdout reply send fails | the guide | Completes the current host batch, then returns the host-channel error; no extra dial worker is started | The plugin exits rather than retrying a healthy socket indefinitely |
| `keys.list` is refused or fails | the guide | Callouts show `?` in the key field; the rest renders | `?` where a key would be, and a log line |
| A surface id in State is not in the fixed table (a later minor added one) | the guide | Boxed and numbered with its humanised name computed from the id, no key field | A callout with a name and an id, no key |
| Terminal below the minimum size | host | No geometry; the guide sends nothing | The host's too-small screen |
| The guide exits or crashes | Pods | Its claim is vacated, its overlay cleared like every departed plugin's content | The annotations disappear; a `failed` toast if it crashed |

### Limits owned here

| Limit | Value | Reason |
| :--- | :--- | :--- |
| Callout fields | 5 (number, name, id, occupant, key) | fits a 40-cell top edge with the id and key, the two a developer needs most |
| Legend width | `min(64, width - 4)` | one row per surface with all five fields at the default sizes |
| Renders per socket-read notification batch | 1, best effort | coalesces the notifications available on one read without assuming a host tick boundary |
| Distinct reconnect-error messages retained per outage | 16, plus one suppression notice | bounds memory and diagnostic output when error text varies; reset on baseline recovery |

### Tests

The library tests in `plugins/guide/` drive the plugin body over pipes, as `plugins/reference/main_test.go` does, with a fake socket server that answers `state.watch` and `keys.list` from fixtures and records `surface.update`:

- a `husk.geometry` fixture with the default layout at 144x40 and the side panel shown (the activity rail, the three sidebar bands, the main header, the main stage and the side panel, plus `app-overlay` at the full frame) renders boxes exactly on the seven outer rects, none on `app-overlay`, and nothing else on the frame (every cell outside the rings, callouts and legend is a plain space with no style);
- numbering follows reading order (`y`, then `x`): with the default heights (navigation and header 4 rows) the activity rail is `①`, the sidebar navigation `②`, the main header `③`, the side panel `④` (all on row 1), the sidebar list `⑤` and the main stage `⑥` (both on row 5), the sidebar footer `⑦`;
- an open modal is numbered last and shows the rail item key then `esc` (the `modal-surface`-scoped `husk.modal.close` entry);
- a hidden sidebar appears in the legend as `hidden` with `husk.sidebar.toggle`'s key;
- an empty surface shows `empty`; a surface whose claimant is `failed` shows `empty` too;
- humanised names are computed from the id (`thread-row-status` is `Thread row status`), and an id outside the fixed table is boxed with its computed name and no key;
- a callout on a 20-cell-wide surface drops fields from the right and keeps the number;
- toggling off sends `"lines": []` and hidden state, retrying both after a valid baseline if either send failed, then sends no more overlay updates until toggled on; toggling on after a State change renders from the latest baseline without a new `state.get`;
- a refused claim makes `guide.toggle` answer `failed` and the plugin stay up;
- a watch connection closed by the fake server is reopened and the next frame uses the new baseline;
- persistent dial errors, watch refusals and undecodable baselines back off from 100 ms to the 5 s cap, reset on baseline success and log repeated errors only once; varying errors produce at most 16 distinct diagnostics and one suppression notice per outage;
- a run-level failed-send batch finishes its mutations and both stdout replies before retrying, rejects a new toggle while disconnected, replays a pending hide, and retains a hide if either replay send fails;
- an already-pending toggle-on replies after reconnecting with a new baseline, while stdout failures during a blocked dial or with a healthy socket return the error without starting another dial.

## Level 2: Migration from today

The guide has landed in `plugins/guide` (library package with `Run` and `Files`, thin `cmd/guide`); the host prerequisites below are implemented, each in its owning spec:

1. **`app-overlay` becomes drawable** ([Cell-buffer compositor](cell-buffer-compositor.md), "Overlay layer"; [State](state.md), "Frontend-owned slices"). The frontend places it at the full frame, includes it in `husk.geometry.rects` and `husk.surfaces.visible`, sends `surface.resized` to its occupant, and `View` blits it with the transparency rule after the modal and before the toast and palette.
2. **Plugins may read the host's keys** ([Keybindings](keybindings.md), "`husk keys`"). A plugin caller of `keys.list` receives its own entries and problems plus the effective entries of `husk.*` actions and declared rail item ids, which the footer, rail and palette already display. Every other entry (another plugin's actions, any shadowed or disabled entry) and other plugins' problems stay hidden.
3. **The footer hint follows liveness** ([Keybindings](keybindings.md), "Rail items, palette and footer"). The frontend hides a `Hints` entry whose action is neither a `husk.*` action nor a rail item id unless its owner is `live` in `husk.pods`, after the service's cap of four.

Then the plugin: an importable library in `plugins/guide/`, with the manifest, protocol loop, renderer and tests. Its exported `Run` function is the plugin's environment-driven main body. `plugins/guide/embed.go` exports the source as an `fs.FS` for scaffolds; it contains no executable named `guide`. A thin `plugins/guide/cmd/guide/main.go` calls `Run`. In a generated bundle, `./guide` is instead a POSIX launcher into the same Husk binary through `HUSK_BIN_PATH` and its hidden local `__guide-plugin` command; the embedded files are non-executable, so scaffolding needs no Go toolchain. Build the standalone command to `plugins/guide/guide` (git-ignored) so the manifest's `./guide` command works. The implementation imports `pkg/protocol` and nothing else from Husk. The host's overlay golden fixture is its regression test; an interactive session toggling the real plugin verifies boxes, callouts, legend and transparent toggle-off behavior end to end.

## Decisions made here

1. **No new wire method or event.** The layout a plugin needs (rect, occupant, visibility, focus, placement) is already in `husk.geometry`, `husk.surfaces` and `husk.focus`, read through one `state.watch` with best-effort read-batch coalescing; a `layout.get` would be a fourth copy of the same facts. The two general gaps were elsewhere: an `app-overlay` occupant could not draw, and a plugin could not read the host's keys. Both are fixed in the specs that own them and are smaller than a new method.
2. **The guide is a plugin, not a host mode.** The host already has a slot explorer; the guide proves the overlay path a third party would take and knows only what the wire tells it.
3. **`app-overlay`, and nothing else.** It is the surface no layout plugin wants, and the overlay layer is transparent by default, so the guide costs the layout nothing while off.
4. **The toggle key lives in a keymap layer**, recommended `f2` with hint `guide`, bound by the project scaffolds in their distribution layer. The manifest cannot bind it and the host hard-codes no key.
5. **Numbers follow reading order of the rects**, modal last, recomputed on every tick, so the legend always matches the screen.
6. **The legend is opaque and bottom-right**, so it never hides the top-left callouts and can be read against any content.
7. **`guide.state` holds `shown` and is not persistent.**
8. **The fixed surface table is the guide's only host knowledge**, limited to the protocol's surface vocabulary and the built-in keymap's action ids.

## Open questions

1. **A second page.** The brief allows "a legend page"; v1 draws the legend beside the callouts. If the legend grows (runtime surfaces, [Protocol](protocol.md), landed in minor 2.8, would add one row each once the guide lists them), a second toggle state showing only the legend may be wanted. Recommended: revisit when the guide starts listing runtime surfaces.
2. **Runtime surfaces.** `husk.surfaces.runtime` (a later minor) will list dynamic `<plugin>.<name>` surfaces with rects in `husk.geometry.rects`; the guide should box them with their title as the name and no key. Nothing in this spec prevents it; the fixed table is a fallback, not a filter.
3. **Should the host's slot explorer be retired** once the guide exists? It is host UI with host keys (`v`, `h`) and fixed descriptions. Recommended: keep it until the guide ships in the scaffolds, then decide with a frame comparison.
