---
title: "Architecture"
description: "Slots, plugins and the IPC protocol."
---

# Architecture

## Process model

The Go host uses Bubble Tea v2 (`charm.land/bubbletea/v2`) for its event loop and Lip Gloss v2 (`charm.land/lipgloss/v2`) for styling. Views declare alternate-screen and mouse tracking settings; typed key and mouse events are translated to the existing JSON input messages. This library migration does not change the plugin wire protocol or palette roles.

Husk is the host. Plugins are separate processes that Husk spawns and supervises. Husk and a plugin exchange newline-delimited JSON over a Unix socket or stdio. The protocol is the only contract between them, so a plugin can be written in any language.

### Socket security

The host socket is local-user only. Husk creates a fresh `0700` directory (`husk-*`) under `$XDG_RUNTIME_DIR`, or under a `mkdtemp` directory in the system temp dir when that variable is unset or unusable, and binds `husk.sock` inside it with mode `0600`. The path is exported to plugins as `HUSK_SOCKET_PATH`. On every accepted connection the host reads the peer uid from the kernel (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS) and closes the connection unless it equals the host uid; if the credentials cannot be read, the connection is refused. The socket and its directory are removed on shutdown (the directory stays only while it holds a non-empty plugin stderr log; see [Plugin processes](#plugin-processes)). Platforms without a supported credential API refuse all socket connections; use stdio transport there.

### Outbound queue and disconnect policy

The host never writes to a plugin from its event loop. Each connection has a bounded outbound queue (1,024 messages) drained by its own writer goroutine, and every write has a 5-second deadline (`SetWriteDeadline` on sockets, a watchdog that closes the pipe on stdio). A plugin that stops reading therefore cannot freeze the workbench; the host disconnects it instead:

| Cause | Reason shown |
| :--- | :--- |
| Queue overflows (the plugin fell more than 1,024 messages behind) | `stopped reading: send queue full (1024 messages)` |
| One write takes longer than 5 seconds | `stopped reading: write timed out after 5s` |
| A write fails | `write error: …` |
| The plugin closes its end | `connection closed (EOF)` |
| Reading from the connection fails | `read error: …` |

The host drops the connection rather than individual messages, so a plugin never sees a stream with holes in it. Every disconnect is logged as `husk: plugin <id> disconnected: <reason>` on stderr (held until exit while the TUI owns the terminal) and shown as a warning toast. Disconnecting removes the plugin's surfaces, rail items and publications as described under [Events and lifetime](#events-and-lifetime). A plugin whose `hello` is rejected receives its `protocol_error` before the connection is closed. A connection that ends without a registered `hello` is logged too (`husk: connection closed before hello: <reason>`, or `connection from plugin "<id>" (not registered) closed: <reason>` after a rejected `hello`), so every end of a read loop leaves a reason. A bad input line does not disconnect; see [Errors](#errors).

### Plugin processes

Husk launches each manifest plugin with its declared transport:

- **socket** (the default): `HUSK_SOCKET_PATH` is set and the plugin connects to it.
- **stdio**: the protocol runs over the plugin's stdin and stdout. `HUSK_SOCKET_PATH` is removed from the plugin's environment, including any value Husk itself inherited, so a client library that prefers the socket when the variable is present really uses stdio.

A plugin's stderr is never discarded: it goes to its own log file, `logs/<id>.log` (mode `0600`) in the socket's private directory. Write diagnostics there freely; they do not touch the terminal.

| Event | What Husk does |
| :--- | :--- |
| A manifest plugin's `hello` disagrees with its manifest | Replies with `protocol_error` (`message_type` `hello`; the message names the extra or missing slots, or the mismatched `plugin_id`), logs `rejected hello from plugin ...`, closes the connection and registers nothing. A stdio stream is tied to its process, so its `hello` must carry that manifest's id; a socket connection is bound to the launched process by its kernel-reported peer pid (`LOCAL_PEERPID` / `SO_PEERCRED`), so a launched socket plugin cannot claim another id. A socket peer that is not itself a launched process (a descendant of a wrapper script, or an external client) is matched by the `plugin_id` it claims, so such a peer that claims an id with no manifest is not checked |
| The plugin cannot be started (missing binary, bad command) | Logs `husk: failed to launch plugin <id>: <error>` and shows an error toast |
| The plugin exits while Husk is running | Logs `husk: plugin <id> exited (<status>); stderr log: <path>` plus its last three stderr lines, and shows a warning toast with the status and the last stderr line. For stdio plugins that never sent `hello`, the line reads `exited before hello` |
| A message arrives on a connection before its `hello` | Replies with `protocol_error` (`message_type` is the message's type, `message` is `hello required before <type>`, or `hello payload required` for a `hello` without a payload) and ignores the message |
| The socket listener fails | Logs and toasts `accept failed: <error>; socket plugins can no longer connect` |
| A connection fails the peer uid check | Logs and toasts `rejected IPC connection: <reason>` |

Log lines go to stderr; while the TUI owns the terminal they are held and printed when Husk exits. On shutdown Husk restores the terminal, sends `SIGTERM` to every plugin, waits up to two seconds for them (one deadline for all), then sends `SIGKILL` to the rest and waits for those as well. Exits during shutdown are not reported. Empty stderr logs are deleted; a non-empty one is kept, its path is logged (`husk: plugin <id> stderr kept at <path>`), and the socket directory stays in place to hold it.

## Slots

Husk has nine fixed slots, modeled on `bb`, plus three placement surfaces (below). The publication registry declares all twelve as region slots (`husk.<id>`).

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

Three placement surfaces sit on top of these: `modal-surface` (floating overlay), `full-canvas` (everything right of the rail) and `workspace-stage` (the canvas minus the sidebar).

## Surfaces and frames

A slot's region on screen is a **surface**, and it is a blank canvas: without a declaration from the occupying plugin, Husk renders its content with no border, title or decoration of any kind. A **frame** is the chrome a plugin asks Husk to draw around its surface:

| Element | Effect | Geometry |
| :--- | :--- | :--- |
| `border` | Box border around the surface | Consumes one cell per edge |
| `label` | Title text in/over the top edge | None |
| `close` | ✕ button; click closes like `esc` | None |

Each element is independently on or off. A plugin declares frames in its `hello` message (`"frames": {"<slot-id>": {"border": true, "label": "…", "close": true}}`) and a rail item can declare one for the surface it opens (`"frame"` on the rail item). Only the border changes layout: plugins that declare a frame get a `slot_resize` with the inner dimensions; plugins that declare nothing get the full rect. Toasts and the command palette are Husk's own UI and keep their own chrome.

### Content clipping

A surface shows exactly its inner area (the `slot_resize` width and height). `slot_update.lines` are clipped to it, never wrapped or scrolled: a line wider than the surface is truncated (with `…` when it is plugin text), and lines past the last row are dropped. Husk does not report that it clipped anything; a plugin learns its size from `slot_resize` and is responsible for fitting, paging or scrolling its own content.

### Styles stay inside their cells

Plugin lines may carry ANSI SGR styling. Husk closes any style a line leaves open at the end of that line's content, so it cannot paint the padding after it, the frame edge or a neighbouring surface. Wherever Husk composes one piece of text over another (published regions over a surface, and the modal, toast and command palette over the workbench) it resets the style at each boundary: overlay cells show only the overlay's own styles, and the base cells on either side keep exactly the style they had. Well-formed lines that already end in a reset are passed through byte for byte.

**Modal owner and hit-testing.** The open modal has exactly one source of truth in the host (`modalOwner()`: owning plugin plus the rail item that opened it); every surface, the modal surface included, is owned by its registry occupant, so write authorisation and published-content cleanup follow the `hello` claim and never the plugin whose rail item opened the modal. Mouse hit-testing walks the laid-out surfaces in an explicit z-order (full-canvas above workspace-stage above the regular regions above the activity rail, ties broken by slot ID), so overlapping rects always resolve to the same surface.

**Minimum size.** The layout needs at least 84x24 cells. On a smaller terminal Husk renders a "terminal too small (need 84x24, have WxH)" screen that fills the real terminal exactly (message shortened or truncated on narrow terminals, never wider than the terminal). While too small, mouse and key input are ignored (only quit works and nothing is forwarded to plugins), the last valid layout is kept and no `slot_resize` is sent; growing the terminal back restores the workbench.

## Protocol

Every message is one JSON object on one line (at most 4 MiB, `huskipc.MaxLineBytes`, excluding the newline), with a `type` and the payload under the key of the same name. Core messages (hello, slot updates, input, toasts) are defined in `pkg/huskipc/protocol.go`; published-slot, hook and event messages (and `protocol_error`) in `pkg/huskipc/published.go`:

| Message | Direction | Purpose |
| :--- | :--- | :--- |
| `hello`, `welcome` | plugin to host, host to plugin | Registration |
| `slot_update` | plugin to host | Render a slot |
| `slot_resize` | host to plugin | Slot dimensions changed |
| `slot_key`, `slot_mouse` | host to plugin | Input routed to a focused slot |
| `key_state` | host to plugin | Modifier state, such as Alt held. Sent on transitions only. `modifiers` is authoritative (it comes from the OS poll or the latched state). `held` and `held_keys` for ordinary keys are best effort: a terminal reports presses and, only with the keyboard protocol, releases, so the host drops a key from the held set 320 ms after its last press or repeat and sends an `up` for it then. Do not treat them as ground truth. On terminal blur the host sends `up` for every modifier and held key it had reported (a latched Alt stays) |
| `workspace_context` | plugin to host, then broadcast | Selected workspace (`cwd`, label, repo) |
| `command_invoke` | host to plugin | A palette command was chosen |
| `toast` | plugin to host | Show a notification |
| `close_modal` | plugin to host | Close the open modal |
| `protocol_error` | either way | A message was rejected (see [Errors](#errors)) |

### JSON Schema

[`wire-schema.json`](wire-schema.json) is a JSON Schema (draft 2020-12) for one protocol line, generated from the `pkg/huskipc` structs, so a Rust or Bash plugin author does not have to infer which fields are optional. It encodes:

- a property is `required` exactly when its Go `json` tag has no `omitempty`; everything else is optional and may simply be left out;
- a Go slice or map that is nil is encoded as `null`, so required arrays and objects also accept `null`;
- no object sets `additionalProperties: false`, because minor releases add optional fields: validators must tolerate unknown fields, as the host does (the Dolphin fixtures carry extra palette keys);
- the envelope `type` is an enum of every message type, and each type requires the payload of the same name (`close_modal` is a boolean, not an object).

Regenerate it with `go generate ./pkg/huskipc` after changing any wire type; `TestCommittedSchemaIsCurrent` fails while the committed file is stale, and `TestGoMarshaledEnvelopesValidate` and `TestDolphinFixturesValidate` check that what the structs marshal and what the Rust crate's fixtures contain both validate. The generator lives in `pkg/huskipc/schema` (it also holds the small validator the tests use) and `cmd/genschema`. The schema describes shape only: it does not say which messages a plugin may send (see [Errors](#errors)).

### Versioning

`hello.protocol_version` and `welcome.protocol_version` carry the wire protocol version as `"MAJOR.MINOR"` (`huskipc.ProtocolVersion`, currently `1.1`). A minor release only adds optional fields and message types, which both sides ignore when they do not know them; a major release is a breaking change.

```json
{"type":"hello","hello":{"protocol_version":"1.0","plugin_id":"example.plugin","slots":["main-stage"]}}
{"type":"welcome","welcome":{"protocol_version":"1.1","host_version":"0.1.0","palette":{},"slot_sizes":{},"focused_slot":"main-stage","context":{},"modifiers":{}}}
```

The host accepts a `hello` whose major equals its own, whatever the minor. A `hello` with another major, or a version that is not `MAJOR` or `MAJOR.MINOR`, is rejected with `protocol_error` (`message_type` `hello`, for example `unsupported protocol_version "2.0": host speaks 1.1 (major 1)`) and the connection is closed. A `hello` without `protocol_version` comes from a plugin written before versioning and is treated as major 1. `welcome.host_version` is the host build's version and is informational only; decide compatibility from `protocol_version`. Go plugins can use `huskipc.CheckProtocolVersion`.

`pkg/huskipc` imports only the Go standard library: `FrameSpec` and `Palette` are plain wire structs, and the host converts them to its own `pkg/frame` and `pkg/theme` types, so a Go plugin importing the wire package pulls in no rendering code.

### Errors

The host answers every message it cannot use with a `protocol_error` and keeps the connection open:

| Input | `message_type` | `message` |
| :--- | :--- | :--- |
| Invalid JSON | `""` | `malformed message: <decoder error>` |
| A field of the wrong JSON type, such as `"lines": "oops"` | the line's `type` | `malformed message: json: cannot unmarshal …` |
| A line over 4 MiB | `""` | `line exceeds 4194304 bytes` |
| No `type` | `""` | `message has no type` |
| A `type` the host does not accept from plugins, including host-to-plugin types | the `type` | `unknown message type "<type>"` |
| `slot_update`, `toast` or `workspace_context` without its payload | the `type` | `<type> payload required` |
| An invalid published-slot, hook or event operation | the `type` | why it was refused |

An oversized line is discarded up to its newline, so the messages after it still arrive. A rejected message has no effect, including any `close_modal` it carries. Each rejection is also logged on stderr as `husk: protocol error from plugin <id>: <message>` (or `from unregistered connection` before a `hello`).

A `protocol_error` sent by a plugin, reporting a host message it could not use, is logged as `husk: plugin <id> rejected a "<type>" message: <message>` and never answered: a peer must not reply to a `protocol_error` with another one, or two strict peers would loop.

Frame declarations ride on existing messages: `hello.frames` (per-slot) and the `frame` field on rail items. Both are optional and additive — an older plugin that omits them simply renders as a blank canvas.

`welcome.palette` and `theme_changed.palette` carry generic colour roles only (`canvas`, `surface*`, `border*`, `ink*`, `accent*`, `success`, `warning`, `danger`, `info`). Values are `#RRGGBB` strings (CharmTone), where hosts speaking protocol 1.0 sent ANSI/256 indices such as `"63"`. The JSON shape and keys are unchanged, but the value format is not: a plugin that interpolates a value into `\e[38;5;%sm` or parses it as an integer breaks, while one that passes it to a terminal-colour API keeps working. This is why the host reports protocol `1.1`; a plugin can read `welcome.protocol_version` and treat `1.0` as index values and `1.1` or later as hex. A plugin that needs its own colour set, such as one colour per entity, defines it itself.

## Input routing

Husk binds no keys to any application. A key press is resolved in this order:

1. **Palette and modal.** While the palette is open it takes every key; printable text of any script (not only ASCII) is appended to the query and `backspace` removes one character. While a modal rail item is open, `esc` or that item's own `shortcut` closes it and every other key goes to the modal plugin as `slot_key` on `modal-surface`.
2. **Host shortcuts.** Quit, home (`esc`, `f1`, `alt+1`), palette, dock toggles, resize, the slot explorer and `tab` focus cycling. Plugins cannot override these.
3. **Rail item shortcuts.** A rail item's `shortcut` opens it; pressing it again while it is active returns home.
4. **Plugin key bindings** from `hello.keybindings` (`{"key":"n","command_id":"a.new","when_slot":"sidebar-list"}`). Bindings whose `when_slot` is the focused surface win, then global bindings (no `when_slot`) of the focused surface's owner, then global bindings of other plugins; ties resolve by plugin ID, then declaration order. While the focused surface has an occupant, other plugins' global bindings apply only to chord keys (`ctrl+`, `alt+` and function keys such as `f5`); every other key goes to the occupant in step 5, so a plugin's unscoped `r` or `1` never steals typing from another plugin's focused surface. Scope single-letter bindings with `when_slot` to the surface they serve. When a binding fires, a built-in `husk.*` command or a registered rail item ID runs in the host; any other ID is sent as `command_invoke` to the plugin that declared the binding.
5. **Focused surface.** Anything left goes as `slot_key` to the plugin occupying the focused surface. For the `full-canvas` or `workspace-stage` surface opened by an active rail item, that is the rail item's plugin when no plugin claimed the surface in `hello.slots`. A key for an unoccupied surface is dropped.

Keys use the `slot_key` spelling (`"f4"`, `"ctrl+g"`, `"alt+2"`, `"n"`, `" "`) and match exactly. `alt+` bindings need a terminal that sends Alt as Meta; layout-specific characters a terminal produces for Option combinations are passed through unchanged.

A palette command whose ID equals a rail item's ID activates that rail item, so a plugin can list its rail items as commands. The activity rail lists rail items in registration order.

All chrome is derived from what plugins declared. With no plugins connected the rail holds only Home, the slot explorer and the palette; the palette lists only the host's `husk.*` workbench commands; the top bar lists only declared rail shortcuts. The footer always shows the host keys (home, resize, docks, palette, quit) and adds the `description`-bearing `hello.keybindings` of the plugin owning the focused surface (those scoped to that surface or unscoped, at most four, first declaration wins per key). Bindings without a `description` are not advertised.

## Published slots, hooks and events

Plugins can publish extension points for other plugins. Resource IDs use the
publisher's `<plugin_id>.<name>` namespace; contribution IDs and `on_click` command
IDs use the contributor's namespace. Husk rejects foreign namespaces with
`{"type":"protocol_error","protocol_error":{"message_type":"slot_declare","message":"..."}}`.
A plugin ID contains letters, digits, hyphens, underscores or dot-separated
components (existing IDs such as `bash.file-search` remain valid); `husk` is reserved.
The **final dot** separates the plugin ID from one non-empty, dot-free resource
name. This applies to publications, contribution IDs, request IDs and `on_click`
commands: `x.y.button` belongs to plugin `x.y`, never parent plugin `x`.
Violations return `protocol_error`. Upserts and cleanup also check the exact
authenticated owner, not a namespace prefix.

The host registers its own slots in the same registry, internally as
`husk.sidebar-list`, `husk.main-stage`, etc. Existing surface IDs such as
`sidebar-list` remain **unchanged** in `hello.slots`, `slot_update`, input and
resize messages. A surface has one occupant; an unknown or occupied surface
rejects registration rather than silently replacing another plugin.

### Slots

Publisher A claims a host surface and declares its extension points at connect
time (the existing `hello` fields are omitted here for brevity):

```json
{"type":"hello","hello":{"plugin_id":"a","slots":["main-stage"],"published_slots":[{"id":"a.agent-line-action","title":"Row actions","description":"Actions beside each row","kind":"inline"}],"hooks":[{"id":"a.before-refresh"}],"events":[{"id":"a.selection-changed"}]}}
```

Declarations can also arrive after connection:

```json
{"type":"slot_declare","slot_declare":{"id":"a.agent-line-action","title":"Row actions","description":"Actions beside each row","kind":"inline"}}
{"type":"slot_contribute","slot_contribute":{"slot_id":"a.agent-line-action","id":"b.inspect-button","content":"[Inspect]","on_click":"b.inspect"}}
```

`content` is a string, including ANSI styles. Contributions target another
publisher's declared slot. Sending the same contribution ID to the same slot
replaces its content without changing its position; distinct contributions
appear in arrival order. The publisher receives a snapshot after declaration
and whenever the list changes, including an empty list after cleanup:

```json
{"type":"slot_contributions","slot_contributions":{"slot_id":"a.agent-line-action","contributions":[{"slot_id":"a.agent-line-action","id":"b.inspect-button","content":"[Inspect]","on_click":"b.inspect","plugin_id":"b"}]}}
```

For an `inline` slot, A places an OSC anchor in a `slot_update.lines` entry:

```json
{"type":"slot_update","slot_update":{"plugin_id":"a","slot":"main-stage","lines":["agent one  \u001b]husk;slot=a.agent-line-action;key=one\u001b\\"]}}
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
compose over the initial blank canvas immediately; no `slot_update` is required:

```json
{"type":"slot_declare","slot_declare":{"id":"a.actions","title":"Actions","description":"Reserved action area","kind":"region","rect":{"surface":"main-stage","x":0,"y":5,"width":28,"height":3}}}
```

Coordinates are zero-based terminal cells in the surface's frame-aware inner
rect. A declared border consumes one cell per edge; label/close overlays do not
add a title row. Chrome-covered contribution cells do not invoke commands.
If rectangles overlap, later slot IDs in lexical order overlay earlier ones.
A click on visible contribution text with `on_click` goes directly to its
contributor, not to the publisher's row handler:

```json
{"type":"command_invoke","command_invoke":{"command_id":"b.inspect","args":{"slot_id":"a.agent-line-action","contribution_id":"b.inspect-button","row_key":"one"}}}
```

`row_key` identifies the anchor instance (empty for region contributions).
Without `on_click`, a contribution is passive: clicks retain the publisher's
normal `slot_mouse` routing instead of being swallowed.

### Hooks

Hooks are asynchronous notifications with one response per subscribed plugin,
not synchronous vetoes. Subscribers receive calls in registration order;
responses stream back in completion order. Duplicate subscriptions are ignored.
The caller's `request_id` must use its own namespace and be unique while pending.

```json
{"type":"hook_declare","hook_declare":{"id":"a.before-refresh","title":"Before refresh","description":"Notify extensions"}}
{"type":"hook_subscribe","hook_subscribe":{"id":"a.before-refresh"}}
{"type":"hook_call","hook_call":{"id":"a.before-refresh","request_id":"a.refresh-1","data":{"reason":"manual"}}}
{"type":"hook_result","hook_result":{"id":"a.before-refresh","request_id":"a.refresh-1","data":{"ready":true}}}
```

Any connected plugin may call a declared hook. Husk fans `hook_call` out to its
subscribers and routes each `hook_result` to the original caller, setting
`hook_result.plugin_id` to the authenticated responder. Only a pending subscriber
may respond, once. `hook_result.error` carries an optional error string. No
subscribers means no results; v1 has no all-results completion message or veto.
A subscriber that does not answer within 10 seconds is answered for: the caller
receives a `hook_result` with `plugin_id` set to that subscriber and
`error: "timeout"`, the call is forgotten, and a late real result is refused as
an unknown request. There is a maximum of 1,024 pending calls overall and 64
waiting on any one subscriber; a `hook_call` over either cap is refused with
`protocol_error`. Declared `title` and `description` of hooks and events are
kept by the host for introspection. Disconnecting a subscriber or hook publisher cancels affected
pending results with `error: "plugin disconnected"`.

### Events and lifetime

Only the declaring plugin may emit an event. Subscribers must subscribe to an
existing declaration; unrelated plugins do not receive it. Event emission keeps
the existing envelope key **`event`**, not `event_emit`:

```json
{"type":"event_declare","event_declare":{"id":"a.selection-changed","description":"Selection updated"}}
{"type":"event_subscribe","event_subscribe":{"id":"a.selection-changed"}}
{"type":"event_emit","event":{"plugin_id":"a","topic":"a.selection-changed","data":{"row":"one"}}}
```

Husk sets `event.plugin_id` from the connection and delivers the same message to
subscribers only. `hello.hooks` and `hello.events` contain the same objects as
`hook_declare` and `event_declare`; `hello.published_slots` contains
`slot_declare` objects. These optional fields do not change legacy messages.
Declarations are immutable until disconnect; subscriptions require the publisher
to be connected first. On stream EOF, Husk removes the plugin's declarations,
subscriptions, occupied surfaces and contributions, clears stale surface content,
and sends updated contribution snapshots to affected publishers.

`plugins/reference` is a runnable example of a publisher (frame, inline slot, hook and event) and `plugins/reference-actions` of a contributor; see [Getting Started](getting-started.md#reference-plugin). A contributor that starts before its publisher is refused with `protocol_error` and should retry.

Run `husk --dump-frame --demo pubslots` to render two in-process fake plugins:
A draws anchored rows and reserves a region; B supplies their actions. The demo
uses the same registration, routing and rendering paths without external tools.

## Design rule

Husk follows `bb`'s dogfooding rule: an API that performs a core action must be the path core itself uses. Husk knows nothing about any application, so application support has to go through the public protocol like any other plugin.

Background and trade-offs are in the design research notes, kept outside the repository.
