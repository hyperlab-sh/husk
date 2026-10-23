package surfaces

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Decode the wire request so this regression also runs against a host that
// predates the additive yield parameter.
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

func TestSurfaceYieldWithoutLiveAlternative(t *testing.T) {
	for _, deadClaimant := range []bool{false, true} {
		t.Run(map[bool]string{false: "alone", true: "dead claimant"}[deadClaimant], func(t *testing.T) {
			live := liveness{dead: map[string]bool{"beta": true}}
			st, eng, pods := newEngine(t, Options{Liveness: live})
			pods.launch("alpha")
			pods.claim("alpha", string(protocol.SurfaceMainStage))
			if deadClaimant {
				pods.launch("beta")
				pods.claim("beta", string(protocol.SurfaceMainStage))
			}
			settle(st, eng)
			if err := eng.SurfaceUpdate("alpha", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage, Capture: protocol.CaptureBool(true)}); err != nil {
				t.Fatal(err)
			}
			if err := eng.SurfaceClose("alpha", yieldParams(t)); err != nil {
				t.Fatal(err)
			}
			settle(st, eng)
			if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != "alpha" {
				t.Fatalf("no live alternative: occupant = %q, want alpha", got)
			}
			if len(resolution(t, st).Capture) != 0 {
				t.Fatal("capture survived yield")
			}
		})
	}
}

func TestSurfaceYieldRanking(t *testing.T) {
	for _, tc := range []struct {
		name        string
		newClaimant string
		clear       bool
		want        string
	}{
		{"two yielders use namespace order", "", false, "git-x"},
		{"new ordinary claimant earlier", "aaa", false, "aaa"},
		{"new ordinary claimant later", "zzz", false, "zzz"},
		{"full resolution retains yield rank", "", true, "git-x"},
		{"full resolution prefers earlier ordinary claimant", "aaa", true, "aaa"},
		{"full resolution prefers later ordinary claimant", "zzz", true, "zzz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, eng, pods := newEngine(t, Options{})
			// Namespace order differs from bare plugin-id order for these ids.
			for _, plugin := range []string{"git", "git-x", "holder"} {
				pods.launch(plugin)
				pods.claim(plugin, string(protocol.SurfaceMainStage))
				settle(st, eng)
			}
			for _, plugin := range []string{"git", "git-x"} {
				if err := eng.SurfaceClose(plugin, yieldParams(t)); err != nil {
					t.Fatal(err)
				}
				settle(st, eng)
			}
			if tc.newClaimant != "" {
				pods.launch(tc.newClaimant)
				pods.claim(tc.newClaimant, string(protocol.SurfaceMainStage))
				settle(st, eng)
			}
			if err := eng.SurfaceClose("holder", protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage}); err != nil {
				t.Fatal(err)
			}
			if tc.clear {
				if _, err := st.Clear(state.SurfacesNamespace); err != nil {
					t.Fatal(err)
				}
			}
			settle(st, eng)
			if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != tc.want {
				t.Fatalf("occupant = %q, want %s", got, tc.want)
			}
			if tc.newClaimant == "" && eng.yielded[claimKey{protocol.SurfaceMainStage, tc.want}] {
				t.Fatal("regaining occupancy did not restore ordinary rank")
			}
		})
	}
}

func TestSurfaceYieldRequiresOccupiedClaim(t *testing.T) {
	for _, tc := range []struct {
		name     string
		surface  protocol.SurfaceID
		plugin   string
		occupied bool
	}{
		{"unoccupied", protocol.SurfaceMainStage, "alpha", false},
		{"other occupant", protocol.SurfaceMainStage, "beta", true},
		{"non-claimed modal", protocol.SurfaceModal, "alpha", true},
		{"unknown surface", protocol.SurfaceID("unknown"), "alpha", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, eng, pods := newEngine(t, Options{})
			pods.launch("alpha")
			if tc.occupied {
				// A frontend-opened surface has occupancy but no claim.
				if err := eng.slice.Update(func(v *Surfaces) { v.Occupants = Occupants{tc.surface: {Surface: tc.surface, Plugin: "alpha"}} }); err != nil {
					t.Fatal(err)
				}
				eng.markRev()
			}
			p := yieldParams(t)
			p.Surface = tc.surface
			before := st.Snapshot().Seq
			err := eng.SurfaceClose(tc.plugin, p)
			if codeOf(t, err) != protocol.CodeInvalidParams || !strings.Contains(err.Error(), "yield") {
				t.Fatalf("error = %v, want invalid_params naming yield", err)
			}
			if st.Snapshot().Seq != before {
				t.Fatal("invalid yield changed State")
			}
		})
	}
}

func TestSurfaceCloseDefaultRetracts(t *testing.T) {
	for _, raw := range []string{`{"surface":"main-stage"}`, `{"surface":"main-stage","yield":false}`} {
		t.Run(raw, func(t *testing.T) {
			st, eng, pods := newEngine(t, Options{})
			pods.launch("alpha")
			pods.claim("alpha", string(protocol.SurfaceMainStage))
			settle(st, eng)
			var p protocol.SurfaceCloseParams
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				t.Fatal(err)
			}
			if err := eng.SurfaceClose("alpha", p); err != nil {
				t.Fatal(err)
			}
			settle(st, eng)
			if len(pods.decl("alpha").occ.Get()) != 0 {
				t.Fatal("plain close retained declaration")
			}
			if _, err := st.Clear(state.SurfacesNamespace); err != nil {
				t.Fatal(err)
			}
			settle(st, eng)
			if len(resolution(t, st).Occupants) != 0 {
				t.Fatal("plain close reoccupied surface")
			}
		})
	}
}

func TestSurfaceYieldRemovalDropsRank(t *testing.T) {
	st, eng, pods := newEngine(t, Options{})
	for _, plugin := range []string{"alpha", "beta"} {
		pods.launch(plugin)
		pods.claim(plugin, string(protocol.SurfaceMainStage))
		settle(st, eng)
	}
	if err := eng.SurfaceClose("alpha", yieldParams(t)); err != nil {
		t.Fatal(err)
	}
	settle(st, eng)
	pods.teardown("alpha")
	settle(st, eng)
	if _, ok := st.Snapshot().Slices["alpha.occupants"]; ok {
		t.Fatal("removed plugin retained declaration")
	}
	if len(eng.yielded) != 0 {
		t.Fatalf("removed plugin retained yielded ranks: %v", eng.yielded)
	}
	if err := eng.SurfaceClose("beta", protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage}); err != nil {
		t.Fatal(err)
	}
	settle(st, eng)
	if len(resolution(t, st).Occupants) != 0 {
		t.Fatal("removed yielder regained occupancy")
	}
}
