package protocol

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SurfacePlacement is the placement of a plugin-opened runtime surface.
type SurfacePlacement string

const (
	// SurfacePlacementModal opens a singleton floating modal.
	SurfacePlacementModal SurfacePlacement = "modal"
	// SurfacePlacementCanvas opens a full canvas above the existing placement.
	SurfacePlacementCanvas SurfacePlacement = "canvas"
)

// SurfaceDimension is an optional JSON integer cell count or string percentage
// of the full terminal. Nil selects the frontend's existing modal default.
type SurfaceDimension json.RawMessage

// Validate checks the wire shape. Sizes outside the usable range clamp at layout.
func (d SurfaceDimension) Validate() error {
	if len(d) == 0 {
		return nil
	}
	var cells int
	if json.Unmarshal(d, &cells) == nil && string(d) != "null" {
		return nil
	}
	var percent string
	if json.Unmarshal(d, &percent) == nil && strings.HasSuffix(percent, "%") {
		n := strings.TrimSuffix(percent, "%")
		if n != "" && strings.IndexFunc(n, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			if _, err := strconv.Atoi(n); err == nil {
				return nil
			}
		}
	}
	return fmt.Errorf("dimension must be integer cells or a percentage string")
}

// Resolve returns a clamped outer dimension, using fallback when unspecified.
func (d SurfaceDimension) Resolve(total, fallback, lower, upper int) int {
	if upper < lower {
		return max(0, total)
	}
	value := fallback
	if len(d) != 0 {
		if json.Unmarshal(d, &value) != nil {
			var p string
			_ = json.Unmarshal(d, &p)
			n, _ := strconv.Atoi(strings.TrimSuffix(p, "%"))
			// Clamp percentages before multiplying to avoid integer overflow.
			value = total * min(n, 100) / 100
		}
	}
	return max(lower, min(upper, value))
}

// MarshalJSON preserves the cells-or-percentage wire representation.
func (d SurfaceDimension) MarshalJSON() ([]byte, error) {
	if len(d) == 0 {
		return []byte("null"), nil
	}
	return json.RawMessage(d).MarshalJSON()
}

// UnmarshalJSON preserves the dimension for validation at ingestion.
func (d *SurfaceDimension) UnmarshalJSON(b []byte) error {
	if strings.TrimSpace(string(b)) == "null" {
		*d = nil
		return nil
	}
	*d = append((*d)[:0], b...)
	return d.Validate()
}

// SurfaceOpenParams asks for a runtime surface independent of manifest claims.
type SurfaceOpenParams struct {
	Placement SurfacePlacement `json:"placement"`
	Name      string           `json:"name"`
	Title     string           `json:"title,omitempty"`
	Width     SurfaceDimension `json:"width,omitempty"`
	Height    SurfaceDimension `json:"height,omitempty"`
	Frame     *FrameSpec       `json:"frame,omitempty"`
}

// Validate checks the served placements and runtime surface limits.
func (p SurfaceOpenParams) Validate() error {
	if p.Placement != SurfacePlacementModal && p.Placement != SurfacePlacementCanvas {
		return fmt.Errorf("placement must be modal or canvas; stage and panel are reserved")
	}
	if !validRuntimeName(p.Name) {
		return fmt.Errorf("name must match [a-z0-9_-]{1,32}")
	}
	if !utf8.ValidString(p.Title) || utf8.RuneCountInString(p.Title) > 80 {
		return fmt.Errorf("title exceeds 80 runes")
	}
	if err := p.Width.Validate(); err != nil {
		return fmt.Errorf("width: %w", err)
	}
	if err := p.Height.Validate(); err != nil {
		return fmt.Errorf("height: %w", err)
	}
	return nil
}

func validRuntimeName(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// SurfaceFocusParams selects an existing runtime surface without creating one.
type SurfaceFocusParams struct {
	Surface SurfaceID `json:"surface"`
}

// SurfaceOpenResult identifies the surface occupied from this reply onward.
type SurfaceOpenResult struct {
	Surface SurfaceID `json:"surface"`
}

// Layout reports whether s is one of the fixed layout surface identifiers.
func (s SurfaceID) Layout() bool {
	switch s {
	case SurfaceActivityRail, SurfaceSidebarNavigation, SurfaceSidebarList,
		SurfaceThreadRowStatus, SurfaceSidebarFooter, SurfaceMainHeader,
		SurfaceMainStage, SurfaceSidePanel, SurfaceAppOverlay, SurfaceModal,
		SurfaceFullCanvas, SurfaceWorkspaceStage:
		return true
	}
	return false
}

func validRuntimeID(s string) bool {
	dot := strings.LastIndexByte(s, '.')
	if dot < 1 || dot > 64 || !validRuntimeName(s[dot+1:]) {
		return false
	}
	plugin := s[:dot]
	if plugin == "husk" {
		return false
	}
	for i, c := range plugin {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '.' || c == '_' || c == '-') {
			continue
		}
		return false
	}
	return true
}
