package host

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/cli"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// cliRun runs one husk subcommand against the harness's runtime directory
// with an empty environment and returns its exit code and output.
func cliRun(h *harness, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = cli.Main(context.Background(), args, cli.Env{
		Stdout:     &out,
		Stderr:     &errb,
		Stdin:      bytes.NewReader(nil),
		Getenv:     func(string) string { return "" },
		RuntimeDir: func() (string, error) { return h.dir, nil },
	})
	return code, out.String(), errb.String()
}

// TestCLIAgainstRealHost drives `husk instances`, `husk ping` and `husk rpc`
// over the real Server socket of a running host.
func TestCLIAgainstRealHost(t *testing.T) {
	hook, ready := readyHook("")
	h, err := boot(t, nil, withHook(hook), WithVersion("9.9.9"))
	if err != nil {
		t.Fatal(err)
	}
	cancel, result := runHost(t, ready, h)
	defer cancel()

	t.Run("ping resolves the only instance by discovery", func(t *testing.T) {
		code, stdout, stderr := cliRun(h, "ping", "--format", "json")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		var ping protocol.SessionPingResult
		if err := json.Unmarshal([]byte(stdout), &ping); err != nil {
			t.Fatal(err)
		}
		if ping.ID != h.id || ping.PID != os.Getpid() || ping.HostVersion != "9.9.9" || ping.ProtocolVersion != protocol.Version {
			t.Fatalf("ping = %+v", ping)
		}
	})

	t.Run("ping in table form", func(t *testing.T) {
		code, stdout, stderr := cliRun(h, "ping", "--instance", h.id, "--format", "table")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
		if len(lines) != 2 || strings.Fields(lines[0])[0] != "ID" || strings.Fields(lines[1])[0] != h.id {
			t.Fatalf("table = %q", stdout)
		}
	})

	t.Run("rpc session.ping prints the raw result", func(t *testing.T) {
		code, stdout, stderr := cliRun(h, "rpc", "session.ping", "{}", "--format", "json")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		var ping protocol.SessionPingResult
		if err := json.Unmarshal([]byte(stdout), &ping); err != nil || ping.ID != h.id {
			t.Fatalf("stdout = %q (%v)", stdout, err)
		}
	})

	t.Run("rpc of an unknown method is the instance's error, exit 1", func(t *testing.T) {
		code, stdout, stderr := cliRun(h, "rpc", "nope.nothing", "--format", "json")
		if code != cli.ExitRemote || stdout != "" {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		var e struct{ Error struct{ Code string } }
		if err := json.Unmarshal([]byte(stderr), &e); err != nil || e.Error.Code != string(protocol.CodeUnknownMethod) {
			t.Fatalf("stderr = %q (%v)", stderr, err)
		}
	})

	t.Run("instances lists the host as live", func(t *testing.T) {
		code, stdout, stderr := cliRun(h, "instances", "--format", "json")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		var rows []struct{ ID, Status string }
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil || len(rows) != 1 || rows[0].ID != h.id || rows[0].Status != "live" {
			t.Fatalf("rows = %q (%v)", stdout, err)
		}
	})

	t.Run("after Close there is no instance", func(t *testing.T) {
		if err := h.root.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		code, _, stderr := cliRun(h, "ping", "--format", "table")
		if code != cli.ExitNoInstance || stderr != "husk: no instance is running\n" {
			t.Fatalf("exit %d stderr %q", code, stderr)
		}
		if _, err := os.Stat(instance.EntryPath(h.dir, h.id)); !os.IsNotExist(err) {
			t.Fatalf("entry left behind: %v", err)
		}
	})
}
