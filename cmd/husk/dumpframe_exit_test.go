package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host"
)

// planHost is a stepHost that plans plugins and optionally fails to run.
type planHost struct {
	stepHost
	planned int
	runErr  error
}

func (h planHost) Planned() int { return h.planned }
func (h planHost) Run(ctx context.Context) error {
	if h.runErr != nil {
		return h.runErr
	}
	return h.stepHost.Run(ctx)
}

var _ host.Looped = planHost{}

// A run where no plugin went live exits 1, after printing the frame and the
// diagnosis, so scripts and CI see the failure.
func TestDumpFrameMainExitsNonZeroWhenNoPluginWentLive(t *testing.T) {
	m, prog := dumpModel()
	fe := withFE(m)
	m.attach(planHost{stepHost: stepHost{fe: fe}, planned: 1}, nil)
	feed(prog, starting(m, "broken"), func() { fe.pods["broken"] = "failed"; fe.publish() })
	var out, errOut bytes.Buffer
	code := dumpFrameMain(m, planHost{stepHost: stepHost{fe: fe}, planned: 1}, prog, "", &out, &errOut)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "no plugin went live") {
		t.Errorf("stderr %q lacks the diagnosis", errOut.String())
	}
	if got := len(strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")); got != goldenH {
		t.Errorf("the frame must still be printed, got %d rows", got)
	}
}

// A host that stops on its own (socket path too long, bind failure) fails the
// run at once instead of rendering an empty workbench.
func TestDumpFrameMainExitsNonZeroWhenHostStops(t *testing.T) {
	m, prog := dumpModel()
	h := planHost{stepHost: stepHost{fe: withFE(m)}, planned: 1, runErr: errors.New("start server: socket path is 116 bytes")}
	m.attach(h, nil)
	var out, errOut bytes.Buffer
	start := time.Now()
	code := dumpFrameMain(m, h, prog, "", &out, &errOut)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "host stopped") || !strings.Contains(errOut.String(), "socket path is 116 bytes") {
		t.Errorf("stderr %q lacks the host error", errOut.String())
	}
	if time.Since(start) > dumpFrameTimeout/2 {
		t.Error("the host failure waited out the settle window")
	}
}

// The cause of a stopped host reaches stderr once: the model's log and the
// run's error must not both print it.
func TestDumpFrameMainReportsAStoppedHostOnce(t *testing.T) {
	m, prog := dumpModel()
	var out, errOut bytes.Buffer
	m.logOut = &errOut
	h := planHost{stepHost: stepHost{fe: withFE(m)}, planned: 1, runErr: errors.New("start server: socket path is 116 bytes")}
	m.attach(h, nil)
	if code := dumpFrameMain(m, h, prog, "", &out, &errOut); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if n := strings.Count(errOut.String(), "socket path is 116 bytes"); n != 1 {
		t.Errorf("the cause printed %d times, want once: %q", n, errOut.String())
	}
}

func TestDumpFrameMainExitsZeroOnSuccess(t *testing.T) {
	m, prog := dumpModel()
	var out, errOut bytes.Buffer
	if code := dumpFrameMain(m, planHost{stepHost: stepHost{fe: withFE(m)}}, prog, "", &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, errOut.String())
	}
}
