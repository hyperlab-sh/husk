---
title: "Workbench chrome: empty surfaces, rail, bars"
description: "What the frontend draws around plugin content: every surface laid out whether occupied or not, the rail tiles, the top bar and the bottom bar."
---

# Workbench chrome: empty surfaces, rail, bars

Status: specified, not landed. Scope: `cmd/husk` layout and rendering (the
frontend). No wire, manifest or plugin-visible change; `husk.surfaces.visible`
and `husk.geometry.rects` change content, not shape.

## Level 0: What and why

Husk is a blank-canvas TUI framework. The frontend's job is to show the canvas,
not to hide it: a developer who starts `husk` with no plugins must see the
layout they will be filling, and a developer whose plugin has not claimed a
surface yet must see where that surface is. Today the frontend does the
opposite in several places. It lays a side column out only when a plugin
occupies it, so an empty workbench is one wide stage and the dock keys in the
footer toggle columns that are not there. Its top bar carries a brand, a layout
label, a plugin count, a theme pill and a rail-key reminder, most of which
duplicate the rail and the footer. Its bottom bar shows an IPC counter nobody
reads. And its rail buttons are tall pills whose caps depend on half-block
glyphs.

This spec fixes four things:

1. **Every surface the layout has a place for is laid out, occupied or not.**
   An unoccupied region slot is drawn with its humanised surface name so the layout
   is visible. The footer's dock keys never point at a dock that pressing them
   cannot bring on screen: a dock key is shown only when pressing it changes
   the frame.
2. **Rail buttons are tiles, closer to square**, drawn from whole cells.
3. **The top bar is the brand and the transient status pills**, nothing else.
4. **The bottom bar shows the layout label at the left and the theme name at
   the right**, around the key hints. The theme name is clickable; it runs
   `husk.settings.open` with `tab: theme`, which opens the Settings center on
   its Theme tab, the theme selector, once that lands.

Husk has no normal/insert modes and this spec introduces none. Where this
spec's requirements say "mode", they mean the layout label defined under "The
layout label" below: a description of what is on screen, derived from the
layout, never a state that changes how keys are read.

Everything here is framework-general. What a surface should contain, whether
an empty workbench should suggest plugins to install, and what a distribution
puts in its rail are consumer concerns and do not belong in the frontend.

## Level 1: Contract

### Terms

- A **surface** is one of the twelve ids in `pkg/protocol/params.go`
  (`SurfaceID`). The **region slots** are the seven the default layout places:
  `activity-rail`, `sidebar-navigation`, `sidebar-list`, `sidebar-footer`,
  `main-header`, `main-stage`, `side-panel`. The **placement surfaces** are
  `modal-surface`, `full-canvas` and `workspace-stage`, which exist while a
  rail item with that placement is active (`modal-surface` while a modal is
  open). `thread-row-status` has no rect (see Decision 7). `app-overlay` is
  not a region slot: it is the overlay layer, with a whole-frame rect
  ([Cell-buffer compositor](cell-buffer-compositor.md), "Overlay layer"; see
  Decision 8).
