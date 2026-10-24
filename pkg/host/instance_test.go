package host

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"testing/fstest"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestNewInstanceDefaultsAndDistribution(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(key, root)
	}
	dir := runtimeDir(t, "defaults")
	t.Setenv("XDG_RUNTIME_DIR", filepath.Dir(dir))
	resolved, _, err := instance.LookupRuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("stop after startup")
	var built *Root
	_, err = NewInstance(contracts.Config{
		Post:         func(any) bool { return false },
		Distribution: fstest.MapFS{"config.json": {Data: []byte(`{"theme":{"name":"charm-light"}}`)}},
	}, func(o *options) {
		o.afterStage = func(stage string) error {
			if stage == "startup" {
				return failure
			}
			return nil
		}
		o.afterFail = func(r *Root) { built = r }
	})
	if !errors.Is(err, failure) || built == nil {
		t.Fatalf("construction = %v, root = %v", err, built)
	}
	id := strconv.Itoa(os.Getpid())
	if built.cfg.InstanceID != id || built.cfg.RuntimeDir != resolved || built.cfg.Entry.ID != id || built.cfg.Entry.PID != os.Getpid() || built.cfg.Entry.Socket != instance.SocketPath(resolved, id) || built.cfg.Entry.ProtocolVersion != protocol.Version {
		t.Fatalf("launch defaults = %+v", built.cfg)
	}
	if got := built.rt.Settings.Snapshot().String("theme.name"); got != "charm-light" {
		t.Fatalf("embedded distribution theme = %q", got)
	}
}
