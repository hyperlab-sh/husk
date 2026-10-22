---
title: "Husk"
description: "A minimal, language-agnostic TUI workbench with out-of-process plugins."
---

# Husk

Husk is a domain-agnostic TUI workbench written in Go (bubbletea and lipgloss). It owns only **slots**, **hooks** and **events**. Everything else is a plugin.

Every plugin is an independent OS process. Plugins talk to Husk over newline-delimited JSON on a Unix socket (`HUSK_SOCKET_PATH`) or over stdio, so they can be written in any language. A crashing plugin never takes the workbench down.

The slot layout follows the model used by `bb`. Husk is application-agnostic: it knows nothing about any particular application; everything app-specific lives in a plugin.

## Status

Husk is early. The workbench core, the wire protocol, the activity rail and plugin manifests work. The bundled plugins are a small reference plugin pair (`plugins/reference`) that shows frames, a published slot, a hook and an event. Open work is tracked in beads.

See [Getting Started](/getting-started/) to run it and [Architecture](/architecture/) for the slot model and protocol.

