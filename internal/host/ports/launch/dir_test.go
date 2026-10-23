package launch_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/ports/launch"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

func TestPodDirWrittenAtStartingAndRetained(t *testing.T) {
	for _, dir := range []string{"/plugins/alpha", ""} {
		t.Run(dir, func(t *testing.T) {
			tree := state.New(state.Options{})
			registry, err := launch.NewPodRegistry(tree)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Reserve(context.Background(), "alpha"); err != nil {
				t.Fatal(err)
			}
			loop := launch.Loop{Registry: registry}
			for _, status := range []wrapper.Liveness{wrapper.Starting, wrapper.Live, wrapper.Exited} {
				msg := pods.LivenessMsg{Info: wrapper.Info{PluginID: "alpha", Status: status}}
				if status == wrapper.Starting {
					msg.Dir = &dir
				}
				loop.Liveness(msg)
				var doc struct{ Plugins map[string]map[string]any }
				raw := tree.Snapshot().Slices[state.PodsNamespace].Value
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				value, present := doc.Plugins["alpha"]["dir"]
				if !present || value != dir || doc.Plugins["alpha"]["status"] != string(status) {
					t.Fatalf("%s", raw)
				}
			}
		})
	}
}
