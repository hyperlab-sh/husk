---
title: "Architecture"
description: "Slots, plugins and the wire protocol."
---

# Architecture

## Process model

The Go host uses Bubble Tea v2 (`charm.land/bubbletea/v2`) for its event loop and Lip Gloss v2 (`charm.land/lipgloss/v2`) for styling. Views declare alternate-screen and mouse tracking settings; typed key and mouse events are translated to the wire's key and mouse notifications.

Husk is the host. Plugins are separate processes that Husk spawns and supervises. Husk and a plugin exchange newline-delimited JSON: Husk writes to the plugin's stdin and reads replies from its stdout, and the plugin calls Husk over a Unix socket. The protocol is the only contract between them, so a plugin can be written in any language. The modules behind it (State, Calls, Surfaces, the Event bus, the Dispatcher, Store, Services and Pods) are specified in [`design/`](design/protocol.md); this page describes what a plugin author sees.

### Socket security

The host socket is local-user only. Husk uses a private `0700` runtime directory shared by the user's instances: `$XDG_RUNTIME_DIR/husk/` when that variable is set (an unusable one is an error, never a fallback), otherwise `/tmp/husk-<uid>/`. It must be a real directory owned by the user with no group or world bits, and it is never removed. Each instance binds `<instance-id>.sock` inside it with mode `0600`. The path is exported to plugins as `HUSK_SOCKET_PATH`. On every accepted connection the host reads the peer uid from the kernel (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS) and closes the connection unless it equals the host uid; if the credentials cannot be read, the connection is refused. The socket is removed on shutdown; stale-socket recovery is described in [Instances](design/instances.md). A socket path longer than the platform's `sun_path` refuses to start. See [Pods](design/pods.md) and [Instances](design/instances.md).

Identity is a per-plugin token. Husk generates one for each launch and injects it as `HUSK_PLUGIN_TOKEN`; a socket request carries it in the top-level `token` member. A request with a valid token is attributed to that plugin, a request with no token comes from the user (the CLI), and an unknown or revoked token is refused with `denied`. The token is revoked as soon as the process is reaped or its channel closes. It is attribution between plugins of one user, not isolation: the peer uid check is the security boundary.

### Outbound queue and disconnect policy

The host never writes to a plugin from its event loop. Each plugin's stdin and each socket connection has a bounded outbound queue (1,024 messages) drained by its own writer goroutine, and every write has a 5-second deadline (a deadline on sockets, a watchdog that closes the pipe on stdio). A plugin that stops reading therefore cannot freeze the workbench; the host stops it instead:

| Cause | Reason shown |
| :--- | :--- |
| Queue overflows (the plugin fell more than 1,024 messages behind) | `stopped reading: send queue full (1024 messages)` |
| One write takes longer than 5 seconds | `stopped reading: write timed out after 5s` |
| The plugin exits | `exited`, or `crashed` with the status |

The host drops the connection rather than individual messages, so a plugin never sees a stream with holes in it. The reason becomes the plugin's `last_error` and is shown as a warning toast. A socket client that stops reading its replies is disconnected the same way. A plugin that goes away loses its surfaces, rail items, subscriptions and contributions, as described under [Events and lifetime](#events-and-lifetime). A bad input line does not disconnect; see [Errors](#errors).

### Plugin processes

Husk launches each manifest plugin from its declared `command`. The plugin's identity is its pipe and its manifest: there is no handshake and nothing for the plugin to claim. Its environment carries `HUSK_SOCKET_PATH`, `HUSK_PLUGIN_TOKEN`, the instance id and the host's protocol version. A plugin is live from the moment its process starts; messages sent before it reads them wait in its stdin queue. Everything a registration reply once carried arrives as ordinary notifications before any request: `theme.changed` (the palette), one `surface.resized` snapshot of the surfaces the plugin occupies, and `input.key_state` (the modifier state).

A plugin's stderr is never discarded: it goes to its own log file (mode `0600`) in the instance's private directory. Write diagnostics there freely; they do not touch the terminal. Stdout carries replies and nothing else.

