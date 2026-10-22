package pods

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// plugin.list reports every Pod's spec summary plus liveness in stable id
// order; plugin.log returns the stderr log path or not_found.
func TestPluginPortListAndLog(t *testing.T) {
	h, _, _, _ := hostFixture(t)
	a := addPod(t, h, "a")
	a.spec.Name = "Alpha"
	a.spec.Version = "1.2.3"
	a.spec.Description = "does things"
	a.info.LogPath = "/runtime/logs/a/plugin.log"
	a.info.PID = 4242
	a.info.Status = wrapper.Live
	b := addPod(t, h, "b")
	b.info.Status = wrapper.Failed
	b.info.LastError = "spawn refused"

	var port contracts.PluginPort = h
	list, err := port.List(context.Background(), contracts.Caller{Kind: contracts.CallerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "a" || list[1].ID != "b" {
		t.Fatal("list order", list)
	}
	first := list[0]
	if first.Name != "Alpha" || first.Version != "1.2.3" || first.Description != "does things" ||
		first.Status != contracts.PodLive || first.PID != 4242 || first.Log != "/runtime/logs/a/plugin.log" {
		t.Fatalf("first entry %+v", first)
	}
	if list[1].Status != contracts.PodFailed || list[1].LastError != "spawn refused" || list[1].PID != 0 {
		t.Fatalf("second entry %+v", list[1])
	}

	if path, err := port.Log(context.Background(), contracts.Caller{Kind: contracts.CallerCLI}, "a"); err != nil || path != "/runtime/logs/a/plugin.log" {
		t.Fatal("log path", path, err)
	}
	if _, err := port.Log(context.Background(), contracts.Caller{Kind: contracts.CallerCLI}, "missing"); contracts.CodeOf(err) != string(protocol.CodeNotFound) {
		t.Fatal("unknown plugin log code", err)
	}
	// A known Pod whose Wrapper never opened a log answers not_found too,
	// rather than an empty path (round-1 note).
	if _, err := port.Log(context.Background(), contracts.Caller{Kind: contracts.CallerCLI}, "b"); contracts.CodeOf(err) != string(protocol.CodeNotFound) {
		t.Fatal("logless plugin log code", err)
	}
}

// plugin.list and plugin.log are the user's own: a plugin caller is denied,
// and a zero-kind caller is denied too — the Caller must be explicit (D57).
func TestPluginPortDeniesNonCLICallers(t *testing.T) {
	h, _, _, _ := hostFixture(t)
	addPod(t, h, "a")
	var port contracts.PluginPort = h
	for _, kind := range []struct {
		name string
		c    contracts.Caller
	}{
		{"plugin", contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "a"}},
		{"stem", contracts.Caller{Kind: contracts.CallerStem}},
		{"zero", contracts.Caller{}},
	} {
		if _, err := port.List(context.Background(), kind.c); contracts.CodeOf(err) != string(protocol.CodeDenied) {
			t.Fatalf("%s list caller: %v", kind.name, err)
		}
		if _, err := port.Log(context.Background(), kind.c, "a"); contracts.CodeOf(err) != string(protocol.CodeDenied) {
			t.Fatalf("%s log caller: %v", kind.name, err)
		}
	}
}

// When the loop's Do seam refuses (no loop runs, or its Post answered
// false), the port answers unavailable instead of reading loop-owned maps
// off-loop (D57).
func TestPluginPortUnavailableWithoutLoop(t *testing.T) {
	h, _, _, _ := hostFixture(t)
	addPod(t, h, "a")
	h.loop = deadLoop{}
	var port contracts.PluginPort = h
	if _, err := port.List(context.Background(), contracts.Caller{Kind: contracts.CallerCLI}); contracts.CodeOf(err) != string(protocol.CodeUnavailable) {
		t.Fatal("list without a loop", err)
	}
	if _, err := port.Log(context.Background(), contracts.Caller{Kind: contracts.CallerCLI}, "a"); contracts.CodeOf(err) != string(protocol.CodeUnavailable) {
		t.Fatal("log without a loop", err)
	}
}

// deadLoop answers Do like a real loop whose Post returned false: the loop
// no longer runs, so nothing executes.
type deadLoop struct{}

func (deadLoop) Post(tea.Msg)                                         {}
func (deadLoop) Opening(string) []protocol.Message                    { return nil }
func (deadLoop) Liveness(LivenessMsg)                                 {}
func (deadLoop) Log(LogMsg)                                           {}
func (deadLoop) Prepare(context.Context, []manifest.Spec) Preparation { return Preparation{} }
func (deadLoop) Do(context.Context, func()) error {
	return context.Canceled
}
