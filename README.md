# Husk

A minimal, language-agnostic TUI workbench. Husk owns the screen, a set of **slots**, and an event bus. Everything you see in it is a **plugin**: an independent OS process, written in any language, that talks to Husk over newline-delimited JSON (requests and notifications on a Unix socket, host messages on stdin).

Why: terminal tools usually force a choice between a monolithic app and plugins that can only be panes in a grid. Husk gives plugins real places in the interface (sidebar, header, stage, overlays) while keeping them fault-isolated. A crashing plugin never takes the workbench down.

Husk is an application-agnostic TUI workbench; everything app-specific lives in plugins. It only provides surfaces, hooks and events.

## Status

Early. The workbench core, wire protocol, activity rail, command palette, mouse-resizable slots and plugin manifests work. The bundled plugins are a small reference plugin pair (`plugins/reference`, `plugins/reference-actions`). See the [documentation](docs/) and the open work in beads.

## Quick start

```bash
go build -o bin/husk ./cmd/husk
./bin/husk                                # empty workbench, no plugins
go build -o plugins/reference/reference ./plugins/reference   # the reference plugins run this binary
./bin/husk --plugin-dir ./plugins         # load every bundled plugin
./bin/husk --plugin ./plugins/reference   # load one plugin (dir, manifest, or executable)
```

Frame dumps render without a terminal:

```bash
./bin/husk --dump-frame                     # default workbench
./bin/husk --dump-frame --hold-alt          # rail shows shortcuts instead of icons
./bin/husk --dump-frame --rail modal        # also: full-canvas, stage
./bin/husk --demo-frame                     # one frame from a built-in demo plugin that declares border+label+close
```

`--dump-frame` always prints the frame on stdout. It exits 0 when the run settled and 1 when no plugin went live, when the wait timed out, or when the host stopped on its own (the cause is printed once on stderr), so scripts and CI can tell a rendered workbench from an empty one.

## Concepts

The layout has nine fixed slots (`activity-rail`, `sidebar-navigation`, `sidebar-list`, `thread-row-status`, `sidebar-footer`, `main-header`, `main-stage`, `side-panel`, `app-overlay`), modeled on `bb`, plus three placement surfaces (`modal-surface`, `full-canvas`, `workspace-stage`): twelve in all. Plugins mount in one of three placements from the activity rail:

| Placement | Example plugin | Behavior |
| :--- | :--- | :--- |
| `modal` | — | Floating overlay |
| `full-canvas` | — | Replaces the whole canvas right of the rail |
| `stage` | — | Fills the canvas but keeps the sidebar |

Husk binds no application keys. A rail item opens from a rail click or the `shortcut` its plugin suggests (bound in a keymap layer), and a manifest declares further focus-local `keybindings`; see [Architecture](docs/docs/architecture.md#input-routing).

Plugins can publish namespaced slots, hooks and events for other plugins to attach to. Inline slots expand at row anchors; region slots reserve a rectangle. Husk clips contributions and routes their clicks to the contributor, fans out asynchronous hooks, and delivers events only to subscribers. Try `husk --dump-frame --demo pubslots` for a self-contained two-plugin frame, or read [`plugins/reference`](plugins/reference/main.go): a small Go plugin that declares a frame, a published slot, a hook and an event and sizes itself from `surface.resized`, paired with `plugins/reference-actions`, which fills its slot. See [Architecture](docs/docs/architecture.md#published-slots-hooks-and-events) for message shapes and lifecycle.

### Surfaces and frames

A **surface** is a slot's region on screen, and it is a blank canvas: Husk renders plugin content with no border, title or decoration of its own. A **frame** is the chrome a plugin asks for around its content — `border`, `label` (title) and `close` (close button), each independently on or off. Husk draws the frame; the plugin only declares what it wants in its manifest (`frame` on a `surfaces` entry) or on a rail item (`frame`). An absent declaration means no chrome at all. Clicking a declared close button closes a modal / full-canvas / stage surface exactly like `esc`.

## Plugin manifests

A plugin directory contains a `husk-plugin.json` manifest:

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

- `command` is the executable plus args, relative to the manifest directory. A manifest without `manifest_version` is read as the older form (`slots`, `hooks`, `events`; `transport` and `language` are ignored with a warning).
- `surfaces` lists the surfaces the plugin may occupy, each with an optional frame. `published_slots`, `hooks`, `events`, `actions`, `rail_items` and focus-local `keybindings` are declared here too, never on the wire; Husk registers them before the plugin starts. See [Architecture](docs/docs/architecture.md#declaring-in-the-manifest).
- The host starts the plugin with `HUSK_SOCKET_PATH` and `HUSK_PLUGIN_TOKEN`; the plugin sends its requests and notifications on the socket with the token in each message.
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

Colours come from Husk's CharmTone palette (hex values, truecolor, downsampled by Bubble Tea on terminals that lack it), checked for WCAG contrast (4.5:1 for text, 3:1 for borders). Slot content areas sit on a slightly lighter (dark theme) or darker (light theme) shade of the canvas, so each slot reads as a panel; cells a plugin colours itself are never repainted. Your terminal's own colour scheme no longer affects the chrome.
