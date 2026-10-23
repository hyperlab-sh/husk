---
title: "Design notes"
description: "Background and trade-offs behind host decisions."
---

# Design notes

Background and trade-offs behind decisions in the host.

## Implemented or assessed

- [Cell-buffer compositor](cell-buffer-compositor.md): how `View` composes the
  frame into a cell grid instead of splicing ANSI strings. Implemented.
- [Command palette input and Bubbles textinput](palette-textinput.md): whether
  the palette query line should adopt the Bubbles `textinput`. Assessed, not
  adopted.
- [Workbench chrome](chrome.md): every surface laid out whether occupied or
  not, with its name when empty; the rail tiles; what the top and bottom bars
  show, including the layout label and the theme name. Landed. The colour
  discipline (which palette role each piece may use) remains specified, not landed.
- [Built-in themes](themes.md): the nine themes Husk ships as a built-in
  `themes` layer (`charm` default, `charm-light`, `dracula`, `base16`,
  `catppuccin-mocha`, `catppuccin-latte`, `rose-pine`, `rose-pine-moon`,
  `rose-pine-dawn`), how each source palette was mapped onto Husk's roles,
  the contrast adjustments, and the exact document. Specified, not landed.

## Architecture v2 (planned)

Progressive specs, each with a Level 0 overview, a Level 1 contract and a
Level 2 migration from today's code. Read [Protocol](protocol.md) and
[Message kinds](message-kinds.md) first.

- [Protocol: wire shapes](protocol.md): the envelopes, framing, versioning,
  limits, error codes and method names shared by Husk, plugins and the CLI.
- [Message kinds](message-kinds.md): contracts for the Dispatcher, Event bus
  and Hook runner: how actions, events and hooks move between Husk, plugins
  and the CLI.
- [Hooks](hooks.md): a hook as an extension point inside its owner's
  operation: owner-only firing, the `transform`, `collect` and `gate` kinds,
  priority order, timeouts, reentrancy, host-fired hooks and `husk hook list`.
- [Calls](calls.md): the Stem module that owns every outstanding
  Husk-to-plugin request: ids, the pending table, timeouts, caps, exit cleanup
  and reply delivery on the Bubble Tea loop.
- [State](state.md): the single in-memory tree of owner-registered slices
  that answers what is true right now in one Husk instance.
- [Store](store.md): how Husk merges config layers, places every file it
  reads or writes, encrypts secrets, and hydrates and persists State slices
  without knowing what any key means.
- [Services](services.md): Profile, Settings, Theme, Keybinding and Secrets
  as stateless modules over Substrate, with cache, reload and CLI contracts.
- [Pods](pods.md): how an instance hosts plugins: one socket server per
  instance, one pod per plugin that converts its manifest, runs its process
  over stdio and frames its surfaces.
- [Instances and the CLI](instances.md): how a Husk process becomes an
  addressable instance, how the `husk` CLI finds one, and the v1 command
  surface that doubles as the plugin API.
- [Keybinding policy](keybindings.md): who may bind keys, which layers a
  keymap comes from, how conflicts resolve, the keymap file shape, and what
  `husk keys` reports.
- [Bundles and `husk create`](bundles.md): a bundle as a directory with a
  small manifest, one folder per plugin and optional config files; how
  `husk --bundle` loads it as plugins plus the distribution layer; and what
  `husk create tui|bundle` writes.
- [Guide plugin](guide-plugin.md): an in-repo developer plugin that draws
  numbered callouts, surface names, occupants and keys over the live layout
  on the `app-overlay` surface; the overlay layer's drawing rules and the
  keys a plugin may read are specified in the compositor, State and
  Keybinding specs.
- [Settings center](settings-center.md): the one host-owned settings modal,
  tabs on the left (General, Keys, Theme, Plugins with Store and Installed)
  and the current profile top-right; forms built with huh over the existing
  methods; the `catalog` document the Store tab reads.

## Adapters (planned)

Everything that knows a foreign plugin format lives outside the host, behind
the generic seams the core specs define (executable converters, runtime and
terminal surfaces, capture modes).

- [Terminal multiplexer adapter](herdr-adapter.md): running an external
  terminal multiplexer's unmodified plugins over its real server through a
  converter and shim shipped in this repository outside core; which server
  mode is used, how its panes and popups become terminal surfaces, and what
  the core must provide.
