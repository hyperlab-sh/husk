package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// bootLoggedPlugin runs a host with plugin alpha whose Info carries log
// (nil: no log opened yet).
func bootLoggedPlugin(t *testing.T, log *string) (*harness, *fakes) {
	t.Helper()
	path := &atomic.Pointer[string]{}
	path.Store(log)
	hook, ready := readyHook("")
	f := newFakes()
	h, err := boot(t, func(c *contracts.Config) { pluginSettings(c)["paths"] = []string{alphaPlugin(t)} }, withHook(hook),
		WithFactory(func(spec manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
			p, err := f.factory(spec, o)
			return loggedProc{p, path}, err
		}))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	return h, f
}

// loggedProc reports a log path in its Info once the test sets one, as a
// Wrapper does after opening the log.
type loggedProc struct {
	wrapper.Process
	path *atomic.Pointer[string]
}

func (p loggedProc) Info() wrapper.Info {
	info := p.Process.Info()
	if s := p.path.Load(); s != nil {
		info.LogPath = *s
	}
	return info
}

// TestCLIPluginCommandsAgainstRealHost drives `husk plugin list` and
// `husk plugin log` over the real Server socket of a host with one plugin.
func TestCLIPluginCommandsAgainstRealHost(t *testing.T) {
	h, _ := bootLoggedPlugin(t, nil)

	t.Run("list in json", func(t *testing.T) {
		code, stdout, stderr := cliRun(h, "plugin", "list", "--format", "json")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		var rows []struct{ ID, Name, Status string }
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil || len(rows) != 1 || rows[0].ID != "alpha" || rows[0].Status != "live" {
			t.Fatalf("rows = %q (%v)", stdout, err)
		}
	})

	t.Run("list in table form has a header and the plugin", func(t *testing.T) {
		code, stdout, stderr := cliRun(h, "plugin", "list", "--format", "table")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
		if len(lines) != 2 || !strings.Contains(lines[0], "id") || !strings.Contains(lines[1], "alpha") {
			t.Fatalf("table = %q", stdout)
		}
	})

	t.Run("log before a log exists is not_found, exit 1", func(t *testing.T) {
		code, stdout, stderr := cliRun(h, "plugin", "log", "alpha", "--format", "json")
		var e struct{ Error struct{ Code string } }
		if code != output.ExitRemote || stdout != "" || json.Unmarshal([]byte(stderr), &e) != nil || e.Error.Code != string(protocol.CodeNotFound) {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
	})

	t.Run("log of an unknown plugin is not_found", func(t *testing.T) {
		code, _, stderr := cliRun(h, "plugin", "log", "nobody", "--format", "json")
		var e struct{ Error struct{ Code string } }
		if code != output.ExitRemote || json.Unmarshal([]byte(stderr), &e) != nil || e.Error.Code != string(protocol.CodeNotFound) {
			t.Fatalf("exit %d stderr %q", code, stderr)
		}
	})
}

// TestCLIPluginLogNamesTheWrappersLogFile: with a log path in the Pod's Info,
// `husk plugin log` prints it, in both formats.
func TestCLIPluginLogNamesTheWrappersLogFile(t *testing.T) {
	want := filepath.Join(t.TempDir(), "alpha.log")
	if err := os.WriteFile(want, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, _ := bootLoggedPlugin(t, &want)
	code, stdout, stderr := cliRun(h, "plugin", "log", "alpha", "--format", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var res struct{ Path string }
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || res.Path != want {
		t.Fatalf("stdout = %q (%v), want path %s", stdout, err, want)
	}
	code, stdout, _ = cliRun(h, "plugin", "log", "alpha", "--format", "table")
	if code != 0 || !strings.Contains(stdout, want) {
		t.Fatalf("table exit %d stdout %q", code, stdout)
	}
}
