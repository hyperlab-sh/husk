package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
)

// Drive the real startup and frontend refresh on one loop, without a renderer,
// polling timers or sleeps. The working command waits for stdin EOF on shutdown.
func TestPluginCommandStartupDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   os.FileMode
		status contracts.PodStatus
	}{
		{"missing", 0, contracts.PodFailed},
		{"not executable", 0o600, contracts.PodFailed},
		{"missing interpreter", 0o700, contracts.PodFailed},
		{"bare name not on PATH", 0, contracts.PodFailed},
		{"absolute external command", 0, contracts.PodFailed},
		{"dangling symlink", 0, contracts.PodFailed},
		{"working", 0o700, contracts.PodLive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			dir := filepath.Join(root, "plugin")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.status == contracts.PodLive {
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(exe, filepath.Join(dir, "plugin")); err != nil {
					t.Fatal(err)
				}
			} else if tc.mode != 0 {
				if err := os.WriteFile(filepath.Join(dir, "plugin"), []byte("not executable"), tc.mode); err != nil {
					t.Fatal(err)
				}
			}
			command := []string{"./plugin", "-test.run=TestPluginCommandChild", "--", "plugin-command-child"}
			switch tc.name {
			case "missing interpreter":
				if err := os.WriteFile(filepath.Join(dir, "plugin"), []byte("#!/nonexistent/interp\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "bare name not on PATH":
				t.Setenv("PATH", t.TempDir())
				command = []string{"husk-no-such-cmd-xyz"}
			case "absolute external command":
				command = []string{filepath.Join(root, "missing-external")}
			case "dangling symlink":
				if err := os.Symlink(filepath.Join(dir, "absent-target"), filepath.Join(dir, "plugin")); err != nil {
					t.Fatal(err)
				}
			}
			doc, err := json.Marshal(map[string]any{"manifest_version": 2, "protocol": 2, "id": "alpha", "name": "Alpha", "version": "1", "command": command})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, manifest.FileName), doc, 0o600); err != nil {
				t.Fatal(err)
			}
			m := goldenModel()
			var stderr bytes.Buffer
			m.logOut = &stderr
			posts := make(chan any, 128)
			var closed atomic.Bool
			ctx, cancel := context.WithCancel(t.Context())
			h, err := host.New(contracts.Config{
				InstanceID: "diagnostics", RuntimeDir: shortRuntimeDir(t, "diagnostics"),
				InstanceDocs: pluginFlagDocs(t, "--plugin", dir),
				Post: func(msg any) bool {
					if closed.Load() {
						return false
					}
					select {
					case posts <- msg:
						return true
					case <-ctx.Done():
						return false
					}
				},
			}, host.WithLogger(func(level, msg string) { m.logf("%s: %s", level, msg) }))
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			loop := h.(host.Looped)
			m.attach(loop, func() { closed.Store(true) })
			done := make(chan error, 1)
			go func() { done <- h.Run(ctx); close(done) }()
			defer func() {
				closed.Store(true)
				cancel()
				if err := h.Close(); err != nil {
					t.Error(err)
				}
				<-done
			}()
			for m.podStatus["alpha"] == "" || m.podStatus["alpha"] == contracts.PodStarting {
				select {
				case msg := <-posts:
					m.Update(msg)
				case err := <-done:
					t.Fatalf("host ended before startup: %v", err)
				case <-ctx.Done():
					t.Fatal("startup did not settle")
				}
			}
			if got := m.podStatus["alpha"]; got != tc.status {
				t.Fatalf("status = %q, want %q", got, tc.status)
			}
			var pods struct {
				Plugins map[string]struct {
					Status    string `json:"status"`
					LastError string `json:"last_error"`
				} `json:"plugins"`
			}
			if err := json.Unmarshal(m.fe.Snapshot().Slices["husk.pods"].Value, &pods); err != nil {
				t.Fatal(err)
			}
			lastError := pods.Plugins["alpha"].LastError
			var lines []string
			for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
				if strings.Contains(line, "alpha:") || strings.Contains(line, "plugin alpha") {
					lines = append(lines, line)
				}
			}
			if tc.status == contracts.PodLive {
				if lastError != "" || len(lines) != 0 {
					t.Errorf("live plugin: last_error=%q stderr=%q", lastError, lines)
				}
				return
			}
			var want string
			switch tc.name {
			case "missing", "dangling symlink":
				want = fmt.Sprintf("command %q not found in %s; build it first", "./plugin", dir)
			case "not executable":
				want = "fork/exec " + filepath.Join(dir, "plugin") + ": permission denied"
			case "missing interpreter":
				want = "fork/exec " + filepath.Join(dir, "plugin") + ": no such file or directory"
			case "bare name not on PATH":
				want = `exec: "husk-no-such-cmd-xyz": executable file not found in $PATH`
			case "absolute external command":
				want = "fork/exec " + command[0] + ": no such file or directory"
			}
			if lastError != want {
				t.Errorf("last_error = %q, want %q", lastError, want)
			}
			wantLine := "husk: error: alpha: launch failed: " + want
			if len(lines) != 1 || lines[0] != wantLine {
				t.Errorf("stderr lines = %q, want [%q]", lines, wantLine)
			}
		})
	}
}

// TestPluginCommandChild is the re-executed working plugin fixture.
func TestPluginCommandChild(t *testing.T) {
	if os.Args[len(os.Args)-1] != "plugin-command-child" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestPodTerminalTransitionsDoNotLog(t *testing.T) {
	for _, to := range []contracts.PodStatus{contracts.PodFailed, contracts.PodExited} {
		for _, from := range []contracts.PodStatus{"", contracts.PodStarting, contracts.PodLive} {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				m := goldenModel()
				var stderr bytes.Buffer
				m.logOut = &stderr
				m.notePodTransition("alpha", from, to, "failure cause")
				m.notePodTransition("alpha", to, to, "failure cause")
				if stderr.Len() != 0 {
					t.Errorf("frontend duplicated Pods terminal log: %q", stderr.String())
				}
				if to == contracts.PodFailed {
					if len(m.failures) != 1 || m.failures[0] != "alpha: failure cause" {
						t.Errorf("failure toasts = %q, want one with the original cause", m.failures)
					}
				} else if len(m.failures) != 0 {
					t.Errorf("clean exit raised failure toasts: %q", m.failures)
				}
			})
		}
	}
}
