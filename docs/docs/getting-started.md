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
./bin/husk --plugin-dir ./plugins         # load every bundled plugin via its manifest
./bin/husk --plugin ./plugins/project-git # load one plugin (directory, husk-plugin.json, or executable)
```

Non-interactive frame dumps are useful for checking rendering without a terminal:

```bash
./bin/husk --dump-frame                                # empty workbench
./bin/husk --dump-frame --plugin-dir ./plugins --rail stage
./bin/husk --dump-frame --hold-alt                     # rail shows shortcuts instead of icons
```

## Plugin manifests

A plugin directory contains a `husk-plugin.json`:

```json
{
  "id": "bash.project-git",
  "name": "Project Git Stage",
  "version": "0.1.0",
  "language": "Bash",
  "description": "Lazygit-style git stage",
  "command": ["./plugin.sh"],
  "transport": "stdio",
  "slots": ["workspace-stage"]
}
```

- `command` is the executable plus arguments. A bare `command[0]` (e.g. `python3`) is looked up on `PATH`; a relative path (e.g. `./plugin.sh`) is resolved against the manifest directory, as are relative arguments starting with `./` or `../`. Arbitrary option arguments are never rewritten. Plugins run with the manifest directory as their working directory.
- `command` given on the command line as `--plugin "<cmd> <args>"` (a path that does not exist) is split like a shell would: spaces separate arguments, single and double quotes group, a backslash escapes. An unterminated quote or trailing backslash is an error. To run an executable whose path contains spaces, point `--plugin` at the file or use a manifest.
- `slots` is enforced. A plugin launched from a manifest must send a `hello` whose `plugin_id` equals the manifest `id` and whose `slots` are exactly the manifest's `slots` (order and duplicates ignored); otherwise Husk replies with a `protocol_error` and closes the connection. A manifest with no `slots` forbids claiming any surface. Plugins started from a bare executable or command line have no manifest and are not checked.
- `transport` is `socket` (default; the plugin connects to the Unix socket named by `HUSK_SOCKET_PATH`) or `stdio` (the same JSON-lines protocol over stdin/stdout).
- `--plugin <path>` accepts an executable, a plugin directory, or a `husk-plugin.json` path, and may be repeated. `--plugin-dir <dir>` loads every immediate subdirectory of `<dir>` that contains a `husk-plugin.json`, and may also be repeated.

## Activity rail

The rail on the left starts a plugin in one of three placements.

| Placement | Example | Behavior |
| :--- | :--- | :--- |
| `modal` | — | Floating overlay |
| `full-canvas` | — | Replaces the whole canvas right of the rail |
| `stage` | `project-git` (`F4`) | Fills the canvas but keeps the sidebar visible |

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

Slot borders can be resized by dragging them with the mouse. Every other key goes to the plugin occupying the focused surface, unless a plugin declared a key binding for it.
