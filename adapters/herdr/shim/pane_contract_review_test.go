package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/proxy"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestPaneActionReplyForEveryCaller(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caller protocol.Caller
	}{
		{"palette", protocol.Caller{Kind: protocol.CallerHost}},
		{"cli", protocol.Caller{Kind: protocol.CallerCLI}},
		{"keymap", protocol.Caller{Kind: protocol.CallerInput}},
		{"shim", protocol.Caller{Kind: protocol.CallerPlugin, PluginID: "other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, terminals := paneRig(t)
			r.start(t)
			r.host.until(t, protocol.MethodStateSet, "")
			id := protocol.NewStringID(tc.name)
			params, err := json.Marshal(map[string]any{"action": "demo.pane-panel", "args": map[string]any{"placement": "overlay"}, "caller": tc.caller})
			if err != nil {
				t.Fatal(err)
			}
			if err := protocol.NewWriter(r.in).Send(protocol.Message{ID: &id, Method: protocol.MethodActionInvoke, Params: params}); err != nil {
				t.Fatal(err)
			}
			var reply protocol.Message
			if err := r.reply.Decode(&reply); err != nil {
				t.Fatal(err)
			}
			if reply.Error != nil || string(reply.Result) != `{"type":"ok"}` {
				t.Fatalf("pane action reply=%s, error=%v", reply.Result, reply.Error)
			}
			// Successful admission has opened the surface and started its terminal,
			// but no process identity or ordinary action record escapes in the result.
			take(t, terminals)
			r.host.until(t, protocol.MethodSurfaceOpen, "")
		})
	}
}

func TestPaneStoppingDoesNotReportCommandCap(t *testing.T) {
	for _, commands := range []int{0, 32} {
		t.Run(fmt.Sprintf("commands=%d", commands), func(t *testing.T) {
			fixture, terminals := paneRig(t)
			data, err := os.ReadFile(filepath.Join(fixture.o.Dir, "herdr-plugin.toml"))
			if err != nil {
				t.Fatal(err)
			}
			m, err := manifest.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := &runner{o: fixture.o, m: m, id: "demo", ctx: ctx, cancel: cancel, log: fixture.logs,
				panes: map[protocol.SurfaceID]*pane{}, processes: map[Process]bool{}, paneGate: make(chan struct{}, 1), state: state{Surfaces: map[string]any{}}}
			socket, err := proxy.Start(ctx, proxy.Options{Dial: r.o.Client.Dial, Clock: r.o.Clock, Intercept: r.intercept})
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			r.proxy = socket
			original := fixture.host.call
			fixture.host.call = func(ctx context.Context, method string, params any) (json.RawMessage, error) {
				if method == protocol.MethodSessionPing {
					// Enter shutdown after the early admission check and context reads, just
					// before final command admission. No sleeps or scheduling race is needed.
					r.mu.Lock()
					r.stopping = true
					r.commands = commands
					r.mu.Unlock()
				}
				return original(ctx, method, params)
			}
			result, failure := r.openPane(ctx, "panel", paneArgs{}, "stopping")
			if result != nil || failure == nil || failure.Code != protocol.CodeTimeout {
				t.Fatalf("stopping pane result=%v, error=%v", result, failure)
			}
			if mapped := paneProxyError(failure); mapped.Code != string(protocol.CodeTimeout) {
				t.Fatalf("shutdown misreported as cap: %+v", mapped)
			}
			select {
			case <-terminals:
				t.Fatal("stopping shim spawned terminal")
			default:
			}
			fixture.host.until(t, protocol.MethodSurfaceClose, "")
			if len(r.panes) != 0 {
				t.Fatal("shutdown refusal retained surface")
			}
		})
	}
}
