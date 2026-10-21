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

## Design rule

Husk follows `bb`'s dogfooding rule: an API that performs a core action must be the path core itself uses. Husk knows nothing about any application, so application support has to go through the public protocol like any other plugin.

Background and trade-offs are in the design research notes, kept outside the repository.
