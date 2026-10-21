# Husk

A minimal, language-agnostic TUI workbench. Husk owns the screen, a set of **slots**, and an event bus. Everything you see in it is a **plugin**: an independent OS process, written in any language, that talks to Husk over newline-delimited JSON on a Unix socket or stdio.

Why: terminal tools usually force a choice between a monolithic app and plugins that can only be panes in a grid. Husk gives plugins real places in the interface (sidebar, header, stage, overlays) while keeping them fault-isolated. A crashing plugin never takes the workbench down.

Husk is the host for [Dolphin](https://github.com/hyperlab-sh/dolphin), an agentic development environment TUI. It knows nothing about Dolphin or any other application; it only provides surfaces, hooks and events.

## Status

Early. The workbench core, IPC protocol, activity rail, command palette and mouse-resizable slots work. The rail plugins (`tv-search`, `fleet-dashboard`, `project-git`) are mock UIs. See the [documentation](docs/) and the open work in beads.

## Quick start

```bash
go build -o bin/husk ./cmd/husk
./bin/husk
```

Frame dumps render without a terminal:

```bash
./bin/husk --dump-frame                     # default 3-column workbench
./bin/husk --dump-frame --hold-alt          # rail shows shortcuts instead of icons
./bin/husk --dump-frame --rail modal        # also: full-canvas, stage
```

## Concepts

The layout has nine slots (`activity-rail`, `sidebar-navigation`, `sidebar-list`, `thread-row-status`, `sidebar-footer`, `main-header`, `main-stage`, `side-panel`, `app-overlay`), modeled on `bb`. Plugins mount in one of three placements from the activity rail:

| Placement | Example plugin | Trigger | Behavior |
| :--- | :--- | :--- | :--- |
| `modal` | `tv-search` | `F2`, `ctrl+f` | Floating fuzzy finder |
| `full-canvas` | `fleet-dashboard` | `F3`, `ctrl+g` | Replaces the whole canvas right of the rail |
| `stage` | `project-git` | `F4`, `ctrl+e` | Fills the canvas but keeps the sidebar |

See [Architecture](docs/docs/architecture.md) for the protocol.

## Keys

| Input | Action |
| :--- | :--- |
| `ctrl+p`, `:` | Command palette |
| `F1`–`F4`, rail click | Switch rail mode |
| `[`, `]`, `/` | Toggle sidebar, toggle inspector, cycle dock layouts |
| Drag a slot border, `=` | Resize slots, reset sizes |
| Hold `Alt`/`Option` | Show shortcuts on the rail |
| `Esc` | Dismiss a modal or leave full-canvas and stage modes |
| `q`, `ctrl+c` | Quit and stop all plugins |
