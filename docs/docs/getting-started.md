---
title: "Getting Started"
description: "Build and run Husk."
---

# Getting Started

## Build

```bash
go build -o bin/husk ./cmd/husk
go build -o bin/herdr-sidebar ./cmd/herdr-sidebar
go vet ./...
```

Plugin manifests reference binaries as `./bin/<name>`, so keep the built binaries in a `bin/` directory next to where you run Husk.

## Run

```bash
./bin/husk
```

Non-interactive frame dumps are useful for checking rendering without a terminal:

```bash
./bin/husk --dump-frame                     # default 3-column workbench
./bin/husk --dump-frame --hold-alt          # rail shows shortcuts instead of icons
./bin/husk --dump-frame --rail modal        # also: full-canvas, stage
```

## Activity rail

The rail on the left starts a plugin in one of three placements.

| Placement | Plugin | Shortcut | Behavior |
| :--- | :--- | :--- | :--- |
| `modal` | `tv-search` | `F2`, `ctrl+f` | Floating fuzzy finder over workspaces, agents and files |
| `full-canvas` | `fleet-dashboard` | `F3`, `ctrl+g` | Replaces the whole canvas right of the rail |
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
