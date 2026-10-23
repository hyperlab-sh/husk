---
title: "Built-in themes"
description: "The nine themes Husk ships as a built-in layer, the rules that mapped each source palette onto Husk's roles, the contrast adjustments, and the exact themes document the implementer copies."
---

# Built-in themes

Status: specified, not landed. Scope: `pkg/theme` (the built-in `themes`
layer, its tests), the Theme Service ([Services](services.md), "Named
themes": the layer and the default), and the name the frontend shows
([Workbench chrome](chrome.md), "The bottom bar"). The rules for which role
the chrome may draw with are in [Workbench chrome](chrome.md), "Colour
discipline"; this spec only supplies the colours. Wire: the default palette
name changes, `theme.list` returns nine `built-in` entries, and one theme
(`base16`) puts ANSI indices in palette values (see "Role values").

## Level 0: What and why

Husk has one palette today, named `CharmTone`, and the chrome draws with
every role in it: the footer keys are accent-coloured, the Alt badges are
yellow, the drag pill is green, the toast border takes the toast's colour.
The result is a frame that uses too many colours, several of them vibrant.
Two things fix that, and they are separate:

1. **Fewer colours in the frame**, whatever the theme. That is a drawing
   rule, not a palette: [Workbench chrome](chrome.md), "Colour discipline".
2. **A calm default and a choice of known themes.** Husk ships nine themes as
   a built-in layer: the `huh` form library's four themes and the three Rosé
   Pine variants, with the light counterparts of Charm and Catppuccin. The
   `huh` Charm theme is the default.

The themes are taken from two files, read as data (their literal colour
values), not run:

- `charmbracelet/huh`, `theme.go`
  (<https://github.com/charmbracelet/huh/blob/main/theme.go>): `ThemeCharm`,
  `ThemeDracula`, `ThemeBase16`, `ThemeCatppuccin`. MIT licence, Copyright
  (c) 2023–2026 Charm. `ThemeCatppuccin` takes its values from
  `github.com/catppuccin/go` (`mocha.go`, `latte.go`; MIT licence, Catppuccin),
  so those files are the literal source for the Catppuccin values.
- `drluckyspin/rose-pine-glow`, `scripts/build-styles.py`
  (<https://github.com/drluckyspin/rose-pine-glow>): the `PALETTES` table
  for `rose-pine`, `rose-pine-moon` and `rose-pine-dawn`. MIT licence,
  Copyright (c) 2026 Todd Papaioannou. The table carries twelve colours per
  variant; the three `highlight` neutrals this spec also uses come from the
  published Rosé Pine palette (<https://rosepinetheme.com/palette>; MIT
  licence, Rosé Pine).

Two roles the `huh` Charm theme has no colour for (`warning`, `info`) and two
the `huh` Dracula theme lacks (`info`, a second accent) are filled from the
vendors' own published palettes: `charmbracelet/x/exp/charmtone` (MIT, Charm;
the module Husk's palette uses today) and the Dracula specification
(<https://draculatheme.com/contribute>; MIT, Zeno Rocha). Every such fill is
marked in the tables.

Colour values are facts and are not what the licences protect; they are
listed so the provenance of every number in this document is on record.

## Level 1: Contract

### The built-in layer

The nine themes are a `themes` document ([Services](services.md), "Named
themes") embedded in the binary beside the Theme Service, in `pkg/theme`,
exactly as the `husk` keymap is embedded beside the Keybinding Service. It is
the lowest layer, named `built-in`, below `distribution`:

| Layer | Source |
| :--- | :--- |
| `built-in` | the `themes` document embedded in the binary (`pkg/theme`): the nine themes below |
| `distribution`, `user`, `profile`, `instance` | `Store.Layers("themes", profile)`: the unmerged layer documents, as today |

The rules of "Named themes" apply to the built-in layer with these
consequences:

1. **No reserved name.** Rule 1's reserved built-in name and its
   `reserved_theme_name` problem are retired: a theme named like a built-in
   one in a higher layer replaces it whole (rule 3), and `theme.list` reports
   that layer as its `source`. A distribution that wants its own `dracula`
   defines it; a user who breaks `charm` gets the usual `low_contrast` error
   and the previous palette keeps serving.
2. **Built-in definitions are complete.** Each sets all nineteen roles,
   `slot_background` included, so nothing in a built-in theme is inherited.
3. **`charm` is the default and the base.** `theme.name` defaults to `charm`;
   the empty string and `charm` both select it. A theme in any other layer
   that omits roles inherits them from `charm` (rule 2's "keeps the built-in
   colour" now means `charm`'s colour), and the build starts from `charm`
   (step 1 of the build).
4. **`theme.list` order.** The nine built-in names first, in the order of
   the table below, each with the layer that won it as `source`
   (`built-in` unless a higher layer redefined the name); then every other
   theme sorted by name. The frontend's theme selector shows this order.
5. **The served palette's `name`** is the selected theme's key, so with
   `theme.name` unset `theme.get`, `theme.changed` and the bottom bar carry
   `charm`.

| Name | Source theme | Light or dark |
| :--- | :--- | :--- |
| `charm` | `huh` `ThemeCharm`, dark side | dark |
| `charm-light` | `huh` `ThemeCharm`, light side | light |
| `dracula` | `huh` `ThemeDracula` | dark |
| `base16` | `huh` `ThemeBase16` | the terminal's |
| `catppuccin-mocha` | `huh` `ThemeCatppuccin`, dark (`catppuccin/go` Mocha) | dark |
| `catppuccin-latte` | `huh` `ThemeCatppuccin`, light (`catppuccin/go` Latte) | light |
| `rose-pine` | `rose-pine-glow` `rose-pine` | dark |
| `rose-pine-moon` | `rose-pine-glow` `rose-pine-moon` | dark |
| `rose-pine-dawn` | `rose-pine-glow` `rose-pine-dawn` | light |

Names are lower-case and hyphenated, as the table shows, and match rule 1's
grammar. `rose-pine-glow` also carries a `rose-pine-moon-dark` palette (Moon's
colours on the main base); it is a pairing for one Markdown renderer, not a
Rosé Pine variant, and is not shipped.

**Light themes are themes, not a mode.** `charm-light`, `catppuccin-latte`
and `rose-pine-dawn` are ordinary named themes: a user selects one like any
other, and nothing else changes. Husk does not read the terminal's background
colour in v1 and has no light/dark switch; see Open questions.

### Role values

A palette role's value is one of:

- `#rrggbb`: a 24-bit colour, case-insensitive on input, emitted lower case.
  This is the only form the Named themes rules accept from a `distribution`,
  `user`, `profile` or `instance` theme or from a `theme.palette.<role>`
  override, as today (`invalid_color` otherwise), because the contrast floor
  cannot be computed for anything else.
- An **ANSI index**: the decimal string `"0"` to `"15"`, no sign, no leading
  zero. Only the built-in layer holds indices, and only `base16` uses them.
  An index means "the terminal's colour N": the terminal's own palette shows
  through, which is the point of that theme.

What an index does where a hex value is expected:

- On the wire (`theme.get`, `theme.changed`) the value is the index string,
  unchanged. A plugin must accept both forms: a value that does not start
  with `#` is an index. The host emits an index only while `base16` is the
  selected theme; every other theme is hex throughout. The `Palette` wire
  type's documentation and the `theme_changed` row of [Protocol](protocol.md)
  are amended to say so; the field types do not change.
- `SlotBackground` returns an index unchanged (it already does for anything
  it cannot parse), so `slot_background` equals `canvas` and the slot panel
  is not painted differently from the canvas. `base16` sets
  `slot_background` to `"0"` explicitly.
- `CheckContrast` cannot measure a pair with an index on either side. For a
  built-in theme such a pair is **unmeasurable** and skipped, not a
  violation; `base16` is therefore the one built-in theme the contrast
  floor does not vouch for, and the spec says so where its table is. No new
  problem code: user themes cannot contain indices.
- The compositor's popup shadow blends the colours of the cells beneath a
  popup toward black ([Cell-buffer compositor](cell-buffer-compositor.md)). A
  cell whose foreground or background is an index cannot be blended and is
  left as it is; under `base16` the shadow is therefore absent. The popup
  itself is unaffected.
- The frontend hands every value to `lipgloss.Color`, which accepts both
  forms; nothing in `cmd/husk` parses a colour.

### How the sources were mapped

Husk's roles are the nineteen in `pkg/theme.Palette`: `canvas`, `surface`,
`surface_elevated`, `surface_selected`, `surface_hover`, `slot_background`,
`border`, `border_subtle`, `border_focused`, `ink`, `ink_muted`,
`ink_subtle`, `accent`, `accent_secondary`, `accent_soft`, `success`,
`warning`, `danger`, `info`. The sources are form-library and
Markdown-renderer palettes, so none of them names these roles. One set of
rules mapped every theme, so a reader can predict a value and the
implementer can check one:

1. **`canvas`** is the source's background. The `huh` Charm and Base16
   themes paint no background (they sit on the terminal's); Charm takes its
   canvas from the xterm-256 grey ramp the theme itself uses for its greys,
   Base16 takes the terminal's colour 0.
2. **`surface`** (the bars and the rail) equals `canvas`, as today's palette
   has it. No source distinguishes a bar from the page.
3. **`slot_background`** is derived from the canvas by `SlotBackground` (the
   fixed Lab lightness shift, [Services](services.md), build step 4) unless
   the source has a panel tint of its own at least one ladder step from the
   canvas: Rosé Pine main and Moon use their `highlight-low`. Dawn's
   `highlight-low` is under a step from its base, so Dawn derives.
4. **`surface_elevated`** and **`surface_hover`** (one value: hover rows and
   raised pills) are the source's first raised neutral that keeps the ladder
   (below). Where the source's next neutral is too close to the slot or too
   far for the text roles, the value is derived by applying the slot shift
   twice to the canvas (Dracula, Catppuccin Latte).
5. **`surface_selected`** is the source's selection or highlight colour
   where it has one (Dracula `selection`, Rosé Pine `highlight-high`,
   Catppuccin `surface1` for Mocha and `surface0` for Latte), else the next
   grey of the ramp.
6. **`border`** is the source's border or muted neutral; **`border_subtle`**
   its darkest raised neutral (lightest, on a light theme), never
   contrast-checked, so it may be faint; **`border_focused`** is the
   source's second accent, a calmer colour than `accent`: a whole frame edge
   in the accent would shout, and the selected or focused mark inside the
   frame already carries the accent.
7. **`ink`**, **`ink_muted`**, **`ink_subtle`** are the source's text,
   secondary text and tertiary text (`text`/`subtext1`/`subtext0`,
   `text`/`subtle`/`muted`, `foreground`/`comment`/`comment`, the grey
   ramp for Charm).
8. **`accent`** is the colour `huh` paints its focused control with (the
   focused button's background: Charm fuchsia, Dracula purple, Base16 `5`)
   or the palette's signature colour where the source is not a form
   (Catppuccin mauve, Rosé Pine rose). **`accent_secondary`** is the title
   colour (Charm indigo, Catppuccin pink as `huh`'s selector, Rosé Pine
   iris, Base16 `6`). **`accent_soft`** is a subdued member of the same
   family for plugins' fills; it is never contrast-checked.
9. **`success`**, **`warning`**, **`danger`**, **`info`** are the source's
   green, yellow, red and blue by the source's own terminal mapping where it
   publishes one (Rosé Pine: pine, gold, love, foam), else by hue.
10. Where the source has no colour for a role, the fill comes from the
    vendor's own published palette (Level 0) and the table says so.

**The ladder.** For every hex theme the surfaces step away from the canvas
in one direction (lighter on a dark canvas, darker on a light one):
`canvas` ≤ `surface`, then `slot_background`, `surface_elevated` (=
`surface_hover`), `surface_selected`, each at least **1.08:1** from the
previous one and `surface_selected` at least 1.08:1 from
`slot_background`, so pills, hover rows and selections stay visible on the
slot panel. These are the pairs `TestSurfaceLadderSeparation` and
`TestDefaultPaletteSurfacesAreOrdered` check for today's palette; both run
over all eight hex themes, with the direction taken from the canvas's
lightness. `base16` is skipped (unmeasurable).

**The contrast floor.** Every hex theme passes `CheckContrast` over
`ContrastPairs()` unchanged: text roles at 4.5:1 on every surface they are
drawn on, borders and non-text indicators at 3:1
([Services](services.md), build step 5). Where a source value fails a pair,
it was **adjusted** by one mechanical rule:

> The failing role is moved along its CIE Lab lightness axis, keeping `a`
> and `b` (so its hue does not change), away from the canvas (lighter on a
> dark theme, darker on a light one), in steps of 0.25 L\*, until every pair
> it takes part in passes; the result is clamped to sRGB. Surfaces are never
> moved: the ladder is fixed first, the chromatic roles, inks and borders
> are fitted to it.

Each theme's table marks every adjusted role with the source value it came
from, so the list of pairs that could not pass unchanged is the list of
marked rows. Some adjustments are small (Mocha's mauve moves one step);
some are not (a mid-tone yellow on a light theme becomes a brown): that is
what 4.5:1 on a grey selection costs, and the alternative, a lower floor
for some themes, would make the floor meaningless. The adjusted values are
what the document holds; the implementer copies them and does not rerun the
adjustment.

### The themes

Each table lists every role with its value and where it came from.
"derived" means rule 3 or 4; "adjusted from X" means the contrast rule moved
X to the value shown; everything else is the source's literal value.

#### `charm` (default)

`huh` `ThemeCharm`, dark side. The theme itself names only greys from the
xterm-256 ramp (`235`, `237`, `238`, `243`, `248`, `252`) and five
chromatic colours (indigo, cream, fuchsia, green, red). The canvas is grey
`234` (`#1c1c1c`), one step below the darkest grey the theme names, because
with `235` as the canvas the raised steps land on `237` and `238`, where the
theme's fuchsia, green and red all fall under 4.5:1; one step down, the
ladder `234`/`236`/`237` holds every source colour but the red, which moves
by one step. The result is close to today's `Pepper` (`#201F26`) canvas.

| Role | Value | From |
| :--- | :--- | :--- |
| `canvas` | `#1c1c1c` | grey 234 (rule 1) |
| `surface` | `#1c1c1c` | = canvas |
| `surface_elevated` | `#303030` | grey 236 |
| `surface_selected` | `#3a3a3a` | grey 237 (`huh`'s blurred-button background) |
| `surface_hover` | `#303030` | = elevated |
| `slot_background` | `#232323` | derived |
| `border` | `#797979` | adjusted from grey 243 `#767676` (`huh`'s description grey; 2.9:1 on elevated) |
| `border_subtle` | `#444444` | grey 238 (`huh`'s border) |
| `border_focused` | `#7571f9` | indigo (dark side) |
| `ink` | `#d0d0d0` | grey 252 (`huh`'s text) |
| `ink_muted` | `#a8a8a8` | grey 248 (`huh`'s placeholder grey) |
| `ink_subtle` | `#979797` | adjusted from grey 243 `#767676` (3.3:1 on elevated) |
| `accent` | `#f780e2` | fuchsia (the focused button's background and the selector) |
| `accent_secondary` | `#9c98ff` | adjusted from indigo `#7571f9` (2.97:1 on selected) |
| `accent_soft` | `#5a56e0` | indigo, light side |
| `success` | `#02bf87` | green (dark side) |
| `warning` | `#f5ef34` | `charmtone` Mustard: `huh` Charm has no yellow |
| `danger` | `#fa6387` | adjusted from red `#ed567a` (3.9:1 on elevated) |
| `info` | `#00a4ff` | `charmtone` Malibu: `huh` Charm has no blue |

Cream (`#FFFDF5`), the fifth chromatic value, is the light theme's canvas.

#### `charm-light`

`huh` `ThemeCharm`, light side: cream canvas, grey `235` text, the light
indigo, the light green and red. Mid-tone colours on a light theme fail
4.5:1 on a grey selection, so every chromatic role except `accent_soft`
moved, the yellow to a brown and the fuchsia to a deep magenta.

| Role | Value | From |
| :--- | :--- | :--- |
| `canvas` | `#fffdf5` | cream |
| `surface` | `#fffdf5` | = canvas |
| `surface_elevated` | `#e4e4e4` | grey 254 |
| `surface_selected` | `#dadada` | grey 253 |
| `surface_hover` | `#e4e4e4` | = elevated |
| `slot_background` | `#f5f3eb` | derived |
| `border` | `#828282` | adjusted from grey 248 `#a8a8a8` (1.9:1 on elevated) |
| `border_subtle` | `#d0d0d0` | grey 252 (`huh`'s blurred-button background, light side) |
| `border_focused` | `#5a56e0` | indigo (light side) |
| `ink` | `#262626` | grey 235 (`huh`'s text, light side) |
| `ink_muted` | `#4e4e4e` | grey 239 |
| `ink_subtle` | `#626262` | grey 241 |
| `accent` | `#a32c8e` | adjusted from fuchsia `#f780e2` (1.6:1 on selected) |
| `accent_secondary` | `#4f4bd5` | adjusted from indigo `#5a56e0` (3.9:1 on selected) |
| `accent_soft` | `#7571f9` | indigo, dark side |
| `success` | `#006f39` | adjusted from green `#02ba84` (1.8:1 on selected) |
| `warning` | `#686200` | adjusted from `charmtone` Mustard `#f5ef34` (1.05:1 on elevated) |
| `danger` | `#ca0d3d` | adjusted from red `#ff4672` (2.6:1 on elevated) |
| `info` | `#0065c0` | adjusted from `charmtone` Malibu `#00a4ff` (2.1:1 on elevated) |

The three inks sit on greys `235`, `239` and `241`: on a light canvas the
4.5:1 floor on the selection pins `ink_muted` under L\* 42 and the floor on
the raised step pins `ink_subtle` under L\* 44, so the inks are closer
together than on the dark theme by construction.

#### `dracula`

`huh` `ThemeDracula`: eight literal colours (background, selection,
foreground, comment, green, purple, red, yellow). The theme has one raised
neutral, `selection`, which is the selected surface; the raised step is
derived. The comment colour serves as border, muted ink and soft accent; as
text it is 3:1 on the background, so the two inks that come from it are
lightened. Dracula has no blue and no second accent in `huh`'s file: `info`
and `accent_secondary` are the Dracula specification's cyan and pink.

| Role | Value | From |
| :--- | :--- | :--- |
| `canvas` | `#282a36` | background |
| `surface` | `#282a36` | = canvas |
| `surface_elevated` | `#383a46` | derived (slot shift twice) |
| `surface_selected` | `#44475a` | selection |
| `surface_hover` | `#383a46` | = elevated |
| `slot_background` | `#30323e` | derived |
| `border` | `#7383b5` | adjusted from comment `#6272a4` (2.4:1 on elevated) |
| `border_subtle` | `#44475a` | selection |
| `border_focused` | `#bd93f9` | purple |
| `ink` | `#f8f8f2` | foreground |
| `ink_muted` | `#a5b5e7` | adjusted from comment `#6272a4` (1.9:1 on selected) |
| `ink_subtle` | `#93a3d5` | adjusted from comment `#6272a4` (2.4:1 on elevated) |
| `accent` | `#cea4ff` | adjusted from purple `#bd93f9` (3.8:1 on selected) |
| `accent_secondary` | `#ff91de` | adjusted from Dracula specification pink `#ff79c6` (3.8:1 on selected) |
| `accent_soft` | `#6272a4` | comment |
| `success` | `#50fa7b` | green |
| `warning` | `#f1fa8c` | yellow |
| `danger` | `#ff7c7c` | adjusted from red `#ff5555` (3.6:1 on elevated) |
| `info` | `#8be9fd` | Dracula specification cyan: `huh` Dracula has no blue |

#### `base16`

`huh` `ThemeBase16`: every colour is an ANSI index, so the terminal's own
palette decides what the user sees. Husk keeps that: the values below are
indices (see "Role values"), and no pair of this theme is contrast-checked.
Indices `0` to `8` and `15` are used, never `9` to `14`: in Base16 terminal
schemes the bright colours repeat the normal ones by design, so a role on
`12` would not be distinct from one on `4`. Nineteen roles on ten slots
means some share: `surface_elevated` stays on the canvas (`0`), so the
raised step is invisible and only hover and selection (`8`) stand out; `4`
is both `info` and `accent_soft`.

| Role | Value | From |
| :--- | :--- | :--- |
| `canvas` | `"0"` | terminal background (Base16 `base00`) |
| `surface` | `"0"` | = canvas |
| `surface_elevated` | `"0"` | = canvas (no third neutral) |
| `surface_selected` | `"8"` | `huh`'s border and description colour (`base03`) |
| `surface_hover` | `"8"` | = selected |
| `slot_background` | `"0"` | explicit: an index derives to itself |
| `border` | `"8"` | `huh`'s border |
| `border_subtle` | `"8"` | = border |
| `border_focused` | `"5"` | `huh`'s focused-button background |
| `ink` | `"15"` | bright foreground (`base07`) |
| `ink_muted` | `"7"` | `huh`'s option text |
| `ink_subtle` | `"8"` | `huh`'s description |
| `accent` | `"5"` | `huh`'s focused-button background |
| `accent_secondary` | `"6"` | `huh`'s title |
| `accent_soft` | `"4"` | blue |
| `success` | `"2"` | `huh`'s selected option |
| `warning` | `"3"` | `huh`'s selector |
| `danger` | `"1"` | red (`huh` uses the bright `9`; see above) |
| `info` | `"4"` | blue |

#### `catppuccin-mocha`

`catppuccin/go` Mocha, the dark flavour `huh`'s `ThemeCatppuccin` selects.
Catppuccin's own ladder (`base`, `surface0`, `surface1`) is Husk's; the slot
is derived because `mantle` and `crust` go the wrong way (darker than
`base`). Only the mauve moves, by one step.

| Role | Value | From |
| :--- | :--- | :--- |
| `canvas` | `#1e1e2e` | base |
| `surface` | `#1e1e2e` | = canvas |
| `surface_elevated` | `#313244` | surface0 |
| `surface_selected` | `#45475a` | surface1 |
| `surface_hover` | `#313244` | = elevated |
| `slot_background` | `#252536` | derived |
| `border` | `#7f849c` | overlay1 (`huh` uses subtext1; overlay1 is the darkest neutral at 3:1 on surface0) |
| `border_subtle` | `#585b70` | surface2 |
| `border_focused` | `#b4befe` | lavender (Catppuccin's active-border colour) |
| `ink` | `#cdd6f4` | text |
| `ink_muted` | `#bac2de` | subtext1 |
| `ink_subtle` | `#a6adc8` | subtext0 (`huh`'s description) |
| `accent` | `#cca7f8` | adjusted from mauve `#cba6f7` (4.49:1 on selected) |
| `accent_secondary` | `#f5c2e7` | pink (`huh`'s selector and focused button) |
| `accent_soft` | `#f2cdcd` | flamingo |
| `success` | `#a6e3a1` | green |
| `warning` | `#f9e2af` | yellow |
| `danger` | `#f38ba8` | red |
| `info` | `#89b4fa` | blue |

#### `catppuccin-latte`

`catppuccin/go` Latte, the light flavour. Latte's `surface0` and `surface1`
are too dark for three distinct inks at 4.5:1 (text and subtext1 collapse
onto one value on `surface1`), so the raised step is derived and `surface0`
is the selection. Every chromatic role moved: Latte's accents are mid-tone.

| Role | Value | From |
| :--- | :--- | :--- |
| `canvas` | `#eff1f5` | base |
| `surface` | `#eff1f5` | = canvas |
| `surface_elevated` | `#dbdde1` | derived (slot shift twice) |
| `surface_selected` | `#ccd0da` | surface0 |
| `surface_hover` | `#dbdde1` | = elevated |
| `slot_background` | `#e5e7eb` | derived |
| `border` | `#6c6f85` | subtext0 (the lightest neutral at 3:1 on elevated) |
| `border_subtle` | `#acb0be` | surface2 |
| `border_focused` | `#5e73e9` | adjusted from lavender `#7287fd` (2.1:1 on elevated) |
| `ink` | `#4c4f69` | text |
| `ink_muted` | `#555870` | adjusted from subtext1 `#5c5f77` (4.1:1 on selected) |
| `ink_subtle` | `#5d6076` | adjusted from subtext0 `#6c6f85` (3.7:1 on elevated) |
| `accent` | `#7324da` | adjusted from mauve `#8839ef` (3.5:1 on selected) |
| `accent_secondary` | `#9c287d` | adjusted from pink `#ea76cb` (1.7:1 on selected) |
| `accent_soft` | `#dd7878` | flamingo |
| `success` | `#046800` | adjusted from green `#40a02b` (2.2:1 on selected) |
| `warning` | `#934200` | adjusted from yellow `#df8e1d` (1.7:1 on selected) |
| `danger` | `#c5002c` | adjusted from red `#d20f39` (4.0:1 on elevated) |
| `info` | `#0055e4` | adjusted from blue `#1e66f5` (3.6:1 on elevated) |

#### `rose-pine`

`rose-pine-glow` `rose-pine` (main), with `highlight-low`, `highlight-med`
and `highlight-high` from the published palette. The source's `surface` and
`overlay` are within 1.07:1 of each other and of `base`, under the ladder
step, so the slot is `highlight-low` and the raised steps are the two upper
highlights. Rosé Pine's `muted` and `subtle` are designed low-contrast and
both lighten. `pine`, the palette's green by its own terminal mapping, is a
dark teal and lightens a long way; it lands beside `foam`, so `success` and
`info` are close in this theme (the toast glyph and title tell them apart,
and `info` is drawn far less often). The alternative, `iris` as `info`,
would contradict the palette's own mapping.

| Role | Value | From |
| :--- | :--- | :--- |
| `canvas` | `#191724` | base |
| `surface` | `#191724` | = canvas |
| `surface_elevated` | `#403d52` | highlight-med |
| `surface_selected` | `#524f67` | highlight-high |
| `surface_hover` | `#403d52` | = elevated |
| `slot_background` | `#21202e` | highlight-low |
| `border` | `#8b87a3` | adjusted from muted `#6e6a86` (2.0:1 on elevated) |
| `border_subtle` | `#26233a` | overlay |
| `border_focused` | `#c4a7e7` | iris |
| `ink` | `#e0def4` | text |
| `ink_muted` | `#c5c1df` | adjusted from subtle `#908caa` (2.4:1 on selected) |
| `ink_subtle` | `#aba7c3` | adjusted from muted `#6e6a86` (2.0:1 on elevated) |
| `accent` | `#ebbcba` | rose |
| `accent_secondary` | `#d5b8f8` | adjusted from iris `#c4a7e7` (3.75:1 on selected) |
| `accent_soft` | `#31748f` | pine |
| `success` | `#8bcee9` | adjusted from pine `#31748f` (1.5:1 on selected) |
| `warning` | `#f6c177` | gold |
| `danger` | `#ff83a6` | adjusted from love `#eb6f92` (3.6:1 on elevated) |
| `info` | `#9ccfd8` | foam |

#### `rose-pine-moon`

`rose-pine-glow` `rose-pine-moon`, with the Moon highlights. The same shape
as main; Moon's rose is darker than main's and moves.

| Role | Value | From |
| :--- | :--- | :--- |
| `canvas` | `#232136` | base |
| `surface` | `#232136` | = canvas |
| `surface_elevated` | `#44415a` | highlight-med |
| `surface_selected` | `#56526e` | highlight-high |
| `surface_hover` | `#44415a` | = elevated |
| `slot_background` | `#2a283e` | highlight-low |
| `border` | `#908ca8` | adjusted from muted `#6e6a86` (1.9:1 on elevated) |
| `border_subtle` | `#393552` | overlay |
| `border_focused` | `#c4a7e7` | iris |
| `ink` | `#e0def4` | text |
| `ink_muted` | `#cbc7e5` | adjusted from subtle `#908caa` (2.3:1 on selected) |
| `ink_subtle` | `#b1adc9` | adjusted from muted `#6e6a86` (1.9:1 on elevated) |
| `accent` | `#ffb8b5` | adjusted from rose `#ea9a97` (3.4:1 on selected) |
| `accent_secondary` | `#dbbefe` | adjusted from iris `#c4a7e7` (3.55:1 on selected) |
| `accent_soft` | `#3e8fb0` | pine |
| `success` | `#84d5f6` | adjusted from pine `#3e8fb0` (2.0:1 on selected) |
| `warning` | `#f6c177` | gold |
| `danger` | `#ff8db0` | adjusted from love `#eb6f92` (3.4:1 on elevated) |
| `info` | `#9ccfd8` | foam |

#### `rose-pine-dawn`

`rose-pine-glow` `rose-pine-dawn`, with the Dawn highlights. Dawn's
`surface` (`#fffaf3`) is lighter than its base and its `highlight-low` is
under a ladder step, so the slot is derived and the raised steps are
`highlight-med` and `highlight-high`. Dawn is the theme that departs
furthest from its source: all six chromatic colours are mid-tone pastels
that fail 4.5:1 on the selection, and the two lower inks fail on their own
base. As in main, `success` (from pine) and `info` (from foam) end close.

| Role | Value | From |
| :--- | :--- | :--- |
| `canvas` | `#faf4ed` | base |
| `surface` | `#faf4ed` | = canvas |
| `surface_elevated` | `#dfdad9` | highlight-med |
| `surface_selected` | `#cecacd` | highlight-high |
| `surface_hover` | `#dfdad9` | = elevated |
| `slot_background` | `#f0eae3` | derived (highlight-low `#f4ede8` is 1.06:1 from base) |
| `border` | `#7e798b` | adjusted from muted `#9893a5` (2.2:1 on elevated) |
| `border_subtle` | `#f2e9e1` | overlay |
| `border_focused` | `#8973a2` | adjusted from iris `#907aa9` (2.7:1 on elevated) |
| `ink` | `#464261` | text |
| `ink_muted` | `#565270` | adjusted from subtle `#797593` (2.7:1 on selected) |
| `ink_subtle` | `#635e70` | adjusted from muted `#9893a5` (2.2:1 on elevated) |
| `accent` | `#903b37` | adjusted from rose `#d7827e` (1.75:1 on selected) |
| `accent_secondary` | `#624c7b` | adjusted from iris `#907aa9` (2.3:1 on selected) |
| `accent_soft` | `#286983` | pine |
| `success` | `#1b5c76` | adjusted from pine `#286983` (3.8:1 on selected) |
| `warning` | `#8d4000` | adjusted from gold `#ea9d34` (1.4:1 on selected) |
| `danger` | `#97465d` | adjusted from love `#b4637a` (3.0:1 on elevated) |
| `info` | `#2a6873` | adjusted from foam `#56949f` (2.5:1 on elevated) |

### The document

This is the built-in `themes` document, byte for byte what `pkg/theme`
embeds. It follows the Named themes file shape; a built-in theme sets every
role and no `name` (its name is its key).

```json
{"version": 1, "themes": {
  "charm": {"palette": {
    "canvas": "#1c1c1c", "surface": "#1c1c1c", "surface_elevated": "#303030", "surface_selected": "#3a3a3a", "surface_hover": "#303030", "slot_background": "#232323",
    "border": "#797979", "border_subtle": "#444444", "border_focused": "#7571f9",
    "ink": "#d0d0d0", "ink_muted": "#a8a8a8", "ink_subtle": "#979797",
    "accent": "#f780e2", "accent_secondary": "#9c98ff", "accent_soft": "#5a56e0",
    "success": "#02bf87", "warning": "#f5ef34", "danger": "#fa6387", "info": "#00a4ff"}},
  "charm-light": {"palette": {
    "canvas": "#fffdf5", "surface": "#fffdf5", "surface_elevated": "#e4e4e4", "surface_selected": "#dadada", "surface_hover": "#e4e4e4", "slot_background": "#f5f3eb",
    "border": "#828282", "border_subtle": "#d0d0d0", "border_focused": "#5a56e0",
    "ink": "#262626", "ink_muted": "#4e4e4e", "ink_subtle": "#626262",
    "accent": "#a32c8e", "accent_secondary": "#4f4bd5", "accent_soft": "#7571f9",
    "success": "#006f39", "warning": "#686200", "danger": "#ca0d3d", "info": "#0065c0"}},
  "dracula": {"palette": {
    "canvas": "#282a36", "surface": "#282a36", "surface_elevated": "#383a46", "surface_selected": "#44475a", "surface_hover": "#383a46", "slot_background": "#30323e",
    "border": "#7383b5", "border_subtle": "#44475a", "border_focused": "#bd93f9",
    "ink": "#f8f8f2", "ink_muted": "#a5b5e7", "ink_subtle": "#93a3d5",
    "accent": "#cea4ff", "accent_secondary": "#ff91de", "accent_soft": "#6272a4",
    "success": "#50fa7b", "warning": "#f1fa8c", "danger": "#ff7c7c", "info": "#8be9fd"}},
  "base16": {"palette": {
    "canvas": "0", "surface": "0", "surface_elevated": "0", "surface_selected": "8", "surface_hover": "8", "slot_background": "0",
    "border": "8", "border_subtle": "8", "border_focused": "5",
    "ink": "15", "ink_muted": "7", "ink_subtle": "8",
    "accent": "5", "accent_secondary": "6", "accent_soft": "4",
    "success": "2", "warning": "3", "danger": "1", "info": "4"}},
  "catppuccin-mocha": {"palette": {
    "canvas": "#1e1e2e", "surface": "#1e1e2e", "surface_elevated": "#313244", "surface_selected": "#45475a", "surface_hover": "#313244", "slot_background": "#252536",
    "border": "#7f849c", "border_subtle": "#585b70", "border_focused": "#b4befe",
    "ink": "#cdd6f4", "ink_muted": "#bac2de", "ink_subtle": "#a6adc8",
    "accent": "#cca7f8", "accent_secondary": "#f5c2e7", "accent_soft": "#f2cdcd",
    "success": "#a6e3a1", "warning": "#f9e2af", "danger": "#f38ba8", "info": "#89b4fa"}},
  "catppuccin-latte": {"palette": {
    "canvas": "#eff1f5", "surface": "#eff1f5", "surface_elevated": "#dbdde1", "surface_selected": "#ccd0da", "surface_hover": "#dbdde1", "slot_background": "#e5e7eb",
    "border": "#6c6f85", "border_subtle": "#acb0be", "border_focused": "#5e73e9",
    "ink": "#4c4f69", "ink_muted": "#555870", "ink_subtle": "#5d6076",
    "accent": "#7324da", "accent_secondary": "#9c287d", "accent_soft": "#dd7878",
    "success": "#046800", "warning": "#934200", "danger": "#c5002c", "info": "#0055e4"}},
  "rose-pine": {"palette": {
    "canvas": "#191724", "surface": "#191724", "surface_elevated": "#403d52", "surface_selected": "#524f67", "surface_hover": "#403d52", "slot_background": "#21202e",
    "border": "#8b87a3", "border_subtle": "#26233a", "border_focused": "#c4a7e7",
    "ink": "#e0def4", "ink_muted": "#c5c1df", "ink_subtle": "#aba7c3",
    "accent": "#ebbcba", "accent_secondary": "#d5b8f8", "accent_soft": "#31748f",
    "success": "#8bcee9", "warning": "#f6c177", "danger": "#ff83a6", "info": "#9ccfd8"}},
  "rose-pine-moon": {"palette": {
    "canvas": "#232136", "surface": "#232136", "surface_elevated": "#44415a", "surface_selected": "#56526e", "surface_hover": "#44415a", "slot_background": "#2a283e",
    "border": "#908ca8", "border_subtle": "#393552", "border_focused": "#c4a7e7",
    "ink": "#e0def4", "ink_muted": "#cbc7e5", "ink_subtle": "#b1adc9",
    "accent": "#ffb8b5", "accent_secondary": "#dbbefe", "accent_soft": "#3e8fb0",
    "success": "#84d5f6", "warning": "#f6c177", "danger": "#ff8db0", "info": "#9ccfd8"}},
  "rose-pine-dawn": {"palette": {
    "canvas": "#faf4ed", "surface": "#faf4ed", "surface_elevated": "#dfdad9", "surface_selected": "#cecacd", "surface_hover": "#dfdad9", "slot_background": "#f0eae3",
    "border": "#7e798b", "border_subtle": "#f2e9e1", "border_focused": "#8973a2",
    "ink": "#464261", "ink_muted": "#565270", "ink_subtle": "#635e70",
    "accent": "#903b37", "accent_secondary": "#624c7b", "accent_soft": "#286983",
    "success": "#1b5c76", "warning": "#8d4000", "danger": "#97465d", "info": "#2a6873"}}
}}
```

### Failure modes

| Condition | Behaviour |
| :--- | :--- |
| The embedded document fails to parse or a built-in theme fails validation | A build error, not a runtime problem: `TestBuiltInThemesValidate` fails. The embedded document is a compile-time constant, so this cannot happen in a shipped binary |
| A higher layer redefines a built-in name | Rule 3: the higher definition replaces it whole; `theme.list` shows that layer as `source`; no problem is reported |
| A higher layer redefines `charm` with a failing palette | The ordinary `low_contrast` error; the previous palette keeps serving; partial user themes still inherit from the replaced `charm`, which is what the user asked for |
| `theme.name` names `base16` and a plugin treats palette values as hex | The plugin's bug: the value grammar is documented on the wire type. The host emits exactly what the document holds |
| A `theme.palette.<role>` override is applied on top of `base16` | The override is hex and is checked; every pair with an index on the other side is unmeasurable and skipped, so only override-against-override pairs are enforced. `husk theme check` reports what it could measure |
| The terminal has no truecolor | Unchanged: Bubble Tea downsamples hex values; indices pass through |

### Tests

In `pkg/theme`, each named for what it pins:

- `TestBuiltInThemesMatchDocument`: the embedded document decodes to nine
  themes with exactly these names and values, every role set, no `name`
  field.
- `TestBuiltInThemesValidate`: each definition passes the Named themes rules
  (name grammar, known roles, value grammar: hex, or an index only in
  `base16`).
- `TestBuiltInThemesContrast`: `CheckContrast` reports nothing for the eight
  hex themes; for `base16` every pair is unmeasurable and none is reported
  as a violation.
- `TestBuiltInThemesLadder`: the ladder rule over the eight hex themes, in
  both directions.
- `TestDefaultIsCharm`: `Default()` equals the `charm` entry, with `name`
  `charm`, so the frontend's fallback palette (used before the first reload)
  and the Theme Service's base agree.
- `TestSlotBackgroundIndexPassesThrough`: `SlotBackground("0")` is `"0"`.
- The existing `TestDefaultPaletteContrast`, `TestDefaultPaletteIsHex`,
  `TestDefaultPaletteSurfacesAreOrdered` and `TestSurfaceLadderSeparation`
  keep running on `Default()` and stay green with `charm`'s values.

In the Theme Service: the built-in layer is read first and ranks lowest; a
user `dracula` wins over the built-in one and lists as `user`; `theme.list`
lists the nine first in document order; `theme.name` unset serves `name`
`charm`; a partial user theme inherits from `charm`, not from the built-in
palette of today; `reserved_theme_name` is gone from the code list and no
name is refused.

In `cmd/husk`: the bottom bar shows `charm` with `theme.name` unset (the
chrome spec's test, renamed); the goldens are regenerated once for the new
values (the chrome spec's "Tests and goldens" owns the mechanics).

## Level 2: Migration from today

| Today | After |
| :--- | :--- |
| `pkg/theme/theme.go`: `Default()` builds `CharmTone` from `charmtone` constants | `Default()` returns the `charm` entry of the embedded document; the `charmtone` import leaves `pkg/theme` (`go.mod` drops the module if nothing else uses it) |
| One built-in palette, no built-in `themes` layer | `pkg/theme/themes.json` (the document above) embedded with `embed`, exposed as `BuiltIn() []LayerDoc` or equivalent for the Theme Service, which prepends it to `Store.Layers("themes", profile)` as layer `built-in` |
| Theme Service rule 1: reserved name `CharmTone`, `reserved_theme_name` | No reserved name; the code is removed from the list in [Services](services.md) |
| Theme Service build step 1: "start from the built-in palette" | Start from `charm` |
| `theme.name` default `CharmTone` ([Services](services.md), Settings) | Default `charm` |
| `theme.list`: built-in first, then by name | The nine in document order, then by name |
| `Overlay` validates `#RRGGBB` only | Unchanged for layered themes and overrides; the built-in document is validated by its own test, and the index form is accepted only when loading the embedded document |
| `CheckContrast`: an unparsable value is a violation | Unchanged for layered themes and overrides (hex-only, so never hit); the built-in test treats index pairs as unmeasurable |
| `protocol.Palette` comment: `#RRGGBB` strings | `#rrggbb`, or an ANSI index `"0"`–`"15"` as a decimal string when the selected theme is `base16`; [Protocol](protocol.md) `theme_changed` row and [Architecture](../architecture.md) "Theme" amended |
| `cmd/husk` goldens show `✦ CharmTone` and today's colours | Regenerated once with the chrome spec's colour changes |
| `AGENTS.md`, `README.md`: "one palette, CharmTone, do not add other themes" | Rewritten by the landing commit to name `charm` and the built-in layer; this spec does not edit them because they describe the code as it is |

## Decisions taken by this spec

1. **The built-in themes are a layer, not nine `Default()`s.** The Named
   themes machinery already handles layers, sources and `theme.list`; a
   second code path for built-ins would duplicate it. The cost is that a
   built-in name can be shadowed, which decision 2 accepts.
2. **No reserved names.** Reserving nine names would need nine problems
   that today's one reserved name needed only because it was not in a
   layer. Rule 3 already says what happens when two layers define one name.
3. **Partial themes inherit from `charm`, not from a frozen "built-in
   palette".** There is one base, and it is the default theme; a
   distribution that replaces `charm` changes that base knowingly.
4. **Charm's canvas is grey 234, one below the ramp the brief names.** With
   `235` as canvas the raised steps fall where the theme's own fuchsia,
   green and red fail 4.5:1 and would all have to move; one step down, only
   the red moves, and the canvas stays close to today's. The brief's greys
   `237`, `238`, `243`, `248` and `252` are all used.
5. **One mechanical adjustment rule** (Lab lightness, hue fixed, away from
   the canvas) rather than hand-picked substitutes, so every adjusted value
   is reproducible from its source and the floor is the same for all
   themes. The light themes pay most; the alternative of a lower floor for
   them was rejected because the floor is the point.
6. **Missing roles are filled from the vendor's own palette** (`charmtone`
   for Charm's yellow and blue; the Dracula specification's cyan and pink),
   not invented and not borrowed across themes. Each fill is marked.
7. **`accent` is the focused-control colour in `huh`** and the signature
   colour elsewhere; `border_focused` is the second accent. A focused frame
   edge in the accent would be the loudest thing on screen, which is what
   the discipline rule is removing.
8. **Base16 stays ANSI** and is unmeasurable. Translating it to hex would
   defeat the theme; measuring it is impossible; so it is the one theme the
   floor does not vouch for, stated plainly. Indices `9`–`14` are avoided
   because Base16 schemes repeat them.
9. **Rosé Pine's `success` and `info` are allowed to end close.** The
   palette's own terminal mapping says green is pine and blue is foam;
   lightening pine to 4.5:1 lands it beside foam. Remapping `info` to iris
   would be a prettier table and a wrong one.
10. **Light themes are themes.** No background detection, no mode, no pair
    of settings; a user picks `charm-light` the way they pick `dracula`.
11. **`rose-pine-moon-dark` is not shipped**: it is one renderer's pairing
    of Moon's colours with main's base, not a Rosé Pine variant.

## Open questions

1. **Terminal background detection.** Bubble Tea can query the terminal's
   background colour; Husk could pick `charm` or `charm-light` from it when
   `theme.name` is unset. v1 does not: the query is asynchronous, it fails
   under some multiplexers, and a theme that changes under the user without
   a setting changing is a mode. A later spec may add a `theme.auto`
   setting naming a dark and a light theme.
2. **Index values outside the built-in layer.** A user who wants their own
   ANSI theme cannot write one (the floor is unmeasurable). Allowing it
   under `theme.allow_low_contrast` is a small rule for a later spec.
3. **Preview in the selector.** The theme selector has no live preview
   ([Services](services.md), "The theme selector"); with nine themes the
   case for a frontend-only preview is stronger. Unchanged here.
