package pods

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type recordingConverter struct {
	calls    atomic.Int32
	spec     *manifest.Spec
	warnings []manifest.Warning
	err      error
}

func (c *recordingConverter) LoadPath(string) (*manifest.Spec, []manifest.Warning, error) {
	c.calls.Add(1)
	return c.spec, c.warnings, c.err
}
func (c *recordingConverter) LoadDir(string) ([]manifest.Result, error) {
	c.calls.Add(1)
	return nil, c.err
}

func TestAddAdmissionNeverConvertsInvalidOrNonCLICallers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source contracts.PluginSource
		caller contracts.Caller
		code   protocol.Code
		reason string
	}{
		{"both", contracts.PluginSource{Path: "/a", Dir: "/b"}, cliCaller, protocol.CodeInvalidParams, "path_and_dir"},
		{"neither", contracts.PluginSource{}, cliCaller, protocol.CodeInvalidParams, "missing_path"},
		{"relative", contracts.PluginSource{Path: "./a"}, cliCaller, protocol.CodeInvalidParams, "relative_path"},
		{"plugin", contracts.PluginSource{Path: "/a"}, contracts.Caller{Kind: contracts.CallerPlugin}, protocol.CodeDenied, ""},
		{"input", contracts.PluginSource{Path: "/a"}, contracts.Caller{Kind: contracts.CallerInput}, protocol.CodeDenied, ""},
		{"host", contracts.PluginSource{Path: "/a"}, contracts.Caller{Kind: contracts.CallerHost}, protocol.CodeDenied, ""},
		{"zero", contracts.PluginSource{Path: "/a"}, contracts.Caller{}, protocol.CodeDenied, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, _ := hostFixture(t)
			converter := &recordingConverter{}
			h.opts.Converter = converter
			h.loop = deadLoop{}
			_, err := h.Add(t.Context(), tc.caller, tc.source)
			wantAddError(t, err, tc.code, tc.reason)
			if converter.calls.Load() != 0 || len(h.pods) != 0 {
				t.Fatal("admission touched converter or Pods")
			}
		})
	}
}

func TestAddPermissionAndInvalidSpecFailuresAreConversion(t *testing.T) {
	for _, kind := range []string{"permission denied", "invalid spec", "no spec", "missing nested file"} {
		t.Run(kind, func(t *testing.T) {
			f, pending := additionFixture(t)
			c := &recordingConverter{}
			switch kind {
			case "permission denied":
				c.err = os.ErrPermission
			case "invalid spec":
				c.spec = &manifest.Spec{ID: "bad", Protocol: 0}
			case "missing nested file":
				c.err = &os.PathError{Op: "open", Path: "missing-dependency", Err: os.ErrNotExist}
			}
			f.onLoop(func() { f.h.opts.Converter = c })
			_, err := f.h.Add(t.Context(), cliCaller, contracts.PluginSource{Path: t.TempDir()})
			wantAddError(t, err, protocol.CodeFailed, "conversion")
			if c.calls.Load() != 1 {
				t.Fatal("converter calls", c.calls.Load())
			}
			select {
			case <-pending:
				t.Fatal("conversion failure reloaded")
			default:
			}
		})
	}
}

func TestAddPendingBatchDuplicateAndWhollyAdmissionRefusedDirectory(t *testing.T) {
	f, pending := additionFixture(t)
	root := t.TempDir()
	path := writeAddPlugin(t, root, "alpha", "alpha")
	first := startAdd(f, t.Context(), contracts.PluginSource{Path: path})
	gate := testclock.Receive(t, pending)
	_, err := f.h.Add(t.Context(), cliCaller, contracts.PluginSource{Path: path})
	wantAddError(t, err, protocol.CodeConflict, "duplicate_plugin_id")
	result, err := f.h.Add(t.Context(), cliCaller, contracts.PluginSource{Dir: root})
	if err != nil || len(result.Refused) != 1 || result.Refused[0].Code != "duplicate_plugin_id" || len(result.Added) != 0 || result.Reports == nil || len(result.Reports) != 0 {
		t.Fatalf("all refused = %+v / %v", result, err)
	}
	select {
	case <-pending:
		t.Fatal("refused batch reloaded")
	default:
	}
	gate.release <- Preparation{Reports: addReports()}
	if got := testclock.Receive(t, first); got.err != nil {
		t.Fatal(got.err)
	}
}

func TestAddWarningsAndStartFailureRemainSuccessfulOutcomes(t *testing.T) {
	f, pending := additionFixture(t)
	spec := manifestSpec("alpha")
	f.onLoop(func() {
		f.h.opts.Converter = &recordingConverter{spec: &spec, warnings: []manifest.Warning{{Code: "suggestion_ignored", Field: "shortcut", Message: "bind in a keymap"}}}
		f.h.opts.Starter = starterFunc(func(string, []protocol.Message) error { return errors.New("start refused") })
	})
	reply := startAdd(f, t.Context(), contracts.PluginSource{Path: t.TempDir()})
	gate := testclock.Receive(t, pending)
	gate.release <- Preparation{ReloadError: errors.New("completed with problems"), Reports: addReports()}
	got := testclock.Receive(t, reply)
	if got.err != nil || len(got.result.Added) != 1 || got.result.Added[0].Status != "failed" || len(got.result.Added[0].Warnings) != 1 || got.result.Added[0].Warnings[0].Code != "suggestion_ignored" || len(got.result.Reports) != 5 {
		t.Fatalf("reply = %+v", got)
	}
	f.onLoop(func() {
		if len(f.loop.logs) < 3 || f.h.pods["alpha"].info.Status != wrapper.Failed {
			t.Error("warnings/reload/start failure not logged")
		}
	})
}

func TestAddRefusesThroughoutLiveRemovalStopAndReload(t *testing.T) {
	f := newRemovalFixture(t)
	f.h.opts.Converter = manifest.NewRegistry()
	path := writeAddPlugin(t, t.TempDir(), "alpha", "alpha")
	f.add("alpha", true)
	process := f.procs["alpha"]
	stopEntered, releaseStop := make(chan struct{}), make(chan struct{})
	process.mu.Lock()
	process.termStep = func() { close(stopEntered); <-releaseStop }
	process.mu.Unlock()
	reply := f.remove(t.Context(), "alpha")
	testclock.Receive(t, stopEntered)
	_, err := f.h.Add(t.Context(), cliCaller, contracts.PluginSource{Path: path})
	wantAddError(t, err, protocol.CodeConflict, "removing")
	reloadEntered, releaseReload := make(chan struct{}), make(chan struct{})
	f.rec.mu.Lock()
	f.rec.reload = func(ctx context.Context) ([]contracts.ReloadReport, error) {
		close(reloadEntered)
		<-releaseReload
		return addReports(), nil
	}
	f.rec.mu.Unlock()
	close(releaseStop)
	testclock.Receive(t, reloadEntered)
	_, err = f.h.Add(t.Context(), cliCaller, contracts.PluginSource{Path: path})
	wantAddError(t, err, protocol.CodeConflict, "removing")
	close(releaseReload)
	if got := testclock.Receive(t, reply); got.err != nil {
		t.Fatal(got.err)
	}
	// Replacing a removed directory's executable is not part of conversion;
	// the same manifest is usable without changing any configuration.
	if _, err := os.Stat(filepath.Join(path, manifest.FileName)); err != nil {
		t.Fatal(err)
	}
}
