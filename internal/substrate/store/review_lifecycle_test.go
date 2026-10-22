package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

// Finding 3: GC must protect live instances and every recent log, including empty logs.
func TestReviewLiveAndRecentLogsAreProtected(t *testing.T) {
	e := fixture(t)
	var pings atomic.Int32
	s := e.open(t, "notes", true, func(o *Options) {
		o.InstanceDeps = &instance.Deps{Ping: func(string) error { pings.Add(1); return nil }}
	})
	if err := instance.Register(s.RuntimeDir(), instance.Entry{ID: "alive", Name: "alive", PID: os.Getpid(), Socket: instance.SocketPath(s.RuntimeDir(), "alive"), StartedAt: e.clock.Now(), ProtocolVersion: "2.0"}); err != nil {
		t.Fatal(err)
	}
	writeLog := func(name string, age time.Duration, content string) string {
		t.Helper()
		p, err := pathInside(s.paths.State, []string{"logs", name, "plugin.log"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		at := e.clock.Now().Add(-age)
		os.Chtimes(p, at, at)
		os.Chtimes(filepath.Dir(p), at, at)
		return p
	}
	live := writeLog("alive", 40*24*time.Hour, "active log")
	freshEmpty := writeLog("quiet", time.Hour, "")
	if _, err := s.GC(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{live, freshEmpty} {
		if _, err := os.Stat(p); err != nil {
			t.Error("protected log removed", p, err)
		}
	}
	if pings.Load() == 0 {
		t.Error("GC did not consult instance liveness")
	}
}

// Finding 5: user clear is local; uninstall alone spans every named instance.
func TestReviewClearIsLocalAndUninstallIsGlobal(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "one", true, nil)
	keys := []Key{{Scope: state.ScopeUser, Path: []string{"slices", "plugin.state", "current.json"}}, {Scope: state.ScopeInstance, Instance: "one", Path: []string{"slices", "plugin.state", "current.json"}}, {Scope: state.ScopeWorkspace, Instance: "one", Path: []string{"plugin.state", "current.json"}}, {Scope: state.ScopeInstance, Instance: "two", Path: []string{"slices", "plugin.state", "current.json"}}, {Scope: state.ScopeWorkspace, Instance: "two", Path: []string{"plugin.state", "current.json"}}}
	for _, k := range keys {
		data, _ := json.Marshal(envelope{Version: 1, Namespace: "plugin.state", Scope: k.Scope, Instance: k.Instance, WrittenAt: e.clock.Now(), Value: []byte(`{"keep":true}`)})
		if err := s.source.Put(context.Background(), k, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ClearSlice("plugin.state"); err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		_, _, err := s.source.Get(context.Background(), k)
		if i < 3 && !errors.Is(err, ErrNotFound) {
			t.Errorf("calling scope not cleared: %v", err)
		}
		if i >= 3 && err != nil {
			t.Errorf("another instance cleared: %v", err)
		}
	}
	if err := s.RemovePlugin("plugin"); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if _, _, err := s.source.Get(context.Background(), k); !errors.Is(err, ErrNotFound) {
			t.Error("uninstall left scope", k, err)
		}
	}
}

// Finding 9: an unlink between ReadDir and Info is not a failed GC pass.
func TestReviewGCContinuesWhenFileDisappears(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", true, nil)
	victim := filepath.Join(s.paths.Data, "gone")
	if err := os.WriteFile(victim, []byte("gone"), 0600); err != nil {
		t.Fatal(err)
	}
	s.walkDir = func(root string, fn fs.WalkDirFunc) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if path == victim {
				if removeErr := os.Remove(path); removeErr != nil {
					t.Error(removeErr)
				}
			}
			return fn(path, d, err)
		})
	}
	if _, err := s.GC(context.Background()); err != nil {
		t.Fatal("disappearing file aborted GC", err)
	}
}

// Finding 10: readable malformed metadata is evidence, not a startup blocker.
func TestReviewCorruptMetadataIsQuarantined(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", true, nil)
	s.Close(context.Background())
	k := Key{Scope: state.ScopeInstance, Instance: "notes", Path: []string{"meta.json"}}
	if err := s.source.Put(context.Background(), k, []byte("{bad metadata")); err != nil {
		t.Fatal(err)
	}
	next := e.open(t, "notes", true, nil)
	data, _, err := next.source.Get(context.Background(), k)
	if err != nil || !json.Valid(data) {
		t.Fatal("metadata not replaced", err)
	}
	report, err := next.Check(context.Background())
	if err != nil || len(report.Quarantined) != 1 {
		t.Fatal("metadata evidence missing", report, err)
	}
}

// Finding 11: denied reads are unavailable, never proof that bytes are corrupt.
func TestReviewUnreadableSliceIsNotQuarantined(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", true, nil)
	s.Close(context.Background())
	k := Key{Scope: state.ScopeInstance, Instance: "notes", Path: []string{"slices", "plugin.state", "current.json"}}
	data, _ := json.Marshal(envelope{Version: 1, Namespace: "plugin.state", Scope: k.Scope, Instance: k.Instance, Value: []byte(`{"safe":true}`)})
	if err := s.source.Put(context.Background(), k, data); err != nil {
		t.Fatal(err)
	}
	p, _ := s.source.path(k, false)
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	next := e.open(t, "notes", true, nil)
	actual, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(actual, data) {
		t.Error("unreadable slice was moved or replaced", err)
	}
	report, err := next.Check(context.Background())
	if err != nil || len(report.Quarantined) != 0 {
		t.Error("read error was quarantined", report, err)
	}
	next.mu.Lock()
	diagnosed := false
	for _, problem := range next.health.Diagnostics {
		if problem.Code == "unavailable" {
			diagnosed = true
		}
	}
	next.mu.Unlock()
	if !diagnosed {
		t.Error("unavailable read not diagnosed")
	}
}

// Finding 12: a substring in an unrelated hidden filename does not confer ownership.
func TestReviewGCSweepsOnlyGeneratedTempNames(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", true, nil)
	f, err := os.CreateTemp(s.paths.Data, ".config.json.tmp-")
	if err != nil {
		t.Fatal(err)
	}
	own := f.Name()
	f.Close()
	unrelated := filepath.Join(s.paths.Data, ".draft.tmp-personal")
	os.WriteFile(unrelated, []byte("keep"), 0600)
	old := e.clock.Now().Add(-time.Hour)
	os.Chtimes(own, old, old)
	os.Chtimes(unrelated, old, old)
	if _, err = s.GC(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(own); !errors.Is(err, os.ErrNotExist) {
		t.Error("own stale temp retained", err)
	}
	if _, err = os.Stat(unrelated); err != nil {
		t.Error("unrelated hidden file deleted", err)
	}
}

// Finding 14: the runtime rule is no group/other bits, not exact owner bits.
func TestReviewOwnerOnlyRuntimeModeIsAccepted(t *testing.T) {
	e := fixture(t)
	runtime := filepath.Join(e.root, "runtime")
	if err := os.Mkdir(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runtime, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(runtime, 0700)
	opts := e.opts("notes", true)
	opts.Env.RuntimeDir = func() (string, error) { return runtime, nil }
	s, err := Open(opts)
	if err != nil {
		t.Fatal("owner-only runtime rejected", err)
	}
	s.Close(context.Background())
}
