---
type: Design Spec
title: "Herdr adapter: running Herdr plugins in Husk"
description: "How Husk runs unmodified Herdr plugins over a real Herdr server: a converter and shim shipped with Husk, outside core, that turns herdr-plugin.toml into Husk plugins and renders their panes and popups as terminal surfaces."
status: planned
generated: { by: claude/fable-5-1, at: 2026-10-23T12:00:00Z }
tags: [architecture-v2, adapters]
---

# Herdr adapter: running Herdr plugins in Husk

## Level 0: What and why

Herdr is a terminal multiplexer for coding agents. Its plugins are directories with a `herdr-plugin.toml` manifest whose sections (`[[build]]`, `[[startup]]`, `[[actions]]`, `[[events]]`, `[[panes]]`, `[[link_handlers]]`) each name an argv command. The Herdr server runs those commands as one-shot processes with the Herdr socket and a context JSON in their environment; the plugin's API is the whole `herdr` CLI and socket, and its only UI is a terminal pane (`overlay`, `popup`, `split`, `tab`, `zoomed`). Nothing in a Herdr plugin speaks Husk's protocol or knows Husk exists.

Operator ruling: Husk runs Herdr plugins itself, unchanged, over a real Herdr server, and every bit of Herdr knowledge lives in an **adapter shipped in the Husk repository, outside core**. Core gains only generic capabilities: terminal surfaces, full-key input capture, plugin-opened surfaces at runtime, mouse forwarding to terminals, an executable manifest converter seam and a live `plugin.add` for every plugin kind. Those are specified in the core specs ([Protocol](protocol.md), [Keybindings](keybindings.md), [State](state.md), [Pods](pods.md), [Instances](instances.md)) as Level 1 deltas; this document names them where the adapter depends on them and owns nothing about them.

The adapter is one binary, `husk-herdr`, in three roles:

1. **Converter.** Through the generic executable-converter seam ([Pods](pods.md), "Executable converters"), the host asks it to detect a Herdr plugin directory, discover the user's installed Herdr plugins, and convert a `herdr-plugin.toml` into a `husk-plugin.json` v2 document. Each Herdr plugin becomes an ordinary Husk plugin with its own Pod, id, actions, liveness, log and token.
2. **Shim.** The converted manifest's `command` is `husk-herdr run <dir>`: a long-lived Husk plugin process, one per Herdr plugin, that speaks Husk's protocol on stdio and the socket, attaches to the Herdr server, and runs the plugin's one-shot commands exactly as the Herdr server would, with the same environment.
3. **Proxy.** The shim gives each spawned command a private socket in `HERDR_SOCKET_PATH`. The real `herdr` CLI (`HERDR_BIN_PATH`) and raw socket clients resolve to it, so every Herdr request the plugin makes passes through the shim, which forwards it to the real server unchanged except for the handful of UI methods (`plugin.pane.open`, `popup.close`, `plugin.action.invoke`, `notification.show`) that Husk must serve itself for the plugin's UI to appear in Husk.

What the adapter is **not**:

- Not a Herdr client. It does not attach to Herdr's TUI socket, render Herdr's workspaces, tabs or user terminals, or replace Herdr's shell. The user's Herdr TUI, if they run one, is untouched.
- Not a Herdr server manager. By default it attaches to the server the user already has and never stops, restarts or reconfigures it (see "The Herdr server").
- Not a plugin installer. It reads Herdr's registry and manifests; `herdr plugin install|link|enable|disable` stay the way to change what exists, and `husk-herdr sync` only tells a running instance about the result (see "Hot registry changes").
- Not core. No package under `cmd/husk`, `internal/` or `pkg/` imports it or names it; the docs lint that keeps the host application-agnostic applies to every file but this one.

Dependencies: the adapter imports `pkg/protocol` (the wire types, as any Go plugin does) and nothing else from Husk. It depends on the core deltas listed under "Core capabilities it needs". Core depends on nothing in the adapter.

## Level 1: Contract

### Terms

| Term | Meaning |
|---|---|
| **Herdr server** | The `herdr server` process that owns a session: workspaces, tabs, panes, agents, the JSON API socket. One per session. |
| **Session** | Herdr's unit of persistence. The default session's socket is `<herdr config dir>/herdr.sock`; a named session has its own server, state and socket under `<herdr config dir>/sessions/<name>/herdr.sock` and shares the global `config.toml` and the global plugin registry. |
| **Registry** | Herdr's `plugins.json` beside `session.json` in the Herdr config directory: one record per installed or linked plugin with `plugin_id`, `manifest_path`, `plugin_root`, `enabled`, `platforms`, the parsed sections and the install source. Global to the user; every Herdr session reads it. |
| **Herdr plugin** | A directory with `herdr-plugin.toml`. Its **entries** are the manifest's `[[actions]]`, `[[events]]`, `[[panes]]`, `[[startup]]`, `[[build]]` and `[[link_handlers]]` items. |
| **Command** | One entry's argv, run as a one-shot process with the Herdr plugin environment. A pane command runs in a PTY; every other command runs with pipes. |
| **Shim** | The `husk-herdr run <dir>` process: the Husk plugin that stands for one Herdr plugin. |
| **Proxy** | The shim's private Herdr-protocol socket that the plugin's commands talk to. |
| **Upstream** | The shim's connection(s) to the real Herdr server socket. |
| **Terminal surface** | A Husk surface whose content is a terminal grid the host emulates from bytes the plugin writes ([Protocol](protocol.md), "Terminal surfaces"). |
| **Runtime surface** | A surface a plugin opens at runtime with `surface.open` (a modal or a full canvas) instead of claiming a layout slot in its manifest ([Protocol](protocol.md), "Runtime surfaces"). |

### Shape and placement

```
cmd/husk-herdr          main: detect | discover | convert | run | sync | doctor
adapters/herdr/manifest herdr-plugin.toml -> husk-plugin.json v2 document (pure)
adapters/herdr/registry reads plugins.json; platform and enabled filters
adapters/herdr/upstream Herdr socket client: dial, ping, version, events.subscribe
adapters/herdr/proxy    per-shim Herdr-protocol socket: forward, intercept
adapters/herdr/shim     the plugin process: Husk stdio, commands, PTYs, surfaces
adapters/herdr/fakeherdr an in-process Herdr server for tests (see "Test strategy")
```

