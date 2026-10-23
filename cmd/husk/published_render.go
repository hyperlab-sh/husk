package main

import (
	"regexp"
	"slices"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

var publishedAnchor = regexp.MustCompile("\x1b\\]husk;slot=([^;\x1b]+);key=([^;\x1b]*)\x1b\\\\")

type contributionHit struct {
	X, Y, Width                                         int
	PluginID, CommandID, SlotID, ContributionID, RowKey string
}

func contributionLine(c contribution) string {
	// An inline contribution occupies one row. Region contributions may contain
	// newlines and are split by the region renderer instead.
	return strings.SplitN(strings.ReplaceAll(c.Content, "\r", ""), "\n", 2)[0]
}

func (r *surfaceCatalog) replaceAnchors(owner, line string, width, y int) (string, []contributionHit) {
	matches := publishedAnchor.FindAllStringSubmatchIndex(line, -1)
	if len(matches) == 0 {
		return line, nil // preserve the host's existing fitLineWidth behavior
	}
	var out strings.Builder
	var hits []contributionHit
	pos, x := 0, 0
	for _, match := range matches {
		prefix := line[pos:match[0]]
		out.WriteString(prefix)
		x += ansi.StringWidth(prefix)
		id, key := line[match[2]:match[3]], line[match[4]:match[5]]
		if slot := r.Slots[id]; slot != nil && slot.Owner == owner && slot.Kind == "inline" {
			for _, c := range slot.Contributions {
				text := contributionLine(c)
				w := ansi.StringWidth(text)
				out.WriteString(text)
				if w > 0 && x < width {
					hits = append(hits, contributionHit{X: x, Y: y, Width: w, PluginID: c.PluginID, CommandID: c.OnClick, SlotID: id, ContributionID: c.ID, RowKey: key})
				}
				x += w
			}
		}
		pos = match[1]
	}
	out.WriteString(line[pos:])
	// Truncate the concatenation once. If a wide glyph cannot fit, later
	// contributions/suffixes must not jump ahead into its unused cell.
	rendered := ansi.Truncate(out.String(), max(0, width), "")
	visibleWidth := ansi.StringWidth(rendered)
	visibleHits := hits[:0]
	for _, hit := range hits {
		if hit.X < visibleWidth {
			hit.Width = min(hit.Width, visibleWidth-hit.X)
			visibleHits = append(visibleHits, hit)
		}
	}
	return rendered, visibleHits
}

// regionDraw is one region rectangle of a publishedLayout: its cells are
// cleared and its contribution lines drawn over the source rows.
type regionDraw struct {
	X, Y, W, H int
	Lines      []string
}

// publishedLayout is a surface's published content, laid out but not drawn:
// the anchor-substituted source rows (clipped to the width, not padded), the
// region rectangles in stacking order, and the contribution hits. Rendering
// draws it onto the canvas; input routing reads only the hits.
type publishedLayout struct {
	Width, Height int
	Rows          []string
	Regions       []regionDraw
	Hits          []contributionHit
}

// draw paints the layout with its top-left content cell at (x, y). Each row
// is parsed from a zero pen, so a style a plugin line leaves open stops at
// its last character; each region clears its whole rectangle before its own
// lines are drawn, so nothing under it shows through or bleeds into it.
func (l publishedLayout) draw(c *canvas, x, y int) {
	c.blitLines(x, y, l.Width, l.Height, l.Rows)
	for _, r := range l.Regions {
		c.blitLines(x+r.X, y+r.Y, r.W, r.H, r.Lines)
	}
}

// renderPublished draws the layout on a scratch canvas and returns its rows,
// each exactly width cells, for callers that want strings.
func (r *surfaceCatalog) renderPublished(surface protocol.SurfaceID, owner string, source []string, width, height int) ([]string, []contributionHit) {
	l := r.layoutPublished(surface, owner, source, width, height)
	if l.Width <= 0 || l.Height <= 0 {
		return nil, nil
	}
	c := newCanvas(l.Width, l.Height)
	l.draw(c, 0, 0)
	rows := make([]string, l.Height)
	for y := range rows {
		rows[y] = uv.Line(c.buf.Lines[y]).Render()
	}
	c.release()
	return rows, l.Hits
}

// layoutPublished is shared by rendering and input routing; coordinates are
// always relative to the host surface's content, in terminal cells, not bytes.
func (r *surfaceCatalog) layoutPublished(surface protocol.SurfaceID, owner string, source []string, width, height int) publishedLayout {
	if width <= 0 || height <= 0 {
		return publishedLayout{}
	}
	l := publishedLayout{Width: width, Height: height, Rows: make([]string, height)}
	var hits []contributionHit
	for y := range l.Rows {
		line := ""
		if y < len(source) {
			line = source[y]
		}
		var rowHits []contributionHit
		l.Rows[y], rowHits = r.replaceAnchors(owner, line, width, y)
		// Clip with the "…" tail; the canvas blanks the rest of the row.
		// The overlay is not truncated: an ellipsis would be an opaque cell
		// painted over the frame edge; the canvas clip drops the overflow instead.
		if surface != protocol.SurfaceAppOverlay && ansi.StringWidth(l.Rows[y]) > width {
			l.Rows[y] = ansi.Truncate(l.Rows[y], width, "…")
		}
		hits = append(hits, rowHits...)
	}
	ids := make([]string, 0)
	for id, s := range r.Slots {
		if s.Owner == owner && s.Kind == "region" && s.Rect != nil && s.Rect.Surface == surface {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids) // deterministic stacking if the publisher reserves overlapping rects
	for _, id := range ids {
		s := r.Slots[id]
		rect := s.Rect
		x, y := rect.X, rect.Y
		if x >= width || y >= height {
			continue
		}
		w, h := min(rect.Width, width-x), min(rect.Height, height-y)
		// Rectangles reserve/clear the entire area, including rows with no contributions.
		// Split older hit ranges at the occluding rectangle so invisible cells cannot click.
		var kept []contributionHit
		for _, hit := range hits {
			if hit.Y < y || hit.Y >= y+h || hit.X+hit.Width <= x || hit.X >= x+w {
				kept = append(kept, hit)
				continue
			}
			if hit.X < x {
				left := hit
				left.Width = x - hit.X
				kept = append(kept, left)
			}
			if hit.X+hit.Width > x+w {
				right := hit
				right.X = x + w
				right.Width = hit.X + hit.Width - right.X
				kept = append(kept, right)
			}
		}
		hits = kept
		regionRows := make([]string, h)
		row := 0
		for _, c := range s.Contributions {
			for _, line := range strings.Split(strings.ReplaceAll(c.Content, "\r", ""), "\n") {
				if row >= h {
					break
				}
				text := ansi.Truncate(line, w, "")
				regionRows[row] = text
				if cw := ansi.StringWidth(text); cw > 0 {
					hits = append(hits, contributionHit{X: x, Y: y + row, Width: cw, PluginID: c.PluginID, CommandID: c.OnClick, SlotID: id, ContributionID: c.ID})
				}
				row++
			}
			if row >= h {
				break
			}
		}
		l.Regions = append(l.Regions, regionDraw{X: x, Y: y, W: w, H: h, Lines: regionRows})
	}
	l.Hits = hits
	return l
}
