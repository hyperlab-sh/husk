package theme

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ContrastPair is one chrome foreground/background requirement.
type ContrastPair struct {
	Foreground string
	Background string
	Minimum    float64
}

// Violation names a failed contrast pair and its measured ratio.
type Violation struct {
	ContrastPair
	Ratio float64
}

// ContrastPairs returns every pairing drawn by chrome, as detached values.
func ContrastPairs() []ContrastPair {
	var out []ContrastPair
	add := func(min float64, fg, bg []string) {
		for _, f := range fg {
			for _, b := range bg {
				out = append(out, ContrastPair{f, b, min})
			}
		}
	}
	surfaces := []string{"canvas", "slot_background", "surface", "surface_elevated", "surface_selected", "surface_hover"}
	base := []string{"canvas", "slot_background", "surface", "surface_elevated"}
	add(4.5, []string{"ink", "ink_muted", "accent", "accent_secondary", "success", "warning"}, surfaces)
	add(4.5, []string{"ink_subtle", "danger", "info"}, base)
	add(4.5, []string{"canvas"}, []string{"accent", "success", "warning"})
	add(3, []string{"border", "border_focused", "danger", "info", "success", "warning"}, base)
	return out
}

// RelativeLuminance computes WCAG relative luminance for one #RRGGBB role.
func RelativeLuminance(hex string) (float64, error) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, fmt.Errorf("want #RRGGBB")
	}
	var rgb [3]float64
	for i := 0; i < 3; i++ {
		n, err := strconv.ParseUint(hex[1+i*2:3+i*2], 16, 8)
		if err != nil {
			return 0, fmt.Errorf("want #RRGGBB")
		}
		v := float64(n) / 255
		if v <= 0.03928 {
			rgb[i] = v / 12.92
		} else {
			rgb[i] = math.Pow((v+0.055)/1.055, 2.4)
		}
	}
	return rgb[0]*0.2126 + rgb[1]*0.7152 + rgb[2]*0.0722, nil
}

// Contrast computes the WCAG ratio, rejecting terminal-dependent colours.
func Contrast(a, b string) (float64, error) {
	la, err := RelativeLuminance(a)
	if err != nil {
		return 0, err
	}
	lb, err := RelativeLuminance(b)
	if err != nil {
		return 0, err
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05), nil
}

// Roles returns an independent JSON-role map including name.
func Roles(p Palette) map[string]string {
	b, _ := json.Marshal(p)
	var m map[string]string
	json.Unmarshal(b, &m)
	return m
}

// CheckContrast checks the same complete table for built-in and user palettes.
func CheckContrast(p Palette) []Violation {
	roles := Roles(p)
	var out []Violation
	for _, pair := range ContrastPairs() {
		ratio, err := Contrast(roles[pair.Foreground], roles[pair.Background])
		if err != nil || ratio < pair.Minimum {
			out = append(out, Violation{pair, ratio})
		}
	}
	return out
}

// Overlay applies hex-only role overrides, deriving slot background from canvas
// unless explicitly supplied. It validates format; CheckContrast is separate.
func Overlay(base Palette, overrides map[string]string) (Palette, error) {
	roles := Roles(base)
	for role, value := range overrides {
		if role == "name" {
			roles[role] = value
			continue
		}
		if _, ok := roles[role]; !ok {
			return Palette{}, fmt.Errorf("unknown palette role %s", role)
		}
		if _, err := RelativeLuminance(value); err != nil {
			return Palette{}, fmt.Errorf("role %s: want #RRGGBB", role)
		}
		roles[role] = strings.ToLower(value)
	}
	if _, ok := overrides["slot_background"]; !ok {
		roles["slot_background"] = SlotBackground(roles["canvas"])
	}
	b, _ := json.Marshal(roles)
	var p Palette
	json.Unmarshal(b, &p)
	return p, nil
}