| Event | What Husk does |
| :--- | :--- |
| The manifest is invalid, declares a duplicate plugin id or another protocol major | The plugin is not launched; others are. The reason is logged |
| The plugin cannot be started (missing binary, bad command) | Liveness `failed` with `last_error`; warning toast `<id>: <last_error>` |
| The plugin exits non-zero or by a signal | Pending calls fail, the token is revoked, liveness `failed`; warning toast with the reason, and the log keeps the last stderr lines |
| The plugin exits 0 | Same cleanup, liveness `exited`; a log line, no toast |
| The plugin writes a malformed line or a non-reply on stdout | The line is dropped and a `protocol.error` notification is sent to the plugin |
| The socket listener fails | The listener stops; running plugins keep their stdio, and Husk logs and toasts the failure |
| A connection fails the peer uid check | Closed before reading; logged and toasted |

Log lines go to stderr; while the TUI owns the terminal they are held and printed when Husk exits. On shutdown Husk restores the terminal, closes every plugin's stdin (the plugin sees EOF), sends `SIGTERM` to each process group, waits up to two seconds for all of them (one deadline), then sends `SIGKILL` to the rest. Exits during shutdown are not reported.

## Slots

Husk has nine fixed slots, modeled on `bb`, plus three placement surfaces (below), twelve surfaces in all. The published-slot catalog declares all twelve as region slots (`husk.<id>`).

| Slot | Purpose |
| :--- | :--- |
| `activity-rail` | Far-left icon bar |
| `sidebar-navigation` | Top sidebar band |
| `sidebar-list` | Primary sidebar surface |
| `thread-row-status` | Narrow status surface beside the sidebar list |
| `sidebar-footer` | Bottom sidebar band |
| `main-header` | Header band above the main stage |
| `main-stage` | Main canvas, or the active stage plugin |
| `side-panel` | Collapsible right-hand drawer |
| `app-overlay` | Command palette, modal plugins and toasts |

Husk assigns no application meaning to a surface; the occupying plugin decides what it shows.

When the side panel is shown, the host leaves one blank column between it and the main header and stage (the centre column is one cell narrower, and `surface.resized` reports that narrower size), so clipped centre text never touches the panel. The gutter is inert: it belongs to no slot, so clicks and wheel events on it reach no surface. The side-panel resize divider is whichever adjacent edge is drawn as a border: the centre column's right border, the panel's left border, or both.

Three placement surfaces sit on top of these: `modal-surface` (floating overlay), `full-canvas` (everything right of the rail) and `workspace-stage` (the canvas minus the sidebar).

## Surfaces and frames

A slot's region on screen is a **surface**, and it is a blank canvas: without a declaration from the occupying plugin, Husk renders its content with no border, title or decoration of any kind. A **frame** is the chrome a plugin asks Husk to draw around its surface:

| Element | Effect | Geometry |
| :--- | :--- | :--- |
| `border` | Box border around the surface | Consumes one cell per edge |
| `label` | Title text in/over the top edge | None |
| `close` | ✕ button; click closes like `esc` | None |

Each element is independently on or off. A plugin declares frames in its manifest, as `frame` on a `surfaces` entry (`"surfaces":[{"id":"<surface-id>","frame":{"border":true,"label":"…","close":true}}]`), and a rail item can declare one for the surface it opens (`"frame"` on the rail item). Only the border changes layout: plugins that declare a frame get a `surface.resized` with the inner dimensions; plugins that declare nothing get the full rect. Toasts and the command palette are Husk's own UI and keep their own chrome.

### Content clipping

A surface shows exactly its inner area (the `surface.resized` width and height). `surface.update` `lines` are clipped to it, never wrapped or scrolled: a line wider than the surface is truncated (with `…` when it is plugin text), and lines past the last row are dropped. Husk does not report that it clipped anything; a plugin learns its size from `surface.resized` and is responsible for fitting, paging or scrolling its own content.

### Styles stay inside their cells

Plugin lines may carry ANSI SGR styling. Husk composes each frame on a grid of cells and parses every line on its own, starting from the default style, so a style a line leaves open ends at that line's last character: it cannot paint the rest of the row, the frame edge or a neighbouring surface. Wherever one piece of content is drawn over another (published regions over a surface, and the modal, toast and command palette over the workbench) the upper piece replaces whole cells: its cells show only its own styles, and the cells on either side keep exactly the style they had. The one exception is the popup shadow: the modal and the command palette (not toasts) each cast a shadow offset 2 columns right and 1 row down (clipped to the screen), and the cells beneath it keep their glyphs and attributes but have their foreground and background blended 65% toward black, so a rule or text passing under a popup is dimmed, not hidden; cells with no colour of their own are resolved against the palette's `ink` and `canvas` first. Because the frame is re-serialized from cells, the bytes Husk writes for a styled line are equivalent to the plugin's but not necessarily identical (for example, a style change may be written as a replacement colour rather than a reset).

