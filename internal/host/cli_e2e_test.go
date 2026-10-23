package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/cli"
	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
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
		RuntimeDir: func() (string, bool, error) { return h.dir, true, nil },
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
		if code != output.ExitRemote || stdout != "" {
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
		if code != output.ExitNoInstance || stderr != "husk: no instance is running\n" {
			t.Fatalf("exit %d stderr %q", code, stderr)
		}
		if _, err := os.Stat(instance.EntryPath(h.dir, h.id)); !os.IsNotExist(err) {
			t.Fatalf("entry left behind: %v", err)
		}
	})
}

// cliRunEnv is cliRun with an environment: a plugin token makes the CLI that
// plugin (the caller identity is the token, not the binary).
func cliRunEnv(h *harness, env map[string]string, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = cli.Main(context.Background(), args, cli.Env{
		Stdout:     &out,
		Stderr:     &errb,
		Stdin:      bytes.NewReader(nil),
		Getenv:     func(k string) string { return env[k] },
		RuntimeDir: func() (string, bool, error) { return h.dir, true, nil },
	})
	return code, out.String(), errb.String()
}

// watchRun starts a streaming CLI command and returns a channel of its
// stdout lines and the channel carrying its exit code once the instance goes
// away. The writer is a pipe so lines can be read as they are printed.
func watchRun(t *testing.T, h *harness, args ...string) (lines <-chan string, exit <-chan int) {
	t.Helper()
	pr, pw := io.Pipe()
	ls := make(chan string, 64)
	ex := make(chan int, 1)
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			ls <- sc.Text()
		}
		close(ls)
	}()
	go func() {
		var errb bytes.Buffer
		code := cli.Main(context.Background(), args, cli.Env{
			Stdout:     pw,
			Stderr:     &errb,
			Stdin:      bytes.NewReader(nil),
			Getenv:     func(string) string { return "" },
			RuntimeDir: func() (string, bool, error) { return h.dir, true, nil },
		})
		_ = pw.Close()
		ex <- code
	}()
	return ls, ex
}

func nextLine(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatal("stream ended")
		}
		return l
	case <-t.Context().Done():
		t.Fatal("test ended while waiting for a stream line")
	}
	return ""
}

