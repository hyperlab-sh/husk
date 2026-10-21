---
title: "Architecture"
description: "Slots, plugins and the IPC protocol."
---

# Architecture

## Process model

Husk is the host. Plugins are separate processes that Husk spawns and supervises. Husk and a plugin exchange newline-delimited JSON over a Unix socket or stdio. The protocol is the only contract between them, so a plugin can be written in any language.

## Slots

Husk uses a nine-slot layout modeled on `bb`.

| Slot | Purpose |
| :--- | :--- |
| `activity-rail` | Far-left icon bar |
| `sidebar-navigation` | Mode and view switcher |
| `sidebar-list` | Spaces, trees and agents |
| `thread-row-status` | Telemetry, spinners and gauges |
| `sidebar-footer` | Daemon health, metrics and shortcuts |
| `main-header` | Stage title and breadcrumbs |
| `main-stage` | Main canvas, or the active stage plugin |
| `side-panel` | Inspection drawer |
| `app-overlay` | Command palette and modal plugins |

Two dynamic placements sit on top of these: `full-canvas` (everything right of the rail) and `workspace-stage` (the canvas minus the sidebar).

## Surfaces and frames

A slot's region on screen is a **surface**, and it is a blank canvas: without a declaration from the occupying plugin, Husk renders its content with no border, title or decoration of any kind. A **frame** is the chrome a plugin asks Husk to draw around its surface:

| Element | Effect | Geometry |
| :--- | :--- | :--- |
| `border` | Box border around the surface | Consumes one cell per edge |
| `label` | Title text in/over the top edge | None |
| `close` | ✕ button; click closes like `esc` | None |

Each element is independently on or off. A plugin declares frames in its `hello` message (`"frames": {"<slot-id>": {"border": true, "label": "…", "close": true}}`) and a rail item can declare one for the surface it opens (`"frame"` on the rail item). Only the border changes layout: plugins that declare a frame get a `slot_resize` with the inner dimensions; plugins that declare nothing get the full rect. Toasts and the command palette are Husk's own UI and keep their own chrome.

## Protocol

Messages in `pkg/huskipc/protocol.go`:

| Message | Direction | Purpose |
| :--- | :--- | :--- |
| `hello`, `welcome` | plugin to host, host to plugin | Registration |
| `slot_update` | plugin to host | Render a slot |
| `slot_resize` | host to plugin | Slot dimensions changed |
| `slot_key`, `slot_mouse` | host to plugin | Input routed to a focused slot |
| `key_state` | host to plugin | Modifier state, such as Alt held |
| `workspace_context` | plugin to host, then broadcast | Selected workspace (`cwd`, label, repo) |
| `command_invoke` | host to plugin | A palette command was chosen |

Frame declarations ride on existing messages: `hello.frames` (per-slot) and the `frame` field on rail items. Both are optional and additive — an older plugin that omits them simply renders as a blank canvas.

## Published slots, hooks and events

Plugins can publish extension points for other plugins. Resource IDs use the
publisher's `<plugin_id>.<name>` namespace; contribution IDs and `on_click` command
IDs use the contributor's namespace. Husk rejects foreign namespaces with
`{"type":"protocol_error","protocol_error":{"message_type":"slot_declare","message":"..."}}`.
A plugin ID contains letters, digits, hyphens, underscores or dot-separated
components (existing IDs such as `bash.tv-search` remain valid); `husk` is reserved.

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
and the full reserved rectangle is cleared and clipped to the surface:

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
There is a maximum of 1,024 pending calls; callers should not assume all plugins
will respond. Disconnecting a subscriber or hook publisher cancels affected
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

Run `husk --dump-frame --demo pubslots` to render two in-process fake plugins:
A draws anchored rows and reserves a region; B supplies their actions. The demo
uses the same registration, routing and rendering paths without external tools.

## Design rule

Husk follows `bb`'s dogfooding rule: an API that performs a core action must be the path core itself uses. Husk knows nothing about any application, so application support has to go through the public protocol like any other plugin.

Background and trade-offs are in the design research notes, kept outside the repository.
