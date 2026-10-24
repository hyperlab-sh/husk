package workbench

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestDumpFramePublicDemosPreserveFrames(t *testing.T) {
	for _, opt := range []Options{
		{Demo: "pubslots", Width: 120, Height: 36},
		{DemoFrame: true, HoldAlt: true},
		{DemoFrame: true, RailMode: "modal"},
		{DemoFrame: true, RailMode: "full-canvas"},
		{DemoFrame: true, RailMode: "stage"},
	} {
		t.Run(opt.Demo+"/"+opt.RailMode, func(t *testing.T) {
			m := newModel()
			if opt.Width > 0 {
				m.width, m.height = opt.Width, opt.Height
			}
			if opt.Demo != "" {
				m.recomputeLayout()
				m.seedPubslotsDemo()
			} else {
				m.altLatched, m.modifiers.Alt = opt.HoldAlt, opt.HoldAlt
				m.demoFrame = true
				m.setupDemoFrame(opt.RailMode)
			}
			got, err := DumpFrame(t.Context(), opt)
			if err != nil || got != m.View().Content {
				t.Fatalf("public demo changed its frame: error=%v", err)
			}
		})
	}
}

func TestDumpFramePublicHostLifecycle(t *testing.T) {
	m := goldenModel()
	fe := withFE(m)
	fe.pods = map[string]string{"alpha": "live"}
	fe.occ = map[protocol.SurfaceID]string{protocol.SurfaceMainStage: "alpha"}
	fe.publish()
	closed := false
	var loop contracts.Config
	opt := Options{Log: io.Discard, NewHost: func(cfg contracts.Config, opts ...host.Option) (host.Looped, error) {
		loop = cfg
		if cfg.Post == nil || cfg.Content == nil || len(opts) == 0 {
			t.Fatal("host factory did not receive loop callbacks and diagnostics option")
		}
		cfg.Content("alpha", protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage, Lines: []string{"PUBLIC-CONTENT"}})
		return closeHost{stepHost: stepHost{fe: fe}, close: func() {
			closed = true
			if loop.Post(struct{}{}) {
				t.Error("post gate still open at host close")
			}
		}}, nil
	}}
	frame, err := DumpFrame(t.Context(), opt)
	if err != nil || !strings.Contains(ansi.Strip(frame), "PUBLIC-CONTENT") || !closed {
		t.Fatalf("frame bytes=%d error=%v closed=%v", len(frame), err, closed)
	}
}

type closeHost struct {
	stepHost
	close func()
}

func (h closeHost) Close() error { h.close(); return nil }
func (h closeHost) Planned() int { return 0 }

func TestPublicRunnersReturnHostConstructionErrors(t *testing.T) {
	failure := errors.New("cannot construct host")
	opt := Options{NewHost: func(contracts.Config, ...host.Option) (host.Looped, error) { return nil, failure }}
	frame, err := DumpFrame(context.Background(), opt)
	if frame != "" || !errors.Is(err, failure) {
		t.Errorf("frame=%q error=%v", frame, err)
	}
	// Construction precedes opening the terminal, so this needs no TTY.
	if err := Run(context.Background(), opt); !errors.Is(err, failure) {
		t.Fatalf("Run error=%v", err)
	}
}
