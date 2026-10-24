package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/workbench"
)

func TestDumpFrameMainOverTheRealHost(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	for _, name := range []string{"CONFIG", "STATE", "DATA"} {
		t.Setenv("XDG_"+name+"_HOME", filepath.Join(root, name))
	}
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	// A configuration-only bundle supplies a deterministic host diagnostic,
	// without a plugin process, keyring, user configuration or launch delay.
	if err := os.WriteFile(filepath.Join(root, "husk-bundle.json"), []byte(`{"manifest_version":1,"id":"config-only"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := pluginFlags(t, "--bundle", root, "--plugin-dir", root)
	additions, err := pluginAdditions(fs)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut, log bytes.Buffer
	opt := workbench.Options{
		NewHost: hostFactory("dump-integration", shortRuntimeDir(t, "dump-integration"), pluginInstanceDocs(fs), nil, additions),
		Log:     &log,
	}
	code := dumpFrameMain(t.Context(), opt, &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q log=%q", code, errOut.String(), log.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 40 || ansi.StringWidth(lines[0]) != 144 {
		t.Fatalf("frame width=%d height=%d; want 144x40", ansi.StringWidth(lines[0]), len(lines))
	}
	if !strings.Contains(log.String(), "bundle config-only: no plugins") {
		t.Fatalf("host diagnostics did not reach Options.Log: %q", log.String())
	}
}

func TestPubslotsCommandIgnoresHoldAlt(t *testing.T) {
	var frames []string
	for _, hold := range []bool{false, true} {
		args := []string{"--demo", "pubslots", "--dump-frame"}
		if hold {
			args = append(args, "--hold-alt")
		}
		cmd := exec.CommandContext(t.Context(), os.Args[0], args...)
		cmd.Env = append(os.Environ(), "HUSK_TEST_RUN_MAIN=1", "HOME="+t.TempDir())
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		if err := cmd.Run(); err != nil || errOut.Len() != 0 {
			t.Fatalf("hold-alt=%v error=%v stderr=%q", hold, err, errOut.String())
		}
		if len(strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")) != 40 {
			t.Fatal("pubslots command lost its full-height frame")
		}
		frames = append(frames, out.String())
	}
	// The original command seeds pubslots before applying --hold-alt. Keep
	// that byte-for-byte command behavior, not just the public runner result.
	if frames[0] != frames[1] {
		t.Fatal("--demo pubslots --hold-alt --dump-frame changed the original unlatchable fixture")
	}
	// Digest of the pre-extraction command's complete ANSI output, including
	// the final newline. Neither command flag may change those bytes.
	const original = "2d1e5f6c1ede090df51601cd76881839cbc2a14770856e7ed638027553bed1d9"
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(frames[1]))); got != original {
		t.Fatalf("pubslots command digest=%s; want original %s", got, original)
	}
}

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
