package pods

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type addReply struct {
	result contracts.PluginAddition
	err    error
}
type pendingAdd struct {
	ctx     context.Context
	ids     []string
	release chan Preparation
}

func addReports() []contracts.ReloadReport {
	reports := []contracts.ReloadReport{}
	for _, name := range []string{"profile", "settings", "theme", "keybinding", "secrets"} {
		reports = append(reports, contracts.ReloadReport{Service: name, Gen: 7, Applied: false})
	}
	return reports
}

func additionFixture(t *testing.T) (*removalFixture, <-chan pendingAdd) {
	t.Helper()
	f := newRemovalFixture(t)
	pending := make(chan pendingAdd, 8)
	f.onLoop(func() {
		f.h.opts.Converter = manifest.NewRegistry()
		f.loop.prepareFn = func(ctx context.Context, _ uint64, specs []manifest.Spec) Preparation {
			ids := []string{}
			for _, s := range specs {
				ids = append(ids, s.ID)
			}
			call := pendingAdd{ctx: ctx, ids: ids, release: make(chan Preparation, 1)}
			pending <- call
			select {
			case result := <-call.release:
				return result
			case <-ctx.Done():
				return Preparation{ReloadError: ctx.Err()}
			}
		}
	})
	return f, pending
}

func startAdd(f *removalFixture, ctx context.Context, source contracts.PluginSource) <-chan addReply {
	out := make(chan addReply, 1)
	go func() { result, err := f.h.Add(ctx, cliCaller, source); out <- addReply{result, err} }()
	return out
}

func writeAddPlugin(t *testing.T, root, name, id string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"manifest_version": 2, "protocol": protocol.Major, "id": id, "name": id, "version": "1", "command": []string{"plugin"}})
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func wantAddError(t *testing.T, err error, code protocol.Code, reason string) {
	t.Helper()
	if contracts.CodeOf(err) != string(code) {
		t.Fatalf("error = %v, want %s", err, code)
	}
	if reason != "" {
		data, ok := err.(contracts.DataError)
		if !ok || data.Data()["reason"] != reason {
			t.Fatalf("error data = %v, want %s", err, reason)
		}
	}
}

