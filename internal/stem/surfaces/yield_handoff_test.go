package surfaces

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Keep this file on the pre-yield public API so the handoff regression can
// compile against the implementation that predates the additive parameter.
func yieldParams(t *testing.T) protocol.SurfaceCloseParams {
	t.Helper()
	var p protocol.SurfaceCloseParams
	if err := json.Unmarshal([]byte(`{"surface":"main-stage","yield":true}`), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSurfaceYieldHandoffAndReturn(t *testing.T) {
	for _, other := range []string{"beta", "zeta"} {
		t.Run(other, func(t *testing.T) {
			st, eng, pods := newEngine(t, Options{})
			pods.launch("alpha")
			pods.claim("alpha", string(protocol.SurfaceMainStage))
			settle(st, eng)
			pods.launch(other)
			pods.claim(other, string(protocol.SurfaceMainStage))
			settle(st, eng)
			if err := eng.SurfaceClose("alpha", yieldParams(t)); err != nil {
				t.Fatal(err)
			}
			settle(st, eng)
			if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != other {
				t.Fatalf("after yield occupant = %q, want %s", got, other)
			}
			if got := pods.decl("alpha").occ.Get()[protocol.SurfaceMainStage].Plugin; got != "alpha" {
				t.Fatalf("yield retracted declaration: %q", got)
			}
			if err := eng.SurfaceClose(other, protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage}); err != nil {
				t.Fatal(err)
			}
			settle(st, eng)
			if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha" {
				t.Fatalf("after borrower closes occupant = %q, want alpha", got)
			}
		})
	}
}
