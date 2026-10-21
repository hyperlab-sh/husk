package theme

import (
	"strings"

	"github.com/lucasb-eyer/go-colorful"
)

// SlotShift is how far SlotBackground moves the app background's CIE Lab
// lightness (L runs 0 to 1 here, so 0.05 is five points of L*). It is small
// enough that slots read as panels rather than as a different colour, and
// large enough to stay visible on dim terminals. Lab lightness is perceptual,
// so the same step looks alike on every hue.
const SlotShift = 0.05

// SlotBackground derives the slot content background from the app background
// (a "#RRGGBB" string): slightly lighter on a dark background and slightly
// darker on a light one, so a slot reads as a panel on either. A background
// counts as dark when its Lab lightness is below 0.5. The result is clamped
// to the sRGB gamut and to the lightness range, so a black or white app
// background still yields a valid, distinct colour. An input that is not a
// "#RRGGBB" colour is returned unchanged, so a slot never gets a colour the
// host could not parse.
func SlotBackground(canvas string) string {
	if len(canvas) != 7 || canvas[0] != '#' {
		return canvas
	}
	c, err := colorful.Hex(strings.ToLower(canvas))
	if err != nil {
		return canvas
	}
	l, a, b := c.Lab()
	if IsDark(canvas) {
		l += SlotShift
	} else {
		l -= SlotShift
	}
	l = min(1, max(0, l))
	return colorful.Lab(l, a, b).Clamped().Hex()
}

// IsDark reports whether a "#RRGGBB" colour is a dark background, by Lab
// lightness below 0.5. An unparsable colour is treated as dark, the common
// terminal case.
func IsDark(hex string) bool {
	c, err := colorful.Hex(strings.ToLower(hex))
	if err != nil {
		return true
	}
	l, _, _ := c.Lab()
	return l < 0.5
}
