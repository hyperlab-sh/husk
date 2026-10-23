package manifest

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestOverlayFrameIgnored(t *testing.T) {
	spec, warnings, err := convertBytes(t, `{"manifest_version":2,"protocol":2,"id":"annotation","name":"Annotation","version":"1","command":["plugin"],"surfaces":[{"id":"app-overlay","frame":{"border":true,"label":"ignored","close":true}},{"id":"main-stage","frame":{"border":true}}]}`, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || warnings[0].Code != "ignored_field" || warnings[0].Field != "surfaces" || warnings[0].Index != 0 {
		t.Fatalf("warnings=%+v", warnings)
	}
	if spec.Surfaces[0].ID != protocol.SurfaceAppOverlay || spec.Surfaces[0].Frame != nil {
		t.Fatalf("overlay frame retained: %+v", spec.Surfaces[0])
	}
	if spec.Surfaces[1].Frame == nil || !spec.Surfaces[1].Frame.Border {
		t.Fatal("regular surface frame dropped")
	}
}
