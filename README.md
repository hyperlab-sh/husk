# Husk Workbench + `herdr-sidebar` (Go) + 3 Activity Rail Plugins (Bash)

**Husk** is a domain-agnostic, pluggable TUI workbench modeled after `bb`'s slot & hook architecture (`/work/lab/bb/plugins/plugin-api-docs`). Following Herdr's out-of-process plugin philosophy, every Husk plugin is an independent OS process communicating over newline-delimited JSON IPC (`HUSK_SOCKET_PATH` or `stdio`) and can be implemented in any language.

## Quick Start

```bash
cd /work/lab/husk
./bin/husk
```

Non-interactive frame dumps for each Activity Rail mode:

```bash
./bin/husk --dump-frame                     # Default 3-column workbench
./bin/husk --dump-frame --hold-alt          # Simulate holding Alt/Option (Rail icons replaced by shortcuts + plugin hints)
./bin/husk --dump-frame --rail modal        # Case 1:  TV Session & File Finder (Modal)
./bin/husk --dump-frame --rail full-canvas  # Case 2:  Cross-Project Fleet & Plugin Hub (Full Canvas)
./bin/husk --dump-frame --rail stage        # Case 3:  Project Git / Lazygit Stage (Canvas minus Sidebar)
```

## Activity Rail (`⓪ activity-rail`) & The 3 Bash Plugins

To the left of the sidebar sits the `bb`-style borderless **Activity Bar / Icon Rail (`⓪`)** using Nerd Font icons and a rounded square pill highlight (`▄▄▄` / `  ` / `▀▀▀`) for active and hovered buttons. Clicking an icon (or pressing its shortcut) triggers its out-of-process plugin in one of three placement modes:

1. **Case 1 (`placement: "modal"`) — `` TV Session & File Finder ([plugins/tv-search/plugin.sh](file:///work/lab/husk/plugins/tv-search/plugin.sh))**:
   - **Trigger**: Click `` in the left rail, press `F2` / `ctrl+f`, or run from Command Palette (`ctrl+p`).
   - **Use Case**: Television (`tv`) / Telescope style fuzzy finder for Herdr Workspaces, Agents, and files in the active project CWD. Type to filter live, `↑`/`↓` to navigate, `Enter` to focus in Herdr & close, `Esc` to dismiss.
2. **Case 2 (`placement: "full-canvas"`) — `` Cross-Project Fleet & Plugin Hub ([plugins/fleet-dashboard/plugin.sh](file:///work/lab/husk/plugins/fleet-dashboard/plugin.sh))**:
   - **Trigger**: Click `` in the left rail, press `F3` / `ctrl+g`, or run from Command Palette.
   - **Use Case**: Cross-project dashboard & plugin manager operating above the workspace level. Replaces the **entire canvas** to the right of the Activity Rail (hiding the workspace sidebar). Press `j`/`k` and `Enter` to jump into any workspace and return to the workbench, or `F1`/`Esc` to exit.
3. **Case 3 (`placement: "stage"`) — `` Lazygit / Project Git Stage ([plugins/project-git/plugin.sh](file:///work/lab/husk/plugins/project-git/plugin.sh))**:
   - **Trigger**: Click `` in the left rail, press `F4` / `ctrl+e`, or run from Command Palette.
   - **Use Case**: Single-project tools (`lazygit`, `gh dash`, `vim`) that use the **canvas area minus the sidebar**. Keeps `herdr-sidebar` visible on the left — as you press `j`/`k` or click different spaces in the sidebar, Husk broadcasts `workspace_context` (`cwd`, `workspace_label`) to `project-git/plugin.sh`, which immediately updates its Git status, commit graph (`l`), and worktree list (`w`) for that project!

## Keybindings & Mouse Controls

| Input | Action |
| :--- | :--- |
| **Hold `Alt` / `Option`** (or press `?` to latch) | Replaces Activity Rail icons with their shortcuts (`F1`, `F2`, `F3`, `F4`, `v`, `^P`) and broadcasts `key_state` (`phase: "down"\|"up"`, `held: true\|false`, `modifiers`) to all plugins |
| **Drag Slot Borders** | Resize any slot divider with the mouse (Sidebar width, Right Inspector width, Navigation height, Footer height, Main Header height); broadcasts live `slot_resize` to plugins |
| `=` | Reset all slot widths and heights to default proportions |
| **Click Rail Icon** or `F1` / `F2` / `F3` / `F4` | Switch Activity Rail mode: ` Home (F1)`, ` TV Modal (F2)`, ` Full-Canvas (F3)`, ` Git Stage (F4)`, ` Slot Lab (v)`, ` Palette (ctrl+p)` |
| `ctrl+f` / `ctrl+g` / `ctrl+e` | Alternate shortcuts for ` Modal` (`ctrl+f`), ` Full-Canvas` (`ctrl+g`), ` Git Stage` (`ctrl+e`) |
| `Esc` | Dismiss active Modal or return from Full-Canvas / Stage mode to default Workbench |
| `j` / `k` or `↑` / `↓` or **Mouse Wheel** | Navigate Spaces, Tabs, Panes, and Agents |
| `Enter` | Focus the selected Workspace / Tab / Pane / Agent in the running `herdr` server |
| `1` / `2` / `3` | Switch sidebar view: **1 Spaces + Agents Split**, **2 Topology Tree**, **3 Agent Mission Control** |
| `s` / `l` / `w` (in `🌿` Git Stage) | Switch Git Stage subtab: **Working Tree Status (`s`)**, **Commit Graph (`l`)**, **Git Worktrees (`w`)** |
| `o` | Toggle Agent sort order (`priority` vs `spaces`) |
| `v` or `ctrl+i` | Toggle `main-stage` to **`bb`-style Slot Architecture Explorer** |
| `t` or `ctrl+t` | Cycle color theme across Husk and all plugins (`Tokyo Night`, `Catppuccin Mocha`, `Rosé Pine`, `Kanagawa Wave`) |
| `[` / `]` / `/` | Toggle Left Sidebar (`[`), Toggle Right Inspector (`]`), or Cycle Dock Layouts (`/`) |
| `ctrl+p` or `:` | Open the searchable **Command Palette (`⑧ app-overlay`)** |
| `q` or `ctrl+c` | Quit Husk and cleanly terminate all spawned Go & Bash plugin processes |
