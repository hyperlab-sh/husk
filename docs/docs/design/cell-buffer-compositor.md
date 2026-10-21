# Cell-buffer compositor

Status: implemented (see "Implementation notes"). Scope: `cmd/husk` rendering only. No wire, manifest or
plugin-visible change.

## Problem

`View` builds the frame by cutting and concatenating ANSI strings. Surfaces are
joined with `lipgloss.JoinHorizontal`/`JoinVertical`, and anything drawn on top
of something else is spliced in by column: `spliceOverlay` (modal, toast,
command palette), `overlayLabel` (hotspot badges, borderless frame labels), the
close glyph in `renderFrame`, and the three-fragment concatenation in the
region compositor (`renderPublished`). `ansi.Cut` keeps every escape of the
line, including those past the cut, so each splice must be followed by
`closeSGR` (`sgr.go`) to stop colour bleeding across the boundary. That fix is
correct but structural debt:

- Every new splice site has to remember `closeSGR`; forgetting it is a bleed
  bug that only a styled-base test catches.
- `sgrOpen` is a hand-rolled SGR scanner; hyperlinks (OSC 8) and other state
  are not covered by it at all.
- Geometry is implied twice: once by `recomputeLayout` (`slotRects`, which
  hit-testing uses) and again by the join order and string heights in `View`.
  They agree today only because every renderer pads to its rect exactly.
- Every overlay re-measures and re-cuts whole lines, at every frame.

## What Bubble Tea v2 expects

In `charm.land/bubbletea/v2` v2.1.0, `tea.View.Content` is a `string`; there is
no layer or drawable field. The renderer parses `Content` with
`uv.NewStyledString` into its own screen buffer and diffs that against the
previous frame. So the host cannot hand Bubble Tea a cell grid; it can only
hand it a string that decodes to the grid it wants. Diffing is already cell
based and stays Bubble Tea's job.

`github.com/charmbracelet/ultraviolet` is a direct requirement in `go.mod`.
`lipgloss.Canvas` wraps a `uv.ScreenBuffer` but its `Render` trims trailing
spaces, which breaks the exact-width frame invariant (`requireFrameGeometry`,
full-width bars), and `lipgloss.Layer` draws a whole multi-line string with one
pen, so a style left open on one plugin line would continue onto the next. The
host therefore composes into its own cell buffer (see Implementation notes).

## Current data flow

```
View
  renderTopWorkbenchBar      -> string (1 line)
  renderActivityRailBox      -> string
  renderSurface(slot)...     -> string per slot
     publishedContent -> renderPublished -> rows (anchors, regions spliced)
     overlayLabel (hotspots), renderFrame (border/label/close spliced)
  JoinVertical / JoinHorizontal (+ gutter column of spaces)
  renderBottomKeybindBar     -> string
  overlayModalPlugin  -> spliceOverlay(base, box, x, y)
  overlayToast        -> spliceOverlay
  overlayCommandPalette -> spliceOverlay
  tea.NewView(string)
```

## Proposed data flow

One `canvas` (a cell grid of `m.width` x `m.height`, width method
`ansi.GraphemeWidth`, matching the `ansi.StringWidth` math the layout uses) is
created per `View` call, written in z order, and serialized once.

Ultraviolet's screen buffers default to `Method: ansi.WcWidth`, so the width
method must be set explicitly; without it the canvas silently disagrees with
the layout on emoji ZWJ sequences and flags. The plan sketched this on a
`uv.ScreenBuffer`; the built canvas keeps the method itself (see
Implementation notes):

```go
func newCanvas(w, h int) *canvas {
	return &canvas{buf: uv.NewBuffer(w, h), method: ansi.GraphemeWidth, w: w, h: h}
}
```

`compose_test.go` pins this: blitting a ZWJ emoji (`👩‍💻`) and a flag (`🇧🇷`)
followed by `x` must place `x` at the column `ansi.StringWidth` predicts.

```
View
  c := newCanvas(m.width, m.height)           // blank cells
  c.blitBlock(0, 0, renderTopWorkbenchBar())  // row 0
  for slot in ascending slotZ (reverse hitOrder):
      m.drawSurface(c, slot)                  // into slotRects[slot]
          frame chrome -> border cells at rect edges
          content     -> c.blitLines(InnerX, InnerY, InnerW, InnerH, rows)
          label/close -> cells drawn over row 0 or the top edge
          regions     -> rect cleared, contribution lines blitted
  c.blitBlock(0, m.height-1, renderBottomKeybindBar())
  m.drawModal(c); m.drawToast(c); m.drawCommandPalette(c)   // same z order as today
  tea.NewView(c.render())
```

