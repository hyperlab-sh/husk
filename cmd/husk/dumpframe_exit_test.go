package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/workbench"
)

// The UI returns a frame and an error; only the binary decides exit status.
func TestDumpFrameMainOutputAndExitPolicy(t *testing.T) {
	for _, cause := range []string{
		"", "no plugin went live", "host stopped: bind failed",
		"timed out waiting for plugins", "--rail modal: no loaded plugin declares a modal rail item",
	} {
		t.Run(cause, func(t *testing.T) {
			var err error
			want := 0
			if cause != "" {
				err, want = errors.New(cause), 1
			}
			var out, errOut bytes.Buffer
			code := printDumpFrame("current frame", err, &out, &errOut)
			if code != want || out.String() != "current frame\n" {
				t.Fatalf("exit=%d frame=%q; want exit=%d and frame even on failure", code, out.String(), want)
			}
			if cause == "" {
				if errOut.Len() != 0 {
					t.Fatalf("success stderr=%q", errOut.String())
				}
			} else if errOut.String() != "husk: --dump-frame: "+cause+"\n" {
				t.Fatalf("diagnosis must appear exactly once: %q", errOut.String())
			}
		})
	}
}

func TestDumpFrameMainUsesWorkbench(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := dumpFrameMain(context.Background(), workbench.Options{DemoFrame: true}, &out, &errOut); code != 0 || out.Len() == 0 || errOut.Len() != 0 {
		t.Fatalf("demo exit=%d frame bytes=%d stderr=%q", code, out.Len(), errOut.String())
	}
	failure := errors.New("host construction failed")
	opt := workbench.Options{NewHost: func(contracts.Config, ...host.Option) (host.Looped, error) { return nil, failure }}
	out.Reset()
	if code := dumpFrameMain(context.Background(), opt, &out, &errOut); code != 1 || out.Len() != 0 || strings.Count(errOut.String(), failure.Error()) != 1 || errOut.String() != "husk: "+failure.Error()+"\n" {
		t.Fatalf("construction exit=%d stderr=%q", code, errOut.String())
	}
}
