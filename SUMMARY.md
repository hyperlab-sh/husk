# Husk & Dolphin Session Summary

## 1. Architectural Vision & Scope

The session originated from an architectural pivot: instead of forking and modifying `herdr` directly, the project established **Husk** — an extensible, minimalist TUI workbench that acts strictly as a shell offering **Surfaces (Slots)**, **Hooks**, and **Events**. 

- **Husk**: Host workbench written in **Go** using Charm libraries (`bubbletea`, `lipgloss`, `bubbles`, `x/ansi`).
- **Dolphin**: The primary Herdr client, architected as an out-of-process Husk plugin (targeted for Rust).
- **Process Plugin Model**: All plugins run as independent OS processes communicating via newline-delimited JSON IPC over Unix sockets (`HUSK_SOCKET_PATH`) or `stdio` pipes, making plugins completely language-agnostic.
- **Slot Model Counterpart**: Adopts the 9-slot layout structure from `bb` (`/work/lab/bb/plugins/plugin-api-docs`):
  - `⓪ activity-rail` (Far-left navigation icon bar)
  - `① sidebar-navigation` (Mode & view switcher)
  - `② sidebar-list` (Primary list of spaces, trees, agents)
  - `③ thread-row-status` (Telemetry, status spinners, context gauges)
  - `④ sidebar-footer` (Daemon health, IPC metrics, global shortcuts)
  - `⑤ main-header` (Stage title, tab breadcrumbs)
  - `⑥ main-stage` (Workspace topology canvas or primary stage plugin)
  - `⑦ side-panel` (Deep inspection drawer for pane/agent metrics)
  - `⑧ app-overlay / modal-surface` (Command palette & floating modal plugins)
  - Dynamic surfaces: `full-canvas` (Canvas minus rail) and `workspace-stage` (Canvas minus sidebar).

Comprehensive research documentation comparing frameworks, languages, and prior art was compiled into `/work/lab/dolphin/research/00` through `09`.

---

## 2. Implemented Components (`/work/lab/husk`)

### A. Husk Workbench Core (`cmd/husk`)
- Built with Charm's `bubbletea` and `lipgloss`.
- **IPC Protocol Engine (`pkg/huskipc/protocol.go`)**: Full JSON-lines IPC supporting plugin registration (`hello`/`welcome`), slot rendering (`slot_update`), dynamic resizing (`slot_resize`), keystrokes (`slot_key`), mouse interaction (`slot_mouse`), modifier state synchronization (`key_state`), workspace context broadcast (`workspace_context`), command palette invocation (`command_invoke`), and toast notifications.
- **Dock & Column Toggles**: Keybindings `[` (toggle sidebar), `]` (toggle inspector panel), and `/` (cycle dock presets).
- **Command Palette (`ctrl+p` / `:`)**: Fuzzy-searchable action overlay aggregating core workbench actions and commands registered dynamically by running plugins.

### B. Out-of-Process Go Plugin: `herdr-sidebar` (`cmd/herdr-sidebar`)
- Connects directly to the active Herdr daemon at `~/.config/herdr/herdr.sock`.
- Renders live spaces, git worktree branches, session hierarchy, and active agent telemetry (context % gauges, status icons, braille spinners).
- Fixed the visual hall-of-mirrors recursive loop by rendering a structured Workspace Topology Deck in `main-stage` instead of raw pane echoing.
- Publishes `workspace_context` (`workspace_id`, `workspace_label`, `cwd`, `repo_name`) whenever space selection changes.

### C. Activity Rail & The 3 Placement Modes
Implemented a dedicated Activity Rail (`⓪ activity-rail`) supporting the three core workbench integration paradigms:
1. **Modal Overlay (`placement: "modal"`)**: `plugins/tv-search/plugin.sh` (``, `F2`/`ctrl+f`) — Floating search modal for sessions and files.
2. **Full Canvas (`placement: "full-canvas"`)**: `plugins/fleet-dashboard/plugin.sh` (``, `F3`/`ctrl+g`) — Cross-project management surface occupying the entire canvas right of the rail.
3. **Stage minus Sidebar (`placement: "stage"`)**: `plugins/project-git/plugin.sh` (``, `F4`/`ctrl+e`) — Project-level tools occupying the stage while keeping the sidebar active on the left.

### D. Minimalist UI & Nerd Font Redesign
- Redesigned the Activity Rail to match the user's `bb` reference screenshot:
  - 5-column borderless vertical strip.
  - Minimal monochrome Nerd Font glyphs (`` Home, `` Search, `` Plugins/Hub, `` Git Stage, `` Architecture Lab, `` Command Palette).
  - 1:1 aspect-ratio rounded square active/hover pills using half-blocks (`▄▄▄` / `  ` / `▀▀▀`).

### E. Interactive Mouse Slot Resizing
- Added interactive drag-to-resize handlers for all 5 workbench slot borders:
  - Sidebar width (`sidebar-width`)
  - Side panel width (`sidepanel-width`)
  - Sidebar navigation height (`sidebar-nav-height`)
  - Sidebar footer height (`sidebar-foot-height`)
  - Main header height (`main-header-height`)
- Live hover highlighting (amber) and drag highlighting (green) with real-time `slot_resize` IPC broadcasts at 60fps.
- Quick reset to default proportions with `=`.

### F. Keyboard Modifier & Key-Hold Synchronization
- Solved terminal emulator limitation regarding bare modifier keys on macOS via `CGEventSourceFlagsState` in `cmd/husk/modifiers_darwin.go`.
- Holding `Alt`/`Option` dynamically swaps rail icons with shortcut badges (`F1`, `F2`, `F3`, `F4`, `v`, `^P`).
- Established the `key_state` IPC event sending both transition phase (`down`/`up`) and authoritative modifier/held state flags (`held: bool`, `modifiers: { alt, ctrl, shift, super }`).
