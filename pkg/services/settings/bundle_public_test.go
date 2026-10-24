package settings

import (
	"context"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func TestBundleSettingPluginRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		layer  store.Layer
		value  string
		source string
	}{
		{name: "unset", source: "default"},
		{name: "user", layer: store.LayerUser, value: "/opt/example-bundle", source: "user"},
		{name: "instance", layer: store.LayerInstance, value: "/opt/instance-bundle", source: "instance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(Options{})
			if err != nil {
				t.Fatal(err)
			}
			in := c.ReloadInput{}
			if tc.layer != "" {
				in.ConfigLayers = []store.LayerDoc{{Layer: tc.layer, Doc: store.Doc{"husk": store.Doc{"bundle": tc.value}}}}
			}
			cand, rep, err := s.Rebuild(context.Background(), in)
			if err != nil || cand == nil {
				t.Fatalf("rebuild: %+v, %v", rep, err)
			}
			if !cand.Apply().Applied {
				t.Fatal("not applied")
			}
			// No declarations are needed to read a public host setting.
			for _, id := range []string{"reader", "Other.Reader"} {
				caller := protocol.Caller{Kind: protocol.CallerPlugin, PluginID: id}
				value, source, err := s.Get(caller, "husk.bundle")
				if err != nil || value != tc.value || source != tc.source {
					t.Errorf("plugin %s: get = %v, %q, %v; want %q, %q", id, value, source, err, tc.value, tc.source)
				}
				snap := s.SnapshotFor(caller)
				if value, ok := snap.Raw("husk.bundle"); !ok || value != tc.value || snap.Source("husk.bundle") != tc.source {
					t.Errorf("plugin %s: filtered bundle = %v, %t, %q", id, value, ok, snap.Source("husk.bundle"))
				}
			}
		})
	}
}

func TestBundleSettingPluginWriteStillDenied(t *testing.T) {
	s, writer, reloader := writeService(t)
	_, _, err := s.Set(context.Background(), protocol.Caller{Kind: protocol.CallerPlugin, PluginID: "reader"}, "husk.bundle", "/opt/replacement-bundle")
	setError(t, err, "denied", "user_only")
	if writer.calls != 0 || reloader.calls != 0 {
		t.Fatalf("plugin write reached persistence: writes=%d reloads=%d", writer.calls, reloader.calls)
	}
}
