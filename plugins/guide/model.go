// Package guide implements the optional live layout annotation plugin.
// It reads public State and effective keys and paints only app-overlay.
package guide

import (
	"encoding/json"
	"sort"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

const toggleAction = "guide.toggle"

type rect struct{ X, Y, Width, Height int }
type geometry struct {
	Terminal struct{ Width, Height int } `json:"terminal"`
	TooSmall bool                        `json:"too_small"`
	Rects    map[protocol.SurfaceID]rect `json:"rects"`
}
type surfaces struct {
	Occupants map[protocol.SurfaceID]struct {
		Plugin string `json:"plugin"`
	} `json:"occupants"`
	Visible   []protocol.SurfaceID `json:"visible"`
	Placement struct {
		RailItem string `json:"rail_item"`
		Kind     string `json:"kind"`
	} `json:"placement"`
	Overlays struct {
		Modal struct {
			Plugin   string `json:"plugin"`
			RailItem string `json:"rail_item"`
		} `json:"modal"`
	} `json:"overlays"`
}
type layout struct {
	Geometry geometry
	Surfaces surfaces
	Focus    struct {
		Surface protocol.SurfaceID `json:"surface"`
	}
}
type sliceValue struct {
	Slice string          `json:"slice"`
	Value json.RawMessage `json:"value"`
}

func (l *layout) apply(v sliceValue) error {
	switch v.Slice {
	case "husk.geometry":
		var next geometry
		if err := json.Unmarshal(v.Value, &next); err != nil {
			return err
		}
		l.Geometry = next
	case "husk.surfaces":
		var next surfaces
		if err := json.Unmarshal(v.Value, &next); err != nil {
			return err
		}
		l.Surfaces = next
	case "husk.focus":
		var next struct {
			Surface protocol.SurfaceID `json:"surface"`
		}
		if err := json.Unmarshal(v.Value, &next); err != nil {
			return err
		}
		l.Focus = next
	}
	return nil
}

// table order is the protocol vocabulary order, also used for hidden entries.
func surfaceTable() []protocol.SurfaceID {
	return []protocol.SurfaceID{
		protocol.SurfaceActivityRail, protocol.SurfaceSidebarNavigation,
		protocol.SurfaceSidebarList, protocol.SurfaceThreadRowStatus,
		protocol.SurfaceSidebarFooter, protocol.SurfaceMainHeader,
		protocol.SurfaceMainStage, protocol.SurfaceSidePanel, protocol.SurfaceAppOverlay,
		protocol.SurfaceModal, protocol.SurfaceFullCanvas, protocol.SurfaceWorkspaceStage,
	}
}

type annotation struct {
	ID     protocol.SurfaceID
	Rect   rect
	Fields [5]string
}

func (l layout) annotations(k keyListing) []annotation {
	var ids []protocol.SurfaceID
	for id := range l.Geometry.Rects {
		if id != protocol.SurfaceAppOverlay {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := ids[i], ids[j]
		if (a == protocol.SurfaceModal) != (b == protocol.SurfaceModal) {
			return b == protocol.SurfaceModal
		}
		ar, br := l.Geometry.Rects[a], l.Geometry.Rects[b]
		if ar.Y != br.Y {
			return ar.Y < br.Y
		}
		if ar.X != br.X {
			return ar.X < br.X
		}
		return a < b
	})
	out := make([]annotation, 0, len(ids))
	for i, id := range ids {
		occupant := l.Surfaces.Occupants[id].Plugin
		if occupant == "" {
			occupant = "empty"
		}
		out = append(out, annotation{id, l.Geometry.Rects[id], [5]string{number(i + 1), id.Humanised(), string(id), occupant, l.surfaceKey(k, id)}})
	}
	return out
}
