package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

// Findings 1 and 6: registration and identical state are not new disk writes.
func TestReviewHydratedRegistrationDoesNotRewrite(t *testing.T) {
	for _, policy := range []state.Retention{{Policy: state.TTL, TTL: time.Minute}, {Policy: state.KeepLastN, N: 3}} {
		t.Run(string(policy.Policy), func(t *testing.T) {
			e := fixture(t)
			s := e.open(t, "notes", nil)
			st := attach(t, s)
			h := slice(t, st, state.ScopeInstance, policy)
			set(t, h, `{"n":1}`)
			st.Flush()
			if err := s.flushNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			spec := st.Snapshot().Slices[h.Namespace()].Spec
			before, err := s.source.List(context.Background(), s.sliceKey(spec, ""))
			if err != nil {
				t.Fatal(err)
			}
			original, _, err := s.source.Get(context.Background(), before[0].Key)
			if err != nil {
				t.Fatal(err)
			}
			s.Close(context.Background())
			e.clock.Advance(30 * time.Second)
			next := e.open(t, "notes", nil)
			ns := attach(t, next)
			nh := slice(t, ns, state.ScopeInstance, policy)
			if !ns.Snapshot().Slices[nh.Namespace()].Hydrated {
				t.Fatal("not hydrated")
			}
			ns.Flush()
			if err = next.flushNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			after, err := next.source.List(context.Background(), next.sliceKey(spec, ""))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("registration touched persistence: before=%v after=%v", before, after)
			}
			current, _, err := next.source.Get(context.Background(), before[0].Key)
			if err != nil || !bytes.Equal(original, current) {
				t.Error("registration replaced persisted bytes", err)
			}
			if policy.Policy == state.TTL {
				e.clock.Advance(30 * time.Second)
				if _, ok := next.Hydrate(spec); ok {
					t.Error("restart extended TTL")
				}
			}
		})
	}
}

type pausedWriteSource struct {
	Source
	started chan struct{}
	release chan struct{}
	once    sync.Once
	failure error
}

func (s *pausedWriteSource) Put(ctx context.Context, k Key, b []byte) error {
	first := false
	s.once.Do(func() { first = true })
	if first {
		close(s.started)
		<-s.release
		if s.failure != nil {
			return s.failure
		}
	}
	return s.Source.Put(ctx, k, b)
}
func pauseNextWrite(t *testing.T, s *Store, failure error) (*pausedWriteSource, func()) {
	t.Helper()
	writer := &pausedWriteSource{Source: s.source, started: make(chan struct{}), release: make(chan struct{}), failure: failure}
	s.writeSource = writer
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(writer.release) }) }
	t.Cleanup(release)
	return writer, release
}

// Finding 6: a failed old write must not suppress the next value's timer.
func TestReviewFailureMarksOnlyItsOwnGeneration(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	st := attach(t, s)
	h := slice(t, st, state.ScopeInstance, state.Retention{Policy: state.KeepLatest})
	writer, release := pauseNextWrite(t, s, errors.New("injected old-generation failure"))
	set(t, h, `{"n":1}`)
	st.Flush()
	done := make(chan error, 1)
	go func() { done <- s.flushNow(context.Background()) }()
	<-writer.started
	e.clock.Advance(100 * time.Millisecond)
	set(t, h, `{"n":2}`)
	st.Flush()
	release()
	if err := <-done; err == nil {
		t.Fatal("missing injected failure")
	}
	e.clock.Advance(quietWindow)
	syncDue(t, s)
	spec := st.Snapshot().Slices[h.Namespace()].Spec
	data, _, err := s.source.Get(context.Background(), s.sliceKey(spec, "current.json"))
	if err != nil || !bytes.Contains(data, []byte(`"n":2`)) {
		t.Fatal("newer generation did not debounce normally", err, string(data))
	}
}

// Finding 1: returning to the old baseline during I/O must not lose that value.
func TestReviewReturnToBaselineDuringInflightWrite(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	st := attach(t, s)
	h := slice(t, st, state.ScopeInstance, state.Retention{Policy: state.KeepLatest})
	set(t, h, `{"n":0}`)
	st.Flush()
	if err := s.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	writer, release := pauseNextWrite(t, s, nil)
	set(t, h, `{"n":1}`)
	st.Flush()
	done := make(chan error, 1)
	go func() { done <- s.flushNow(context.Background()) }()
	<-writer.started
	set(t, h, `{"n":0}`)
	st.Flush()
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := string(stored(t, s, st.Snapshot().Slices[h.Namespace()].Spec, "current.json").Value); got != `{"n":0}` {
		t.Fatal("lost return-to-baseline value", got)
	}
}

// Finding 13: teardown must preserve a previously registered slice's last change.
func TestReviewUnregisterPersistsPendingValue(t *testing.T) {
	for _, policy := range []state.Retention{{Policy: state.KeepLatest}, {Policy: state.KeepLastN, N: 2}, {Policy: state.TTL, TTL: time.Minute}, {Policy: state.Manual}} {
		t.Run(string(policy.Policy), func(t *testing.T) {
			e := fixture(t)
			s := e.open(t, "notes", nil)
			st := attach(t, s)
			h := slice(t, st, state.ScopeInstance, policy)
			set(t, h, `{"last":true}`)
			st.Flush()
			spec := st.Snapshot().Slices[h.Namespace()].Spec
			h.Unregister()
			st.Flush()
			if err := s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			entries, err := s.source.List(context.Background(), s.sliceKey(spec, ""))
			if err != nil || len(entries) != 1 {
				t.Fatal("unregister lost pending value", len(entries), err)
			}
			data, _, err := s.source.Get(context.Background(), entries[0].Key)
			if err != nil || !bytes.Contains(data, []byte(`"last":true`)) {
				t.Fatal("wrong pending value", err)
			}
		})
	}
}

// Finding 4: the evidence retention window starts when quarantine happens.
func TestReviewQuarantineRefreshesItsAge(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	s.Close(context.Background())
	key := Key{Scope: state.ScopeInstance, Instance: "notes", Path: []string{"slices", "plugin.state", "current.json"}}
	if err := s.source.Put(context.Background(), key, []byte("{broken")); err != nil {
		t.Fatal(err)
	}
	p, _ := s.source.path(key, false)
	old := e.clock.Now().Add(-100 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	next := e.open(t, "notes", nil)
	report, err := next.Check(context.Background())
	if err != nil || len(report.Quarantined) != 1 {
		t.Fatal("new evidence collected immediately", report, err)
	}
	info, err := os.Stat(report.Quarantined[0])
	if err != nil || !info.ModTime().Equal(e.clock.Now()) {
		t.Fatal("quarantine did not record its new age", err)
	}
	e.clock.Advance(29 * 24 * time.Hour)
	if _, err = next.GC(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(report.Quarantined[0]); err != nil {
		t.Fatal("evidence removed before 30 days", err)
	}
}