- A surface is **drawn** when the frontend gives it a rect in `husk.geometry`
  and lists it in `husk.surfaces.visible`. It is **occupied** when
  `husk.surfaces.occupants` has an entry for it ([State](state.md), "Surfaces
  resolution"). A drawn **region slot** with no occupant is **empty**; a
  placement surface is never empty (below). Drawn and occupied are independent:
  a drawn surface may be empty, and an occupied surface may be hidden (its dock
  is off, or it does not fit).
- A **dock** is a side column the user can show or hide: the sidebar (the three
  `sidebar-*` bands) and the side panel. Their shown flags and sizes are the
  user's preferences in `husk.layout`.
- The **humanised name** of a surface is its id with every `-` replaced by a
  space and the first letter upper-cased, nothing else: `sidebar-list` is
  "Sidebar list", `thread-row-status` is "Thread row status", `full-canvas` is
  "Full canvas". It is computed from the id by one function, never looked up in
  a table, so a surface added to `pkg/protocol` gets a name without a frontend
  change. The slot explorer's titles (`Sidebar List`, `Row Status`) are a
  different thing and are unchanged.

### Layout: every surface is laid out

The layout is a function of the terminal size, `husk.layout` (dock preferences)
and the active placement. Occupancy is not an input. This reverses the rule
under which a side column existed only when a plugin occupied one of its
surfaces; `leftSideShown` and `rightSideShown` lose their occupancy term.

Default placement (no rail item active), left to right:

| Column | Surfaces | Width | Drawn when |
| :--- | :--- | :--- | :--- |
| Rail | `activity-rail` | 7 cells, fixed | Always |
| Sidebar | `sidebar-navigation`, `sidebar-list`, `sidebar-footer`, stacked | `husk.layout.sidebar.width`, capped at half the width right of the rail | `husk.layout.sidebar.shown` |
| Centre | `main-header` above `main-stage` | What is left, never under 30 cells; one cell narrower (the gutter) when the side panel is drawn | Always |
| Side panel | `side-panel` | `husk.layout.side_panel.width`, capped at half the width left after the sidebar | `husk.layout.side_panel.shown` and it fits (below) |

Heights: the top bar is row 0, the bottom bar is the last row, the body is the
rows between. In the sidebar, `nav_height` and `footer_height` are at least 3
rows and the list takes the rest, at least 6; when the two bands plus 6 rows
would not fit the body, both bands shrink to 4. The main header is at least 3
rows, 4 when header plus 8 would not fit, and the stage takes the rest, at
least 8. Nothing here changes.

Placements: `full-canvas` draws the rail and one `full-canvas` surface over
everything right of it; `stage` draws the rail, the sidebar (if its dock is on)
and one `workspace-stage` surface over the rest; the side panel is never drawn
in either. `modal-surface` is drawn over the default layout at the modal rect.
A placement surface is drawn only while its rail item is active (or its modal
is open), and it always has a plugin behind it: its registry occupant, or,
when no plugin claimed it in a manifest, the plugin that declared the active
rail item, which then receives its keys ([Architecture](../architecture.md),
"Input routing"; `keyOwner` in `cmd/husk`). So a placement surface is never
empty and never shows its name, whatever `husk.surfaces.occupants` says; until
content arrives it is a blank canvas like any occupied surface.

`husk.surfaces.visible` lists every drawn surface, empty ones included; a
surface that is not drawn is not listed, whatever its occupancy.
`husk.geometry.rects` has a rect for every drawn surface and for no other.
`surface.resized` is unchanged: it reaches occupants only, so an empty surface
tells nobody its size, and a plugin that claims it later is told then.

#### Fit and collapse

The minimum terminal size stays 84x24. Below it the whole frame is the
"terminal too small" screen of [Architecture](../architecture.md), "Minimum
size"; nothing in this spec applies there.

At or above the minimum, exactly one surface can be collapsed for lack of room:
the **side panel**. It is drawn only when the width right of the sidebar (the
rail and the sidebar subtracted, before the panel takes its share) is at least
72 cells (`minPanelCentreWidth`, the test `remW-leftW >= minPanelCentreWidth`
in `placeSlots`); with the 7-cell rail and the default sidebar width of 38 that
is a terminal of at least 117 cells, and with the sidebar
dock off a panel always fits at the minimum width. The sidebar never collapses
for width (its cap at half the remaining width keeps the centre at or above
30), and no surface collapses for height (the bands shrink to the minimums
above instead). So the order in which surfaces give way as a terminal narrows
is: the side panel, then nothing, then the too-small screen.

A collapsed side panel has **no collapsed form**: no column, no divider, no
marker. The frame is the one the user would get with the panel dock off, and
the three things that describe the layout agree with it: `husk.surfaces.visible`
omits `side-panel`, the layout label omits "Right", and the footer omits the
side-panel key (next section). The dock preference itself is untouched, so the
panel reappears, without user action, as soon as the terminal is wide enough.
An occupant of a collapsed panel is told `visible: false` and its size when it
is drawn again, as today.

#### Dock keys in the footer

The footer's docks group ([Keybindings](keybindings.md), "Rail items, palette
and footer") shows `husk.sidebar.toggle`, `husk.sidepanel.toggle` and
`husk.docks.cycle`. Each key is shown only when pressing it changes the frame:

| Key | Shown when |
| :--- | :--- |
| `husk.sidebar.toggle` | The placement is not `full-canvas` |
| `husk.sidepanel.toggle` | The placement is the default one (an open modal does not change the placement) and the panel fits, computed with the sidebar's current shown state (so it is shown when the panel is drawn, and when it is hidden by preference but would be drawn if shown) |
| `husk.docks.cycle` | Both of the above are shown |

Keys whose action nothing binds are left out as before. When no key is shown
the group is omitted. The keys themselves keep working whatever the footer
shows: hiding a key hides a hint, never a binding.

### Empty surfaces

A drawn, empty surface (a region slot with no occupant) shows its humanised
name, and nothing else:

- The name is drawn in one line, centred horizontally and vertically in the
  surface's inner rect (the rect itself, since an empty surface has no
  occupant and therefore no declared frame and no border). A rect of odd
  leftover is biased up and left, as `lipgloss.Place` with `Center` does.
