# Husk — Agent Instructions

Husk is a minimal, domain-agnostic TUI workbench in Go (Bubble Tea v2 + Lip Gloss v2). It only provides **slots**, **hooks** and **events**; every plugin is an independent OS process speaking newline-delimited JSON over a Unix socket (`HUSK_SOCKET_PATH`, in a private `0700` directory, socket `0600`, peer uid checked) or stdio, in any language. The layout mirrors `bb`'s 9-slot model. Husk must stay application-agnostic: nothing application-specific belongs in this repo. The docs site (docmd) is in `docs/`.

## Build & Test

```bash
go build -o bin/husk ./cmd/husk
go vet ./...
go test ./pkg/...

./bin/husk                              # run the workbench (no plugins by default)
./bin/husk --plugin-dir ./plugins       # load bundled plugins via manifests
./bin/husk --dump-frame                 # non-interactive frame dump
./bin/husk --dump-frame --rail modal    # opens the first loaded rail item with that placement; also full-canvas, stage; --hold-alt
```

Tests live in `pkg/manifest` (manifest parsing) and `pkg/frame` (frame geometry), plus `cmd/husk` host regression tests for frame resolution, sizing and rendering; run them all with `go test ./...`. Use `--dump-frame` to check rendering changes and `--demo-frame` (optionally with `--rail modal|full-canvas|stage`) to render the built-in demo plugin that declares a border+label+close frame. Surfaces are a blank canvas; all plugin-surface chrome (border/label/close) is declared by the plugin (`hello.frames` or rail-item `frame`) and drawn by `cmd/husk` `renderFrame`. Manifests reference binaries as `./bin/<name>`.

Registry, published-slot rendering/routing and protocol compatibility tests live beside their code. Run affected tests with `go test ./cmd/husk ./pkg/huskipc`; use `--dump-frame --demo pubslots` for a self-contained two-plugin frame and `--dump-frame --plugin-dir ./plugins` for rendering with external plugins. Manifests reference binaries as `./bin/<name>`.

## Charm v2

- Import `charm.land/bubbletea/v2` and `charm.land/lipgloss/v2`; stable v2 uses these canonical vanity paths, not the old GitHub module paths. Bubbles is not currently used.
- `View()` returns `tea.View`: use `.Content` for headless frame output/assertions, and set `AltScreen` / `MouseMode` on the view rather than program options.
- Handle `tea.KeyPressMsg` separately from `tea.KeyReleaseMsg`; mouse click, release, motion and wheel events are distinct types implementing `tea.MouseMsg` (`.Mouse()` exposes coordinates).
- Keep existing IPC key spellings through `legacyKeyString`: Space is `" "`, NUL is `"ctrl+@"`, Alt precedes other modifiers (`"alt+ctrl+x"`), and printable shifted Alt keys use their text (`"alt+X"`). Native releases may omit `Text`; normalize `ShiftedCode` too so held keys clear. Ignore Num/Scroll Lock for matching, but retain Caps Lock for printable fallback casing. Decoder-backed tests include CSI-u lock modifiers and native releases. `tea.PasteMsg` retains v1's bracket-wrapped key spelling so pasted text cannot run host shortcuts.
- `lipgloss.Color` is a function returning `image/color.Color`; styles are values and there is no global renderer or `SetColorProfile`. Bubble Tea handles interactive color downsampling; headless frame dumps retain full ANSI styling.
- Use `go build -buildvcs=false` for exported-source builds and the toolchain's `gofmt` binary if a PATH wrapper does not support `-l`.

## Layout

| Path | Purpose |
| :--- | :--- |
| `cmd/husk/` | Host workbench: slots, dock/palette, mouse resize, activity rail. Files: `main.go` (flags, startup, `--dump-frame` driver), `model.go` (model type, `Init`/`Update`, key-state), `transport.go` (accept/spawn/stream/cleanup), `input.go` (keys, mouse, drag), `commands.go` (rail buttons, palette), `layout.go` (`recomputeLayout`, rects, frames), `render.go` (`View` and helpers), `demo.go` (`--demo-frame` / `--demo pubslots` fixtures only); `modifiers_darwin.go` polls Alt/Option via Quartz at 25Hz |
| `pkg/huskipc/` | IPC envelope and legacy payloads (`protocol.go`), published extension payloads (`published.go`), JSON-lines reader/writer; `queue.go` is the non-blocking queued writer the host uses per connection (bounded queue, write deadline, disconnect on overflow/timeout). Never call a synchronous `NewConnWriter` from `Update` on a real connection |
| `pkg/manifest/` | `husk-plugin.json` parsing (`manifest.go`): id, name, version, language, description, command (argv, relative to the manifest dir), transport (`socket` default \| `stdio`), slots |
| `pkg/theme/` | Styling (being moved to Charm defaults) |
| `plugins/` | Out-of-process plugins, each a directory with a `husk-plugin.json` and its code; currently `project-git` (stage, Bash `plugin.sh` mock UI). The module path is `github.com/hyperlab-sh/husk`; `pkg/huskipc` and `pkg/theme` are imported externally, so keep them stable |

