package main

import (
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

var publishedAnchor = regexp.MustCompile("\x1b\\]husk;slot=([^;\x1b]+);key=([^;\x1b]*)\x1b\\\\")

type contributionHit struct {
	X, Y, Width                                         int
	PluginID, CommandID, SlotID, ContributionID, RowKey string
}

func contributionLine(c huskipc.SlotContribution) string {
	// An inline contribution occupies one row. Region contributions may contain
	// newlines and are split by the region renderer instead.
	return strings.SplitN(strings.ReplaceAll(c.Content, "\r", ""), "\n", 2)[0]
}

func (r *publicationRegistry) replaceAnchors(owner, line string, width, y int) (string, []contributionHit) {
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
		if slot := r.Slots[id]; slot != nil && slot.Owner == owner && slot.Spec.Kind == "inline" {
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

// renderPublished is shared by rendering and input routing; coordinates are
// always relative to the host surface's content, in terminal cells, not bytes.
func (r *publicationRegistry) renderPublished(surface huskipc.SlotID, owner string, source []string, width, height int) ([]string, []contributionHit) {
	if width <= 0 || height <= 0 {
		return nil, nil
	}
	rows := make([]string, height)
	var hits []contributionHit
	for y := range rows {
		line := ""
		if y < len(source) {
			line = source[y]
		}
		var rowHits []contributionHit
		rows[y], rowHits = r.replaceAnchors(owner, line, width, y)
		// A plugin line may leave a style open; close it before the padding
		// and the frame edge so it cannot paint cells the plugin does not own.
		rows[y] = fitLineWidth(closeSGR(rows[y]), width)
		hits = append(hits, rowHits...)
	}
	ids := make([]string, 0)
	for id, s := range r.Slots {
		if s.Owner == owner && s.Spec.Kind == "region" && s.Spec.Rect != nil && s.Spec.Rect.Surface == surface {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids) // deterministic stacking if the publisher reserves overlapping rects
	for _, id := range ids {
		s := r.Slots[id]
		rect := s.Spec.Rect
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
		// Each fragment is closed before the next is appended (and before its
		// own padding), so no colour crosses a region edge; see spliceOverlay.
		for i, text := range regionRows {
			rows[y+i] = fitLineWidth(closeSGR(ansi.Cut(rows[y+i], 0, x)), x) + fitLineWidth(closeSGR(text), w) + fitLineWidth(closeSGR(ansi.Cut(rows[y+i], x+w, width)), width-x-w)
		}
	}
	return rows, hits
}
