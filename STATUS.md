# Husk Status Report

**Date**: 2026-10-21  
**Location**: `/work/lab/husk`  
**Host Target**: Go + Charm (`bubbletea`, `lipgloss`)  
**Plugin Protocol**: JSON-Lines IPC over Unix Domain Sockets (`husk.sock`) and `stdio`

---

## 1. Current State of Components

| Component | Status | Details |
| :--- | :--- | :--- |
| **Husk Workbench (`cmd/husk`)** | **Functional** | Multi-slot TUI workbench running live with 9 slots, dock toggles (`[`, `]`, `/`), Command Palette (`ctrl+p`), Alt-key shortcut peek, and mouse border dragging (`=`). |
| **Herdr Go Plugin (`cmd/herdr-sidebar`)** | **Moved to the Dolphin repo** | Communicates with active Herdr socket (`~/.config/herdr/herdr.sock`), handles 11 workspaces & agents, publishes `workspace_context`, eliminates recursive mirroring loops. |
| **Activity Rail (`⓪ activity-rail`)** | **Functional** | Minimal borderless 5-col column using Nerd Font glyphs (``, ``, ``, ``, ``, ``) with half-block square pill selection. |
| **Alt/Option Modifier Poller** | **Functional** | macOS Quartz `CGEventSourceFlagsState` poller at 25Hz broadcast via `key_state` IPC. Toggles rail icons to shortcut labels in real time. |
| **Activity Rail Plugins (Bash)** | **Functional (Mock/TUI stage)** | Scripts for `tv-search`, `fleet-dashboard`, and `project-git` operate over `stdio` IPC, demonstrating modal, full-canvas, and stage modes. |

---

## 2. Recent User Feedback & Active Issues to Address

### A. iTerm2 Mouse Interaction Issue
- **Issue**: Mouse clicks and dragging are unresponsive when launching Husk inside iTerm2.
- **Root Cause Analysis**:
  - `tea.WithMouseAllMotion()` enables any-event tracking (`?1003h`) but omits traditional button tracking (`?1000h`) and button event motion (`?1002h`) in some terminal contexts.
  - On macOS trackpads, clicking often triggers `MouseActionMotion` with `Button == MouseButtonLeft` instead of pure `MouseActionPress`.
  - Terminal subshells or external TUI launches disable mouse mode without restoring it on return.
- **Fix Required**: Ensure explicit SGR and button mouse sequence initialization (`\x1b[?1000h\x1b[?1002h\x1b[?1006h`), treat left-drag motion as click/drag initiate, and re-enable mouse sequences on subprogram lifecycle events.

### B. Launch Real Tools (`lazygit`, `gh dash`, `tv`) with Space Checkout Paths
- **Requirement**: The activity rail plugins should not just display mock views; they must launch the actual binaries:
  - `lazygit -p <checkout_path>`
  - `gh dash` (in the repo checkout directory with repo configuration)
  - `tv` (fuzzy file/session finder)
- **Dynamic Relaunching**: Changing the selected workspace/space in the left sidebar (`herdr-sidebar`) broadcasts the new checkout path (`cwd`). The active stage plugin (`lazygit` or `gh dash`) must kill the existing process and relaunch itself with the newly focused space's path.
- **Architecture Path**: Embed a PTY surface executor (`creack/pty` + virtual terminal screen parser) or run foreground interactive subprocesses suspended/resumed via `tea.ExecProcess`, keeping the sidebar synchronized.

### C. Color Palette: Use Charm Defaults
- **Requirement**: Remove any custom colors derived from Herdr themes (`Tokyo Night`, `Catppuccin`, etc.) across Husk and its plugins.
- **Fix Required**: Refactor styling across `pkg/theme`, `cmd/husk`, and plugins to use Charm's canonical default styles and ANSI color definitions (`lipgloss.Color("63")`, `lipgloss.Color("205")`, default terminal ANSI 16 colors, and standard Lipgloss presets).

---

## 3. Next Action Items

1. **Fix Mouse in iTerm2**:
   - Update `cmd/husk/main.go` to explicitly send complete mouse escape sequences on startup (`?1000h`, `?1002h`, `?1006h`).
   - Relax `handleMouse` to treat `MouseActionMotion` with `MouseButtonLeft` as a valid drag/click trigger.
2. **Switch to Charm Default Palette**:
   - Strip out custom hex palettes in `pkg/theme/theme.go` and replace with standard Charm / Lipgloss default colors.
3. **Implement Real Tool Launching for Activity Rail Plugins**:
   - Add PTY or interactive process launcher support to Husk for `workspace-stage` and `modal-surface`.
   - Update `plugins/project-git/plugin.sh` to launch real `lazygit -p "$CURRENT_CWD"` and relaunch on `workspace_context`.
   - Update `plugins/fleet-dashboard/plugin.sh` / `plugins/gh-dash/plugin.sh` to launch real `gh dash` in `$CURRENT_CWD` and relaunch on `workspace_context`.
   - Update `plugins/tv-search/plugin.sh` to launch real `tv` in `$CURRENT_CWD`.
