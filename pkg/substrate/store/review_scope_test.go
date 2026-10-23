package store

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

// Findings 1 and 13 also apply while a plugin changes registration scope.
func TestReviewUnregisteredPendingScopeSurvivesReregistration(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	st := attach(t, s)
	h := slice(t, st, state.ScopeInstance, state.Retention{Policy: state.Manual})
	set(t, h, `{"instance":1}`)
	st.Flush()
	oldSpec := st.Snapshot().Slices[h.Namespace()].Spec
	func() {
		s.ioMu.Lock()
		defer s.ioMu.Unlock()
		h.Unregister()
		st.Flush()
		h = slice(t, st, state.ScopeUser, state.Retention{Policy: state.Manual})
		set(t, h, `{"user":2}`)
		st.Flush()
	}()
	if err := s.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := string(stored(t, s, oldSpec, "current.json").Value); got != `{"instance":1}` {
		t.Fatal("old scope pending value lost", got)
	}
	if got := string(stored(t, s, st.Snapshot().Slices[h.Namespace()].Spec, "current.json").Value); got != `{"user":2}` {
		t.Fatal("new scope value lost", got)
	}
}
func TestReviewNormalizedHydrationPreservesOriginalFile(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	s.Close(context.Background())
	spec := state.Spec{Namespace: "plugin.state", Owner: "plugin", Scope: state.ScopeInstance, Persistent: true, Retention: state.Retention{Policy: state.Manual}}
	raw, _ := json.Marshal(envelope{Version: 1, Namespace: spec.Namespace, Scope: spec.Scope, Instance: "notes", Value: []byte(` { "z": 9007199254740993, "a": 1 } `)})
	key := s.sliceKey(spec, "current.json")
	if err := s.source.Put(context.Background(), key, raw); err != nil {
		t.Fatal(err)
	}
	_, before, err := s.source.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)
	next := e.open(t, "notes", nil)
	st := attach(t, next)
	h := slice(t, st, spec.Scope, spec.Retention)
	st.Flush()
	if err = next.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	actual, after, err := next.source.Get(context.Background(), key)
	if err != nil || !bytes.Equal(raw, actual) || !before.Modified.Equal(after.Modified) {
		t.Fatal("normalization alone rewrote file", err)
	}
	if got := string(st.Snapshot().Slices[h.Namespace()].Value); got != `{"a":1,"z":9007199254740993}` {
		t.Fatal("hydration precision or normalization lost", got)
	}
}
