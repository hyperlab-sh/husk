---
title: "Design notes"
description: "Background and trade-offs behind host decisions."
---

# Design notes

Background and trade-offs behind decisions in the host.

- [Cell-buffer compositor](cell-buffer-compositor.md): how `View` composes the
  frame into a cell grid instead of splicing ANSI strings. Implemented.
- [Command palette input and Bubbles textinput](palette-textinput.md): whether
  the palette query line should adopt the Bubbles `textinput`. Assessed, not
  adopted.