Directions: `manifest` imports the TOML library and nothing from Husk; `shim` imports `pkg/protocol` and the sibling packages; nothing under `adapters/` is imported by `cmd/husk`, `internal/` or `pkg/`, and a test in `adapters/herdr` pins that (`TestAdapterIsNotImportedByCore`, the mirror image of core's allow-list tests).

Why one binary with roles, and not a single Husk plugin: a single plugin would have to declare every Herdr plugin's actions in one static manifest, which means either runtime-declared actions in core (a capability Herdr plugins themselves do not have: "runtime action registration is not part of plugin v1") or regenerating a manifest file on disk before every start. The converter seam gives the same result with no new core wire method: conversion runs at every Husk start and on `husk-herdr sync`, the catalog is as current as Herdr's registry, and each Herdr plugin is a Pod with its own liveness, log, token, `husk plugin remove` and `plugin.add`. Why not a core package: the placement rule above, and the docs lint (`TestDocsHaveNoApplicationResidue`, `TestHostSourceHasNoApplicationIdentifiers`) that keeps the host application-agnostic; this file is the one documented exemption.

Why a proxy rather than an environment trick: a Herdr plugin reaches Herdr through `HERDR_BIN_PATH` (the real `herdr` binary) or by writing to `HERDR_SOCKET_PATH`. Both resolve the socket from `HERDR_SOCKET_PATH` when it is set (Herdr's documented resolution order: `--session`, then `HERDR_SOCKET_PATH`, then `HERDR_SESSION`, then the default), so pointing that variable at the shim's socket is enough to see every request without a shim binary in front of `herdr`. A command that passes `--session <name>` explicitly bypasses the proxy; that is the one documented hole (see Limits).

### The Herdr server

Herdr plugins are useful only against a running Herdr server: their actions list workspaces, move panes, read agents. Three ways to get one were weighed. Facts used, all from Herdr's documentation and `--help` output:

- Two servers coexist only as different **sessions**. Each session has its own server, socket, panes, tabs, workspaces and runtime state; a second server for the same session cannot bind the socket. All sessions share one `config.toml` and one plugin registry (installed, linked and enabled state are "global to the current user and available in every Herdr session").
- `[[startup]]` hooks "run once for each enabled plugin after Herdr restores the session and its API socket is ready", per server. `[[events]]` hooks run per server for that server's events. So a second server runs every enabled plugin's startup hooks again, and its event hooks fire for its own events.
- The default `herdr` command starts the default session's server if none runs and attaches a TUI to it. `herdr server` runs a headless server. `herdr server stop` and `herdr session stop <name>` stop a server, which ends every pane it owns.

| | (A) Attach, stay out of the way | (B) Husk-owned server in a dedicated named session | (C) Hybrid: attach if present, else start |
|---|---|---|---|
| What runs underneath | The user's own session (default, or `--session <name>` passed to the adapter). | A session `husk-<instance>` (or a configured name) the adapter starts with `herdr server` and that only Husk uses. | (A) when a server answers `ping`; otherwise the adapter starts the **default** session's server, exactly as the `herdr` command would, and attaches. |
| Coexistence | Nothing new runs. | Two servers: the user's and Husk's. Separate sockets and state; shared config and plugin registry. | One server either way. |
| Whose hooks run where | The user's server already runs every enabled plugin's startup and event hooks. The adapter must not run them again (see "Startup and event hooks": `auto` runs them only for plugins the attached server does not have enabled). | Husk's server runs every enabled plugin's startup hooks a second time at start and their event hooks for Husk's session; the user's server keeps doing the same for theirs. Daemon-style plugins (an animator, a title watcher) start twice and fight over shared files and `config.toml` blocks. | As (A) when attached; when the adapter started the server there was no other server, so hooks run once. |
| The user's workspaces | Visible to plugins (that is the point: the sidebar plugin lists the user's agents). Pane-opening side effects of plugin commands run by the *server* (event hooks) land in the user's Herdr TUI, as today. | Invisible: plugins see an empty session. A sidebar shows nothing; a mover has nothing to move. | As (A). |
| Isolation and safety | The adapter never stops, restarts, reloads or reconfigures the server. It only reads, subscribes and invokes what plugin commands ask for. | The adapter owns start and stop of its session. Stopping is safe because no user pane lives there, but `herdr session list` now shows a session the user did not create, and its `session.json` persists. | As (A). A server the adapter started is **not** stopped at Husk exit: it is the user's default session from then on, as if they had run `herdr`; it persists panes by design. |
| Herdr not installed | Plugins fail at shim start with `herdr binary not found`; the converter still works (conversion needs no binary), so the catalog lists them as `unavailable`. | Cannot start a server: same failure. | Same as (A) when no server runs. |
| Versions | The shim reads the server version from `ping` and refuses a plugin whose `min_herdr_version` is newer (`failed`, `herdr_too_old`). The adapter needs a server at or above 0.9 (popup placement, `plugin.pane.open` with `placement`, agent session fields). | Same, plus the adapter must know how to start a server of whatever version is installed. | Same. |
| Testing | The fake server stands in for the user's server; nothing to start. | Tests must drive a real `herdr server` or fake its start-up sequence and socket layout. | Both paths need tests. |

Decision: **(A) is the default**, (C) is the opt-in `--server start`, and (B) is not offered. (A) honours the operator's reading ("it would probably be enough for the herdr server to be running"), never touches the user's server, shows plugins the user's real workspaces, and runs no hook twice. (B) is wrong for a user who uses Herdr: it duplicates every daemon-style plugin and shows plugins an empty world; it returns only if a user asks for an isolated session, as `--server start --session <name>`, with the duplicate-hooks warning in `doctor`. (C) exists for a user whose only Herdr front is Husk: when no server runs, starting the default session is what they would get by typing `herdr`, and nothing else is on that machine to duplicate. The adapter never passes `server.stop`, `server.reload_config`, `session.stop`, `plugin.link`, `plugin.unlink`, `plugin.enable`, `plugin.disable` or `plugin.install` upstream on its own initiative. A plugin command that sends one is **forwarded unchanged and logged at warning level** naming the plugin and the method, because the plugin could have sent it to the real socket just the same, and the adapter is not a sandbox (neither is Herdr: "Herdr does not review or sandbox plugin code"); the log line is so the user can tell which plugin stopped their server.

Attach sequence, in the shim, before `live` work begins:

1. Resolve the Herdr binary: `--herdr-bin <path>` converter argument, else `herdr` on `PATH`. Missing: `failed`, `last_error` `herdr binary not found`.
2. Resolve the socket: `--session <name>` converter argument, else `HERDR_SOCKET_PATH` **if Husk itself runs inside a Herdr pane** (the variable then names the user's session; the shim does not strip it from its own environment), else the default session socket under the Herdr config directory (`HERDR_CONFIG_PATH` respected, as Herdr does).
3. `ping`. A reply is the server; its `version` is recorded. No socket, or no reply within 2 s: with `--server attach` (default) the shim stays up, writes `{"server": "absent"}` to its slice, and retries every 5 s with backoff to 30 s; its actions answer `unavailable` with `data.reason` `herdr_absent` until a server appears. With `--server start`, the shim takes the adapter's start lock (a `flock` on `<husk state dir>/herdr/start.lock`, so twelve shims start one server), runs `herdr server` detached (its own session, stdio to the log) if the lock holder found no server, waits for `ping` up to 10 s, and releases the lock. Failure: `failed`, `last_error` `herdr server did not start`.
4. Compare `min_herdr_version` with the server version (semver, Herdr's rule). Older server: `failed`, `herdr_too_old`, nothing else runs.
5. Read `plugin.list` upstream once: whether this plugin id is **enabled in the server** decides the hooks rule below; the answer is re-read on each reconnect.
6. Open the event subscription (if the plugin has `[[events]]`), open the proxy socket, run startup hooks (per the rule), then serve.

Reconnect: an upstream EOF or `events_lost` marks `{"server": "lost"}`, drops the subscription, and re-runs steps 3 to 6 with the same backoff. Actions during the gap answer `unavailable` / `herdr_absent`. Open terminal surfaces keep running: their processes already have what they need.

### Discovery and conversion

The converter role implements the executable-converter contract ([Pods](pods.md)): `husk-herdr detect <dir>` exits 0 when `<dir>/herdr-plugin.toml` exists; `husk-herdr discover` prints one directory per line; `husk-herdr convert <dir>` prints a `husk-plugin.json` v2 document on stdout and conversion problems as JSON lines on stderr. Conversion is pure: it reads the manifest and the registry, writes nothing, starts nothing, and never contacts a server.

Discovery reads the registry and prints `plugin_root` for every record whose `enabled` is true and whose `platforms` (top-level) include the current OS. Disabled plugins are skipped and reported once (`disabled_in_registry`), so a user controls the set with `herdr plugin enable|disable`, the same switch Herdr itself uses. A record whose manifest is missing or unparsable is reported (`registry_warning`, with Herdr's own `warnings` text when present) and skipped. A directory passed to Husk directly (`--plugin <dir>`) is converted whether or not it is in the registry: that is how an author tests a plugin before linking it.

Conversion of the top level:

| `herdr-plugin.toml` | `husk-plugin.json` v2 | Rule |
|---|---|---|
| `id` | `id` | Herdr allows letters, digits, `.`, `:`, `_`, `-`; Husk allows `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`. A `:` becomes `-`; an id that then collides with another converted id, or exceeds 64 characters, fails conversion (`id_unmappable`). The original id is kept in the shim for `HERDR_PLUGIN_ID` and every Herdr request. |
| `name`, `version`, `description` | same | Copied. |
| `min_herdr_version` | `x-herdr.min_version` | Carried for the shim's check; not a Husk field. Missing: fail (`manifest_invalid`), as Herdr does. |
| `platforms` | filter | A plugin whose top-level list excludes this OS fails conversion (`platform_unsupported`). Entries carry their own `platforms`; entries excluded for this OS are dropped silently, as Herdr would refuse them with `platform_unsupported` at invocation. |
| (none) | `protocol: 2`, `manifest_version: 2` | The shim speaks the host's major. |
| (none) | `command` | `["husk-herdr", "run", "<dir>", <converter args>]`, the same argv Husk used to call the converter plus `run`; absolute path of the converter binary. |
| (none) | `events.subscribe` | `[]`. Herdr events are not Husk events (see "Startup and event hooks"). |
| (none) | `state` | Absent: `<id>.state`, instance scope, not persistent. |

Conversion of the entries:

| Entry | Husk declaration | Rule |
|---|---|---|
| `[[build]]` | nothing | Never run. Herdr runs builds at install; a linked plugin is built by its author. One `build_not_run` note in the log, no problem. |
| `[[startup]]` | nothing | Run by the shim after attach, per the hooks rule. Listed in `x-herdr.startup` for `husk-herdr doctor`. |
| `[[actions]]` | `actions[]`: `id` `<id>.<action-id>`, `title`, `description`, `public: true` | Herdr qualifies actions the same way (`plugin.id.action`), so the ids agree. `public: true` because any Herdr process may invoke any plugin action, and the proxy's `plugin.action.invoke` for another adapter-run plugin becomes a Husk `action.invoke` between plugins ([Message kinds](message-kinds.md), permission). `contexts` and `command` go to `x-herdr.actions` for the shim. A `default_key` (seen in the wild, not in Herdr's documented schema) is a `suggestion_ignored` problem like a `shortcut`. |
| `[[panes]]` | `actions[]`: `id` `<id>.pane-<pane-id>`, `title` "Open <pane title>", `public: true` | A pane is opened, in Herdr, by `plugin.pane.open` from a command or the CLI; the synthesized action is how Husk's palette and keymap reach the same thing, and how the proxy routes `plugin.pane.open` for another plugin's entrypoint. A collision with a declared action id fails conversion (`duplicate_action`). `placement`, `width`, `height` and `command` go to `x-herdr.panes`. |
| `[[events]]` | nothing | Union of `on` names goes to `x-herdr.events`; the shim subscribes upstream. |
| `[[link_handlers]]` | nothing | `pattern` is compiled with Go's `regexp` (RE2). Rust and RE2 share the syntax Herdr plugins use in practice (anchors, classes, alternation, no backreferences or lookaround); a pattern RE2 rejects drops the handler with `pattern_unsupported`. `action` must name a declared action (`unknown_action` otherwise). Kept in `x-herdr.link_handlers` in manifest order. |
| (none) | `keybindings` | `[]`. Herdr's `[[keys.command]]` bindings live in `config.toml` and are never imported: two sources of truth for one key is a support problem, and Herdr's `prefix+` spellings have no Husk equivalent. The user binds `<id>.<action>` with `husk keys` in a Husk keymap layer ([Keybindings](keybindings.md)); `doctor` lists the Herdr bindings it found so the user can copy them. |

`x-herdr` is a vendor object the v2 converter retains and core ignores ([Store](store.md): unknown fields in a declared document are retained and ignored). The shim re-reads `herdr-plugin.toml` itself at start, so `x-herdr` is informational (for `doctor` and tests), never the shim's source of truth.

Ids inside the plugin follow Herdr's rule (letters, digits, `:`, `_`, `-`, no dots) and Husk's action-name rule (one dot-free segment); both are satisfied by the same string, with `:` mapped as for plugin ids.

**Hot registry changes.** Discovery runs when Husk starts, so a Herdr plugin installed or linked while Husk runs is not in the instance. Core's live `plugin.add` ([Pods](pods.md), [Instances](instances.md)) adds one plugin directory to a running instance for every plugin kind; the adapter adds nothing of its own on top of it. `husk-herdr sync [--instance <name>]` is the user's command for the gap: it runs discovery, lists the instance's plugins, and for every discovered directory whose converted id the instance does not have it requests `plugin.add` with that directory and the converter arguments the instance started with; the result is one line per plugin (`added`, `present`, `refused` with the conversion or launch problem). `sync` never removes: a plugin disabled or uninstalled in Herdr while Husk runs stays loaded until `husk plugin remove <id>` or the next start, and a plugin the user removed with `husk plugin remove` is added again by `sync` only if it is still enabled in the registry, which is the user's call on both sides. `husk-herdr doctor` reports every registry plugin that is enabled for this OS and absent from the instance, with `sync` as the remedy. Nothing watches `plugins.json`: a registry change is the result of a user command, and the user runs `sync` after it.

### The shim at run time

The shim is a Husk plugin ([Pods](pods.md)): it reads requests from stdin, replies on stdout, and sends everything else through the Husk socket with its token. Its Pod is `live` once started; the Herdr attach above runs inside it and is reported through the plugin's slice, not through Husk liveness, except for the terminal failures named (`herdr binary not found`, `herdr_too_old`, `herdr server did not start`), which exit the process with status 1 so the Pod shows `failed` with that `last_error`.

**Environment of every command** the shim runs, built from the shim's own environment with every `HUSK_*` variable removed except `HUSK_SOCKET_PATH`, `HUSK_INSTANCE` and `HUSK_BIN_PATH` (a Herdr plugin never acts as a Husk plugin; a shell it opens acts as the user, [Instances](instances.md)), then:

| Variable | Value | Source of the rule |
|---|---|---|
| `HERDR_SOCKET_PATH` | the shim's proxy socket | the proxy |
| `HERDR_BIN_PATH` | the resolved real `herdr` binary | Herdr: "Most plugins should call Herdr through `HERDR_BIN_PATH`" |
| `HERDR_ENV` | `1` | Herdr sets it in every managed process |
| `HERDR_PLUGIN_ID` | the original Herdr id | Herdr |
| `HERDR_PLUGIN_ROOT` | the plugin directory | Herdr |
| `HERDR_PLUGIN_CONFIG_DIR` | Herdr's own directory for this plugin, `<herdr config dir>/plugins/config/<id>` (what `herdr plugin config-dir` prints); created if absent | shared with Herdr on purpose: a plugin configured under Herdr is configured under Husk |
| `HERDR_PLUGIN_STATE_DIR` | Herdr's own state directory for this plugin, resolved exactly the way Herdr resolves it (the rule is read from Herdr's source or schema in unit A3 and pinned by a test). If the rule cannot be determined for the installed version, `$XDG_STATE_HOME/herdr/plugins/<id>` (`~/.local/state` when the variable is unset), with one warning in the log | same reason |
| `HERDR_PLUGIN_CONTEXT_JSON` | the invocation context, below | Herdr |
| `HERDR_WORKSPACE_ID`, `HERDR_TAB_ID`, `HERDR_PANE_ID` | from the context when present | Herdr |
| `HERDR_PLUGIN_ACTION_ID` | action commands | Herdr |
| `HERDR_PLUGIN_EVENT`, `HERDR_PLUGIN_EVENT_JSON` | `startup` for startup hooks; the event name and payload for event hooks | Herdr |
| `HERDR_PLUGIN_ENTRYPOINT_ID` | pane commands | Herdr |
| `HERDR_PLUGIN_CLICKED_URL`, `HERDR_PLUGIN_LINK_HANDLER_ID` | link-handler invocations | Herdr |
| `TERM`, `COLORTERM`, `TERM_PROGRAM`, `TERM_PROGRAM_VERSION` | pane commands only: `xterm-256color`, `truecolor`, `husk`, Husk's version | Herdr sets `TERM_PROGRAM=herdr`; the adapter says `husk`, so a plugin that branches on it sees Husk honestly, and one that misbehaves is a bug report to that plugin |

The working directory is the plugin directory, as in Herdr. Commands run through `exec.Command` without a shell, in their own process group; the shim stops a command's group with SIGTERM then SIGKILL after 2 s when the shim itself is stopped (EOF on stdin), the same policy Husk applies to the shim ([Pods](pods.md), Shutdown). Exit status and the last stderr lines of every command are appended to the shim's log (which is the plugin's Husk log, `husk plugin log <id>`), one line per command, matching what `herdr plugin log list` records.

**Invocation context** (`HERDR_PLUGIN_CONTEXT_JSON`) reproduces the object Herdr builds: `invocation_source`, `request_id`, `action_id` or `entrypoint_id`, `workspace_id`, `tab_id`, `pane_id`, the `workspace`, `tab` and `pane` records, `worktree` and `agent` when present, and for link clicks `clicked_url` and `link_handler_id`. The shim fills the ids and records from upstream at invocation time: `pane.current` without `caller_pane_id` (the server's focused pane, which is what Herdr uses for "missing context fields are filled from the active workspace, tab, focused pane"), then `workspace.get` and `tab.get`. The field names and the `invocation_source` vocabulary come from the server's own schema (`herdr api schema --json`, the plugin invocation context definition) and are pinned by a test against a committed copy of that schema; a field the shim cannot know (`selected_text`) is omitted. Husk-originated invocations use the schema's generic source value; invocations the proxy relays keep the source the caller gave.

**Actions.** `action.invoke` arrives on stdin with `args` and `caller` ([Message kinds](message-kinds.md)). The shim builds the context, spawns the command, and replies at once with `{"started": true, "pid": <pid>, "log": "<log path>"}`: that is Herdr's contract too (`plugin.action.invoke` "returns the Herdr-built invocation context plus the started command log record"), and a reply at exit would hit Husk's 10 s action timeout for every daemon-style action. A non-zero exit later produces one `toast.show` (`<name>: <action title> exited <status>`) and a log line. `args` are ignored except `placement`, `width`, `height`, `cwd`, `env`, `focus` and `target` on a `pane-*` action, which carry the `plugin.pane.open` parameters (below). An action whose `platforms` exclude this OS was dropped at conversion, so it is `not_found` in Husk, where Herdr would answer `platform_unsupported`.

**Startup and event hooks.** The rule is one setting, `--hooks auto|always|never`:

- `auto` (default): run this plugin's `[[startup]]` and `[[events]]` commands only when the attached server does **not** list the plugin as enabled. A server that has the plugin enabled already runs those hooks for that session; running them again would start every daemon twice and double every event reaction. The shim records which way it decided in its slice (`"hooks": "server" | "shim"`).
- `always`: run them regardless (a user who disabled the plugin's hooks in Herdr some other way, or a test).
- `never`: never run them; actions and panes only.

Startup hooks run once per attach (so again after a reconnect to a *new* server, which is what Herdr does on live handoff), asynchronously, with `HERDR_PLUGIN_EVENT=startup`; completion is logged; a failure never affects the Pod. Event hooks: one upstream `events.subscribe` with one entry per distinct `on` name; each pushed event spawns that entry's command with `HERDR_PLUGIN_EVENT=<name>` and the event object in `HERDR_PLUGIN_EVENT_JSON`. The server validates `on` names at link time; the shim passes them through and logs a subscription refusal naming the entry (`unknown_event`). Herdr events are **not** republished on Husk's bus: a plugin may publish only under its own id, there is no shared `herdr.` owner, and no Husk plugin consumes them. Republishing would need one publisher (a shared bridge Pod) and a reserved core prefix; it is revisited when a Husk-native plugin asks for a Herdr event.

**Panes and popups.** This is where the adapter needs the core deltas. A pane opens for one of three reasons: a `pane-<id>` action was invoked (palette, keymap, CLI, or another plugin), the proxy intercepted `plugin.pane.open` for one of this plugin's entrypoints, or a startup or event command of this plugin opened one through the proxy. In every case the shim:

1. Maps the placement: `popup` → `surface.open {"placement": "modal", "width", "height"}` with the manifest or request size (cells or `"NN%"`, Husk clamps to its modal minimum as Herdr clamps to the popup minimum); `overlay` and `zoomed` → `surface.open {"placement": "canvas"}`; `tab` → `canvas` as well; `split` → `canvas` until the runtime `stage` and `panel` placements land ([State](state.md), runtime surfaces; unit C5), then `panel`, the stacked side-panel dock. A `split` becoming a full-canvas surface is the honest interim rendering of a sidebar plugin inside Husk's fixed layout, and sidebar plugins are the reason C5 follows the terminal units.
2. Receives the runtime surface id (`<id>.<name>`, name chosen by the shim: the entrypoint id plus a counter) and `terminal.attach {"surface": ..., "capture": "all"}`; the result carries the inner size.
3. Spawns the pane command in a PTY of that size (`creack/pty`), with the environment above plus the terminal variables, and pumps PTY output into `terminal.write` chunks of at most 64 KiB (base64; the socket connection stays open for the life of the surface) and `terminal.input` bytes into the PTY. `surface.resized` resizes the PTY.
4. On process exit closes the surface (`surface.close`); on `surface.close` from the host (the user pressed the modal's close key, the frame's close button, or the plugin was removed) sends SIGHUP to the process group, then the stop policy.
5. Replies to the opener. For the proxy, `plugin.pane.open` answers Herdr's shape: `{"type": "ok"}` for a popup, and for the other placements a `pane` record with a synthetic `pane_id` of the form `husk:<surface id>`, which `plugin.pane.focus` and `plugin.pane.close` accept back (and nothing else: it is not a Herdr pane id and never reaches the server). The process gets no `HERDR_PANE_ID`, exactly as a Herdr popup process does not; the underlying focused Herdr pane is in the context JSON.

A `modal` surface is a singleton in Husk as a popup is in Herdr: a second open while one is showing answers `ui_busy` through the proxy and `busy` to a Husk caller. `popup.close` through the proxy closes this shim's modal if it has one, else is forwarded upstream (a Herdr-side popup, or `popup_not_open`). `plugin.pane.focus` for a `husk:` id focuses the surface; for a real pane id it is forwarded.

**Link handlers.** When the host reports a modified click on a URL inside one of this plugin's terminal surfaces (`terminal.link`, [Protocol](protocol.md)), the shim tests the URL against its handlers in manifest order; the first match runs that handler's action with `invocation_source` `link_click`, `clicked_url` and `link_handler_id` in the context and the two `HERDR_PLUGIN_*` variables set. No match: an `http` or `https` URL is opened with the OS opener (`open`, `xdg-open`), as Herdr does; anything else is ignored. Herdr applies link handlers to clicks in every pane of the session; the adapter sees only clicks in surfaces this shim hosts (Limits).

**Notifications.** `notification.show` through the proxy becomes `toast.show` in Husk (title and body joined, Herdr's 80/240 character trims applied) and answers `{"type": "notification_show", "shown": true, "reason": "shown"}`; it is not forwarded, because the user is looking at Husk. `client.window_title.set|clear` are forwarded unchanged (they act on Herdr's client, if any).

**The proxy**, precisely. One Unix socket per shim, mode 0600, in a private 0700 directory the shim creates with `os.MkdirTemp` (never in Husk's runtime directory, which belongs to the Server). For each accepted connection the proxy dials one upstream connection and copies lines both ways, so pipelining, ids and `events.subscribe` streams behave as they would against the real socket. It parses each request line only far enough to read `method`; the intercepted set is closed:

| Method | Proxy behaviour |
|---|---|
| `plugin.pane.open` | For an entrypoint of this plugin: open it here, answer as above. For another adapter-run plugin: Husk `action.invoke` of that plugin's `pane-<entrypoint>` action with the request's placement fields as `args`, answer from its reply. For a plugin this adapter does not run: forward upstream (the pane opens in Herdr). |
| `plugin.pane.focus`, `plugin.pane.close` | `husk:` ids are served here; others forwarded. |
| `popup.close` | This shim's modal if open; else forwarded. |
| `plugin.action.invoke` | An action of an adapter-run plugin (this one or another): Husk `action.invoke`, context relayed in `args.context`; answer Herdr's shape from the reply. Others forwarded. |
| `notification.show` | `toast.show`, not forwarded. |
| everything else | Forwarded byte for byte. |

The adapter-run set is read from Husk's own catalog (`husk action list`, filtered to actions whose declaring document carries `x-herdr`), cached per `services.reloaded`. A connection that fails upstream (server gone) gets Herdr's error shape `{"error": {"code": "unavailable", "message": "herdr server unavailable"}}` for each request it sends, and EOF when it stops.

### Keys and mouse

Inside a terminal surface the terminal application owns the keyboard: Herdr popups "receive all terminal input, including Escape". The shim attaches every pane surface with `capture: "all"` ([Keybindings](keybindings.md), "Capture modes"): every key goes to the emulator, which encodes it for the application's current modes (cursor keys, bracketed paste, kitty keyboard flags) and the bytes arrive at the shim as `terminal.input`. Only the host's **release key** (`husk.capture.release`, bound to `ctrl+]` in the built-in keymap) is held back: it suspends capture for the next key, so `ctrl+] ctrl+p` opens Husk's palette from inside a popup and `ctrl+] ctrl+]` sends a literal `ctrl+]` to the application. Mouse events on a focused terminal surface are encoded by the emulator when the application enabled mouse reporting and otherwise scroll the host-side scrollback; the shim sees only bytes. The shim never interprets a key itself; it has no key table.

Outside terminal surfaces the adapter has no input: Herdr plugin actions are reached through Husk's palette and keymap like any other plugin's actions, under the normal ranking.

### Context mapping

| Herdr | Husk | How the adapter bridges |
|---|---|---|
| Focused workspace, tab, pane (server-side) | `husk.focus` names a Husk surface | Unrelated by design. Herdr context comes from upstream at invocation time; Husk focus decides only which terminal surface receives keys. |
| Popup (session-modal, no pane id) | runtime `modal` surface, singleton | `surface.open`/`terminal.attach`; `ui_busy` ↔ `busy`. |
| Overlay, zoomed, tab pane | runtime `canvas` surface | Full canvas; the user returns with the surface's close key or by closing the process. |
| Split pane | runtime `canvas` until C5; `panel` after | The stacked side-panel dock is the only faithful home for a persistent side pane. |
| `HERDR_PANE_ID` of a plugin pane | none (`husk:<surface>` only inside the proxy) | As for Herdr popups. |
| Herdr event stream | the shim's upstream subscription | Not on Husk's bus. |
| `notification.show` | `toast.show` | Intercepted. |
| Plugin enabled/disabled | discovered or not; `husk plugin remove` for this instance | Herdr's registry is the switch; removal in Husk is per instance, as for every plugin. |
| Plugin logs (`plugin.log.list`) | `husk plugin log <id>` | One shim log per plugin with one line per command. |

### The plugin's slice

Each shim keeps `<id>.state` (instance scope, not persistent, 256 KiB cap) as its observable state for `husk state get` and tests:

```json
{"server": "attached", "socket": "/home/u/.config/herdr/herdr.sock", "version": "0.9.5", "hooks": "server",
 "startup": [{"command": ["node", "bin/agent-icons.js"], "status": 0, "at": "2026-10-23T12:00:01Z"}],
 "surfaces": {"herdr-bar.bar-1": {"entrypoint": "bar", "placement": "modal", "pid": 4242}},
 "commands_running": 1, "last_error": ""}
```

`server` is one of `absent`, `attached`, `lost`, `too_old`. The slice carries no secret and no context JSON.

### Invariants

1. **Core never names Herdr.** No file under `cmd/husk`, `internal/` or `pkg/` imports `adapters/` or contains the string; the existing docs lint keeps every doc but this one clean, and `TestAdapterIsNotImportedByCore` keeps the import direction.
2. **Unchanged plugins.** The adapter runs the manifest's argv commands with Herdr's documented environment and cwd; it never rewrites a plugin's files, and a plugin that works under Herdr and uses only documented variables and socket methods works under the adapter or fails with a named limit below.
3. **The user's server is read, never managed.** In the default mode the adapter sends the server nothing a plugin command did not ask for, except `ping`, `plugin.list`, `pane.current`, `workspace.get`, `tab.get` and `events.subscribe`. It never stops, reloads, links, unlinks, enables or disables.
4. **No hook runs twice because of Husk.** Under `--hooks auto` the shim runs startup and event hooks only for plugins the attached server does not have enabled.
5. **One Pod per Herdr plugin.** Liveness, log, token, slice and removal are Husk's ordinary per-plugin mechanics; the adapter adds no registry of its own.
6. **Every intercepted method answers Herdr's shape** (success and error objects as Herdr's schema defines them), so a plugin cannot tell the proxy from the server except by the `husk:` pane id form and the limits below.
7. **Conversion is pure and total over the registry**: a bad manifest fails only itself, every drop is a reported problem, and the same registry always yields the same catalog.
8. **Terminal bytes are opaque to the shim.** It copies PTY output to `terminal.write` and `terminal.input` to the PTY; the host emulates, encodes and renders.

### Failure modes

| Condition | Who detects | What happens | What the user sees |
|---|---|---|---|
| `herdr` binary not on `PATH` and no `--herdr-bin` | shim, attach step 1 | exits 1 | `husk plugin list`: `failed`, `last_error` `herdr binary not found`; one toast per plugin, joined as "N plugins failed" |
| No Herdr server (default mode) | shim, step 3 | stays `live`, slice `server: absent`, retries with backoff | actions answer `unavailable` / `herdr_absent`; palette rows stay, a toast on invoke |
| No server and `--server start` fails | shim | exits 1 | `failed`, `herdr server did not start` |
| Server older than `min_herdr_version` | shim, step 4 | exits 1 | `failed`, `herdr_too_old: needs 0.9.0, server is 0.8.2` |
| Server older than 0.9 (adapter minimum) | shim | exits 1 | `failed`, `herdr_too_old` naming the adapter's floor |
| Upstream EOF or `events_lost` | shim | slice `server: lost`; resubscribe and re-read `plugin.list` on reconnect; running terminals continue | nothing unless an action is invoked meanwhile (`unavailable`) |
| Registry missing or unreadable | converter `discover` | prints nothing, exit 0, one problem line | no Herdr plugins; `husk-herdr doctor` names the file |
| Manifest invalid, `min_herdr_version` missing, platform unsupported, id unmappable | converter `convert` | that plugin fails conversion | listed as a refused plugin like any invalid manifest ([Pods](pods.md)) |
| Entry excluded by its `platforms` | converter | dropped silently | the action is absent, as it would be refused in Herdr |
| `pattern` not RE2 | converter | handler dropped, `pattern_unsupported` | problem in the log; the URL opens externally instead |
| Plugin enabled in the server and `--hooks auto` | shim, step 5 | hooks left to the server | slice `hooks: server`; nothing runs twice |
| Action command fails to spawn | shim | reply `failed` with the exec error | error toast with the action title |
| Action command exits non-zero | shim | log line, `toast.show` | warning toast `<name>: <title> exited 2` |
| Pane command exits | shim | `surface.close` | the modal or canvas disappears |
| Second popup while one is open | shim | proxy `ui_busy`; Husk caller `busy` | as in Herdr |
| PTY allocation fails | shim | `surface.close`, reply `failed` | error toast |
| `terminal.write` refused (surface gone) | shim | stops the pump, SIGHUPs the group | nothing further |
| Command passes `--session <name>` to `herdr` | nobody | bypasses the proxy: its UI calls reach the server, its pane opens in Herdr | documented limit |
| Plugin sends `server.stop`, `session.stop` or `plugin.unlink|disable|enable` through the proxy | proxy | forwarded (the plugin could have done it anyway) and logged at warning level with the plugin id and method | the server stops or the registry changes, as under Herdr; the log says which plugin did it |
| Herdr plugin installed while Husk runs | nobody until `sync` or `doctor` | `husk-herdr sync` requests `plugin.add` for it | `doctor` lists it as absent from the instance until then |
| Two Husk instances run the same Herdr plugin | nobody | two shims, two subscriptions; `auto` hooks still defer to the server; with `--hooks always` both run them | same as two Herdr sessions would |
| `husk plugin remove <id>` | Husk | the shim gets EOF, stops its commands and PTYs, closes its surfaces | as for any plugin; Herdr's registry is untouched |

### Limits

| Limit | Value | Reason |
|---|---|---|
| Herdr server version | ≥ 0.9 | popup placement, `plugin.pane.open` placement override, agent session fields |
| Concurrent commands per shim | 32 | a runaway event hook (`pane.focused` on every click) must not fork-bomb; excess events are dropped with one log line per second |
| Terminal surfaces per shim | 8 | a plugin opens a popup or a pane, not a wall of them |
| `terminal.write` chunk | 64 KiB raw (about 88 KiB encoded) | well under the 4 MiB line, large enough to drain a busy PTY |
| Attach retry | 5 s, backoff to 30 s | a user who starts Herdr later gets plugins within half a minute |
| Attach ping timeout | 2 s | a live server answers `ping` without touching its panes |
| Server start wait (`--server start`) | 10 s | Herdr restores a session first |
| Startup hook grace at shim stop | 2 s, then SIGKILL | the stop policy Husk applies to the shim |
| Link handlers | this shim's surfaces only | Husk has no view of other panes |
| Registry re-read | at Husk start and on `husk-herdr sync` | the catalog is config; a registry change is a user command, so the user runs `sync` after it |

### Core capabilities it needs

All Level 1, specified in the core specs, none landed. The adapter is blocked on executable converters for existing at all (actions, startup and event hooks need nothing else), on runtime surfaces, terminal surfaces, capture modes and mouse forwarding for its UI, and on `plugin.add` only for `sync`.

| Capability | Spec | Used for |
|---|---|---|
| Executable converters and discovery (`--converter`, `husk.plugins.converters`) | [Pods](pods.md), "Executable converters" | roles 1 and 2 |
| Live `plugin.add` for every plugin kind (the companion of `plugin.remove`; its wire shape and the add/remove race are core's) | [Pods](pods.md), [Instances](instances.md) | `husk-herdr sync` |
| Runtime surfaces: `surface.open` with `modal` and `canvas` placements | [Protocol](protocol.md) "Runtime surfaces"; [State](state.md) "Runtime surfaces" | popups, overlays, zoomed and tab panes |
| Terminal surfaces: `terminal.attach|write|detach|input`, host-side emulator, scrollback | [Protocol](protocol.md) "Terminal surfaces"; [Pods](pods.md) Frontend seams | every pane |
| Capture modes (`none|text|all`) and the release key | [Keybindings](keybindings.md), "Capture modes" | keys inside panes |
| Mouse forwarding to terminal surfaces | [Keybindings](keybindings.md), "Mouse on terminal surfaces" | mouse inside panes |
| `terminal.link` | [Protocol](protocol.md) | link handlers |
| Runtime `stage` and `panel` placements | [State](state.md), "Open questions" (still open there; unit C5) | split panes (later) |

### Test strategy

- **Fixtures.** `adapters/herdr/testdata/manifests/` holds anonymized copies of real manifests covering every section and every quirk seen in the wild: per-platform duplicate entries with `-windows` ids, `default_key`, `contexts`, popup sizes in cells and percentages, relative commands, `sh -c` one-liners, startup hooks that are daemons, event hooks on `pane.focused`. Conversion tests are golden: `convert` output per fixture is committed and diffed.
- **Fake Herdr server** (`adapters/herdr/fakeherdr`): an in-process Unix socket server speaking Herdr's NDJSON shapes for the methods the adapter touches (`ping`, `plugin.list`, `pane.current`, `workspace.get`, `tab.get`, `events.subscribe` with a test-driven event injector, `popup.close`, `notification.show`, `plugin.pane.open` for non-adapter plugins) plus a passthrough recorder for everything else. Its responses are built from the committed copy of Herdr's API schema so a shape drift fails a test, not a user. It can simulate `events_lost`, a slow `ping`, an older version and a server that is absent then appears. No real `herdr` runs in CI.
- **Shim tests** drive `husk-herdr run` over pipes the way `plugins/reference/main_test.go` does: a fake Husk host on the other end of stdio and a fake Husk socket; assertions on the commands spawned (a recording `exec` seam with a fake executable that dumps its environment), on `surface.open`/`terminal.*` traffic, and on the slice writes.
- **Proxy tests**: a client that is the real `herdr` CLI when present on the machine (skipped in CI) and a scripted NDJSON client otherwise; interception table rows each have a case; passthrough is byte-exact; a dropped upstream yields the documented error shape.
- **PTY tests** use a fixed-output program (`printf` of a known escape sequence and `stty size`) and assert the bytes reach `terminal.write` and a resize reaches the PTY.
- **Host end-to-end** (`internal/host/cli_e2e_test.go` style, in `adapters/herdr`): a real `husk` host with `--converter husk-herdr`, the fake server, two fixture plugins; `husk action list` shows their actions, invoking one spawns the command with the right environment, `husk plugin remove` stops everything. Terminal rendering is covered by core's own golden frames once terminal surfaces land; the adapter asserts only the wire.
- **Manual check** against the real server, documented in `husk-herdr doctor` output: attached socket, version, the registry's plugins with their enabled state and whether each is in the instance, which hooks the shim would run, duplicates, the Herdr `[[keys.command]]` bindings that name plugin actions.

## Level 2: Status and work units

Nothing here is landed. Status rows:

| Area | Status |
|---|---|
| `cmd/husk-herdr`, `adapters/herdr/*` | not started |
| Core: executable converters | Level 1 ([Pods](pods.md)) |
| Core: live `plugin.add` | Level 1, specified on its own ([Pods](pods.md), [Instances](instances.md)) |
| Core: runtime surfaces | Level 1 ([Protocol](protocol.md), [State](state.md)) |
| Core: terminal surfaces | Level 1 ([Protocol](protocol.md), [Pods](pods.md) frontend seams) |
| Core: capture modes, release key, mouse to terminals | Level 1 ([Keybindings](keybindings.md)) |
| Core: `terminal.link`, runtime `stage`/`panel` | Level 1, after the above |

Work units, in order. Sizes: S under a day, M two to three days, L a week. Core units are Herdr-free and independently useful; adapter units are `adapters/herdr` only.

| # | Unit | Size | Depends on |
|---|---|---|---|
| C1 | **Executable converters.** `manifest.ExecConverter` (`detect`, `convert`, `discover`), `--converter` flag, `husk.plugins.converters` setting (launch-only, like `plugins.paths`), converter problems into the plugin's declared `problems`, tests with a fake converter executable. | M | — |
| A1 | **Conversion.** `adapters/herdr/manifest` and `registry`: TOML parsing, id mapping, platform filters, the entry table, golden fixtures, `husk-herdr detect|discover|convert`. | M | — |
| A2 | **Fake server and upstream client.** `fakeherdr`, `upstream` (dial, ping, version compare, `events.subscribe` reader with `events_lost`), committed schema copy and the shape test. | M | — |
| A3 | **Shim without UI.** `husk-herdr run`: Husk stdio, attach sequence and backoff, hooks rule, actions, startup and event hooks, the proxy with passthrough and the `plugin.action.invoke` and `notification.show` interceptions, the warning log for forwarded management methods, the state directory rule, the slice, the log lines, `doctor`. End-to-end test with the host and C1. | L | C1, A1, A2 |
| C6 | **Live `plugin.add`.** Core, for every plugin kind; specified in its own core spec and built before A4. | M | C1 |
| A7 | **Registry sync.** `husk-herdr sync`, the `doctor` row for registry plugins absent from the instance, end-to-end test that installs a fixture plugin into a fake registry while the host runs. | S | A3, C6 |
| C2 | **Runtime surfaces.** `surface.open` (`modal`, `canvas`), dynamic surface ids, `husk.surfaces.runtime`, modal singleton, frontend placement, close paths, golden frames. | L | — |
| C3 | **Terminal surfaces.** Emulator package over `charmbracelet/x/vt`, `terminal.attach|write|detach|input`, frontend draw into the cell canvas, scrollback on wheel, cursor, title in the frame label, golden frames with a scripted byte stream. | L | C2 |
| C4 | **Capture modes and the release key.** `capture` tri-state on the wire and in `husk.surfaces`, `all` filter, `husk.capture.release` and its one-shot, mouse and paste to terminal surfaces. | M | C3 |
| A4 | **Panes and popups.** PTYs, placement mapping, `plugin.pane.open|focus|close` and `popup.close` interception, `husk:` ids, exit and close paths, limits. | M | A3, C2, C3, C4 |
| C5 | **`terminal.link`** (OSC 8 and URL detection on modified click) and runtime `stage`/`panel` placements with the stack rule. | M | C3 |
| A5 | **Link handlers and `split` → `panel`.** | S | A4, C5 |
| A6 | **Hardening.** Reconnect under load, the two-instance case, 32-command cap, PTY teardown races, a run against the real server with the installed plugins recorded in a checklist. | M | A4 |

Critical path: C1 → A3 (actions usable), then C2 → C3 → C4 → A4 (panes visible). C2 to C4 can run in parallel with A1 to A3; C6 → A7 runs beside them and lands before A4.

## Decisions made here

1. **Converter plus shim, one binary, one Pod per Herdr plugin** (the operator's placement ruling). Rejected: a single adapter plugin with a regenerated manifest (stale catalog, one liveness for twelve plugins) and runtime-declared actions in core (Herdr plugins have no runtime registration, so core would gain a capability for nothing).
2. **Attach to the user's server by default; never manage it.** (B) duplicates every daemon-style plugin and shows plugins an empty session; (C) is opt-in for a Husk-only user and starts the default session, which is what `herdr` would start, and never stops it.
3. **Hooks follow the server (`--hooks auto`).** A plugin enabled in the attached server already has its startup and event hooks run there; the shim runs them only for plugins the server does not have enabled. Herdr's registry remains the one switch for what runs.
4. **A proxy socket, not a shim binary in front of `herdr`.** Herdr resolves the socket from `HERDR_SOCKET_PATH`, so the real `herdr` binary stays in `HERDR_BIN_PATH` and the interception set stays closed and small.
5. **Actions reply at spawn, not at exit**, as `plugin.action.invoke` does, so Husk's 10 s action timeout never fires for daemon-style actions; exit status becomes a log line and a toast.
6. **Panes are runtime terminal surfaces owned by the shim's PTY and emulated by the host.** The host must own the emulator to render into its cell canvas and to encode keys per the application's modes; the process belongs to the party that knows its environment and lifecycle. Rejected: host-spawned PTYs (core would exec a foreign command on a plugin's behalf) and shim-side emulation with `surface.update` lines (loses the cursor, colours beyond a line's SGR, modes and throughput).
7. **Panes are also actions** (`<id>.pane-<entrypoint>`), so the palette, the keymap and cross-plugin `plugin.pane.open` share one path.
8. **`split` renders as a full canvas until runtime `panel` lands.** Honest over clever: Husk's layout is fixed slots and Herdr's is a BSP tree; the only faithful mapping of a persistent side pane is a dock placement, which is a core unit of its own.
9. **Herdr events stay off Husk's bus**; `notification.show` becomes a toast; `client.window_title.*` is forwarded.
10. **Plugin config and state directories are Herdr's own**, so a plugin configured under Herdr is configured under Husk and vice versa.
11. **No Herdr keybinding import.** Husk's keymap layers bind `<id>.<action>`; the Herdr `config.toml` is Herdr's.
12. **Builds are never run.** Install-time concerns stay with `herdr plugin install`.
13. **Management methods are forwarded and logged.** `server.stop`, `session.stop` and `plugin.unlink|disable|enable` from a plugin command pass through the proxy unchanged, with one warning log line naming the plugin and the method. The adapter is not a sandbox and Herdr is not either; the log line only answers "which plugin did that".
14. **`TERM_PROGRAM=husk` in pane processes.** Honest over compatible; a plugin that branches on `herdr` and breaks has a bug.
15. **`HERDR_PLUGIN_STATE_DIR` is resolved as Herdr resolves it**, with `$XDG_STATE_HOME/herdr/plugins/<id>` and a warning as the fallback when the rule cannot be determined.
16. **Hot registry changes go through core's `plugin.add`.** It is a generic core capability for every plugin kind, built before the adapter's pane work; the adapter adds `husk-herdr sync` on top of it and nothing else, and `doctor` reports registry plugins absent from the instance. Rejected: an adapter-specific relaunch, and waiting for the next start.
17. **One connection set per shim, no shared bridge Pod.** Herdr supports many subscribers; a bridge would be a single point of failure and a routing problem between Pods.

## Resolved

1. Server mode: attach by default, `--server start` as the opt-in, no Husk-owned named session (decision 2).
2. Management methods through the proxy: forwarded, logged at warning level naming the plugin (decision 13).
3. `split` placement: full canvas until the stacked side-panel dock (C5) lands, then `panel` (decision 8).
4. Herdr `[[keys.command]]` bindings: not imported; `husk keys` is the way (decision 11).
5. Herdr events on Husk's bus: not republished for now (decision 9).
6. `HERDR_PLUGIN_STATE_DIR`: resolved exactly as Herdr does, `$XDG_STATE_HOME/herdr/plugins/<id>` with a warning as the fallback (decision 15).
7. `TERM_PROGRAM` in pane processes: `husk` (decision 14).
8. Hot registry changes: core builds a generic live `plugin.add` before the adapter's pane work; the adapter uses it through `husk-herdr sync`, and `doctor` reports registry plugins absent from the instance (decision 16; the earlier recommendation, waiting for the next start, was overruled).
9. Shared bridge Pod: no; one connection set per shim (decision 17).

## Open questions

1. **Should `sync` run by itself?** A watch on Herdr's `plugins.json` (or a Herdr `plugin.*` event, if the server publishes one) could request `plugin.add` without the user typing `husk-herdr sync`. The spec keeps it a user command: a registry change is itself a user command, a watcher is one more long-lived process (or one more duty for every shim), and a half-written registry file would be read mid-install. Recommended: keep it manual until a user asks; if it becomes automatic, it belongs in one process, not in every shim.
