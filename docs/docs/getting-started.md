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
- `transport` is `socket` (default; the plugin connects to the Unix socket named by `HUSK_SOCKET_PATH`) or `stdio` (the same JSON-lines protocol over stdin/stdout).
- `--plugin <path>` accepts an executable, a plugin directory, or a `husk-plugin.json` path, and may be repeated. `--plugin-dir <dir>` loads every immediate subdirectory of `<dir>` that contains a `husk-plugin.json`, and may also be repeated.

## Activity rail

The rail on the left starts a plugin in one of three placements.

| Placement | Example | Shortcut | Behavior |
| :--- | :--- | :--- | :--- |
| `modal` | — | `F2`, `ctrl+f` | Floating overlay |
| `full-canvas` | — | `F3`, `ctrl+g` | Replaces the whole canvas right of the rail |
| `stage` | `project-git` | `F4`, `ctrl+e` | Fills the canvas but keeps the sidebar visible |

## Keys

| Key | Action |
| :--- | :--- |
| `ctrl+p`, `:` | Command palette |
| `[`, `]`, `/` | Toggle sidebar, toggle inspector, cycle dock layouts |
| `=` | Reset slot sizes |
| `1`, `2`, `3` | Sidebar views: spaces and agents, topology tree, agent mission control |
| `Esc` | Dismiss a modal or leave full-canvas and stage modes |
| `q`, `ctrl+c` | Quit and stop all plugin processes |

Slot borders can be resized by dragging them with the mouse.
