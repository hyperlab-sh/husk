package theme

import (
	"strings"

	"github.com/lucasb-eyer/go-colorful"
)

// SlotShift is how far SlotBackground moves the app background's CIE Lab
// lightness (L runs 0 to 1 here, so 0.035 is three and a half points of L*). It is small
// enough that slots read as panels rather than as a different colour, and
// large enough to stay visible on dim terminals. Lab lightness is perceptual,
// so the same step looks alike on every hue.
const SlotShift = 0.035

// SlotBackground derives the slot content background from the app background:
// slightly lighter on a dark background and slightly darker on a light one, so
// a slot reads as a panel on either. A background counts as dark when its Lab
// lightness is below 0.5. The result is clamped to the sRGB gamut and to the
// lightness range, so a black or white app background still yields a valid,
// distinct colour (always "#rrggbb", lower case).
//
// Accepted inputs are "#RRGGBB" and "#RGB". Anything else, including
// "#RRGGBBAA" (a translucent background has no meaningful panel shade),
// terminal palette indices and the empty string, is returned unchanged: the
// caller gets the canvas back, which means "no panel", never a colour the host
// could not parse.
func SlotBackground(canvas string) string {
	hex, ok := normalizeHex(canvas)
	if !ok {
		return canvas
	}
	c, err := colorful.Hex(hex)
	if err != nil {
		return canvas
	}
	l, a, b := c.Lab()
	if l < 0.5 {
		l += SlotShift
	} else {
		l -= SlotShift
	}
	l = min(1, max(0, l))
	return colorful.Lab(l, a, b).Clamped().Hex()
}

// normalizeHex returns a lower-case "#rrggbb" for "#RRGGBB" or "#RGB".
func normalizeHex(s string) (string, bool) {
	if len(s) == 0 || s[0] != '#' {
		return "", false
	}
	s = strings.ToLower(s)
	switch len(s) {
	case 7:
		return s, true
	case 4:
		return "#" + strings.Repeat(string(s[1]), 2) + strings.Repeat(string(s[2]), 2) + strings.Repeat(string(s[3]), 2), true
	}
	return "", false
}