- Style: the palette's `ink_subtle` on the slot background (`slot_background`),
  no bold, no border, no box. Husk draws no chrome of its own around a surface,
  occupied or not; the name sits on the same slot background that is painted
  under plugin content, so adjacent empty surfaces are told apart by their
  names and by the layout's gutters, as adjacent borderless plugin surfaces
  are.
- A name wider than the inner width is clipped with a `…` tail like plugin
  text. An inner rect narrower than 3 cells or shorter than 1 row shows
  nothing.
- Published regions never apply: a region is declared on a surface its
  publisher occupies, so an empty surface has none.
- Hotspot badges (toggled by `husk.hotspots.badges`) are drawn after the
  name, so a badge that lands on the name's row covers it.
- While the slot explorer is open (`husk.hotspots.toggle_stage`), `main-stage`
  shows the explorer as today, not its name.

Which surfaces are empty is read from `husk.surfaces.occupants` of the current
snapshot, the same source the renderer uses to decide whose content a surface
shows (`surfaceOwner`), for region slots only. An occupant that leaves `live` is vacated by the Surfaces engine, so a
crashed plugin's surface shows the name from the tick the engine vacates it,
and an occupant that has claimed but not drawn shows a blank canvas, never the
name: the pixels of an occupied surface belong to its occupant.

Input on an empty surface is unchanged: a key for it is dropped, a click on it
reaches no plugin, and it can hold focus (the default focus before any plugin
connects is `sidebar-list`, and `husk.focus.next` keeps its fixed order,
`sidebar-list`, `main-stage`, `side-panel` (`sidebar-list`,
`workspace-stage` under `stage`), skipping those not drawn, empty or not). Focus is not drawn on an empty surface because there
is no border to highlight.

### The rail

