package main

import (
	"regexp"
	"strings"
)

// sgrSeq matches a CSI SGR sequence ("ESC [ params m"). Private-marker CSI
// sequences such as "ESC [ > 4 ; 1 m" do not match.
var sgrSeq = regexp.MustCompile("\x1b\\[([0-9;:]*)m")

// sgrReset returns the terminal to its default graphic rendition.
const sgrReset = "\x1b[m"

// sgrOpen reports whether s leaves any graphic rendition active at its end,
// that is whether text written right after s would inherit a colour or
// attribute from it. It is conservative: an attribute switched off by its
// specific code (22, 39, 49, ...) still counts as open, which at worst costs
// one redundant reset.
func sgrOpen(s string) bool {
	if !strings.Contains(s, "\x1b[") {
		return false
	}
	open := false
	for _, m := range sgrSeq.FindAllStringSubmatch(s, -1) {
		params := m[1]
		if params == "" {
			open = false
			continue
		}
		toks := strings.Split(params, ";")
		for i := 0; i < len(toks); i++ {
			switch toks[i] {
			case "", "0":
				open = false
			case "38", "48", "58":
				// Extended colour: skip its arguments so a 0 component is not
				// read as a reset.
				open = true
				if i+1 < len(toks) {
					switch toks[i+1] {
					case "5":
						i += 2
					case "2":
						i += 4
					}
				}
			default:
				open = true
			}
		}
	}
	return open
}

// closeSGR appends a reset when s leaves a graphic rendition active, so the
// next fragment spliced after s starts from the default state. Well-formed
// fragments (already ending in a reset, or unstyled) are returned unchanged.
func closeSGR(s string) string {
	if sgrOpen(s) {
		return s + sgrReset
	}
	return s
}
