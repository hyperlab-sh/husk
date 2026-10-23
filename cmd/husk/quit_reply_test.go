package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/internal/cli"
	"github.com/hyperlab-sh/husk/internal/instance"
)

// `husk rpc action.invoke` and `husk action invoke` of husk.quit each get the
// `{}` reply and exit 0, and only then does the instance shut down: the program ends and the entry and
// socket are gone. The host action must not close the Server inside the
// invocation, before the reply is written (instances.md, cross-table rule 8).
func TestQuitActionRepliesBeforeTheInstanceStops(t *testing.T) {
	forms := []struct {
		name string
		args func(id string) []string
	}{
		{"rpc action.invoke", func(id string) []string {
			return []string{"rpc", "action.invoke", `{"action":"husk.quit"}`, "--instance", id, "--format", "json"}
		}},
		{"action invoke", func(id string) []string {
			return []string{"action", "invoke", "husk.quit", "--instance", id, "--format", "json"}
		}},
	}
	for i, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			quitRepliesBeforeStop(t, "quitreply"+string(rune('a'+i)), f.args)
		})
	}
}

func quitRepliesBeforeStop(t *testing.T, id string, quitArgs func(id string) []string) {
	state0 := t.TempDir()
	t.Setenv("HOME", state0)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(state0, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(state0, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(state0, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(state0, "run"))

	dir := shortRuntimeDir(t, id)
	model := goldenModel()
	prog := newProgramRef()
	p := tea.NewProgram(probeModel{model},
		tea.WithContext(t.Context()),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignals(),
		tea.WithoutSignalHandler(),
		tea.WithWindowSize(goldenW, goldenH),
	)
	prog.set(p)
	h, err := newHost(prog, model, id, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	model.attach(h, prog.closeGate)
	ended := make(chan error, 1)
	go func() { _, err := p.Run(); ended <- err }()
	t.Cleanup(func() { prog.closeGate(); p.Kill() })

	run := func(args ...string) (code int, stdout, stderr string) {
		var out, errb bytes.Buffer
		code = cli.Main(t.Context(), args, cli.Env{
			Stdout:     &out,
			Stderr:     &errb,
			Stdin:      bytes.NewReader(nil),
			Getenv:     func(string) string { return "" },
			RuntimeDir: func() (string, bool, error) { return dir, true, nil },
		})
		return code, out.String(), errb.String()
	}

	// The host is listening once its entry is registered: wait on a loop round
	// trip between checks instead of spinning on connections.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(instance.SocketPath(dir, id)); err == nil {
			if entries, err := instance.List(dir); err == nil && len(entries) == 1 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the instance never registered")
		}
		pm := probeMsg{fn: func(*WorkbenchModel) {}, done: make(chan struct{})}
		p.Send(pm)
		<-pm.done
	}

	code, stdout, stderr := run(quitArgs(id)...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "{}" {
		t.Fatalf("reply = %q, want {}", got)
	}

	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("program: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the program did not end after husk.quit")
	}
	if !model.closed {
		t.Error("the host was not closed")
	}
	for _, path := range []string{instance.EntryPath(dir, id), instance.SocketPath(dir, id)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s left behind: %v", filepath.Base(path), err)
		}
	}
	if code, _, _ := run("ping", "--instance", id); code != 3 {
		t.Errorf("ping after quit exit %d, want 3", code)
	}
}