The activity rail is 7 cells wide (`activityRailWidth`, was 5). A rail button
is a **tile**: 5 cells wide and 3 rows tall, drawn from whole cells with the
tile background, no half-block caps, with one cell of rail background on each
side. The icon sits on the middle row, centred in the 5 cells (a 1-cell icon
has two cells of tile on each side; a 2-cell icon has two on the left and one
on the right). In rail rows (row 0 is the rail rect's first row), the first
tile is rows 1 to 3 and each next tile starts 4 rows lower (one blank row
between tiles). The palette tile is rows `h-4` to `h-2` of a rail `h` rows
tall when `h >= next+6`, `next` being the row the palette tile would otherwise
start on (today's test, with the new stride); otherwise it is stacked like the
others. With the canonical 1:2 terminal cell a tile is 5 units wide by 6 tall,
and with the 1:1.67 cell of most monospace fonts it is square.

The three states keep today's colours and the rules for them. **Active**:
tile background `surface_selected`, icon `accent`, bold. **Hovered**: tile
background `surface_elevated`, icon `ink`. **Idle**: no tile; the icon alone in
`ink_muted` on the rail background. While Alt is held (or latched) every button
is drawn as a tile (the active one on `surface_selected`, the others on
`surface_elevated`) with its effective key (`KeysFor(id)[0]`, spelled as the
rail spells it today, `ctrl+p` as `^P`) centred on the middle row in `warning`,
bold, clipped to the 5 cells; a button with no key keeps its icon. The hit zone of a button is its 3 rows across the
rail's width, as today. `husk.hotspots.toggle_stage` and `husk.palette.open`
keep their tiles; what the rail holds with no plugins is unchanged (home, the
slot explorer, the palette).

### The top bar

Row 0, full width, background `surface`. From the left: the **brand**, the text
`◈ HUSK` in `canvas` on `accent`, bold, one cell of padding on each side. From
the right: the **transient pills**, exactly the ones drawn today and in the
same priority (dragging a divider, hovering a divider, hovering a rail button,
Alt held), one at a time. Nothing else: the layout label, the plugin count, the
theme pill and the "Rail: …" key reminder are removed. The space between is
blank bar background.

The glyph: `◈` (U+25C8, white diamond containing black small diamond) replaces
`◆`. Why this one: an outer shape holding an inner one is what a husk is, a
shell around a kernel, which is the one thing the brand should say; it lives in
the same Unicode block (Geometric Shapes) and the same East Asian width class
(ambiguous) as the glyph it replaces, so it needs no font the current brand
does not already need and changes no width arithmetic; and it is distinct at a
glance from the filled `◆`, so the change is visible rather than cosmetic.

### The bottom bar

The last row, full width, background `surface`. Three parts:

1. **The layout label**, at the left, one cell of padding on each side, in
   `ink_muted`. Defined under "The layout label".
2. **The key hints**, after the label, separated from it and from each other by
   ` │ ` in `border`: the groups [Keybindings](keybindings.md) lists, in that
   order, with the docks group filtered as above. Key in `accent` bold,
   description in `ink_muted`.
3. **The theme name**, at the right edge, `✦ <name>` with one cell of padding
   on each side, in `accent_secondary` on `surface_elevated` (the pill the top
   bar showed, moved down).

The cells between the last hint and the theme name are blank bar background.

The label and the theme name are always drawn. The theme name is clipped to 24
cells with a `…` tail, so the two together never exceed 54 cells and always fit
the 84-cell minimum. When the three parts do not fit the width, hint groups are
dropped from the end of the hint list (quit first) until they do; a group is
dropped whole, never clipped. The `ipc: N` counter is removed and
`totalIPCEvents` leaves the model.

**The theme name** is the served palette's `name` (`protocol.Palette.Name`, the
value `theme.changed` carries). Theme copies the `theme.name` setting into it,
and that setting's default is the built-in palette's name
([Services](services.md), "Theme"), so with `theme.name` unset the slot shows
`CharmTone`. Should the name be empty (`theme.name` set to `""`) the slot
shows `theme`, so the click target below keeps a visible extent. With named
themes ([Services](services.md), "Named themes"; not landed) the name is
always the selected theme's name and `""` selects the built-in palette, so the
empty case then cannot arise; the `theme` fallback stays as a guard. The
frontend reads the name from `Frontend.Palette()`, never from Settings.

**Clicking the theme name** (a left click on any cell of the pill) opens the
Settings center on its Theme tab, the theme selector: it runs the host action
`husk.settings.open` with the argument `tab: theme` ([Settings center](settings-center.md),
"Opening and closing"; [Services](services.md), "The theme selector"; not
landed). Until that lands the click is consumed
(it reaches no surface and starts no drag) and does nothing; hover does not
change the pill. No other cell of the bottom bar is a click target.

### The layout label

The label names what the layout is showing. It is derived, every frame, from
the active placement and from which side surfaces are drawn (have a rect),
never from a stored flag:

| Situation | Label |
| :--- | :--- |
| Default placement, no side column drawn | `Workbench` |
| Default placement, sidebar drawn | `Workbench · Left` |
| Default placement, side panel drawn | `Workbench · Right` |
| Default placement, both drawn | `Workbench · Left + Right` |
| `stage` placement, sidebar drawn | `Stage · Left` |
| `stage` placement, sidebar not drawn | `Stage` |
| `full-canvas` placement | `Full canvas` |
| A modal is open (whatever is beneath) | `Modal` |

"Sidebar drawn" means `sidebar-list` has a rect; "side panel drawn" means
`side-panel` has a rect. With every surface laid out, the label is a readout of
the dock preferences and the fit rule: `Workbench · Left + Right` on a wide
terminal with both docks on, `Workbench · Left` when the panel is off or does
not fit. The word "Mode" and the forms `Full-Canvas Mode`,
`Stage-minus-Sidebar Mode` and `Modal Overlay Active` are gone. This is the
only sense in which the frontend has a "mode": a label. No key is read
differently because of it, and nothing may introduce normal/insert or similar
input modes under this name.

### Failure modes

| Condition | Behaviour |
| :--- | :--- |
| Terminal below 84x24 | Too-small screen; nothing of this spec is drawn |
| Side panel dock on, panel does not fit | Panel not drawn, no trace; `visible` omits it; label omits `Right`; footer omits the panel and cycle keys |
| Inner rect of an empty surface under 3 cells wide | Name not drawn; slot background only |
| Occupant leaves `live` | Engine vacates; the surface shows its name from that tick |
| Occupant claimed, no content yet | Blank canvas (slot background), not the name |
| Palette name empty | Theme slot shows `theme` |
| Theme name clicked before the Settings center exists | Click consumed, nothing happens |
| Theme name longer than 24 cells | Clipped with `…`; the label and the hints are unaffected |
| Bottom bar too narrow for every hint group | Groups dropped from the end until the rest fits; the label and the theme name stay |
| Rail shorter than the tiles need | Tiles that do not fit are not drawn, as today |

### Tests and goldens

Every golden in `cmd/husk/testdata` changes (`TestGoldenFrameEmptyWorkbench`,
`TestGoldenFrameOnePlugin`, `TestGoldenFramePaletteOpen`,
`TestGoldenFrameFullSlots`): the rail is wider, the bars are different, and the
empty workbench gains its side columns and surface names. They are regenerated
once with `go test ./cmd/husk -run TestGoldenFrame -update` in the commit that
lands the layout change, and the commit body says what changed in each. The
`--dump-frame` outputs change the same way. Tests the implementer adds or
rewrites, each named for the behaviour it pins:

- zero plugins lay out rail, sidebar bands, header, stage and (when wide
  enough) side panel, and every empty one shows its humanised name centred;
- the humanised name is computed from the id (a table-driven test over all
  twelve ids);
- a plugin that claims `sidebar-list` replaces the name with its content; its
  exit brings the name back in the tick the engine vacates the surface;
- the side panel is not drawn at 84 wide with the sidebar on, is drawn at 117,
  and the footer's docks group matches in both;
- the footer omits the sidebar key under `full-canvas`, and the panel and
  cycle keys under `stage`;
- the layout label for each row of the table above;
- the theme slot shows `CharmTone` with no override, the override with one, and
  `theme` for an empty name; a click on it does not reach any surface;
- the top bar is brand plus at most one transient pill, no plugin count, no
  layout label, no rail reminder;
- rail tiles are 5x3 whole cells with a one-row gap, and the Alt badge is
  centred and clipped to the tile;
- the tests from the reversed commit that asserted "zero plugins lay out no
  side rects" are inverted, not deleted.

## Level 2: Migration from today

All in `package main` under `cmd/husk`. `pkg/` is untouched.

| Today | After |
| :--- | :--- |
| `leftSideShown`, `rightSideShown` (`layout.go`): dock on and a plugin occupies | Dock on only; the panel's fit test stays in `placeSlots` |
| `activityRailWidth = 5` (`model.go`) | `7` |
| `renderActivityRailBox` (`render.go`): 3-cell pill with `▄▄▄`/`▀▀▀` caps | 5x3 whole-cell tile; `railButtons` stride 4 |
| `renderTopWorkbenchBar`: brand, `defaultLayoutLabel`, plugin count, theme pill, rail hint, transient pills | Brand `◈ HUSK` and transient pills only |
| `defaultLayoutLabel` | Becomes `layoutLabel` with the table above; called by the bottom bar |
| `renderBottomKeybindBar`: hints and `ipc: N` | Layout label, hints with group dropping, theme pill; a hit rect for the pill recorded for `input.go` |
| `footerHints` (`commands.go`): docks group from bound keys | Docks group filtered by placement and fit |
| `drawSurface`: content or blank canvas | Content, or the humanised name when the slot is a region slot and `surfaceOwner(slot) == ""` (never for `full-canvas` or `workspace-stage`; `drawModal` is unchanged) |
| `dropVanishedSides`, `leaveUndrawnSides` | Unchanged; a side column now vanishes only by its dock or the fit rule, never because a plugin left |
| `totalIPCEvents` | Removed |
| `mode_label_test.go` | Rewritten for the table above and the inverted layout rule; other `cmd/husk` tests that assume zero plugins lay out no side rects, or read the top-bar label (`focus_fallback_test.go`, `surface_owner_test.go`), are updated the same way |

A click on the theme pill is routed in `input.go` before surface hit-testing,
as the rail and the dividers are. The action it runs is `husk.settings.open`
with `tab: theme` ([Settings center](settings-center.md)); until that lands
the handler is a no-op that still consumes the click.

[Architecture](../architecture.md) ("Slots", "Surfaces and frames", "Minimum
size", "Input routing"), [Services](services.md) ("Settings", `theme.name`),
[Keybindings](keybindings.md) ("Rail items, palette and footer") and the
`husk.surfaces` example in [State](state.md) are amended with this spec.

## Decisions taken by this spec

1. **Humanised names are mechanical**: hyphens to spaces, first letter
   upper-cased, computed from the id. No table, so no drift when a surface is
   added; the slot explorer's hand-written titles stay what they are.
2. **The glyph is `◈`** (U+25C8), for the reasons under "The top bar": the
   meaning (a shell around a kernel), the same block and width class as `◆`,
   and visible distinctness.
3. **Rail tiles are 5x3 whole cells in a 7-cell rail** rather than a wider
   half-block pill. Whole cells make the shape independent of how a font draws
   `▄`/`▀` (some draw them full height, which is what made the pills read as
   tall); 5x3 is square at the 1:1.67 cell most monospace fonts have and 5:6 at
   1:2, against 3:4 (or 3:6 with full-height caps) today; the one-cell pads
   keep a tile off the sidebar's edge; the one-row gap keeps adjacent tiles
   apart now that there are no half-row caps. The rail costs two more columns;
   the panel's fit threshold moves from 115 to 117 cells with default widths.
4. **Collapse order is: side panel, then nothing, then the too-small screen.**
   This is today's fit rule stated as a rule rather than widened: the panel
   already yields when fewer than 72 cells are left right of the sidebar, and the sidebar's half-width cap
   already keeps the centre at 30 or more. No new thresholds.
5. **A collapsed panel has no collapsed form.** A stub column or marker would
   be chrome Husk invents, and the label and footer already say the panel is
   not there.
6. **Dock keys are shown only when they change the frame**, computed from
   placement and fit, including the side-panel key while the panel is hidden
   by preference but would fit. The simpler "show when drawn" would hide the
   key the user needs to bring the panel back.
7. **`thread-row-status` is not laid out.** It has never had a rect: its
   geometry (a column inside or beside the list? how wide?) is unspecified, a
   plugin claiming it today gets no size, and giving it one changes the
   sidebar list's width for every plugin there. That is a layout decision
   outside the four rulings; it is listed under Open questions.
8. **`app-overlay` is not a region slot.** It is the overlay layer, with a
   whole-frame rect, specified in the
   [Cell-buffer compositor](cell-buffer-compositor.md) spec ("Overlay
   layer"). The empty-surface name rule applies to region slots only, so an
   unoccupied `app-overlay` draws nothing.
9. **Placement surfaces are never "empty".** A rail item's plugin occupies the
   surface it opens or, when it did not claim it, receives its keys, so the
   name is not drawn there; before content arrives
   the surface is a blank canvas like any occupied one.
10. **The layout label drops the word "Mode"** and the three placement
    phrases, and a modal shows `Modal` alone (as the top bar showed one phrase
    for an open modal today). Husk is mode-free; the label is a readout.
11. **The theme slot reads `Palette.Name`**, not the `theme.name` setting
    directly, because Theme already folds the setting into the served palette
    and the frontend has no Settings read seam. `CharmTone` when unset (the
    setting's default); an empty name shows `theme`.
12. **Hint groups are dropped whole from the end**, never clipped, so a
    half-shown key never misleads; the label and theme name always draw.

## Open questions

1. `thread-row-status`: does it get a rect (a narrow column to the right of
   `sidebar-list`, inside the sidebar width), or is it retired from the
   surface list? Either is a later spec; this one leaves it undrawn.
2. Resolved: the theme pill runs `husk.settings.open` with `tab: theme`, which
   opens the Settings center on its Theme tab, the theme selector
   ([Settings center](settings-center.md); [Services](services.md), "The theme
   selector").
