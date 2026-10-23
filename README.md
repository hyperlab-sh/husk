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
./bin/husk --instance notes               # a named instance: husk ping --instance notes reaches it
```

`--instance <name>` names the instance (`[a-z0-9][a-z0-9-]*`, at most 32 bytes, not digits only; invalid is exit 2 before anything is created); without it the instance is called by its pid. A second start with a name a live instance holds fails with `instance "<name>" is already running`. Only named instances persist instance- and workspace-scoped state.

Frame dumps render without a terminal:

```bash
./bin/husk --dump-frame                     # default workbench
./bin/husk --dump-frame --hold-alt          # rail shows shortcuts instead of icons
./bin/husk --dump-frame --rail modal        # also: full-canvas, stage
./bin/husk --demo-frame                     # one frame from a built-in demo plugin that declares border+label+close
```

`--dump-frame` always prints the frame on stdout. It exits 0 when the run settled and 1 when no plugin went live, when the wait timed out, when `--rail` names a placement no loaded plugin declares a rail item for, or when the host stopped on its own (the cause is printed once on stderr), so scripts and CI can tell a rendered workbench from an empty one.

## Command line

A first non-flag argument makes `husk` a client of a running instance instead of starting one. Run these from another shell, or from a plugin (`HUSK_SOCKET_PATH` and `HUSK_INSTANCE` select the instance):

```bash
husk instances                        # list running instances and whether they answer
husk ping                             # session.ping to the one running instance
husk rpc session.ping '{}'            # any method, params as a JSON object, raw result
husk ping --instance 4242             # choose an instance by id (or --socket <path>)
husk ping --format table --timeout 5s

husk state                            # slice descriptors (state.snapshot without values)
husk state get <slice> [<pointer>]    # one slice, or the value at a JSON Pointer
husk state dump                       # every slice with its value
husk state watch [<slice>...]         # baseline, then one JSON line per change
husk state set '<json>'               # {"slice": ..., "value": ...}; a plugin's own slice (else denied)
husk state patch '<json>' [--if-revision <n>]   # {"slice": ..., "patch": ...}, merge patch
husk state clear <namespace>          # reset a slice to its initial value (the user only)
husk action list [--owner <id>]       # the action catalog
husk action invoke <action-id> [--arg k=v]... [--args-json '<json>']
husk event emit <name> [--data '<json>']
husk event watch '<pattern>'          # one JSON line per matching event, e.g. 'user.*'
```

JSON arguments (`<json>`, `--args-json`, `--data`) must be objects; `-` reads one from stdin. `--arg` pairs build an object of strings and exclude `--args-json`. `state watch` and `event watch` print the opening reply and then one JSON line per notification until a signal ends them; they reject `--timeout`, bound the opening at 5 s, and exit 5 (`instance went away`) when the instance exits. The CLI checks syntax only: ownership, namespace and permission rules are the instance's and come back as exit 1 with its message.

Global flags follow the subcommand (`husk ping --format json`); before it they would reach the instance's own flag parser. `ping` and `rpc` take `--instance <id>` or `--socket <path>` (not both), `--format json|table` (default `table` on a terminal, `json` otherwise) and `--timeout <duration>`; the flags may come before or after the arguments. `instances` reads the local registry only, so it takes `--format` and rejects `--instance`, `--socket` and `--timeout` (exit 2). Explicit targets (`--socket`, `--instance`, `HUSK_SOCKET_PATH`, `HUSK_INSTANCE`) are never probed; only discovery (no target, so the registry is surveyed) probes, 500 ms per entry. With exactly one live instance, that one is used; unresponsive entries are reported as `unresponsive` warnings. The client opens one connection and sends `session.ping` first to check the protocol major, then the command's request. `--timeout` starts after connecting (`ping` 2s, `rpc` 30s by default; `0` means the default, a negative value is a usage error). `husk` installs no signal handlers, so SIGINT, SIGTERM and SIGPIPE end it by the signal. An absent runtime directory is an empty registry (`husk instances` lists nothing, discovery exits 3); an unusable one (not private to the user, or a symlink) exits 7.

Results go to stdout. Errors go to stderr in the chosen format: one JSON object in JSON mode, `husk: <message>` (plus candidate lines) in table mode. Exit codes: 0 success, 1 the instance replied with an error, 2 usage, 3 no instance, 4 several instances and none chosen, 5 transport (connect failed, timeout, connection dropped, or only unresponsive instances), 6 protocol (unreadable reply or a different protocol major), 7 environment (unusable runtime directory). The JSON `code` is from a closed set: `usage`, `no_instance`, `ambiguous`, `transport`, `protocol`, `environment`, plus the instance's own code for exit 1. See [Instances and the CLI](docs/docs/design/instances.md).

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
