package workbench

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// planHost publishes its scripted notices through the public constructor's
// callbacks, on the real program loop, and optionally fails to run.
type planHost struct {
	stepHost
	planned int
	runErr  error
	config  contracts.Config
	steps   func(contracts.Config) []hostStep
}

func (h planHost) Planned() int { return h.planned }
func (h planHost) newHost(cfg contracts.Config, _ ...host.Option) (host.Looped, error) {
	h.config = cfg
	fe := h.fe.(*fakeFE)
	fe.pods, fe.occ = map[string]string{}, map[protocol.SurfaceID]string{}
	return h, nil
}
func (h planHost) Run(ctx context.Context) error {
	if h.runErr != nil {
		return h.runErr
	}
	if h.steps != nil {
		for _, step := range h.steps(h.config) {
			if !h.config.Post(step) {
				return ctx.Err()
			}
		}
	}
	return h.stepHost.Run(ctx)
}

var _ host.Looped = planHost{}

func TestDumpFramePublicNoPluginWentLive(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	h := planHost{stepHost: stepHost{fe: fe}, planned: 1, steps: func(contracts.Config) []hostStep {
		return []hostStep{starting(m, "broken"), func() { fe.pods["broken"] = "failed"; fe.publish() }}
	}}
	frame, err := DumpFrame(t.Context(), Options{NewHost: h.newHost})
	if err == nil || !strings.Contains(err.Error(), "no plugin went live") {
		t.Fatalf("error=%v; want no plugin went live", err)
	}
	if got := len(strings.Split(frame, "\n")); got != goldenH {
		t.Errorf("the frame must still be returned, got %d rows", got)
	}
}

// A discovery refusal is not an empty plugin plan: it fails unless an
// accepted plugin goes live, retaining both the refusal and the frame.
func TestDumpFramePublicRefusalsRequireALivePlugin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refused bool
		live    bool
		failure bool
	}{
		{"empty plan", false, false, false},
		{"all refused before a Pod", true, false, true},
		{"live sibling", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := goldenModel()
			fe := withFE(m)
			if tc.refused {
				fe.Refused = []contracts.PluginRefusal{{Reason: "manifest requires name and version"}}
			}
			h := planHost{stepHost: stepHost{fe: fe}}
			if tc.live {
				h.planned = 1
				h.steps = func(cfg contracts.Config) []hostStep {
					return []hostStep{
						register(m, "healthy", []protocol.SurfaceID{protocol.SurfaceMainStage}),
						func() {
							cfg.Content("healthy", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage, Lines: []string{"HEALTHY-SIBLING"}})
						},
					}
				}
			}
			var errOut bytes.Buffer
			frame, err := DumpFrame(t.Context(), Options{NewHost: h.newHost, Log: &errOut})
			if (err != nil) != tc.failure {
				t.Errorf("error=%v want failure=%v; log=%q", err, tc.failure, errOut.String())
			}
			if tc.failure && (err == nil || !strings.Contains(err.Error(), "no plugin went live")) {
				t.Errorf("error lacks failure diagnosis: %v", err)
			}
			if tc.refused {
				if n := strings.Count(errOut.String(), "plugin refused: manifest requires name and version"); n != 1 {
					t.Errorf("refusal logged %d times, want once: %q", n, errOut.String())
				}
				if !strings.Contains(ansi.Strip(frame), "Plugin failed") {
					t.Error("frame lost the refusal toast")
				}
			}
			if tc.live && !strings.Contains(ansi.Strip(frame), "HEALTHY-SIBLING") {
				t.Error("frame lost the live sibling's content")
			}
			if got := len(strings.Split(frame, "\n")); got != goldenH {
				t.Errorf("frame has %d rows, want %d", got, goldenH)
			}
		})
	}
}

// A host that stops on its own fails the run at once, returning its cause and
// the current frame instead of waiting out the settle window.
func TestDumpFramePublicHostStops(t *testing.T) {
	m := goldenModel()
	h := planHost{stepHost: stepHost{fe: withFE(m)}, planned: 1, runErr: errors.New("start server: socket path is 116 bytes")}
	start := time.Now()
	frame, err := DumpFrame(t.Context(), Options{NewHost: h.newHost})
	if err == nil || !strings.Contains(err.Error(), "host stopped") || !strings.Contains(err.Error(), "socket path is 116 bytes") {
		t.Fatalf("error=%v; want host failure", err)
	}
	if frame == "" {
		t.Error("host failure lost the current frame")
	}
	if time.Since(start) > dumpFrameTimeout/2 {
		t.Error("the host failure waited out the settle window")
	}
}

// The stopped host's cause is returned once, not also printed by the UI's log.
func TestDumpFramePublicReportsAStoppedHostOnce(t *testing.T) {
	m := goldenModel()
	var errOut bytes.Buffer
	h := planHost{stepHost: stepHost{fe: withFE(m)}, planned: 1, runErr: errors.New("start server: socket path is 116 bytes")}
	frame, err := DumpFrame(t.Context(), Options{NewHost: h.newHost, Log: &errOut})
	if err == nil || frame == "" {
		t.Fatalf("error=%v frame bytes=%d", err, len(frame))
	}
	if n := strings.Count(err.Error()+errOut.String(), "socket path is 116 bytes"); n != 1 {
		t.Errorf("the cause appeared %d times, want once: error=%v log=%q", n, err, errOut.String())
	}
}

func TestDumpFramePublicSuccess(t *testing.T) {
	m := goldenModel()
	h := planHost{stepHost: stepHost{fe: withFE(m)}}
	var errOut bytes.Buffer
	frame, err := DumpFrame(t.Context(), Options{NewHost: h.newHost, Log: &errOut})
	if err != nil || frame == "" || errOut.Len() != 0 {
		t.Fatalf("error=%v frame bytes=%d log=%q", err, len(frame), errOut.String())
	}
}

func TestDumpFramePublicNoRailItemMatches(t *testing.T) {
	for _, mode := range []string{"modal", "full-canvas", "stage"} {
		t.Run(mode, func(t *testing.T) {
			m := goldenModel()
			h := planHost{stepHost: stepHost{fe: withFE(m)}, planned: 1, steps: func(cfg contracts.Config) []hostStep {
				return []hostStep{
					register(m, "alpha", []protocol.SurfaceID{protocol.SurfaceMainStage}),
					func() {
						cfg.Content("alpha", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage, Lines: []string{"ONLY-STAGE"}})
					},
				}
			}}
			frame, err := DumpFrame(t.Context(), Options{NewHost: h.newHost, RailMode: mode})
			want := "no loaded plugin declares a " + mode + " rail item"
			if err == nil || strings.Count(err.Error(), want) != 1 {
				t.Errorf("error=%v; want cause once: %s", err, want)
			}
			if !strings.Contains(ansi.Strip(frame), "ONLY-STAGE") {
				t.Errorf("the frame must still be returned:\n%s", ansi.Strip(frame))
			}
		})
	}
}
