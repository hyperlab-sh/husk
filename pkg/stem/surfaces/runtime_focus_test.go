package surfaces

import (
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"testing"
)

func TestRuntimeFocus(t *testing.T) {
	_, e, _ := newEngine(t, Options{})
	for _, name := range []string{"a", "b"} {
		if _, err := e.SurfaceOpen("p", protocol.SurfaceOpenParams{Placement: protocol.SurfacePlacementCanvas, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.SurfaceFocus("p", protocol.SurfaceFocusParams{Surface: "p.a"}); err != nil {
		t.Fatal(err)
	}
	rows := e.RuntimeSurfaces()
	if len(rows) != 2 || rows[1].Surface != "p.a" || rows[1].FocusRevision != 1 {
		t.Fatalf("focus did not promote A: %+v", rows)
	}
	for _, tc := range []struct {
		caller string
		id     protocol.SurfaceID
		code   protocol.Code
	}{
		{"other", "p.a", protocol.CodeDenied}, {"p", "p.closed", protocol.CodeNotFound}, {"p", protocol.SurfaceMainStage, protocol.CodeNotFound},
	} {
		if got := codeOf(t, e.SurfaceFocus(tc.caller, protocol.SurfaceFocusParams{Surface: tc.id})); got != tc.code {
			t.Fatalf("%s %s: %s want %s", tc.caller, tc.id, got, tc.code)
		}
	}
	if err := e.CloseSurface("p.a"); err != nil {
		t.Fatal(err)
	}
	if got := codeOf(t, e.SurfaceFocus("p", protocol.SurfaceFocusParams{Surface: "p.a"})); got != protocol.CodeNotFound {
		t.Fatalf("closed focus: %s", got)
	}
}
