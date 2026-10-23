package pods

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// TestOpeningBlockedChild is a re-exec mode that never reads its stdin.
func TestOpeningBlockedChild(t *testing.T) {
	if os.Args[len(os.Args)-1] != "opening-blocked-child" {
		t.Skip("child process mode only")
	}
	r, w, err := os.Pipe()
	if err != nil {
		os.Exit(2)
	}
	_, _ = io.Copy(io.Discard, r)
	runtime.KeepAlive(w)
	os.Exit(0)
}

func TestWrapperOpeningFailureReportsExitOnlyOnce(t *testing.T) {
	h, pub, _ := publisherFixture(t)
	loop := h.loop.(*fakeLoop)
	dir := t.TempDir()
	var process wrapper.Process
	h.opts.Factory = func(s manifest.Spec, o wrapper.Options) (wrapper.Process, error) {
		o.LogPath = func(string) (string, error) {
			path := filepath.Join(dir, "alpha.log")
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return "", err
			}
			return path, f.Close()
		}
		var err error
		process, err = wrapper.New(s, o)
		return process, err
	}
	t.Cleanup(func() {
		if process != nil {
			process.MarkShutdown()
			process.CloseStdin()
			_ = process.Kill()
			testclock.Receive(t, process.Done())
		}
	})
	spec := manifestSpec("alpha")
	spec.Command = []string{os.Args[0], "-test.run=^TestOpeningBlockedChild$", "--", "opening-blocked-child"}
	p, err := h.AddPod(spec)
	if err != nil {
		t.Fatal(err)
	}
	entry := &addition{done: make(chan struct{}), result: contracts.PluginAddition{Added: []contracts.PluginAdded{{Plugin: "alpha"}}}}
	h.adding[1] = entry
	// Envelope validation succeeds before spawn; Send rejects the oversized
	// line afterward, deterministically exercising spawned-but-never-live.
	opening := protocol.Message{Method: protocol.MethodThemeChanged, Params: json.RawMessage(`{"padding":"` + strings.Repeat("x", protocol.MaxLineBytes) + `"}`)}
	if err := opening.Validate(); err != nil {
		t.Fatal(err)
	}
	err = h.Start("alpha", []protocol.Message{opening})
	if err == nil {
		t.Fatal("non-live Wrapper returned success")
	}
	h.Handle(LaunchFailedMsg{PluginID: "alpha", Err: err})
	h.finishAddition(1, addReports())
	firstError := p.info.LastError
	if entry.result.Added[0].Status != "failed" || firstError == "" {
		t.Fatalf("reply=%+v", entry.result)
	}
	testclock.Receive(t, process.Done())
	exitNotices := 0
drain:
	for {
		select {
		case msg := <-loop.events:
			if _, ok := msg.(ExitMsg); ok {
				exitNotices++
			}
			h.Handle(msg)
		default:
			break drain
		}
	}
	failedWrites, exited, errorLogs := 0, 0, 0
	for _, change := range loop.statuses {
		if change.Info.Status == wrapper.Failed {
			failedWrites++
			if change.Info.LastError != firstError {
				t.Errorf("reap replaced first failure: %+v", change.Info)
			}
		}
		if change.Info.Status == wrapper.Live {
			t.Error("opening failure briefly became live")
		}
	}
	for _, event := range pub.events {
		if event.name == PluginExitedEvent {
			exited++
		}
	}
	for _, log := range loop.logs {
		if log.Level == "error" {
			errorLogs++
		}
	}
	if exitNotices != 1 || exited != 1 || errorLogs != 1 || failedWrites != 2 || p.info.Status != wrapper.Failed || p.info.LastError != firstError || p.info.PID == 0 || p.info.ExitStatus == "" {
		t.Fatalf("notices=%d exited=%d errors=%d failed writes=%d info=%+v", exitNotices, exited, errorLogs, failedWrites, p.info)
	}
}