func TestAddPathConversionAndFailureChangesNothing(t *testing.T) {
	for _, kind := range []string{"directory", "manifest", "executable", "missing with space", "unclaimed", "malformed", "stat error"} {
		t.Run(kind, func(t *testing.T) {
			f, pending := additionFixture(t)
			root := t.TempDir()
			path := writeAddPlugin(t, root, "alpha", "alpha")
			code, reason := protocol.Code(""), ""
			switch kind {
			case "manifest":
				path = filepath.Join(path, manifest.FileName)
			case "executable":
				path = filepath.Join(root, "alpha-executable")
				if err := os.WriteFile(path, []byte("executable fixture"), 0700); err != nil {
					t.Fatal(err)
				}
			case "missing with space":
				path = filepath.Join(root, "missing argument")
				code = protocol.CodeNotFound
			case "unclaimed":
				path = filepath.Join(root, "unknown.txt")
				if err := os.WriteFile(path, []byte("unknown"), 0600); err != nil {
					t.Fatal(err)
				}
				code, reason = protocol.CodeFailed, "conversion"
			case "malformed":
				if err := os.WriteFile(filepath.Join(path, manifest.FileName), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				code, reason = protocol.CodeFailed, "conversion"
			case "stat error":
				path = filepath.Join(root, "cycle")
				if err := os.Symlink(path, path); err != nil {
					t.Fatal(err)
				}
				code, reason = protocol.CodeFailed, "conversion"
			}
			reply := startAdd(f, t.Context(), contracts.PluginSource{Path: path})
			if code != "" {
				got := testclock.Receive(t, reply)
				wantAddError(t, got.err, code, reason)
				f.onLoop(func() {
					if len(f.h.pods) != 0 || len(f.h.gates) != 0 || len(f.rec.order) != 0 || len(f.procs) != 0 {
						t.Error("failed conversion mutated instance")
					}
				})
				select {
				case <-pending:
					t.Fatal("conversion failure prepared a batch")
				default:
				}
				return
			}
			call := testclock.Receive(t, pending)
			call.release <- Preparation{Reports: addReports()}
			got := testclock.Receive(t, reply)
			if got.err != nil || len(got.result.Added) != 1 || got.result.Added[0].Status != "live" || got.result.Added[0].Warnings == nil || len(got.result.Reports) != 5 {
				t.Fatalf("reply = %+v / %v", got.result, got.err)
			}
		})
	}
}

func TestAddDirectoryRefusalsStayInDirectoryOrder(t *testing.T) {
	f, pending := additionFixture(t)
	root := t.TempDir()
	writeAddPlugin(t, root, "a-live", "loaded")
	bad := writeAddPlugin(t, root, "b-bad", "bad")
	if err := os.WriteFile(filepath.Join(bad, manifest.FileName), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	writeAddPlugin(t, root, "c-good", "alpha")
	writeAddPlugin(t, root, "d-duplicate", "alpha")
	if err := os.Mkdir(filepath.Join(root, "e-unclaimed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f-file"), []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	f.add("loaded", false)
	f.onLoop(func() { f.h.pods["loaded"].info.Status = wrapper.Live })
	reply := startAdd(f, t.Context(), contracts.PluginSource{Dir: root})
	call := testclock.Receive(t, pending)
	if !slices.Equal(call.ids, []string{"alpha"}) {
		t.Fatal(call.ids)
	}
	call.release <- Preparation{Reports: addReports()}
	got := testclock.Receive(t, reply)
	if got.err != nil || len(got.result.Added) != 1 {
		t.Fatalf("reply = %+v", got)
	}
	refused := got.result.Refused
	if len(refused) != 3 || refused[0].Code != "duplicate_plugin_id" || refused[1].Code != "conversion" || refused[2].Code != "duplicate_plugin_id" || refused[1].Path != bad || refused[1].Plugin != "" {
		t.Fatalf("refusals = %+v", refused)
	}
}

func TestAddEmptyOrWhollyRefusedDirectoryDoesNotReachLoopOrReload(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "bad manifest"}[bad], func(t *testing.T) {
			f, pending := additionFixture(t)
			root := t.TempDir()
			if bad {
				dir := writeAddPlugin(t, root, "bad", "bad")
				if err := os.WriteFile(filepath.Join(dir, manifest.FileName), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := f.loop.doing.Load()
			result, err := f.h.Add(t.Context(), cliCaller, contracts.PluginSource{Dir: root})
			if err != nil || len(result.Added) != 0 || result.Added == nil || result.Reports == nil || len(result.Reports) != 0 {
				t.Fatalf("reply = %+v / %v", result, err)
			}
			if f.loop.doing.Load() != before || f.rec.reloadCount() != 0 {
				t.Fatal("empty conversion reached loop or reload")
			}
			select {
			case <-pending:
				t.Fatal("empty batch prepared")
			default:
			}
		})
	}
}

func TestAddGateIsBatchScopedAndReplyFollowsEveryStart(t *testing.T) {
	f, pending := additionFixture(t)
	root := t.TempDir()
	// A startup Pod has not even requested its gate: an add must not sweep it.
	f.add("startup", false)
	a := startAdd(f, t.Context(), contracts.PluginSource{Path: writeAddPlugin(t, root, "a", "a")})
	ca := testclock.Receive(t, pending)
	b := startAdd(f, t.Context(), contracts.PluginSource{Path: writeAddPlugin(t, root, "b", "b")})
	cb := testclock.Receive(t, pending)
	if !slices.Equal(ca.ids, []string{"a"}) || !slices.Equal(cb.ids, []string{"b"}) || ca.ctx != f.h.ctx || cb.ctx != f.h.ctx {
		t.Fatal("batch/context mismatch", ca.ids, cb.ids)
	}
	for _, id := range []string{"a", "b", "startup"} {
		if status, _ := f.podStatus(id); status != wrapper.Starting {
			t.Fatal("gate opened before reload")
		}
	}
	ca.release <- Preparation{Reports: addReports()}
	got := testclock.Receive(t, a)
	if got.err != nil || !reflect.DeepEqual(got.result.Reports, addReports()) {
		t.Fatalf("reports = %+v / %v", got.result, got.err)
	}
	if status, _ := f.podStatus("a"); status != wrapper.Live {
		t.Fatal("reply preceded start")
	}
	if status, _ := f.podStatus("b"); status != wrapper.Starting {
		t.Fatal("first gate started second batch")
	}
	cb.release <- Preparation{Failed: map[string]error{"b": errors.New("slice failed")}, Reports: addReports()}
	second := testclock.Receive(t, b)
	if second.err != nil || second.result.Added[0].Status != "failed" {
		t.Fatalf("second reply = %+v", second)
	}
	f.onLoop(func() {
		if len(f.h.adding) != 0 || f.procs["b"] != nil || f.procs["startup"] != nil {
			t.Error("gate leaked or started failed/startup Pod")
		}
		if !slices.Equal(f.rec.order, []string{PluginAddedEvent, PluginExitedEvent, PluginAddedEvent}) {
			t.Error("addition events", f.rec.order)
		}
	})
}

func TestAddDuplicateAgainstEveryPodStatusAndStartup(t *testing.T) {
	for _, status := range []wrapper.Liveness{wrapper.Starting, wrapper.Live, wrapper.Exited, wrapper.Failed} {
		t.Run(string(status), func(t *testing.T) {
			f, pending := additionFixture(t)
			f.add("alpha", false)
			f.onLoop(func() { f.h.pods["alpha"].info.Status = status })
			path := writeAddPlugin(t, t.TempDir(), "alpha", "alpha")
			_, err := f.h.Add(t.Context(), cliCaller, contracts.PluginSource{Path: path})
			wantAddError(t, err, protocol.CodeConflict, "duplicate_plugin_id")
			if err.(contracts.DataError).Data()["plugin"] != "alpha" {
				t.Fatal(err)
			}
			select {
			case <-pending:
				t.Fatal("duplicate prepared")
			default:
			}
		})
	}
}

func TestAddRemovalWinsPendingGateAndOwnsIDUntilReloadDone(t *testing.T) {
	f, pending := additionFixture(t)
	path := writeAddPlugin(t, t.TempDir(), "alpha", "alpha")
	reply := startAdd(f, t.Context(), contracts.PluginSource{Path: path})
	call := testclock.Receive(t, pending)
	var rm *removal
	f.onLoop(func() {
		var err error
		rm, err = f.h.beginRemove("alpha")
		if err != nil {
			t.Error(err)
		}
	})
	_, err := f.h.Add(t.Context(), cliCaller, contracts.PluginSource{Path: path})
	wantAddError(t, err, protocol.CodeConflict, "removing")
	reloadEntered, release := make(chan struct{}), make(chan struct{})
	f.rec.mu.Lock()
	f.rec.reload = func(context.Context) ([]contracts.ReloadReport, error) {
		close(reloadEntered)
		<-release
		return addReports(), nil
	}
	f.rec.mu.Unlock()
	call.release <- Preparation{Reports: addReports()}
	got := testclock.Receive(t, reply)
	if got.err != nil || got.result.Added[0].Status != "removed" {
		t.Fatalf("add = %+v", got)
	}
	testclock.Receive(t, reloadEntered)
	_, err = f.h.Add(t.Context(), cliCaller, contracts.PluginSource{Path: path})
	wantAddError(t, err, protocol.CodeConflict, "removing")
	f.onLoop(func() {
		if f.procs["alpha"] != nil || slices.Contains(f.rec.order, PluginAddedEvent) || len(f.rec.undeclared) != 1 {
			t.Error("removed gate started or announced a Pod, or repeated teardown")
		}
	})
	close(release)
	testclock.Receive(t, rm.done)
	if rm.status != contracts.PodStarting {
		t.Fatal("removal status", rm.status)
	}
	again := startAdd(f, t.Context(), contracts.PluginSource{Path: path})
	next := testclock.Receive(t, pending)
	next.release <- Preparation{Reports: addReports()}
	if got := testclock.Receive(t, again); got.err != nil || got.result.Added[0].Status != "live" {
		t.Fatalf("re-add = %+v", got)
	}
}

func TestAddShutdownCancelsPendingBatchAndRefusesBeforeConversion(t *testing.T) {
	f, pending := additionFixture(t)
	path := writeAddPlugin(t, t.TempDir(), "alpha", "alpha")
	reply := startAdd(f, t.Context(), contracts.PluginSource{Path: path})
	call := testclock.Receive(t, pending)
	f.onLoop(func() { f.h.Stop() })
	got := testclock.Receive(t, reply)
	wantAddError(t, got.err, protocol.CodeCancelled, "shutdown")
	if got.result.Reports != nil {
		t.Fatal("shutdown returned reports")
	}
	// A missing path would otherwise be not_found, proving shutdown precedes conversion.
	_, err := f.h.Add(t.Context(), cliCaller, contracts.PluginSource{Path: filepath.Join(t.TempDir(), "absent")})
	wantAddError(t, err, protocol.CodeUnavailable, "shutdown")
	call.release <- Preparation{Reports: addReports()}
	awaitMsg[PreparedMsg](f)
	f.onLoop(func() {
		if len(f.procs) != 0 || len(f.h.adding) != 0 {
			t.Error("shutdown launched or leaked a batch")
		}
	})
}

func TestAddDisconnectedCallerDoesNotCancelBatch(t *testing.T) {
	f, pending := additionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	reply := startAdd(f, ctx, contracts.PluginSource{Path: writeAddPlugin(t, t.TempDir(), "alpha", "alpha")})
	call := testclock.Receive(t, pending)
	cancel()
	if got := testclock.Receive(t, reply); !errors.Is(got.err, context.Canceled) {
		t.Fatal(got.err)
	}
	if call.ctx.Err() != nil {
		t.Fatal("request cancellation ended Host worker")
	}
	call.release <- Preparation{Reports: addReports()}
	awaitMsg[PreparedMsg](f)
	f.onLoop(func() {
		if len(f.h.adding) != 0 || f.h.pods["alpha"].info.Status != wrapper.Live || !slices.Contains(f.rec.order, PluginAddedEvent) {
			t.Error("detached batch did not complete")
		}
	})
}

func TestAddDirectoryRootErrors(t *testing.T) {
	f, _ := additionFixture(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		code   protocol.Code
		reason string
	}{{file, protocol.CodeInvalidParams, "not_a_directory"}, {file + "missing", protocol.CodeNotFound, ""}} {
		_, err := f.h.Add(t.Context(), cliCaller, contracts.PluginSource{Dir: tc.path})
		wantAddError(t, err, tc.code, tc.reason)
	}
}
