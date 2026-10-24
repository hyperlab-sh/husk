package terminals

import (
	"regexp"
	"strings"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
)

// linkScheme is immutable after compilation and shared by loop-only lookups.
var linkScheme = regexp.MustCompile(`(?:^|[^A-Za-z0-9+.-])([a-z][a-z0-9+.-]*://)`)

// SetLinkHandler installs the loop-only delivery callback for ctrl+left clicks.
// Delivery must be nonblocking; a refused delivery still consumes the gesture.
func (t *Terminal) SetLinkHandler(send func(url string, row, col int)) { t.link = send }

// Link resolves and consumes a link gesture without encoding any mouse input.
// It is loop-only and reports whether the event belongs to a consumed gesture.
// A release outside the surface may not be routed here. The next routed left
// press always resets the pending gesture before resolving its URL.
func (t *Terminal) Link(event uv.MouseEvent) bool {
	if t.closed {
		return false
	}
	m := event.Mouse()
	if _, ok := event.(uv.MouseReleaseEvent); ok && (m.Button == uv.MouseLeft || m.Button == uv.MouseNone) && t.linkPress {
		t.linkPress = false
		return true
	}
	if _, ok := event.(uv.MouseMotionEvent); ok && t.linkPress && m.Button == uv.MouseLeft {
		return true
	}
	if _, ok := event.(uv.MouseClickEvent); !ok || m.Button != uv.MouseLeft {
		return false
	}
	t.linkPress = false
	if !m.Mod.Contains(uv.ModCtrl) {
		return false
	}
	url := t.linkAt(m.X, m.Y)
	if url == "" {
		return false
	}
	t.linkPress = true
	if t.link != nil {
		t.link(url, m.Y, m.X)
	}
	return true
}

// linkAt reads the same visible cells as Draw, including the history viewport.
func (t *Terminal) linkAt(col, row int) string {
	if col < 0 || row < 0 || col >= t.em.Width() || row >= t.em.Height() {
		return ""
	}
	cells := make([]string, t.em.Width())
	clicked := col
	var osc string
	for x := 0; x < len(cells); {
		var cell *uv.Cell
		if y := row - t.offset; y < 0 {
			cell = t.em.ScrollbackCellAt(x, t.em.ScrollbackLen()+y)
		} else {
			cell = t.em.CellAt(x, y)
		}
		width := 1
		text := " "
		if cell != nil {
			width = max(1, cell.Width)
			if cell.Content != "" {
				text = cell.Content
			}
			if col >= x && col < x+width {
				clicked = x
				osc = cell.Link.URL
			}
		}
		cells[x] = text
		x += width
	}
	if osc != "" {
		return osc
	}
	start, end := clicked, clicked+1
	space := func(s string) bool { return strings.ContainsFunc(s, unicode.IsSpace) }
	if space(cells[clicked]) {
		return ""
	}
	for start > 0 && !space(cells[start-1]) {
		start--
	}
	for end < len(cells) && !space(cells[end]) {
		end++
	}
	text := strings.Join(cells[start:end], "")
	// Scheme matching is deliberately case-sensitive and row-local.
	scheme := linkScheme.FindStringSubmatchIndex(text)
	if scheme == nil {
		return ""
	}
	url := text[scheme[2]:]
	for len(url) > 0 {
		last := url[len(url)-1]
		if strings.ContainsRune(`.,;:!?'"`, rune(last)) {
			url = url[:len(url)-1]
			continue
		}
		open := byte(0)
		switch last {
		case ')':
			open = '('
		case ']':
			open = '['
		case '}':
			open = '{'
		case '>':
			open = '<'
		}
		if open != 0 && strings.Count(url, string(last)) > strings.Count(url, string(open)) {
			url = url[:len(url)-1]
			continue
		}
		break
	}
	// Prefixes and trimmed suffixes are not themselves clickable URL cells.
	pos := len(strings.Join(cells[start:clicked], ""))
	if pos < scheme[2] || pos >= scheme[2]+len(url) {
		return ""
	}
	return url
}
