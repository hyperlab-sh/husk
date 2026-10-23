package protocol

import "testing"

func TestSurfaceIDHumanised(t *testing.T) {
	for _, tc := range []struct {
		id   SurfaceID
		want string
	}{
		{SurfaceActivityRail, "Activity rail"}, {SurfaceSidebarNavigation, "Sidebar navigation"},
		{SurfaceSidebarList, "Sidebar list"}, {SurfaceThreadRowStatus, "Thread row status"},
		{SurfaceSidebarFooter, "Sidebar footer"}, {SurfaceMainHeader, "Main header"},
		{SurfaceMainStage, "Main stage"}, {SurfaceSidePanel, "Side panel"},
		{SurfaceAppOverlay, "App overlay"}, {SurfaceModal, "Modal surface"},
		{SurfaceFullCanvas, "Full canvas"}, {SurfaceWorkspaceStage, "Workspace stage"},
		{"écran-test", "Écran test"}, {"", ""},
	} {
		t.Run(string(tc.id), func(t *testing.T) {
			if got := tc.id.Humanised(); got != tc.want {
				t.Fatalf("name %q, want %q", got, tc.want)
			}
		})
	}
}
