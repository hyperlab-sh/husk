package workbench

import "github.com/hyperlab-sh/husk/pkg/protocol"

// surfaceMeta describes one of the fixed host surfaces for the architecture
// explorer. Titles and descriptions are generic layout roles: Husk assigns no
// application meaning to a surface; the occupying plugin decides what it shows.
type surfaceMeta struct {
	ID          protocol.SurfaceID
	Number      int
	Title       string
	Description string
}

var allSurfaces = []surfaceMeta{
	{protocol.SurfaceActivityRail, 0, "Activity Rail", "Far-left icon column; launches plugin rail items as modal, full-canvas or stage placements."},
	{protocol.SurfaceSidebarNavigation, 1, "Sidebar Navigation", "Top sidebar band above the sidebar list."},
	{protocol.SurfaceSidebarList, 2, "Sidebar List", "Primary sidebar surface."},
	{protocol.SurfaceThreadRowStatus, 3, "Row Status", "Narrow status surface beside the sidebar list."},
	{protocol.SurfaceSidebarFooter, 4, "Sidebar Footer", "Bottom sidebar band."},
	{protocol.SurfaceMainHeader, 5, "Main Header", "Header band above the main stage."},
	{protocol.SurfaceMainStage, 6, "Main Stage", "Central canvas."},
	{protocol.SurfaceSidePanel, 7, "Side Panel", "Collapsible right-hand drawer."},
	{protocol.SurfaceAppOverlay, 8, "App Overlay", "Overlay layer for the command palette, modal rail items and toasts."},
}
