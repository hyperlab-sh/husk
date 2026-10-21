# Husk

A minimal, language-agnostic TUI workbench. Husk owns the screen, a set of **slots**, and an event bus. Everything you see in it is a **plugin**: an independent OS process, written in any language, that talks to Husk over newline-delimited JSON on a Unix socket or stdio.

Why: terminal tools usually force a choice between a monolithic app and plugins that can only be panes in a grid. Husk gives plugins real places in the interface (sidebar, header, stage, overlays) while keeping them fault-isolated. A crashing plugin never takes the workbench down.

Husk is an application-agnostic TUI workbench; everything app-specific lives in plugins. It only provides surfaces, hooks and events.

## Status

Early. The workbench core, IPC protocol, activity rail, command palette, mouse-resizable slots and plugin manifests work. The bundled plugins (`project-git`) are mock UIs. See the [documentation](docs/) and the open work in beads.

## Quick start

```bash
go build -o bin/husk ./cmd/husk
./bin/husk                                # empty workbench, no plugins
./bin/husk --plugin-dir ./plugins         # load every bundled plugin
./bin/husk --plugin ./plugins/project-git # load one plugin (dir, manifest, or executable)
```

Frame dumps render without a terminal:

```bash
./bin/husk --dump-frame                     # default 3-column workbench
./bin/husk --dump-frame --hold-alt          # rail shows shortcuts instead of icons
./bin/husk --dump-frame --rail modal        # also: full-canvas, stage
./bin/husk --demo-frame                     # one frame from a built-in demo plugin that declares border+label+close
```

## Concepts

The layout has nine slots (`activity-rail`, `sidebar-navigation`, `sidebar-list`, `thread-row-status`, `sidebar-footer`, `main-header`, `main-stage`, `side-panel`, `app-overlay`), modeled on `bb`. Plugins mount in one of three placements from the activity rail:

| Placement | Example plugin | Behavior |
| :--- | :--- | :--- |
| `modal` | — | Floating overlay |
| `full-canvas` | — | Replaces the whole canvas right of the rail |
| `stage` | `project-git` (`F4`) | Fills the canvas but keeps the sidebar |

Husk binds no application keys. A rail item opens from a rail click or the `shortcut` its plugin declares, and plugins declare further `keybindings` in `hello`; see [Architecture](docs/docs/architecture.md#input-routing).

Plugins can publish namespaced slots, hooks and events for other plugins to attach to. Inline slots expand at row anchors; region slots reserve a rectangle. Husk clips contributions and routes their clicks to the contributor, fans out asynchronous hooks, and delivers events only to subscribers. Try `husk --dump-frame --demo pubslots` for a self-contained two-plugin frame. See [Architecture](docs/docs/architecture.md#published-slots-hooks-and-events) for message shapes and lifecycle.

### Surfaces and frames

A **surface** is a slot's region on screen, and it is a blank canvas: Husk renders plugin content with no border, title or decoration of its own. A **frame** is the chrome a plugin asks for around its content — `border`, `label` (title) and `close` (close button), each independently on or off. Husk draws the frame; the plugin only declares what it wants in its `hello` (`frames`, keyed by slot) or on a rail item (`frame`). An absent declaration means no chrome at all. Clicking a declared close button closes a modal / full-canvas / stage surface exactly like `esc`.

## Plugin manifests

A plugin directory contains a `husk-plugin.json` manifest:

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

- `command` is the executable plus args, relative to the manifest directory.
- `transport` is `socket` (default; the plugin connects to the Unix socket named by `HUSK_SOCKET_PATH`) or `stdio` (the plugin speaks the same JSON-lines protocol on stdin/stdout).
- `--plugin <path>` accepts an executable, a plugin directory, or a `husk-plugin.json` path, and may be repeated. `--plugin-dir <dir>` loads every immediate subdirectory of `<dir>` that contains a `husk-plugin.json`, and may also be repeated.

## Keys

| Input | Action |
| :--- | :--- |
| `ctrl+p`, `:` | Command palette |
| `F1`, `alt+1` | Home workbench |
| Rail click, a rail item's declared shortcut | Open or close that rail item |
| `[`, `]`, `/` | Toggle sidebar, toggle inspector, cycle dock layouts |
| Drag a slot border, `=` | Resize slots, reset sizes |
| Hold `Alt`/`Option` | Show shortcuts on the rail |
| `Esc` | Dismiss a modal or leave full-canvas and stage modes |
| `q`, `ctrl+c` | Quit and stop all plugins |

Colours come from the Charm default palette: CharmTone hex values (truecolor, downsampled by Bubble Tea on terminals that lack it), checked for WCAG contrast (4.5:1 for text, 3:1 for borders). Your terminal's own colour scheme no longer affects the chrome.
