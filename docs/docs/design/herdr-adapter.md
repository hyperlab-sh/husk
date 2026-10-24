---
type: Design Spec
title: "Herdr adapter: running Herdr plugins in Husk"
description: "A converter, action runner and three-method proxy over an existing Herdr server."
status: implemented
tags: [architecture-v2, adapters]
---

# Herdr adapter: running Herdr plugins in Husk

## Level 0: What and why

Herdr plugins are argv commands described by `herdr-plugin.toml`. Their API is the whole Herdr CLI and JSON socket; their UI is a terminal command. The adapter exposes enabled plugins' actions in Husk and renders their popup and full-canvas commands as runtime terminal surfaces. A real Herdr server remains required for workspace, tab, pane and agent operations.

One binary, `husk-herdr`, has three roles:

1. **Converter and discovery:** read the registry and manifests, producing one ordinary Husk Pod per enabled, platform-compatible plugin.
2. **Action runner:** `husk-herdr run <dir>` serves Husk actions and spawns manifest argv with the upstream environment, cwd and invocation context. Actions use pipes, panes use `pkg/ptyhost`.
3. **Proxy:** commands receive a private `HERDR_SOCKET_PATH`. Exactly `plugin.pane.open`, `popup.close` and `plugin.action.invoke` are intercepted; everything else is forwarded byte for byte to the existing server.

The adapter neither runs hooks nor manages a server. Herdr already runs startup and event hooks for every enabled plugin, which is precisely the discovery set. No event subscription, periodic probe, identity tracking or reconnect state machine is needed. Each action checks the server once and the next action simply dials again.

**Out of scope:** plugins that lay out or focus Herdr's own panes (split sidebars, docks, focus-driven tools) are not served by this adapter. Running the `herdr` TUI in a plain Husk terminal surface serves them without an adapter. Link handlers are not implemented.

All application knowledge stays in `adapters/herdr/`, `cmd/husk-herdr/` and this document. Core imports no adapter. The adapter uses the public protocol and generic plugin-side PTY helper; the host never executes a foreign command on the plugin's behalf.

## Level 1: Contract

### Upstream sources

