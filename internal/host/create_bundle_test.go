package host

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/internal/bundle"
	"github.com/hyperlab-sh/husk/internal/cli/createcmd"
	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestCreatedBundleLoadsBothPluginsAndDistributionKeymap(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	var stdout bytes.Buffer
	if err := createcmd.Command("").Run(context.Background(), []string{"bundle", "demo"}, output.Options{Format: output.JSON, Stdout: &stdout}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "demo")
	b, warnings, err := bundle.Load(dir)
	if err != nil || len(warnings) != 0 || b.Manifest.ID != "demo" {
		t.Fatalf("Load=%+v warnings=%v err=%v", b, warnings, err)
	}
	f := newFakes()
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) { bundleFlagDoc(c, dir) }, withHook(hook), WithFactory(f.factory))
	if err != nil {
		t.Fatal(err)
	}
	if h.root.Planned() != 2 || len(h.root.Frontend().Refusals()) != 0 {
		t.Fatalf("planned=%d refusals=%v", h.root.Planned(), h.root.Frontend().Refusals())
	}
	runHost(t, ready, h)
	started := map[string]bool{}
	for range 2 {
		started[<-f.started] = true
	}
	if !started["hello"] || !started["guide"] {
		t.Fatalf("started=%v", started)
	}
	reply := rpc(t, h, "", protocol.MethodKeysList, map[string]any{})
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	var listing struct {
		Entries []struct {
			Action string          `json:"action"`
			Keys   []string        `json:"keys"`
			Hint   string          `json:"hint"`
			State  string          `json:"state"`
			Source protocol.Source `json:"source"`
		} `json:"entries"`
		Problems []protocol.Problem `json:"problems"`
	}
	if err := json.Unmarshal(reply.Result, &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Problems) != 0 {
		t.Fatalf("keymap parse problems=%v", listing.Problems)
	}
	for _, e := range listing.Entries {
		if e.Action == "guide.toggle" && len(e.Keys) == 1 && e.Keys[0] == "f2" && e.Hint == "guide" && e.Source.Layer == "distribution" && e.State == "effective" {
			return
		}
	}
	t.Fatalf("guide key not loaded: %s", reply.Result)
}
