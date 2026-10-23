---
title: "Getting Started"
description: "Build and run Husk."
---

# Getting Started

## Build

```bash
go build -o bin/husk ./cmd/husk
go vet ./...
go test ./pkg/...
```

## Run

```bash
./bin/husk                                # empty workbench: no plugins without flags
go build -o plugins/reference/reference ./plugins/reference   # the reference plugins run this binary
./bin/husk --plugin-dir ./plugins         # load every bundled plugin via its manifest
./bin/husk --plugin ./plugins/reference   # load one plugin (directory, husk-plugin.json, or executable)
```

Non-interactive frame dumps are useful for checking rendering without a terminal:

```bash
./bin/husk --dump-frame                                # empty workbench
./bin/husk --dump-frame --plugin-dir ./plugins    # reference plugins: frame, filled row-action slot
./bin/husk --dump-frame --hold-alt                     # rail shows shortcuts instead of icons
```

The frame always goes to stdout. The exit status is 1 when no plugin went live, when the wait timed out, or when the host stopped (the cause is printed once on stderr), and 0 otherwise.

## Plugin manifests

A plugin directory contains a `husk-plugin.json`:

```json
{
  "manifest_version": 2,
  "protocol": 2,
  "id": "go.reference",
  "name": "Reference plugin",
  "version": "0.1.0",
  "description": "Declares a frame, a published slot, a hook and an event",
  "command": ["./reference"],
  "surfaces": [
    {"id": "main-stage", "frame": {"border": true, "label": "Reference plugin", "close": true}}
  ],
  "published_slots": [
    {"id": "go.reference.row-actions", "title": "Row actions", "description": "Actions beside each row", "kind": "inline"}
  ]
}
```

- `command` is the executable plus arguments. A bare `command[0]` (e.g. `python3`) is looked up on `PATH`; a relative path (e.g. `./plugin.sh`) is resolved against the manifest directory, as are relative arguments starting with `./` or `../`. Arbitrary option arguments are never rewritten. Plugins run with the manifest directory as their working directory.
- `manifest_version` and `protocol` must be `2` for the current form. A manifest without `manifest_version` is read as the older form (`slots`, `hooks`, `events`); its `transport` and `language` fields are ignored with a warning.
- `surfaces` is what the plugin may occupy, with an optional frame per surface. Everything a plugin offers (`actions`, `rail_items`, `published_slots`, `hooks`, `events`, focus-local `keybindings`, settings and secret requests) is declared in the manifest and never on the wire, so Husk knows it before the plugin starts. A plugin that claims a surface its manifest does not list is refused. Two plugins with the same `id` cannot be loaded together; the later one fails to launch.
- A plugin is identified by the token Husk gives it at launch (`HUSK_PLUGIN_TOKEN`), not by its process tree; it connects to the Unix socket named by `HUSK_SOCKET_PATH` and sends the token in each message. Host messages arrive on its stdin, and its stdout carries replies.
- `--plugin "<cmd> <args>"` (a path that does not exist) is split like a shell would: spaces separate arguments, single and double quotes group, a backslash escapes; an unterminated quote or trailing backslash is an error. To run an executable whose path contains spaces, point `--plugin` at the file or use a manifest.
- `--instance <name>` names the instance (`[a-z0-9][a-z0-9-]*`, at most 32 bytes, not digits only; invalid is exit 2 before anything is created); without it the instance is called by its pid. A second start with a name a live instance holds fails with `instance "<name>" is already running`. Only named instances persist instance- and workspace-scoped state.
- `--plugin <path>` accepts an executable, a plugin directory, or a `husk-plugin.json` path, and may be repeated. A bare executable has no manifest and declares nothing. `--plugin-dir <dir>` loads every immediate subdirectory of `<dir>` that contains a `husk-plugin.json`, and may also be repeated.

## Reference plugin

`plugins/reference` is the plugin to copy when starting a new one. It is a few hundred lines of Go on top of `pkg/protocol` and uses every extension point:

| Feature | Where |
| :--- | :--- |
| Frame (`border`, `label`, `close`) | `surfaces[].frame` for `main-stage` in `husk-plugin.json` |
| Published inline slot `go.reference.row-actions` | `published_slots` in the manifest; rows carry the `ESC ] husk;slot=…;key=… ESC \` anchor |
| Hook `go.reference.before-refresh` | a `gate` declared under `hooks.publish` in the manifest; `r` and the `go.reference.refresh` action (`husk action invoke go.reference.refresh`) fire it with `hook.call` and the outcome (allowed by N, or vetoed by whom) is shown in the surface status line, not in the CLI output: the action replies as soon as the call is sent |
| Event `go.reference.selection-changed` | declared under `events.publish` in the manifest; `j`/`k` emit it with `event.emit` |
| Width and height | read from `surface.resized`, never hard-coded; it re-renders only when the size or its content changes, not on `input.key_state` |

A plugin cannot contribute to its own slot, so the same binary also runs as `plugins/reference-actions` (`--contributor`): it fills the slot with an `[inspect]` action (`slot.contribute`), subscribes to the hook and the event, allows the gate with an empty reply and toasts on a click. It may start before the publisher has declared anything, so it retries its setup when the host answers with an error. Build the binary first (`go build -o plugins/reference/reference ./plugins/reference`), then `husk --dump-frame --plugin-dir ./plugins` renders the framed surface with `[inspect]` beside each row. `go test ./plugins/reference` drives both roles over pipes.

## Activity rail

The rail on the left starts a plugin in one of three placements.

| Placement | Example | Behavior |
| :--- | :--- | :--- |
| `modal` | — | Floating overlay |
| `full-canvas` | — | Replaces the whole canvas right of the rail |
| `stage` | — | Fills the canvas but keeps the sidebar visible |

Husk binds no keys to any plugin. Each rail item opens from a click on the rail or from the `shortcut` its plugin suggests and a keymap layer binds; pressing the shortcut again closes it.

## Keys

| Key | Action |
| :--- | :--- |
| `ctrl+p`, `:` | Command palette |
| `[`, `]`, `/` | Toggle sidebar, toggle inspector, cycle dock layouts |
| `=` | Reset slot sizes |
| `F1`, `alt+1` | Home workbench |
| `tab` | Move focus between surfaces |
| `Esc` | Dismiss a modal or leave full-canvas and stage modes |
| `q`, `ctrl+c` | Quit and stop all plugin processes |

Slot borders can be resized by dragging them with the mouse. Only a border the occupying plugin declared (`"border": true`) is a drag handle; a click on an unbordered surface always reaches its content. Every other key goes to the plugin occupying the focused surface, unless a keymap layer or the focused plugin's manifest binds it.