Rules:

- **Geometry has one source.** Every surface draws at its `SlotRect`. The
  joins and the explicit gutter fill disappear; the gutter is simply a column
  no surface writes, so it stays blank, as it is today.
- **Styles live on cells.** Each blit parses its string with
  `uv.NewStyledString` starting from a zero pen and writes cells carrying their
  own `uv.Style` and `uv.Link`. Nothing a fragment leaves open can reach a cell
  the fragment did not write, so there is no boundary to close.
- **Plugin lines are blitted one line at a time** (`blitLines`), each clipped
  to the inner width, so the existing contract holds: a style left open by a
  line stops at the line's last character and the padding is unstyled.
  Clipping and the `…` tail keep using `fitLineWidth` / `surfaceContentLines`
  first, so truncation semantics do not change.
- **Opaque blits.** `StyledString.Draw` clears its area before drawing; an
  overlay box or a region rect fully replaces what was under it, which is what
  the splices do now. Regions clear their whole reserved rect, as specified.
- **Lip Gloss stays the widget toolkit.** Bars, rail pills, the palette box,
  toast and the explorer rows are still built with Lip Gloss styles and blitted
  as blocks. Only composition changes.
- **Serialization** is `uv.Lines(buf.Lines).Render()`, not `Canvas.Render`: it
  emits every cell, so each line is exactly `m.width` cells and the frame is
  exactly `m.height` lines.
- **Purity.** The canvas is a local of `View`; it is never cached on the model.
  At 144x40 it is 5,760 cells per frame.

## Functions that change

All in `package main` under `cmd/husk`; no exported API in `pkg/` changes.

| Today | After |
| :--- | :--- |
| — | `compose.go`: `type canvas`, `newCanvas(w, h)`, `(*canvas).blitBlock(x, y int, s string)`, `(*canvas).blitLines(x, y, w, h int, lines []string)`, `(*canvas).set(x, y int, glyph string, st uv.Style)`, `(*canvas).render() string` |
| `View` | composes into a canvas as above |
| `renderSurface(slot, callout, name) string` | `drawSurface(c, slot, callout, name)` |
| `renderFrame(slot, rect, spec, label, lines) string` | `drawFrame(c, slot, rect, spec, label, lines)`; edge styles still from `dividerEdgeStyles` |
| `renderHotspotArchitectureStage() string` | `drawHotspotArchitectureStage(c)` |
| `renderActivityRailBox() string` | unchanged; blitted into the rail rect |
| `overlayModalPlugin(base) string` | `drawModal(c)` |
| `overlayToast(base) string` | `drawToast(c)` |
| `overlayCommandPalette(base) string` | `drawCommandPalette(c)` |
| `spliceOverlay`, `overlayLabel` | deleted |
| `(*publicationRegistry).renderPublished(...) ([]string, []contributionHit)` | split: `layoutPublished(...) publishedLayout` (anchor-substituted rows, region draws, hits; used by input) and `(publishedLayout).draw(c, x, y)`. `renderPublished` stays as a thin wrapper that draws into a scratch canvas and returns its rows, so input routing and `published_render_test.go` keep their shape |
| `publishedContent` | returns `publishedLayout` |
| `surfaceContentLines` | drops its `closeSGR` call in slice 4 (`blitLines` parses each line from a zero pen, so the close is redundant); clipping and the `…` tail are unchanged, padding is dropped (see "Implementation notes") |
| `sgr.go` (`sgrOpen`, `closeSGR`) | deleted in slice 5; its callers (`spliceOverlay`, `overlayLabel`, the close glyph in `renderFrame`, the region compositor, `surfaceContentLines`) are all gone by the end of slice 4 |
| `renderTooSmall`, `renderTopWorkbenchBar`, `renderBottomKeybindBar`, `fitLineWidth`, `recomputeLayout`, `hitOrder` | unchanged |

Hit-testing (`hitOrder`, `clickContributionAt`, `detectDividerAt`) keeps
working on `slotRects` and contribution hits; the compositor does not take
over input.

## Migration order

Each slice builds, passes `go test ./cmd/husk ./pkg/...`, and is shippable on
its own.

