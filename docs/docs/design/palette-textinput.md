# Command palette input and Bubbles textinput

Status: assessed, not adopted. Scope: `pkg/workbench` command palette query line.

## Question

The command palette query line is the only core widget with hand-written
editing: `handleKey` (`input.go`) appends typed text accepted by
`paletteTextFor` and removes one rune on backspace, and `drawCommandPalette`
(`render.go`) draws the query followed by a `█` cursor glyph. Could
`charm.land/bubbles/v2` `textinput` replace it without changing the palette
golden frame or the palette's key routing?

## Answer: no

Checked against `charm.land/bubbles/v2` v2.2.1 built with the pinned
Bubble Tea v2.1.0, Lip Gloss v2.0.6, `x/ansi` and Ultraviolet. Dependency
resolution keeps the pinned versions (Bubbles requires older ones), so the
module itself is not the obstacle. The widget's behaviour is.

1. **The frame changes.** `textinput.Model.View` always draws its virtual
   cursor at the end of the value as a reverse-video space
   (`cursor.Model.View`: `Style.Reverse(true).Render(" ")`); there is no
   option for a glyph. With `Prompt = "❯ "` and blinking off, an empty
   focused input renders `"\x1b[37m❯ \x1b[m\x1b[7;37m \x1b[m"`. The palette
   draws `❯ █` (`TestGoldenFramePaletteOpen` asserts that substring), so
   `TestGoldenFramePaletteOpen.golden` would change. Keeping the frame means
   drawing the query from `Value()`/`Position()` by hand, which leaves the
   rendering hand-written and only the dozen lines of editing replaced.

2. **Text acceptance differs.** `textinput.Update` inserts `msg.Text` for any
   key press its key map does not claim. The palette instead accepts the
   legacy key spelling filtered by `paletteTextFor`: text from Ctrl, Super,
   Meta and Hyper chords is dropped (`legacyKeyString`), the kitty key-0
   pure text event is turned back into text (`normalizeTextEvent`), and
   composed multi-rune text is accepted only when it is not a chord
   spelling. Matching this means pre-filtering every key and handing the
   widget a synthesized `tea.KeyPressMsg`, which is the existing logic plus
   an adapter.

3. **Paste and clipboard.** The host turns `tea.PasteMsg` into a
   bracket-wrapped key so pasted text cannot run host shortcuts; the palette
   then ignores ASCII pastes. `textinput` inserts `tea.PasteMsg` content
   directly, and its default `ctrl+v` binding reads the system clipboard
   through `github.com/atotto/clipboard`, which shells out to a platform
   tool. Both are behaviour changes; the second would need the binding
   disabled.

4. **Default bindings change the key routing.** `ctrl+k` is the palette's
   "previous item" key but `textinput`'s delete-to-end; `up`, `down`, `tab`
   and `ctrl+p` are suggestion keys in its default map; `ctrl+a`, `ctrl+e`,
   `ctrl+u`, `ctrl+w`, `ctrl+d`, `home`, `end`, `left`, `right` and `delete`
   would start editing the query where today they are swallowed. The host
   could keep intercepting its own keys first, but the palette's key set
   would still grow.

5. **More state, more code.** Adopting it needs a `textinput.Model` field,
   construction (prompt, styles with blinking off, focus, a trimmed key map),
   a reset on close, the key adapter from point 2 and either the widget's
   view (point 1) or a hand-written one. That is more code than the editing
   it replaces, plus a new module requirement.

Adopting `textinput` makes sense once the palette needs real line editing
(cursor movement, word deletion). That would be a deliberate behaviour
change that regenerates the palette golden frame, not a drop-in swap.

## Bubblezone

`bubblezone` was considered for mouse hit-testing and rejected: the host
already knows every slot rect (`recomputeLayout`, `hitOrder`), and the cell
compositor would have to strip zone markers from every fragment it draws.
