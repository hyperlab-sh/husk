# Husk — Agent Instructions

Husk is a minimal, domain-agnostic TUI workbench in Go (bubbletea + lipgloss). It only provides **slots**, **hooks** and **events**; every plugin is an independent OS process speaking newline-delimited JSON over a Unix socket (`HUSK_SOCKET_PATH`) or stdio, in any language. The layout mirrors `bb`'s 9-slot model (`/work/lab/bb/plugins/plugin-api-docs`). The first real client is `herdr-sidebar`; Dolphin (a Rust Herdr client, repo `hyperlab-sh/dolphin`) is a planned plugin and does not live here. Design research is not in the repo: it lives in the space's local-only `notes/research/` (`../../notes/research/` from the worktree). The docs site (docmd) is in `docs/`.

This is a hyper space: project files live in `worktrees/main/`; `bin/`, `data/`, `notes/`, `scratch/` at the space root are local-only. Read `HYPER.md` at the space root for layout rules.

## Build & Test

```bash
go build -o ../../bin/husk ./cmd/husk
go build -o ../../bin/herdr-sidebar ./cmd/herdr-sidebar
go vet ./...

../../bin/husk                              # run the workbench
../../bin/husk --dump-frame                 # non-interactive frame dump
../../bin/husk --dump-frame --rail modal    # also: full-canvas, stage; --hold-alt
```

There are no Go tests yet. Use `--dump-frame` to check rendering changes. Manifests reference binaries as `./bin/<name>`, resolved against the space root's `bin/`.

## Layout

| Path | Purpose |
| :--- | :--- |
| `cmd/husk/` | Host workbench: slots, dock/palette, mouse resize, activity rail; `modifiers_darwin.go` polls Alt/Option via Quartz at 25Hz |
| `cmd/herdr-sidebar/` | Go plugin talking to `~/.config/herdr/herdr.sock`; publishes `workspace_context` |
| `pkg/huskipc/` | IPC protocol (`protocol.go`) and client (`client.go`) |
| `pkg/herdr/` | Herdr socket client |
| `pkg/theme/` | Styling (being moved to Charm defaults) |
| `plugins/` | Out-of-process plugins: `tv-search` (modal), `fleet-dashboard` (full-canvas), `project-git` (stage), all Bash `plugin.sh`; `herdr-sidebar/husk-plugin.json` is the manifest for the Go plugin |

IPC messages: `hello`/`welcome`, `slot_update`, `slot_resize`, `slot_key`, `slot_mouse`, `key_state`, `workspace_context`, `command_invoke`, toasts. Plugin placements: `modal`, `full-canvas`, `stage`.

## Conventions

- Husk core stays domain-agnostic: Herdr-specific logic belongs in a plugin, not `cmd/husk`.
- Plugins must never assume a language; the JSON-lines protocol is the only contract. Changing it means updating `pkg/huskipc/protocol.go` and every plugin.
- Use Charm's default palette (ANSI / lipgloss presets). Do not add custom hex themes.
- Conventional commits with detailed messages. No attribution trailers.
- `STATUS.md` tracks known issues; `SUMMARY.md` is the design history; `README.md` is user-facing.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:1105d646 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. The database is shared with Dolphin and lives at `/work/lab/.beads` (Husk issues use the `husk-` prefix, Dolphin issues `dolphin-`). Run `bd prime` to see full workflow context and commands.

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