**Slot background.** A surface's content area is painted with the palette's `slot_background` colour so slots read as panels. A cell the plugin left with the default background takes it; a cell the plugin gave a background of its own (any SGR background, including one equal to the slot colour) is left exactly as drawn, and reverse-video cells are not repainted. The same applies to the modal surface and to the slot architecture explorer. A bordered frame's ring and label keep their own styling and sit on the canvas colour; a borderless frame's label row, drawn over the first content row, is covered. The app background itself is painted too: every cell no surface or overlay coloured takes the palette's `canvas` colour, and the view also asks the terminal to use it as its background (OSC 11, reset on exit), so the shade a slot is derived from is the one on screen. The token is derived from the canvas colour by `theme.SlotBackground`: the canvas's CIE Lab lightness moves by a fixed 0.035 (3.5 points of L\*), up when the canvas is dark (Lab lightness below 0.5) and down when it is light, clamped to the colour range, so a slot is slightly lighter than the app background on a dark theme and slightly darker on a light one. For the default palette that is `#201F26` (canvas) to `#27262D` (slot). The step is that small because the next role up, `surface_elevated` (`#2D2C36`), is capped by the 4.5:1 text contrast of the roles drawn on it; a test keeps canvas, slot, elevated/hover and selected at least 1.08:1 apart. `#RRGGBB` and `#RGB` canvases are derived; any other value, including `#RRGGBBAA`, is returned unchanged (no panel).

**Modal owner and hit-testing.** The open modal has exactly one source of truth in the host (`modalOwner()`: owning plugin plus the rail item that opened it); every surface, the modal surface included, is owned by its registry occupant, so write authorisation and published-content cleanup follow the manifest claim and never the plugin whose rail item opened the modal. Mouse hit-testing walks the laid-out surfaces in an explicit z-order (full-canvas above workspace-stage above the regular regions above the activity rail, ties broken by slot ID), so overlapping rects always resolve to the same surface.

**Minimum size.** The layout needs at least 84x24 cells. On a smaller terminal Husk renders a "terminal too small (need 84x24, have WxH)" screen that fills the real terminal exactly (message shortened or truncated on narrow terminals, never wider than the terminal). While too small, mouse and key input are ignored (only quit works and nothing is forwarded to plugins), the last valid layout is kept and no `surface.resized` is sent; growing the terminal back restores the workbench.

## Protocol

The wire is specified in [Protocol](design/protocol.md); the payloads of each method are specified by the module that owns it ([Message kinds](design/message-kinds.md), [Calls](design/calls.md), [State](design/state.md), [Services](design/services.md)). The Go package `pkg/protocol` (standard library only) holds the types, the reader and the queued writer, so a Go plugin imports nothing else from Husk.

Every message is one JSON object on one line (at most 4 MiB, excluding the newline). There are three forms:

- a **request** has an `id` and a `method`, with optional `params`;
- a **notification** has a `method` and no `id`;
- a **reply** has the request's `id` and exactly one of `result` or `error`.

A request on the socket may carry the plugin `token`. Method names are `<namespace>.<verb>`. The ones a plugin author meets most:

