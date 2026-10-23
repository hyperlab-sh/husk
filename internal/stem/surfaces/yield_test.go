package surfaces

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

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
			if tc.clear {
				// Withdraw the holder's declaration and clear resolution in
				// the same tick: there is no release-time freed marker.
				pods.unclaim("holder", string(protocol.SurfaceMainStage))
				if _, err := st.Clear(state.SurfacesNamespace); err != nil {
					t.Fatal(err)
				}
			} else if err := eng.SurfaceClose("holder", protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage}); err != nil {
				t.Fatal(err)
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
		standing bool
	}{
		{"unoccupied", protocol.SurfaceMainStage, "alpha", false, false},
		{"other occupant", protocol.SurfaceMainStage, "beta", true, false},
		{"non-claimed modal", protocol.SurfaceModal, "alpha", true, false},
		{"refused standing claimant", protocol.SurfaceMainStage, "beta", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, eng, pods := newEngine(t, Options{})
			pods.launch("alpha")
			pods.launch("beta")
			if tc.standing {
				pods.claim("alpha", string(tc.surface))
				pods.claim("beta", string(tc.surface))
			} else if tc.occupied {
				// A frontend-opened surface has occupancy but no claim.
				if err := eng.slice.Update(func(v *Surfaces) { v.Occupants = Occupants{tc.surface: {Surface: tc.surface, Plugin: "alpha"}} }); err != nil {
					t.Fatal(err)
				}
				eng.markRev()
			}
			settle(st, eng)
			settle(st, eng) // drain the resolution write before the refusal
			p := yieldParams(t)
			p.Surface = tc.surface
			before := eng.slice.Get()
			err := eng.SurfaceClose(tc.plugin, p)
			if codeOf(t, err) != protocol.CodeInvalidParams || !strings.Contains(err.Error(), "yield") {
				t.Fatalf("error = %v, want invalid_params naming yield", err)
			}
			if !reflect.DeepEqual(eng.slice.Get(), before) {
				t.Error("invalid yield changed resolution before flush")
			}
			if len(eng.yielded) != 0 || len(eng.freed) != 0 || eng.dirty {
				t.Errorf("invalid yield changed engine bookkeeping: yielded=%v freed=%v dirty=%t", eng.yielded, eng.freed, eng.dirty)
			}
			settle(st, eng)
			after := resolution(t, st)
			if !reflect.DeepEqual(after, before) {
				t.Errorf("invalid yield changed resolution after flush: got %+v, want %+v", after, before)
			}
			if tc.occupied && after.Occupants[tc.surface].Plugin != "alpha" {
				t.Error("invalid yield released alpha's occupancy")
			}
			if len(eng.yielded) != 0 || len(eng.freed) != 0 {
				t.Errorf("invalid yield left pending claims after flush: yielded=%v freed=%v", eng.yielded, eng.freed)
			}
		})
	}
}

func TestSurfaceCloseReleaseFailureWithUnrelatedYield(t *testing.T) {
	for _, raw := range []string{`{"surface":"main-stage"}`, `{"surface":"main-stage","yield":false}`} {
		t.Run(raw, func(t *testing.T) {
			st, eng, pods := newEngine(t, Options{})
			flaky := &flakyPod{pod: pods}
			eng.decls = flaky
			for _, plugin := range []string{"closer", "yielder", "borrower"} {
				pods.launch(plugin)
			}
			pods.claim("closer", string(protocol.SurfaceMainStage))
			pods.claim("yielder", string(protocol.SurfaceSidebarList))
			settle(st, eng)
			pods.claim("borrower", string(protocol.SurfaceSidebarList))
			settle(st, eng)
			if err := eng.SurfaceClose("yielder", protocol.SurfaceCloseParams{Surface: protocol.SurfaceSidebarList, Yield: true}); err != nil {
				t.Fatal(err)
			}
			settle(st, eng)
			if !eng.yielded[claimKey{protocol.SurfaceSidebarList, "yielder"}] {
				t.Fatal("precondition: unrelated yielded claim is missing")
			}
			flaky.failRelease = true
			var p protocol.SurfaceCloseParams
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				t.Fatal(err)
			}
			if err := eng.SurfaceClose("closer", p); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				settle(st, eng)
				if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != "" {
					t.Fatalf("failed plain release reoccupied main-stage: %q", got)
				}
			}
			eng.CatalogChanged() // an unrelated dirty pass must not re-award it
			settle(st, eng)
			if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != "" {
				t.Fatalf("unrelated dirty pass reoccupied main-stage: %q", got)
			}
			if _, err := st.Clear(state.SurfacesNamespace); err != nil {
				t.Fatal(err)
			}
			settle(st, eng)
			if got := resolution(t, st).Occupants[protocol.SurfaceMainStage].Plugin; got != "closer" {
				t.Fatalf("full resolution lost standing claim: %q", got)
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
