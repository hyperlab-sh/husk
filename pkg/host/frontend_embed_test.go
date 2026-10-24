package host

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	portsstem "github.com/hyperlab-sh/husk/pkg/host/ports/stem"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/stem/eventbus"
)

func TestFrontendAddPlugin(t *testing.T) {
	h, f, fe := bootFrontend(t, nil)
	beta := writeFrontendPlugin(t, "beta", nil, nil)
	res, err := awaitFrontend(t, h, func(reply func(contracts.PluginAddition, error)) {
		fe.AddPlugin(contracts.PluginSource{Path: beta}, reply)
	})
	if err != nil || len(res.Added) != 1 || res.Added[0].Plugin != "beta" || len(res.Reports) != 5 {
		t.Fatalf("%+v %v", res, err)
	}
	<-f.started
	infos, err := h.root.pods.List(t.Context(), contracts.Caller{Kind: protocol.CallerCLI})
	if err != nil || len(infos) != 2 {
		t.Fatalf("%+v %v", infos, err)
	}
}

func TestFrontendAddPluginRefusals(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	_, err := awaitFrontend(t, h, func(reply func(contracts.PluginAddition, error)) {
		fe.AddPlugin(contracts.PluginSource{Path: "relative"}, reply)
	})
	if contracts.CodeOf(err) != string(protocol.CodeInvalidParams) {
		t.Fatalf("relative: %v", err)
	}
	// A duplicate id is refused with the CLI path's coded error.
	_, err = awaitFrontend(t, h, func(reply func(contracts.PluginAddition, error)) {
		fe.AddPlugin(contracts.PluginSource{Path: writeFrontendPlugin(t, "alpha", nil, nil)}, reply)
	})
	data, ok := err.(contracts.DataError)
	if err == nil || !ok || data.Data()["reason"] != "duplicate_plugin_id" {
		t.Fatalf("duplicate: %v", err)
	}
}

// emitAs publishes an event as the named plugin does over the wire.
func emitAs(t *testing.T, h *harness, plugin, name string, data map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(data)
	ev := portsstem.NewEvents(h.root.bus, h.root.loopSeam)
	if err := ev.Emit(t.Context(), contracts.Caller{Kind: contracts.CallerPlugin, PluginID: plugin}, name, raw); err != nil {
		t.Fatal(err)
	}
}

func TestFrontendWatchEvents(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	var got []protocol.EventDeliverParams
	var cancel func()
	h.loop(func() {
		var err error
		cancel, err = fe.WatchEvents("alpha.*", func(p protocol.EventDeliverParams) {
			if !h.root.onLoop() {
				t.Error("fn off-loop")
			}
			got = append(got, p)
		})
		if err != nil {
			t.Error(err)
		}
	})
	emitAs(t, h, "alpha", "alpha.saved", map[string]any{"n": 1})
	emitAs(t, h, "alpha", "alpha.other", map[string]any{"n": 2})
	h.loop(func() {})
	h.loop(func() {
		if len(got) != 2 || got[0].Name != "alpha.saved" || got[0].Source != "alpha" || got[1].Name != "alpha.other" {
			t.Errorf("got %+v", got)
		}
		if len(got) > 0 && got[0].Data["n"] != json.Number("1") && got[0].Data["n"] != float64(1) {
			t.Errorf("data %+v", got[0].Data)
		}
	})
	// Not matching: other namespace, never delivered.
	h.loop(func() { got = nil })
	h.loop(func() { h.root.bus.Publish(eventbus.Event{Name: "beta.saved", Source: "beta"}) })
	h.loop(func() {})
	h.loop(func() {
		if len(got) != 0 {
			t.Errorf("non-matching event delivered: %+v", got)
		}
	})
	h.loop(func() { cancel(); cancel() })
	emitAs(t, h, "alpha", "alpha.saved", nil)
	h.loop(func() {})
	h.loop(func() {
		if len(got) != 0 {
			t.Errorf("delivered after cancel: %+v", got)
		}
	})
}

func TestFrontendWatchEventsInvalidPattern(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	h.loop(func() {
		cancel, err := fe.WatchEvents("bad pattern *", func(protocol.EventDeliverParams) { t.Error("delivered") })
		if contracts.CodeOf(err) != string(protocol.CodeInvalidParams) || cancel != nil {
			t.Errorf("err=%v cancel=%v", err, cancel != nil)
		}
	})
}
