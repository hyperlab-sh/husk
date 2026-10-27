# Husk Design Summary

Design history and the shape of what exists today. `README.md` is the user-facing entry point and `docs/docs/architecture.md` is the specification.

## 1. Vision and scope

Husk is an extensible, minimalist TUI workbench that acts strictly as a shell. It offers **surfaces (slots)**, **hooks** and **events** and knows nothing about any application; everything application-specific is a plugin.

- **Host**: written in Go with Charm v2 (`charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`, `x/ansi`).
- **Process plugin model**: every plugin is an independent OS process speaking newline-delimited JSON, over a Unix socket (`HUSK_SOCKET_PATH`) or over stdio, so plugins are language-agnostic and a crashing plugin never takes the host down.
- **Surfaces**: nine fixed slots plus three placement surfaces, twelve in total (all declared in the publication registry as `husk.<id>`):
  - `activity-rail`: far-left navigation icon bar
  - `sidebar-navigation`: top sidebar band
  - `sidebar-list`: primary sidebar surface
  - `thread-row-status`: narrow status surface beside the list
  - `sidebar-footer`: bottom sidebar band
  - `main-header`: header band above the stage
  - `main-stage`: main canvas
  - `side-panel`: collapsible right-hand drawer
  - `app-overlay`: command palette and toasts
  - Placement surfaces opened from the rail: `modal-surface` (floating overlay), `full-canvas` (canvas minus the rail) and `workspace-stage` (canvas minus the sidebar).

## 2. Implemented components

### A. Workbench UI (`pkg/workbench`) and binary (`cmd/husk`)
- The importable workbench exposes `Options`, `Run` and `DumpFrame`; the model, renderer and post gate stay private. The binary retains argv/flags, host construction and output/exit policy.
- Bubble Tea v2 event loop; `View` is pure and derives chrome from the model and the publication registry.
- Dock toggles: `[` (sidebar), `]` (inspector), `/` (cycle dock presets), `=` (reset sizes).
- Command palette (`ctrl+p` / `:`): fuzzy-searchable overlay aggregating core actions and commands registered by running plugins.
- Plugin lifecycle (`pkg/pods`): spawn from manifests, per-plugin stderr logs, exit and launch-failure reports, SIGTERM-then-kill shutdown.
- Socket security: private `0700` directory, `0600` socket, peer uid check. Outbound writes go through a bounded per-connection queue with a write deadline.

### B. Wire protocol (`pkg/protocol`)
- JSON lines, one request, reply or notification per line, with no handshake: a plugin is identified by the credential it receives at launch. The methods and their payloads are specified in `docs/docs/design/protocol.md` and `docs/docs/design/message-kinds.md`.
- Protocol versioning (`MAJOR.MINOR`), a generated JSON Schema (`docs/docs/wire-schema-v2.json`), and a wire package that imports the standard library only.

### C. Activity rail and the three placements
The rail opens a plugin's rail item in one of three placements: `modal` (floating overlay), `full-canvas` (everything right of the rail) and `stage` (the canvas with the sidebar kept visible). Husk binds no key to any plugin: an item opens from a rail click or from the `shortcut` its plugin declares.

### D. Frames
Surfaces are blank canvases. A plugin declares the chrome it wants (`border`, `label`, `close`) in `hello.frames` or on a rail item, and the host draws it.

### E. Interactive mouse slot resizing
Drag-to-resize for the sidebar width, side-panel width, sidebar navigation height, sidebar footer height and main header height. Only a border the occupying plugin declared is a drag handle. Hover and drag are highlighted, affected plugins receive a `slot_resize` when a size changes, and `=` resets the proportions.

### F. Keyboard modifier and key-hold synchronization
- On macOS, bare modifier keys are read through `CGEventSourceFlagsState` (`pkg/workbench/modifiers_darwin.go`).
- Holding `Alt`/`Option` swaps rail icons for the shortcut badges their plugins declared.
- The `key_state` event carries the transition phase (`down`/`up`/`repeat`/`sync`) and the authoritative modifier and held state.

### G. Reference plugins (`plugins/reference`, `plugins/reference-actions`)
One Go binary in two roles that exercises every extension point: a publisher (frame, inline published slot, hook, event) and a contributor that fills the slot. `plugins/reference/main_test.go` drives both over pipes.

## 3. History

- **2026-10-23, importable workbench UI.** The model, rendering, input, modals, dump-frame helpers, tests and unchanged frame goldens moved to `pkg/workbench`. `cmd/husk` delegates through `workbench.Options`, `workbench.Run` and `workbench.DumpFrame`. No wire or rendering behaviour changed; `create tui` still awaits its scaffold implementation.

- **2026-10-23, protocol 2.3 stamp.** Protocol 2.3 advertises `theme.list` and `settings.set` (served earlier, stamped once minor 2.2 landed); no payload change.
- **2026-10-23, runtime plugin addition.** `plugin.add` and `husk plugin add` load a path or directory batch into the current instance without changing configuration. Startup and runtime share conversion, registration and launch gates; each admitted batch waits for its reload and all launch outcomes. Removal owns its id until its reload completes, disconnected callers do not cancel accepted additions, and re-add keeps persisted slices and grants. Protocol 2.2 advertises the method; addition events follow the final liveness flush.
- **2026-10-22, v1 wire retired.** The v1 wire package, the v1 manifest package and the frozen v1 schema were deleted once nothing imported them. `cmd/husk` runs on the v2 host, plugins speak `pkg/protocol`, and `husk-plugin.json` is read by `pkg/pods/manifest`.