1. **Canvas and serializer, no composition change.** Add `compose.go`. `View`
   still builds the spliced string, then blits it into a canvas and returns
   `c.render()`. Cells are identical; bytes are not (Ultraviolet coalesces
   SGR differently). Add a test helper `frameCells(s string) uv.Lines` that
   decodes a frame through `uv.NewStyledString` into a buffer, and a test that
   asserts the new frame decodes to the same cells as each committed golden.
   Run it against the old goldens, then regenerate them with `-update` in the
   same commit; the commit body states that only bytes changed and the
   cell-equivalence test proves it. From here on the serializer is fixed, so
   every later slice must leave the goldens byte-identical.

   Tests that read `View` bytes must be converted in this slice, because the
   serializer changes them. `uv.Lines.Render` writes style transitions with
   `StyleDiff`, which emits off codes and replacement colours rather than a
   reset (a red cell followed by an fg-styled cell serializes as
   `\x1b[41mss\x1b[38;2;1;2;3;49mX`). The test helper `cellStyles` only
   accumulates parameters and treats only `0` or an empty list as a reset, so
   it would read the cells after that `49` as still `41`. Two tests are
   affected:

   - `TestCommandPaletteOverStyledStageNoBleed` (runs `cellStyles` over
     `m.View().Content`): rewritten to decode the frame with `frameCells` and
     check each cell's `uv.Style.Bg` against the red stage colour. The
     assertion is the same: red only on stage content cells outside the
     palette box.
   - `TestChromeRendersPaletteAsTruecolor` (substring search for
     `38;2;R;G;B` / `48;2;R;G;B` in `View` bytes): rewritten to look for a
     decoded cell whose `Fg` / `Bg` equals each palette role, so it does not
     depend on how the serializer groups parameters.

   Every other test that reads `View().Content` compares `ansi.Strip` text and
   is unaffected. Tests that run `cellStyles` on the output of `spliceOverlay`,
   `renderPublished` or `surfaceContentLines` see the same bytes as before in
   this slice and are converted with the code they test (slices 2 and 4).
2. **Overlays.** `drawModal`, `drawToast`, `drawCommandPalette` blit their
   boxes onto the canvas after the base is blitted. Delete `spliceOverlay`.
   Rewrite `TestSpliceOverlayNoSGRBleed` and
   `TestSpliceOverlayWellFormedBytesUnchanged` as canvas tests that assert on
   decoded cells (overlay cells carry only overlay styles; base cells right of
   the overlay keep their own style).
3. **Surfaces.** `drawSurface` / `drawFrame` / `drawHotspotArchitectureStage`
   draw at their rects; bars and rail blit at their rows/rect. Remove the
   joins and the gutter fill. A golden diff here is not "expected churn": it
   means `slotRects` and the old join geometry disagreed, which is a latent
   hit-testing bug to fix, not to `-update` away.
4. **Regions and labels.** Split `renderPublished` into layout and draw;
   regions clear and fill their rect on the canvas; labels and the close glyph
   are drawn as cells. Remove the `closeSGR` call from `surfaceContentLines`:
   content now reaches the canvas only through `blitLines`, which parses each
   line from a zero pen, so the close is redundant. `TestRegionCompositorNoSGRBleed`
   and `TestPublishedLineOpenStyleStopsAtContent` move to cell assertions,
   including the part of the latter that checks `surfaceContentLines` bytes
   directly: it becomes "blit the line with `blitLines`, the padding cells
   have a zero style". Its `sgrOpen(rows[1])` check is dropped with it.
   After this slice no production code calls `closeSGR` or `sgrOpen`.
5. **Cleanup.** Delete `sgr.go` and `TestSgrOpen` (slice 4 removed the last
   callers; `go vet` and the build confirm it); update the AGENTS.md paragraph on string splicing and the architecture
   doc's rendering notes. Record `BenchmarkIdleTickView` before slice 1 and
   after slice 5 in the commit body.

## Keeping the gates green

- **Golden frames** (`cmd/husk/golden_frame_test.go`, 144x40): regenerated
  exactly once, in slice 1, behind the cell-equivalence test. Slices 2 to 5
  must not touch `testdata/*.golden`. `requireFrameGeometry` keeps checking
  40 lines of 144 cells, which the full-cell serializer guarantees.
- **Headless dumps**: before slice 2, save `--dump-frame --demo-frame` (plain
  and with `--rail modal|full-canvas|stage`) and `--dump-frame --demo pubslots`
  output; each later slice must reproduce them byte for byte. The Rust-plugin
  dump (`--dump-frame --plugin-dir <plugins>`) must exit 0 and still print
  `Plugin PID: N (Rust)` and `SPACES (`; it is compared with the PID line
  masked.
