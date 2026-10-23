package guide

import (
	"strings"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type keyEntry struct {
	Keys   []string           `json:"keys"`
	Action string             `json:"action"`
	Focus  protocol.SurfaceID `json:"focus"`
	Plugin string             `json:"plugin"`
	State  string             `json:"state"`
	Source protocol.Source    `json:"source"`
}
type keyListing struct {
	Entries []keyEntry `json:"entries"`
	Failed  bool       `json:"-"`
}

func layerRank(layer string) int {
	switch layer {
	case "profile":
		return 4
	case "user":
		return 3
	case "distribution":
		return 2
	case "focus-local":
		return 1
	}
	return 0
}

func displayBefore(a, b keyEntry) bool {
	if (a.Focus == "") != (b.Focus == "") {
		return a.Focus == ""
	}
	if layerRank(a.Source.Layer) != layerRank(b.Source.Layer) {
		return layerRank(a.Source.Layer) > layerRank(b.Source.Layer)
	}
	if (a.Plugin == "") != (b.Plugin == "") {
		return a.Plugin != ""
	}
	return a.Source.Order < b.Source.Order
}

func (k keyListing) effective(action string, surface protocol.SurfaceID, fallback string) string {
	if k.Failed {
		return "?"
	}
	var best *keyEntry
	for i := range k.Entries {
		e := &k.Entries[i]
		if e.State != "effective" || e.Action != action || e.Focus != "" && e.Focus != surface || len(e.Keys) == 0 {
			continue
		}
		if best == nil || displayBefore(*e, *best) {
			best = e
		}
	}
	if best == nil {
		return fallback
	}
	return strings.Join(best.Keys, " ")
}

func (l layout) surfaceKey(k keyListing, id protocol.SurfaceID) string {
	switch id {
	case protocol.SurfaceActivityRail:
		return ""
	case protocol.SurfaceSidebarNavigation, protocol.SurfaceSidebarList, protocol.SurfaceThreadRowStatus, protocol.SurfaceSidebarFooter:
		return k.effective("husk.sidebar.toggle", id, "unbound")
	case protocol.SurfaceMainHeader, protocol.SurfaceMainStage:
		return k.effective("husk.home", id, "unbound")
	case protocol.SurfaceSidePanel:
		return k.effective("husk.sidepanel.toggle", id, "unbound")
	case protocol.SurfaceAppOverlay:
		return k.effective(toggleAction, id, "unbound")
	case protocol.SurfaceModal:
		rail := l.Surfaces.Overlays.Modal.RailItem
		if rail == "" {
			return "rail"
		}
		return k.effective(rail, id, "palette") + ", " + k.effective("husk.modal.close", id, "unbound")
	case protocol.SurfaceFullCanvas, protocol.SurfaceWorkspaceStage:
		rail := l.Surfaces.Placement.RailItem
		if rail == "" {
			return "rail"
		}
		value := k.effective(rail, id, "palette")
		if id == protocol.SurfaceWorkspaceStage {
			value += ", " + k.effective("husk.home", id, "unbound")
		}
		return value
	}
	return ""
}