`View` is pure: it reads the model and never writes it (`TestViewIsPure` deep-compares a rendered model with an unrendered twin). Derived UI such as the architecture explorer is built locally in the render function, never stored in `slotUpdates`; state changes belong in `Update`. String splicing (`spliceOverlay`, the region compositor in `published_render.go`) closes open SGR state at every fragment boundary with `closeSGR` (`sgr.go`), because `ansi.Cut` keeps every escape of the line, including those past the cut. Slot content is clipped to the inner rect, never wrapped or scrolled (`surfaceContentLines`).

Minimum terminal size is 84x24 (`minTermWidth`/`minTermHeight` in `layout.go`): below it `View` renders `renderTooSmall`, a frame that exactly fills the real terminal; `m.width`/`m.height` hold the real size and are never clamped.

IPC messages: `hello`/`welcome`, `slot_update`, `slot_resize`, `slot_key`, `slot_mouse`, `key_state`, `workspace_context`, `command_invoke`, toasts. Plugin placements: `modal`, `full-canvas`, `stage`.

A plugin that stops reading is disconnected (queue of 1,024 messages, 5s write deadline), never waited on; every disconnect reason (EOF, read error, write timeout, queue overflow) is logged and toasted. Policy: `docs/docs/architecture.md` "Outbound queue and disconnect policy".

## Conventions

- Husk core stays domain-agnostic: application-specific logic belongs in a plugin, not in `cmd/husk`. The host hard-codes no plugin command or rail item ID, no application key, and no application colour; keys reach plugins through rail item `shortcut`s, `hello.keybindings` and focused-surface `slot_key` (see `docs/docs/architecture.md`, "Input routing"). `TestHostSourceHasNoApplicationIdentifiers` guards the source.
- Plugins are launched only via `--plugin` (executable, plugin dir, or `husk-plugin.json`) or `--plugin-dir` (scans immediate subdirectories). There is no default plugin.
- Plugins must never assume a language; the JSON-lines protocol is the only contract. Changing it means updating `pkg/huskipc/protocol.go` and every plugin.
- Use Charm's default palette (ANSI / lipgloss presets). Do not add custom hex themes.
- Conventional commits with detailed messages. No attribution trailers.
- `SUMMARY.md` is the design history; `README.md` is user-facing.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:1105d646 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. The database is shared with a sibling project (Husk issues use the `husk-` prefix). Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/core-concepts/sync-concepts.md for details and anti-patterns.

## Agent Context Profiles

The managed Beads block is task-tracking guidance, not permission to override repository, user, or orchestrator instructions.

- **Conservative (default)**: Use `bd` for task tracking. Do not run git commits, git pushes, or Dolt remote sync unless explicitly asked. At handoff, report changed files, validation, and suggested next commands.
- **Minimal**: Keep tool instruction files as pointers to `bd prime`; use the same conservative git policy unless active instructions say otherwise.
- **Team-maintainer**: Only when the repository explicitly opts in, agents may close beads, run quality gates, commit, and push as part of session close. A current "do not commit" or "do not push" instruction still wins.

## Session Completion

1. **File issues for remaining work** - Create beads for anything that needs follow-up
2. **Run quality gates** (if code changed) - build, `go vet`, `--dump-frame` checks
3. **Update issue status** - Close finished work, update in-progress items
4. **Handle git/sync by active profile** - report `git status` and proposed commands; wait for approval
5. **Hand off** - Summarize changes, validation, issue status, and any blocked sync/commit/push step

**Critical rules:**
- Explicit user or orchestrator instructions override this Beads block.
- Do not commit or push without clear authority from the active profile or the current user request.
- If a required sync or push is blocked, stop and report the exact command and error.
<!-- END BEADS INTEGRATION -->