- **`TestViewIsPure`** stays green because the canvas never leaves `View`.
- **Bleed tests** are not deleted, only re-expressed on cells, so they become
  stronger: they check every cell's style, not the absence of a byte pattern.
  The ones that read `View` bytes are converted in slice 1, together with the
  serializer change that breaks their byte assumptions; the rest are
  converted in the slice that replaces the code they test.

## Risks

- **Width method.** The canvas uses grapheme width to agree with the layout's
  `ansi.StringWidth`; this is set explicitly in `newCanvas` because the
  buffer defaults to wcwidth. Bubble Tea's renderer uses wcwidth unless the terminal
  reports grapheme clustering. That mismatch exists today and is not made
  worse, but it is now visible in one place; do not "fix" it here.
- **Wide glyphs at clip edges.** A wide cell cut by a region rect or an
  overlay edge must leave no half-glyph. Ultraviolet writes placeholders for
  wide cells; add a test with a CJK glyph straddling a region edge and an
  overlay edge.
- **Non-SGR escapes in plugin lines.** Today they pass through to the terminal
  raw. Through `StyledString` they are interpreted (OSC 8 becomes a cell link,
  closed at the clip edge) or carried as pass-through on the next cell. `\t`
  and `\r` inside a line may now move the pen. Any visible change shows up in
  the dump comparison; if it does, sanitize control characters in
  `surfaceContentLines` rather than special-casing the compositor.
- **Cost.** The frame is parsed twice (by the host into the canvas, by Bubble
  Tea into its buffer). Gate: `BenchmarkIdleTickView` may not regress by more
  than 1.5x; if it does, stop at the slice that crossed it and report.
- **Scope creep.** Converting widgets to direct cell drawing, or moving
  hit-testing onto a compositor, are tempting while the canvas exists. Both
  are out of scope.

## Implementation notes

Where the code differs from the plan above:

- The canvas holds a plain `uv.Buffer` plus its own width method, not a
  `uv.ScreenBuffer`: the screen buffer's touch tracking is unused cost here,
  and a small `uv.Screen` adapter over the buffer enforces each write's clip
  rectangle and blanks a wide glyph that would cross the clip's right edge.
- Cell buffers are recycled through a `sync.Pool` (package state, never model
  state), which keeps the per-frame garbage close to the string compositor's.
- `drawFrame` is split into a content blit and `drawChrome`; surfaces and the
  modal draw their published layout and then call `drawChrome` directly.
- `surfaceContentLines` no longer pads with spaces. Padding after a line that
  leaves a style open would carry that style once `closeSGR` was gone; the
  canvas clears the rest of each row instead.
- `BenchmarkIdleTickView/view` at 120x40 went from about 0.40 ms/op before
  slice 1 to about 0.30 ms/op after slice 5.

## Popup shadow

Popups (the modal and the command palette) are opaque, so on their own they
read flat. After each is drawn, `View` calls `canvas.castShadow` with its rect
(`blitBlock`'s result for the palette, the slot rect for the modal). Toasts
are not popups and cast none. The shadow is the
popup rect shifted 2 columns right and 1 row down, minus the popup itself,
clipped to the canvas (a popup against an edge just loses the part that falls
off-screen; it is not flipped).

- Cells under the shadow keep their glyph, width, link and attributes; only
  `Fg`, `Bg` and the underline colour change, each blended 65% of the way to black
  (`popupShadowStrength`). Black is the target on light themes as well, since
  a shadow darkens.
- A cell with no foreground or background of its own (plugin cells that
  never set one) is resolved first: foreground against the palette `Ink`,
  background against `Canvas`, the colours the app background pass would give
  them. The pass runs before that background fill, and its cells then carry an
  explicit background, so the fill leaves them alone.
- A wide glyph is darkened whole when the shadow covers either of its cells.
- Cells carry their own colours, so nothing leaks into neighbours when the
  frame is serialized.
- The canvas remembers which popup rects already cast in the frame; casting
  the same rect twice does not darken twice. A different rect (a palette over a
  modal) stacks and darkens the overlap further.

## Out of scope

- Any change to Bubble Tea's renderer or to `tea.View` usage beyond
  `Content` (there is no layer field to use in v2.1.0).
- `lipgloss.Compositor` / `Layer` hit-testing; input stays on `slotRects`.
- Rewriting Lip Gloss widgets (bars, rail, palette, toast, explorer) as cell
  code.
- Content semantics: clipping stays clip-not-wrap, no scrolling, no clip
  notification to plugins.
- `renderTooSmall`, colour profiles, the palette, the wire protocol and the
  width-method mismatch above.