// writeStatePlugin is writePlugin plus a declared instance-scoped state slice,
// which is what lets the plugin (and so a CLI carrying its token) write
// <id>.state.
func writeStatePlugin(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"manifest_version": 2, "protocol": 2, "id": id, "name": id, "version": "1",
		"command":  []string{"./plugin"},
		"surfaces": []any{map[string]any{"id": "sidebar-list"}},
		"slice":    map[string]any{"scope": "instance", "persistent": false},
	})
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestStateStemCLIAgainstRealHost drives the state, action and event commands
// over the real Server socket of a running host.
func TestStateStemCLIAgainstRealHost(t *testing.T) {
	hook, ready := readyHook("")
	f := newFakes()
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{writeStatePlugin(t, "alpha")} },
		withHook(hook), WithFactory(f.factory), WithVersion("9.9.9"))
	if err != nil {
		t.Fatal(err)
	}
	cancel, result := runHost(t, ready, h)
	defer cancel()
	if id := <-f.started; id != "alpha" {
		t.Fatalf("started %q", id)
	}
	plugin := map[string]string{cli.EnvPluginToken: f.proc("alpha").Token()}

	quit := make(chan struct{})
	h.loop(func() {
		fe := h.root.Frontend()
		// Like cmd/husk's husk.quit: the host shuts down from inside the
		// handler, on the loop, before the action.invoke reply is written.
		if err := fe.RegisterHostAction("husk.quit", func(map[string]any) (map[string]any, error) {
			_ = h.root.Close()
			close(quit)
			return nil, nil
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("state lists the slice descriptors", func(t *testing.T) {
		code, stdout, stderr := cliRun(h, "state", "--format", "table")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if lines := strings.Split(stdout, "\n"); !strings.HasPrefix(lines[0], "SLICE") || !strings.Contains(stdout, "husk.pods") || !strings.Contains(stdout, "alpha.state") {
			t.Fatalf("stdout = %q", stdout)
		}
	})

	t.Run("state set from a bare shell is denied", func(t *testing.T) {
		code, _, stderr := cliRun(h, "state", "set", `{"slice":"alpha.state","value":{"n":0}}`, "--format", "json")
		if code != output.ExitRemote || !strings.Contains(stderr, `"code":"denied"`) {
			t.Fatalf("exit %d stderr %q", code, stderr)
		}
	})

	var stateWatchExit <-chan int // joined in the last subtest
	t.Run("state set, get and a watch seeing the change", func(t *testing.T) {
		lines, exit := watchRun(t, h, "state", "watch", "alpha.state", "--format", "json")
		var open struct {
			Slices []struct{ Slice string } `json:"slices"`
		}
		if err := json.Unmarshal([]byte(nextLine(t, lines)), &open); err != nil || len(open.Slices) != 1 || open.Slices[0].Slice != "alpha.state" {
			t.Fatalf("opening = %+v (%v)", open, err)
		}

		code, stdout, stderr := cliRunEnv(h, plugin, "state", "set", `{"slice":"alpha.state","value":{"n":1}}`, "--format", "json")
		if code != 0 {
			t.Fatalf("set: exit %d: %s", code, stderr)
		}
		var set struct {
			Slice    string
			Revision uint64
		}
		if err := json.Unmarshal([]byte(stdout), &set); err != nil || set.Slice != "alpha.state" || set.Revision == 0 {
			t.Fatalf("set result = %q (%v)", stdout, err)
		}

		var changed struct {
			Slice    string
			Revision uint64
			Value    map[string]int
		}
		if err := json.Unmarshal([]byte(nextLine(t, lines)), &changed); err != nil || changed.Slice != "alpha.state" || changed.Revision != set.Revision || changed.Value["n"] != 1 {
			t.Fatalf("watch line = %+v (%v)", changed, err)
		}

		code, stdout, stderr = cliRun(h, "state", "get", "alpha.state", "--format", "table")
		if code != 0 || stdout != `{"n":1}`+"\n" {
			t.Fatalf("get: exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		code, stdout, stderr = cliRun(h, "state", "get", "alpha.state", "/n", "--format", "table")
		if code != 0 || stdout != "1\n" {
			t.Fatalf("get pointer: exit %d stdout %q stderr %q", code, stdout, stderr)
		}

		code, _, stderr = cliRunEnv(h, plugin, "state", "patch", `{"slice":"alpha.state","patch":{"n":2}}`, "--if-revision", "999")
		if code != output.ExitRemote || !strings.Contains(stderr, "if_revision") {
			t.Fatalf("stale patch: exit %d stderr %q", code, stderr)
		}
		code, _, stderr = cliRunEnv(h, plugin, "state", "patch", `{"slice":"alpha.state","patch":{"n":2}}`, "--if-revision", strconv.FormatUint(set.Revision, 10))
		if code != 0 {
			t.Fatalf("patch: exit %d: %s", code, stderr)
		}
		if err := json.Unmarshal([]byte(nextLine(t, lines)), &changed); err != nil || changed.Value["n"] != 2 {
			t.Fatalf("watch line after patch = %+v (%v)", changed, err)
		}

		code, stdout, stderr = cliRun(h, "state", "dump", "--format", "json")
		if code != 0 || !strings.Contains(stdout, `"alpha.state"`) {
			t.Fatalf("dump: exit %d stdout %.80q stderr %q", code, stdout, stderr)
		}
		stateWatchExit = exit
	})

	t.Run("state clear is the user's", func(t *testing.T) {
		code, _, stderr := cliRunEnv(h, plugin, "state", "clear", "alpha.state", "--format", "json")
		if code != output.ExitRemote || !strings.Contains(stderr, `"code":"denied"`) {
			t.Fatalf("plugin clear: exit %d stderr %q", code, stderr)
		}
		code, _, stderr = cliRun(h, "state", "clear", "alpha.state", "--format", "json")
		if code != 0 {
			t.Fatalf("clear: exit %d: %s", code, stderr)
		}
		code, stdout, stderr := cliRun(h, "state", "get", "alpha.state", "--format", "table")
		if code != 0 || stdout != "{}\n" {
			t.Fatalf("after clear: exit %d stdout %q stderr %q, want the initial {}", code, stdout, stderr)
		}
	})

	t.Run("action list", func(t *testing.T) {
		code, stdout, stderr := cliRun(h, "action", "list", "--format", "json")
		if code != 0 || !strings.Contains(stdout, `"id":"husk.quit"`) {
			t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		code, stdout, _ = cliRun(h, "action", "list", "--owner", "nobody", "--format", "json")
		if code != 0 || stdout != "[]\n" {
			t.Fatalf("filtered: exit %d stdout %q", code, stdout)
		}
	})

	t.Run("action invoke of an unknown action is the instance's error", func(t *testing.T) {
		code, _, stderr := cliRun(h, "action", "invoke", "husk.nope", "--format", "json")
		if code != output.ExitRemote || !strings.Contains(stderr, `"code":"not_found"`) {
			t.Fatalf("exit %d stderr %q", code, stderr)
		}
	})

	t.Run("event emit is seen by event watch", func(t *testing.T) {
		lines, _ := watchRun(t, h, "event", "watch", "user.*", "--format", "json")
		if got := nextLine(t, lines); got != "{}" {
			t.Fatalf("opening = %q", got)
		}
		if code, _, stderr := cliRun(h, "event", "emit", "user.other", "--format", "json"); code != 0 {
			t.Fatalf("emit: exit %d: %s", code, stderr)
		}
		if code, _, stderr := cliRun(h, "event", "emit", "user.x", "--data", `{"a":1}`, "--format", "json"); code != 0 {
			t.Fatalf("emit: exit %d: %s", code, stderr)
		}
		var ev struct {
			Name, Source string
			Data         map[string]int
		}
		for _, want := range []string{"user.other", "user.x"} {
			if err := json.Unmarshal([]byte(nextLine(t, lines)), &ev); err != nil || ev.Name != want {
				t.Fatalf("event = %+v (%v), want %s", ev, err, want)
			}
		}
		if ev.Data["a"] != 1 {
			t.Fatalf("event = %+v", ev)
		}
		code, _, stderr := cliRun(h, "event", "emit", "state.changed", "--format", "json")
		if code != output.ExitRemote || !strings.Contains(stderr, `"code":"denied"`) {
			t.Fatalf("reserved name: exit %d stderr %q", code, stderr)
		}
	})

	t.Run("action invoke husk.quit ends the instance and every watch with exit 5", func(t *testing.T) {
		lines, exit := watchRun(t, h, "event", "watch", "user.*", "--format", "json")
		nextLine(t, lines) // opening

		code, _, stderr := cliRun(h, "action", "invoke", "husk.quit", "--format", "json")
		// Known failure, bead husk-f9p: the real handler closes the Server
		// before the reply is written, so the CLI sees the connection drop.
		// When that is fixed this must be exit 0 with the {} result; until
		// then the exact failure is pinned so it cannot pass vacuously and
		// fails loudly once the host is fixed.
		if code != output.ExitTransport || !strings.Contains(stderr, "connection closed by host") {
			t.Fatalf("invoke husk.quit: exit %d stderr %q; husk-f9p expects exit 5 until the host replies before shutdown (then expect exit 0)", code, stderr)
		}
		select {
		case <-quit:
		case <-t.Context().Done():
			t.Fatal("husk.quit handler never ran")
		}
		cancel()
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		for name, ch := range map[string]<-chan int{"event watch": exit, "state watch": stateWatchExit} {
			select {
			case code := <-ch:
				if code != output.ExitTransport {
					t.Fatalf("%s exit = %d, want %d", name, code, output.ExitTransport)
				}
			case <-t.Context().Done():
				t.Fatalf("%s outlived the instance", name)
			}
		}
	})
}
