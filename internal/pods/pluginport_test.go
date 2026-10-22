package pods

import (
	"context"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
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
}
