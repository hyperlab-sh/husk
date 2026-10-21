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

## Plugin manifests

A plugin directory contains a `husk-plugin.json`:

```json
{
  "id": "go.reference",
  "name": "Reference plugin",
  "version": "0.1.0",
  "language": "Go",
  "description": "Declares a frame, a published slot, a hook and an event",
  "command": ["./reference"],
  "transport": "stdio",
  "slots": ["main-stage"]
}
```

- `command` is the executable plus arguments. A bare `command[0]` (e.g. `python3`) is looked up on `PATH`; a relative path (e.g. `./plugin.sh`) is resolved against the manifest directory, as are relative arguments starting with `./` or `../`. Arbitrary option arguments are never rewritten. Plugins run with the manifest directory as their working directory.
- `command` given on the command line as `--plugin "<cmd> <args>"` (a path that does not exist) is split like a shell would: spaces separate arguments, single and double quotes group, a backslash escapes. An unterminated quote or trailing backslash is an error. To run an executable whose path contains spaces, point `--plugin` at the file or use a manifest.
- `slots` is enforced. A plugin launched from a manifest must send a `hello` whose `slots` are exactly the manifest's `slots` (order and duplicates ignored) and whose `plugin_id` equals the manifest `id`; otherwise Husk replies with a `protocol_error` and closes the connection. The `plugin_id` is bound to the manifest for stdio plugins and for socket plugins whose connecting process is the one Husk launched or a descendant of it (for example the real plugin a wrapper script started); a socket connection from any other process (for example a daemon that detached from the launched process) is checked against the manifest named by the `plugin_id` it claims. A manifest with no `slots` forbids claiming any surface, so a manifest that omits `slots` but whose plugin claims surfaces is now rejected: add the slots to `husk-plugin.json`. Two plugins with the same `id` cannot be loaded together; the later one fails to launch. Plugins started from a bare executable or command line have no manifest and are not checked.
- `transport` is `socket` (default; the plugin connects to the Unix socket named by `HUSK_SOCKET_PATH`) or `stdio` (the same JSON-lines protocol over stdin/stdout).
- `--plugin <path>` accepts an executable, a plugin directory, or a `husk-plugin.json` path, and may be repeated. `--plugin-dir <dir>` loads every immediate subdirectory of `<dir>` that contains a `husk-plugin.json`, and may also be repeated.

## Reference plugin

`plugins/reference` is the plugin to copy when starting a new one. It is about 300 lines of Go on top of `pkg/huskipc` and uses every extension point:

| Feature | Where in `plugins/reference/main.go` |
| :--- | :--- |
| Frame (`border`, `label`, `close`) | `hello.frames` for `main-stage` |
| Published inline slot `go.reference.row-actions` | `hello.published_slots`; rows carry the `ESC ] husk;slot=…;key=… ESC \` anchor |
| Hook `go.reference.before-refresh` | declared in `hello.hooks`; `r` calls it and the replies are shown |
| Event `go.reference.selection-changed` | declared in `hello.events`; `j`/`k` emit it |
| Width and height | read from `welcome.slot_sizes` and `slot_resize`, never hard-coded; it re-renders only when the size or its content changes, not on `key_state` |

A plugin cannot contribute to its own slot, so the same binary also runs as `plugins/reference-actions` (`--contributor`): it fills the slot with an `[inspect]` action, subscribes to the hook and the event, answers the hook and toasts on a click. It may start before the publisher has declared anything, so it retries its setup when the host answers with `protocol_error` (and never answers one). Build the binary first (`go build -o plugins/reference/reference ./plugins/reference`), then `husk --dump-frame --plugin-dir ./plugins` renders the framed surface with `[inspect]` beside each row. `go test ./plugins/reference` drives both roles over pipes.

## Activity rail

The rail on the left starts a plugin in one of three placements.

| Placement | Example | Behavior |
| :--- | :--- | :--- |
| `modal` | — | Floating overlay |
| `full-canvas` | — | Replaces the whole canvas right of the rail |
| `stage` | — | Fills the canvas but keeps the sidebar visible |

Husk binds no keys to any plugin. Each rail item opens from a click on the rail or from the `shortcut` its plugin declares; pressing the shortcut again closes it.

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

Slot borders can be resized by dragging them with the mouse. Only a border the occupying plugin declared (`"border": true`) is a drag handle; a click on an unbordered surface always reaches its content. Every other key goes to the plugin occupying the focused surface, unless a plugin declared a key binding for it.
