package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/internal/host"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
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

// A discovery refusal is not an empty plugin plan: it fails unless an
// accepted plugin goes live, retaining both the refusal and the frame.
func TestDumpFrameMainRefusalsRequireALivePlugin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refused bool
		live    bool
		code    int
	}{
		{"empty plan", false, false, 0},
		{"all refused before a Pod", true, false, 1},
		{"live sibling", true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, prog := dumpModel()
			fe := withFE(m)
			if tc.refused {
				fe.Refused = []contracts.PluginRefusal{{Reason: "manifest requires name and version"}}
			}
			h := planHost{stepHost: stepHost{fe: fe}}
			if tc.live {
				h.planned = 1
				feed(prog,
					register(m, "healthy", []protocol.SurfaceID{protocol.SurfaceMainStage}),
					slotMsg(m, "healthy", protocol.SurfaceMainStage, "HEALTHY-SIBLING"),
				)
			}
			m.attach(h, nil)
			var out, errOut bytes.Buffer
			m.logOut = &errOut
			if code := dumpFrameMain(m, h, prog, "", &out, &errOut); code != tc.code {
				t.Errorf("exit code=%d want=%d; stderr=%q", code, tc.code, errOut.String())
			}
			if tc.code == 1 && !strings.Contains(errOut.String(), "no plugin went live") {
				t.Errorf("stderr lacks failure diagnosis: %q", errOut.String())
			}
			if tc.refused {
				if n := strings.Count(errOut.String(), "plugin refused: manifest requires name and version"); n != 1 {
					t.Errorf("refusal printed %d times, want once: %q", n, errOut.String())
				}
				if !strings.Contains(ansi.Strip(out.String()), "Plugin failed") {
					t.Error("frame lost the refusal toast")
				}
			}
			if tc.live && !strings.Contains(ansi.Strip(out.String()), "HEALTHY-SIBLING") {
				t.Error("frame lost the live sibling's content")
			}
			if got := len(strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")); got != goldenH {
				t.Errorf("frame has %d rows, want %d", got, goldenH)
			}
		})
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

// --rail with a placement no loaded plugin declares exits 1, with the cause
// once on stderr and the frame still on stdout.
func TestDumpFrameMainExitsNonZeroWhenNoRailItemMatches(t *testing.T) {
	for _, mode := range []string{"modal", "full-canvas", "stage"} {
		t.Run(mode, func(t *testing.T) {
			m, prog := dumpModel()
			h := planHost{stepHost: stepHost{fe: withFE(m)}, planned: 1}
			m.attach(h, nil)
			feed(prog,
				register(m, "alpha", []protocol.SurfaceID{protocol.SurfaceMainStage}),
				slotMsg(m, "alpha", protocol.SurfaceMainStage, "ONLY-STAGE"),
			)
			var out, errOut bytes.Buffer
			if code := dumpFrameMain(m, h, prog, mode, &out, &errOut); code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			want := "no loaded plugin declares a " + mode + " rail item"
			if n := strings.Count(errOut.String(), want); n != 1 {
				t.Errorf("stderr %q has the cause %d times, want once", errOut.String(), n)
			}
			if !strings.Contains(ansi.Strip(out.String()), "ONLY-STAGE") {
				t.Errorf("the frame must still be printed:\n%s", out.String())
			}
		})
	}
}