The contract is pinned to upstream [plugin invocation context](https://github.com/herdrdev/herdr/blob/5608de0608918aba2f9d501e7793b984a0ca9cfa/src/app/api/plugins/context.rs#L374), [plugin operations](https://github.com/herdrdev/herdr/blob/5608de0608918aba2f9d501e7793b984a0ca9cfa/src/app/api/plugins/mod.rs), [platform validation](https://github.com/herdrdev/herdr/blob/5608de0608918aba2f9d501e7793b984a0ca9cfa/src/app/api/plugins/manifest.rs#L502-L514), [registry loading](https://github.com/herdrdev/herdr/blob/5608de0608918aba2f9d501e7793b984a0ca9cfa/src/persist/plugin_registry.rs), [configuration and state directory resolution](https://github.com/herdrdev/herdr/blob/5608de0608918aba2f9d501e7793b984a0ca9cfa/src/config/io.rs), [session paths](https://github.com/herdrdev/herdr/blob/5608de0608918aba2f9d501e7793b984a0ca9cfa/src/session.rs#L163-L182) and [plugin paths](https://github.com/herdrdev/herdr/blob/5608de0608918aba2f9d501e7793b984a0ca9cfa/src/plugin_paths.rs). The committed upstream API schema pins invocation context shapes in tests.

### Placement and dependencies

| Package | Responsibility |
|---|---|
| `cmd/husk-herdr` | `detect`, `discover`, `convert`, `run`, `sync`, `doctor` |
| `adapters/herdr/manifest` | Pure TOML conversion and diagnostics |
| `adapters/herdr/registry` | Enabled and platform-filtered registry discovery |
| `adapters/herdr/upstream` | Independent request connections, ping and version checks |
| `adapters/herdr/proxy` | Private NDJSON socket and byte-exact forwarding |
| `adapters/herdr/shim` | Stdio actions, process environment/context and terminal surfaces |
| `adapters/herdr/fakeherdr` | Guarded test responder and upstream shape fixture |

Core capabilities remain generic: executable converters, `plugin.add`, `surface.open`, terminal attachment/output/input, full capture and the capture-release key. `surface.focus` and `terminal.link` have no remaining consumers and are removed in protocol 2.11.

### Settings and request setup

The adapter reads `$XDG_CONFIG_HOME/husk-herdr/config.toml`, falling back to `~/.config/husk-herdr/config.toml`. A missing file supplies defaults; malformed settings fail conversion with `adapter_config_invalid`. Detection and discovery do not validate settings. Only two settings remain:

| Key | Value | Default |
|---|---|---|
| `herdr_bin` | Absolute binary path | `herdr` on `PATH` |
| `session` | Herdr session name | Inherited socket inside Herdr, otherwise default session |

The converted command is the bare `husk-herdr run <dir>`. Conversion/runtime reread settings at process start; restarting the Pod applies changes. Converter roles take no flags. User-run `sync` and `doctor` additionally accept `--instance`.

Socket resolution uses explicit session first, then inherited `HERDR_SOCKET_PATH` only when `HERDR_ENV=1`, then the default socket. A named session uses `sessions/<name>/herdr.sock` under the global configuration directory. `upstream.ConfigDir` is shared with registry discovery: nonempty absolute `XDG_CONFIG_HOME` wins; otherwise Unix uses home plus `.config/herdr`, and Windows tries absolute APPDATA, USERPROFILE, then home. A relative XDG setting resolves nothing. `HERDR_CONFIG_PATH` changes the config file only, never the registry or socket directory.

Before spawning an action or pane, send one bounded `ping`, validate its reply and check the server against `min_herdr_version` and the adapter floor 0.9. There is no background attachment. An absent server fails that request with `unavailable`, reason `herdr_absent`, and a clear message telling the user to start `herdr`; the Pod stays live. A subsequent request repeats the check and can succeed. Old versions and malformed replies fail the request without spawning. The adapter never starts, stops, reloads or reconfigures the server on its own initiative.

### Discovery and conversion

`detect <dir>` succeeds when the manifest exists. `discover` reads global `plugins.json` and prints enabled, OS-compatible plugin directories; disabled or invalid records produce diagnostics and are skipped. An unreadable registry is an empty discovery result with a warning. Direct conversion of a directory is allowed for development. Conversion reads files only, starts nothing and contacts no socket.

Plugin ids map `:` to `-`, then must match Husk's plugin-id rule. All distinct enabled registry ids mapping to the same id are refused deterministically with `id_unmappable`. Symlink-resolved directory membership determines registry collision scope; a direct unregistered directory is mapped alone. Inner entry ids are dot-free `[A-Za-z0-9_-]+` after mapping, without the plugin-id length or reserved-word restriction. Top-level unsupported platforms refuse conversion; entry-local unsupported platforms are silently dropped. An explicitly empty platform list is invalid. A missing `min_herdr_version` is invalid.

| Manifest entry | Conversion |
|---|---|
| Metadata | Copy id, name, version and description; protocol and manifest major 2 |
| `[[actions]]` | Public `<mapped-id>.<action>` actions, with context/argv metadata |
| `[[panes]]` | Public `<mapped-id>.pane-<entrypoint>` actions, with placement and size metadata |
| `[[build]]` | Never run; informational `build_not_run` diagnostic |
| `[[startup]]`, `[[events]]` | May be parsed for diagnostics, never executed or subscribed by the shim |
| `[[link_handlers]]` | Ignored: not converted, no diagnostic |
| Keys | Not imported; bind actions in a Husk keymap layer |

Duplicate synthesized action ids fail with `duplicate_action`. Shortcut/default-key suggestions produce `suggestion_ignored`. Informational vendor metadata is not the runtime source of truth: the shim rereads the manifest. Fatal conversion emits its fatal problem first; direct conversion uses readable registry records for collisions but does not repeat discovery warnings.

### Action process contract

The shim reads Husk requests on stdin, replies on stdout, and uses the authenticated Husk socket for host calls. Ordinary actions decode only `args.context`; pane actions additionally decode placement, dimensions, cwd and environment. Every action replies at spawn, not exit, with `started`, `pid`, `log`, upstream context and command record. Nonzero exit logs status and bounded stderr and emits one warning toast. Unknown actions answer `not_found`; spawn failures answer `failed`.

Commands run argv without a shell, cwd equal to the plugin directory, with pipes and their own process group. Shutdown follows the bounded TERM/KILL policy; a descendant retaining stderr cannot indefinitely block leader reaping. An observed-empty process group is retired before later signals. There is one plain 32-concurrent-command counter shared by action and pane commands, matching upstream `MAX_PLUGIN_COMMANDS_IN_FLIGHT`; reaping a leader releases its slot. A cap refusal is Husk `busy` with reason `too_many_commands`; both proxied action and pane requests translate it to upstream `plugin_command_limit_reached`. No adapter surface cap exists (host runtime admission still applies).

Inherited `HUSK_*` values are removed except user-facing `HUSK_SOCKET_PATH`, `HUSK_INSTANCE` and `HUSK_BIN_PATH`; credentials never reach child commands. The environment sets:

| Variable | Value |
|---|---|
| `HERDR_SOCKET_PATH` | Private proxy socket |
| `HERDR_BIN_PATH`, `HERDR_ENV` | Resolved real binary, `1` |
| `HERDR_PLUGIN_ID`, `HERDR_PLUGIN_ROOT` | Original id, plugin directory |
| `HERDR_PLUGIN_CONFIG_DIR`, `HERDR_PLUGIN_STATE_DIR` | Upstream's shared plugin directories, created if absent |
| `HERDR_PLUGIN_CONTEXT_JSON` | Upstream-shaped invocation context |
| `HERDR_WORKSPACE_ID`, `HERDR_TAB_ID`, `HERDR_PANE_ID` | Context fields when present; pane children omit pane id |
| `HERDR_PLUGIN_ACTION_ID` | Action commands only |
| `HERDR_PLUGIN_ENTRYPOINT_ID` | Pane commands only |
| `TERM`, `COLORTERM`, `TERM_PROGRAM`, `TERM_PROGRAM_VERSION` | Panes only: `xterm-256color`, `truecolor`, `husk`, host version |

Plugin directory components percent-encode bytes outside lowercase ASCII letters, digits, dot, underscore and hyphen, escape trailing dots and Windows reserved stems, and cap long components at 120 characters with a 12-hex SHA-256 suffix. Configuration appends `plugins/config/<component>`. State appends `plugins/<component>` to present XDG_STATE_HOME plus `herdr` (even empty), otherwise Unix home plus `.local/state/herdr`; Windows tries LOCALAPPDATA, USERPROFILE, then home; the upstream temporary state directory is the final fallback.

Context is read per invocation from `pane.current`, `workspace.get` and `tab.get`, never from Husk focus or caller identity. Husk calls use source `api` and the stdin request id as correlation id. Non-null schema-defined `args.context` fields override derived values; unknown fields are dropped. Context fixtures are validated against the committed upstream schema. Schema-defined `clicked_url` and `link_handler_id` remain in the context JSON when supplied, but the removed link-handler environment variables are never exported.

### Three interceptions

The private socket is mode 0600 in a private 0700 temporary directory. Interceptions have a 10-second cancellation bound. The closed interception set is:

| Method | Behavior |
|---|---|
| `plugin.pane.open` | Own entrypoint opens locally. Another enabled, platform-compatible registry plugin routes through its public Husk pane action; `not_found` falls back to unchanged forwarding. |
| `popup.close` | Close this shim's modal, otherwise forward unchanged. |
| `plugin.action.invoke` | An action of this plugin or of another eligible registry plugin routes through Husk `action.invoke` to that plugin's Pod, relaying context; `not_found` falls back to forwarding. A missing `plugin_id` takes the longest eligible id prefix of a qualified action id. |
| Everything else | Forward byte for byte, including `notification.show`, `plugin.pane.focus`, `plugin.pane.close` and event streams requested by plugin commands. |

Registry routing rereads changed registry data and never guesses from an unrelated native plugin's action id. Management requests from plugin commands are forwarded and logged, not sandboxed. An unavailable upstream produces one clear error per request. The next request can redial. Explicit `--session` passed to the real CLI bypasses the proxy and is a documented limitation.

### Two terminal pane kinds

`popup` opens runtime `modal`; `overlay`, `zoomed`, `tab` and `split` open runtime `canvas`. Missing placement defaults to overlay. Split is a full-canvas approximation, not a sidebar or a promise of future pane-layout support. Popup dimensions use manifest/request cells or percentages; host admission and geometry clamping apply. Other placement-specific focus/direction/target/workspace fields are ignored.

Every successful open returns `{"type":"ok"}`, including non-popups: no pane id, synthetic identity or proxy focus/close control plane. Reopening an entrypoint opens another canvas or gets modal busy; it never focuses an old lifetime. Only host `busy` with reason `modal_open` maps to upstream `ui_busy`, preserving popup retry loops. Command-cap refusals are not popup conflicts. Host `busy` with reason `too_many_surfaces` (the host's eight-runtime-surface limit) becomes the adapter-defined `limit_reached`; any other host error keeps its code.

Each open chooses a unique bounded runtime name, declares border/label/close chrome and attaches with capture `all`. If geometry is not ready, wait for a positive resize before retrying. `pkg/ptyhost` owns the sole PTY implementation. The shim uses `pkg/ptyhost.Pump` as the sole output reader. Its callback synchronously acknowledges `terminal.write` chunks no larger than 64 KiB, one in flight per terminal session; the next PTY read waits for ingestion. Input has a bounded 4 MiB queue, dropping newest overflow bytes with a rate-limited warning rather than blocking stdin. Resize applies inner dimensions to the PTY.

An invisible 0×0 resize is checked against runtime State before teardown: hidden or not-yet-laid-out canvases are retained. State failure is retried once inside a shared two-second bound; unconfirmed lifetimes are retained. Confirmed removal closes the PTY and stops its group without redundant surface-close notification. Process exit, output failure and shim EOF close the surface and run bounded group shutdown. The shim closes the terminal master without waiting, sends HUP then TERM to the process group, and arms its injected two-second KILL timer. It retains descendant-group tracking through shim shutdown and reaps the leader through the shared PTY helper; it does not call the helper's separate `Close`/`StopGrace` policy. Terminal bytes remain opaque; the shim interprets no keys. The host's capture-release key remains available.

### Sync and doctor

`sync [--instance <name>]` discovers, converts, lists instance plugins through the user CLI and adds only missing ids using `plugin.add`. It never removes or watches the registry. Output is one flattened `added <id>`, `present <id>` or `refused <id-or-dir>: <code>: <message>` line per discovered plugin. Discovery warnings go only to stderr. Launch refusal includes `last_error` when available. Exit status is the maximum contributing CLI/local failure; an invalid list reply is exit 6 and invalid arguments exit 2. A failed list makes no additions.

`doctor` checks settings, binary, socket/version, registry, conversion, loaded instance plugins and upstream action keybindings. An absent server does not suppress the other checks; malformed server replies or invalid config fail the command. Missing eligible plugins get a `sync` remedy only after successful conversion. Doctor has no hook or identity rows.

### Invariants and verification

- Core names no adapted application and imports no adapter.
- Plugins are unchanged: argv, cwd, environment and schema-shaped context remain upstream-compatible.
- Server lifecycle and hook execution belong solely to upstream.
- One Pod per plugin retains normal liveness, token, log and removal semantics.
- Three interceptions only; all other request bytes are preserved.
- Every test uses fake upstream/guarded dialing, never a real server; no sleeps.
- Behavior tests cover environment/cwd/context, each interception, exact forwarding, modal busy, all four canvas placements, absent-then-successful redial, and the command cap. New behavior tests are mutation-proven.
- Conversion and frame fixtures are generated by their harnesses, not hand-edited. The remote gate runs build, vet, formatting and race tests.

## Level 2: Status and work units

| Unit | Status |
|---|---|
| A1 conversion/discovery | Retained |
| A2 upstream client/fake | Superseded by per-request client and trimmed fake in A8 |
| A3 action shim | Retained process/environment/context; hooks and attachment superseded by A8 |
| A4 panes/popups | Simplified by A8 to modal/canvas and shared PTY helper |
| A5 link handlers | Removed by A8 |
| A6 hardening | Closed unmerged; superseded by A8, no server/reconnect hardening required |
| A7 manual registry sync | Retained |
| A8 simplification | Three interceptions, no hooks or server lifecycle, shared PTY, core consumer removals; protocol 2.11 |

## Decisions made here

1. Keep one converter/shim binary and one Pod per plugin; no shared bridge or core application knowledge.
2. Existing server only; per-action one-shot ping/version check, no attachment state machine.
3. Upstream alone runs hooks for enabled plugins; no adapter hook settings or subscriptions.
4. Exactly three interceptions; pane focus/close and notifications belong to the real server.
5. Modal and canvas only; every open creates a lifetime, with popup-shaped replies and no pane identity.
6. Use the generic shared PTY implementation, preserving acknowledged output, bounded input and confirmed-close detection.
7. Keep the upstream 32-command cap, no adapter-specific surface cap.
8. Remove unused core `surface.focus` and `terminal.link`, stamped as protocol 2.11.
9. Retain discovery, conversion, schema-shaped process context, manual sync and trimmed doctor.

## Open questions

None.