| Method | Direction | Purpose |
| :--- | :--- | :--- |
| `surface.update`, `surface.close` | plugin to host (socket) | Replace a surface's content; close a dynamic surface such as the modal |
| `surface.resized` | host to plugin | A snapshot of every affected surface: inner width and height, `focused`, `visible` |
| `input.key`, `input.mouse` | host to plugin | Input routed to a focused surface |
| `input.key_state` | host to plugin | Modifier state, such as Alt held. Sent on transitions only. `modifiers` is authoritative (it comes from the OS poll, from bare modifier key events on terminals that speak the kitty keyboard protocol, or from the latched state). `held` and `held_keys` for ordinary keys are best effort: a terminal reports presses and, only with the keyboard protocol, releases, so unless the terminal confirmed release reporting the host drops a key from the held set 320 ms after its last press or repeat and sends an `up` for it then; with confirmed release reporting the `up` follows the real key release, including for keys held under text the keyboard layout produced (AltGr or dead keys), whose `up` carries the name of the press. The host requests kitty flag 8 (report all keys as escape codes), so on terminals that grant it Enter, Tab and Backspace get a real `up` on release. Only where a terminal reports release events but not flag 8 do those keys stay in `held_keys` until the 1 s safety net sends their `up`. Key names follow the layout-independent base key where the terminal reports one: a shifted Alt symbol is named by the symbol (`alt+!`, not `alt+shift+1`) and a chord typed on a non-Latin layout is named by its base key (`ctrl+a` for Ctrl+ф on a Russian layout). Under tmux the terminal reports releases only on recent versions with `extended-keys on`. Do not treat them as ground truth. On terminal blur the host sends `up` for every modifier and held key it had reported (a latched Alt stays) |
| `action.invoke` | host to the owning plugin; plugin or CLI to host | Run an action. The reply is the owner's result |
| `slot.contribute`, `slot.contributions` | plugin to host; host to publisher | Published slots (below) |
| `hook.call` | caller to host; host to each subscriber | Ask subscribers; the caller gets one reply with every result |
| `event.emit`, `event.watch`, `event.deliver` | plugin or CLI to host; host to subscriber | Events (below) |
| `state.get`, `state.set`, `state.patch`, `state.snapshot`, `state.watch`, `state.changed` | plugin or CLI, and host | Live JSON slices; see [State](design/state.md) |
| `toast.show` | plugin to host | Show a notification |
| `theme.changed`, `theme.get` | host to plugin; plugin to host | The palette |
| `settings.*`, `secrets.*`, `keys.list`, `plugin.*`, `config.reload`, `profile.*` | plugin or CLI to host | Services; see [Services](design/services.md) |
| `protocol.error` | either way | A line was rejected and could not be answered as a reply (see [Errors](#errors)) |

Direction rule: stdin carries Husk's requests and notifications; stdout carries the plugin's replies and nothing else. Everything a plugin sends on its own initiative, a render above all, goes to the socket over a connection it keeps open, with its token on each request. There are no `declare` or `subscribe` methods: actions, surfaces, focus-local key bindings, published slots, hooks, events and the plugin's State slice are declared in the manifest (see [Declaring in the manifest](#declaring-in-the-manifest)).

### JSON Schema

[`wire-schema-v2.json`](wire-schema-v2.json) is a JSON Schema (draft 2020-12) of the envelope plus one params and one result schema per method, keyed by method name. It is generated from the `pkg/protocol` types by `cmd/genschema` (`go generate ./pkg/protocol`), and a test fails when the committed file is stale. It encodes:

- a property is `required` exactly when its Go `json` tag has no `omitempty`; everything else is optional and may simply be left out;
- a Go slice or map that is nil is encoded as `null`, so required arrays and objects also accept `null`;
- no object sets `additionalProperties: false`, because minor releases add optional fields: validators must tolerate unknown fields, as the host does.

The schema describes shape only: it does not say which caller may use which method (see [Errors](#errors)).

### Enumerated fields

Fields with a closed vocabulary use named Go string types in `pkg/protocol`, and the package exports the validation so consumers do not re-implement it: `SurfaceID` (`surface.update.surface`, `input.key.surface`, ...), `KeyPhase` (`input.key_state.phase`: `down`, `up`, `repeat`, `sync`) and the error `Code` each have a `Valid()` method, and `KeyStateParams` and `SurfaceKeyParams` have `Validate()`. The named types marshal exactly as plain strings, and the schema keeps them as `string` because minor releases may add values.

### Versioning

`protocol.Version` is `"MAJOR.MINOR"` (currently `2.0`). A minor release only adds optional fields, methods, error codes and enum values, which both sides ignore when they do not know them; a major release is a breaking change. Husk speaks one major at a time and accepts any minor of its own.

There is no handshake, so the check happens where the plugin is introduced: its manifest's integer `protocol` must equal `protocol.Major` (checked by `Spec.Validate` at conversion; a mismatched plugin is not launched). A plugin that still sends a v1 `hello` gets one v1-shaped `protocol_error` line, and its Pod fails with `version_mismatch`. The host's full version is in the plugin's `HUSK_PROTOCOL_VERSION` environment variable and in the `session.ping` result. `protocol.CheckVersion` validates a "MAJOR.MINOR" string for Go plugins.

`pkg/protocol` imports only the Go standard library: `FrameSpec` and `Palette` are plain wire structs, and the host converts them to its own `pkg/frame` and `pkg/theme` types, so a Go plugin importing the wire package pulls in no rendering code.

### Errors

A request that fails gets an error reply `{"id": …, "error": {"code": …, "message": …}}`; the codes are stable strings such as `invalid_params`, `unknown_method`, `denied`, `not_found`, `unavailable`, `timeout`, `busy` and `line_too_long` (the full list is in [Protocol](design/protocol.md)). A line that is not a usable request gets a `protocol.error` notification with the code and message, and the connection stays open:

| Input | Code |
| :--- | :--- |
| Invalid JSON, or a field of the wrong JSON type | `invalid_params` or `parse_error`, naming the decoder error |
| A line over 4 MiB | `line_too_long` (the line is discarded up to its newline, so the lines after it still arrive) |
| A method the host does not serve, or one a plugin may not call | `unknown_method` or `denied` |
| Params that fail validation | `invalid_params` |
| More than 64 requests in flight or 64 pending ordered notifications on one connection | `busy` |

A rejected message has no effect. Each rejection is logged. A peer never answers a `protocol.error` with another one, or two strict peers would loop; the host sends at most 10 per second per channel.

Frame declarations live in the manifest (`surfaces[].frame`) and on rail items (`frame`). Both are optional: a plugin that omits them renders as a blank canvas.

`theme.changed` carries generic colour roles only (`canvas`, `slot_background`, `surface*`, `border*`, `ink*`, `accent*`, `success`, `warning`, `danger`, `info`). Values are `#RRGGBB` strings (CharmTone). `slot_background` is the colour Husk paints under slot content; a plugin that wants its own background to blend with the slot, rather than cut out of it, can use it, and one that ignores it is unaffected. A plugin that needs its own colour set, such as one colour per entity, defines it itself.

## Input routing

Husk binds no keys to any application. Keys are resolved against layered keymaps ([Keybindings](design/keybindings.md)): the built-in `husk` layer, then the distribution, user and profile layers, then each plugin's focus-local bindings. A key press is resolved in this order:

1. **Palette and modal.** While the palette is open it takes every key; printable text of any script (not only ASCII) is appended to the query and `backspace` removes one character. While a modal rail item is open, only bindings scoped to `modal-surface` and the toggle that opened it are considered: `esc` or that item's own key closes it and every other key goes to the modal plugin as `input.key` on `modal-surface`.
2. **Keymap bindings.** The host workbench keys (quit, home, palette, dock toggles, resize, the slot explorer, `tab` focus cycling) are ordinary bindings of the built-in layer that a user can rebind. A binding runs an action: a `husk.*` host action or a rail item ID runs in the host; any other ID is sent as `action.invoke` to the one plugin that owns it.
3. **Focus-local bindings.** A plugin's manifest `keybindings` (`{"key":"n","action":"a.new","focus":"sidebar-list"}`) apply only while the focused surface is one that plugin occupies, and may name only its own actions or `husk.*` host actions. There are no plugin-wide global bindings, so a plugin's `r` or `1` never steals typing from another plugin's focused surface. A rail item's `shortcut` is only a suggestion; a key for it is bound in a keymap layer.
4. **Focused surface.** Anything left goes as `input.key` to the plugin occupying the focused surface. For the `full-canvas` or `workspace-stage` surface opened by an active rail item, that is the rail item's plugin when no plugin claimed the surface in its manifest. A key for an unoccupied surface is dropped.

Keys use one spelling (`"f4"`, `"ctrl+g"`, `"alt+2"`, `"n"`, `" "`) and match exactly. `alt+` bindings need a terminal that sends Alt as Meta; layout-specific characters a terminal produces for Option combinations are passed through unchanged. Below the minimum size only the quit binding resolves.

A palette command whose ID equals a rail item's ID activates that rail item, so a plugin can list its rail items as actions. The activity rail lists rail items in registration order.

All chrome is derived from what plugins declared. With no plugins connected the rail holds only Home, the slot explorer and the palette; the palette lists only the host's `husk.*` workbench actions plus the actions of live plugins; the top bar lists only declared rail items. The footer always shows the host keys and adds the hinted focus-local bindings of the plugin owning the focused surface (at most four, first declaration wins per key). Bindings without a hint are not advertised.

## Published slots, hooks and events

Plugins can publish extension points for other plugins. Resource IDs use the
publisher's `<plugin_id>.<name>` namespace; contribution IDs and `on_click`
action IDs use the contributor's namespace. Husk refuses foreign namespaces
with an `invalid_params` or `denied` error reply.
A plugin ID contains letters, digits, hyphens, underscores or dot-separated
components (existing IDs such as `bash.file-search` remain valid); `husk` is reserved.
The **final dot** separates the plugin ID from one non-empty, dot-free resource
name. This applies to publications, contribution IDs and `on_click`
actions: `x.y.button` belongs to plugin `x.y`, never parent plugin `x`.
Ownership checks use the exact authenticated plugin, not a namespace prefix.

The host registers its own surfaces in the same catalog, internally as
`husk.sidebar-list`, `husk.main-stage`, etc. Surface IDs such as
`sidebar-list` stay unchanged in `surface.update`, input and resize messages.
A surface has one occupant: the first claim wins, and a refused claim never
takes the surface.

### Slots

Publisher A claims a host surface and declares its extension points in its
manifest (see [Declaring in the manifest](#declaring-in-the-manifest)). Another
plugin contributes with a request:

```json
{"id":1,"method":"slot.contribute","token":"…","params":{"slot_id":"a.agent-line-action","id":"b.inspect-button","content":"[Inspect]","on_click":"b.inspect"}}
```

`content` is a string, including ANSI styles. Contributions target another
publisher's declared slot. Sending the same contribution ID to the same slot
replaces its content without changing its position; distinct contributions
appear in arrival order. The publisher receives a `slot.contributions` snapshot
whenever the list changes, including an empty list after cleanup:

```json
{"method":"slot.contributions","params":{"slot_id":"a.agent-line-action","contributions":[{"slot_id":"a.agent-line-action","id":"b.inspect-button","content":"[Inspect]","on_click":"b.inspect","plugin_id":"b"}]}}
```

For an `inline` slot, A places an OSC anchor in a `surface.update` `lines` entry:

```json
{"method":"surface.update","params":{"surface":"main-stage","lines":["agent one  \u001b]husk;slot=a.agent-line-action;key=one\u001b\\"]}}
```

On the wire, JSON encodes ESC as `\u001b` and the final backslash as `\\`.
The decoded marker is `ESC ] husk;slot=<id>;key=<row-key> ESC \` (without spaces).
Husk replaces each anchor with that slot's concatenated contributions, clipping
to the remaining terminal-cell width. Only the publisher's own anchors expand;
unknown or foreign anchors disappear. Inline content uses its first line.
Publishers must send anchors, **not** also render the snapshot themselves.

A `region` slot reserves a rectangle relative to the content of a host surface
held by the publisher. Rows stack vertically, multiline content is supported,
and the full reserved rectangle is cleared and clipped to the surface. Regions
compose over the initial blank canvas immediately; no `surface.update` is
required. A region's manifest declaration looks like:

```json
{"id":"a.actions","title":"Actions","description":"Reserved action area","kind":"region","rect":{"surface":"main-stage","x":0,"y":5,"width":28,"height":3}}
```

Coordinates are zero-based terminal cells in the surface's frame-aware inner
rect. A declared border consumes one cell per edge; label/close overlays do not
add a title row. Chrome-covered contribution cells do not invoke actions.
If rectangles overlap, later slot IDs in lexical order overlay earlier ones.
A click on visible contribution text with `on_click` goes directly to its
contributor as an `action.invoke`, not to the publisher's row handler:

```json
{"id":9,"method":"action.invoke","params":{"action":"b.inspect","args":{"slot_id":"a.agent-line-action","contribution_id":"b.inspect-button","row_key":"one"}}}
```

`row_key` identifies the anchor instance (empty for region contributions).
Without `on_click`, a contribution is passive: clicks retain the publisher's
normal `input.mouse` routing instead of being swallowed.

### Hooks

Hooks fan a call out to every subscribed plugin. A caller sends one
`hook.call`; the host sends the same request to each subscriber's stdin and
replies to the caller once, after every subscriber has answered, timed out or
disconnected. They are not vetoes: the reply lists results and the caller decides.

```json
{"id":12,"method":"hook.call","token":"…","params":{"hook":"a.before-refresh","data":{"reason":"manual"}}}
{"id":12,"result":{"results":[{"plugin_id":"b","data":{"ready":true}},{"plugin_id":"c","error":{"code":"timeout","message":"no reply in 10s"}}]}}
```

Any authenticated plugin or the CLI may call a declared hook. Each result names
the authenticated subscriber. A subscriber that does not answer within 10
seconds is answered for with a `timeout` error and its late reply is dropped.
No subscribers means an empty `results`. Pending-call caps and deadlines are
owned by [Calls](design/calls.md); the hook runner is specified in
[Message kinds](design/message-kinds.md). Declared `title` and `description` of
hooks and events are kept by the host for introspection.

### Events and lifetime

Events are named fan-out. A plugin or the CLI emits with `event.emit`; the reply
says the event was accepted onto the bus, not delivered. Subscribers (declared
in their manifest, or a socket connection that sent `event.watch`) receive an
`event.deliver` notification:

```json
{"id":3,"method":"event.emit","token":"…","params":{"name":"a.selection-changed","data":{"row":"one"}}}
{"method":"event.deliver","params":{"name":"a.selection-changed","source":"a","data":{"row":"one"}}}
```

Husk sets `source` from the authenticated caller and delivers only to current
subscribers; unrelated plugins do not receive it. Declarations are immutable
while the plugin runs.

When a plugin's process ends or its channel closes, Husk drops its subscriptions,
occupied surfaces and contributions, clears stale surface content, and sends
updated `slot.contributions` snapshots to affected publishers. Calls still
waiting on it fail with a coded error. Its declarations stay in the catalog, so
the plugin remains listed with its `failed` or `exited` status.

### Declaring in the manifest

Declarations are manifest-only. A plugin's `husk-plugin.json` carries its
`protocol` major, `command`, `surfaces` (each with an optional `frame`), `actions`, `keybindings`,
`rail_items`, `published_slots`, `hooks` and `events`, plus optional State
slice, settings and secrets declarations. [Pods](design/pods.md) specifies the
schema and `internal/pods/manifest` converts it into the plain declaration
catalog the rest of the host reads:

```json
{
  "manifest_version": 2, "protocol": 2,
  "id": "a", "name": "A", "version": "1", "command": ["./a"],
  "surfaces": [{"id": "main-stage"}],
  "published_slots": [{"id":"a.agent-line-action","title":"Row actions","kind":"inline"}],
  "hooks": {"publish": [{"id":"a.before-refresh","title":"Before refresh"}]},
  "events": {"publish": [{"name":"a.selection-changed"}]}
}
```

A manifest without `manifest_version` is read as the older form (`slots`, array-shaped `hooks` and `events`).

Declarations get the same validation as runtime requests (the
`<plugin-id>.<name>` namespace, slot `kind`, a positive `rect`); a region's
`rect.surface` must be one of the manifest's own `surfaces`. A manifest with an
invalid declaration is not launched: Husk logs the problem and registers nothing
from it, and other plugins still launch. Focus-local `keybindings` are validated
at conversion too: a binding for a surface the plugin does not occupy, for
another plugin's action, or beyond the first 128 is dropped with a reported
problem.

`plugins/reference` is a runnable example of a publisher (frame, inline slot, hook and event) and `plugins/reference-actions` of a contributor; see [Getting Started](getting-started.md#reference-plugin). A contributor that starts before its publisher is refused and should retry.

Run `husk --dump-frame --demo pubslots` to render two in-process fake plugins:
A draws anchored rows and reserves a region; B supplies their actions. The demo
uses the same registration, routing and rendering paths without external tools.

## Design rule

Husk follows `bb`'s dogfooding rule: an API that performs a core action must be the path core itself uses. Husk knows nothing about any application, so application support has to go through the public protocol like any other plugin.

Background and trade-offs are in the design research notes, kept outside the repository.
