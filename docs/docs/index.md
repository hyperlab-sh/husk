---
title: "Husk"
description: "A minimal, language-agnostic TUI workbench with out-of-process plugins."
---

# Husk

Husk is a domain-agnostic TUI workbench written in Go (bubbletea and lipgloss). It owns only **slots**, **hooks** and **events**. Everything else is a plugin.

Every plugin is an independent OS process. Plugins talk to Husk over newline-delimited JSON on a Unix socket (`HUSK_SOCKET_PATH`) or over stdio, so they can be written in any language. A crashing plugin never takes the workbench down.

The slot layout follows the model used by `bb`. The first real plugin is `herdr-sidebar`, which brings Herdr workspaces and agents into Husk. [Dolphin](https://github.com/hyperlab-sh/dolphin) builds on Husk as a Herdr client.

## Status

Husk is early. The workbench core, the IPC protocol, the activity rail and `herdr-sidebar` work. The rail plugins (`tv-search`, `fleet-dashboard`, `project-git`) are still mock UIs. Open work is tracked in beads.

See [Getting Started](/getting-started/) to run it and [Architecture](/architecture/) for the slot model and protocol.

