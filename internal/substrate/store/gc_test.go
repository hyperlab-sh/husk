package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

func TestGCStaleTempsQuarantineLimitsAndCheckOrphans(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", true, nil)
	spec := state.Spec{Namespace: "old.state", Owner: "old", Scope: state.ScopeInstance, Persistent: true, Retention: state.Retention{Policy: state.Manual}}
	key := s.sliceKey(spec, "current.json")
	old := e.clock.Now().Add(-100 * 24 * time.Hour)
	data, _ := json.Marshal(envelope{Version: 1, Namespace: spec.Namespace, Scope: spec.Scope, Instance: "notes", WrittenAt: old, RegisteredAt: old, Value: []byte(`{}`)})
	if err := s.source.Put(context.Background(), key, data); err != nil {
		t.Fatal(err)
	}
	path, _ := s.source.path(key, false)
	dir := filepath.Dir(path)
	const oldTemp = ".current.json.tmp-1000000000"
	const freshTemp = ".current.json.tmp-1000000001"
	names := []string{oldTemp, freshTemp}
	for i := 0; i < 4; i++ {
		names = append(names, "current.json.corrupt-"+e.clock.Now().Add(time.Duration(i)).UTC().Format("20060102T150405.000000000Z"))
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("evidence"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	os.Chtimes(filepath.Join(dir, oldTemp), old, old)
	// New temps and quarantines are in injected-clock time, not wall-clock time.
	for _, name := range names[1:] {
		os.Chtimes(filepath.Join(dir, name), e.clock.Now(), e.clock.Now())
	}
	before, err := s.Check(context.Background())
	if err != nil || len(before.Orphans) != 1 || len(before.Quarantined) != 4 {
		t.Fatal(before, err)
	}
	if _, err = os.Stat(filepath.Join(dir, oldTemp)); err != nil {
		t.Fatal("Check deleted files", err)
	}
	report, err := s.GC(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Removed) != 2 {
		t.Fatal(report)
	}
	after, err := s.Check(context.Background())
	if err != nil || len(after.Quarantined) != 3 {
		t.Fatal(after, err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("unknown retention removed legacy value", err)
	}
	if _, err = os.Stat(filepath.Join(dir, freshTemp)); err != nil {
		t.Fatal("removed fresh temp", err)
	}
}
func TestPersistenceFailureCapAndRetryOnChange(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", true, nil)
	st := attach(t, s)
	h := slice(t, st, state.ScopeInstance, state.Retention{Policy: state.KeepLatest})
	set(t, h, `{"n":1}`)
	st.Flush()
	entry := st.Snapshot().Slices[h.Namespace()]
	path, err := s.source.path(s.sliceKey(entry.Spec, "current.json"), true)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err = os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err = s.flushNow(context.Background()); err == nil {
		t.Fatal("unsafe persistence succeeded")
	}
	os.Chmod(dir, 0700)
	syncDue(t, s)
	if _, _, err = s.source.Get(context.Background(), s.sliceKey(entry.Spec, "current.json")); !errors.Is(err, ErrNotFound) {
		t.Fatal("retried without change")
	}
	set(t, h, `{"n":2}`)
	st.Flush()
	if err = s.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	entry.MaxBytes = 2
	s.ioMu.Lock()
	err = s.persist(context.Background(), entry, e.clock.Now())
	s.ioMu.Unlock()
	if !errors.Is(err, state.ErrTooLarge) {
		t.Fatal("persist cap not rechecked", err)
	}
	if string(stored(t, s, entry.Spec, "current.json").Value) != `{"n":2}` {
		t.Fatal("failed cap write replaced previous value")
	}
}
func TestCloseDeadlineAndRuntimeFailure(t *testing.T) {
	e := fixture(t)
	opts := e.opts("notes", true)
	called := false
	if _, err := openStore(opts, func() (string, error) { called = true; return "", errors.New("unsafe runtime") }); err == nil || !called {
		t.Fatal("runtime error ignored")
	}
	s := e.open(t, "notes", true, nil)
	st := attach(t, s)
	h := slice(t, st, state.ScopeInstance, state.Retention{Policy: state.Manual})
	set(t, h, `{"n":1}`)
	st.Flush()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Wait for shutdown deterministically, with no sleep or timer.
	<-s.done
	if err := s.Close(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal("exit flush hid deadline", err)
	}
}
