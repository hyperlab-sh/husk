package store

import (
	"context"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

func TestFrozenClockGenerationsAndSameProcessRehydration(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", true, nil)
	st := attach(t, s)
	r := state.Retention{Policy: state.KeepLastN, N: 2}
	h := slice(t, st, state.ScopeInstance, r)
	for i := 1; i <= 12; i++ {
		set(t, h, `{"n":`+strings.Repeat("1", i)+`}`)
		st.Flush()
		if err := s.flushNow(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	spec := st.Snapshot().Slices[h.Namespace()].Spec
	entries, err := s.source.List(context.Background(), s.sliceKey(spec, ""))
	if err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}
	h.Unregister()
	st.Flush()
	h = slice(t, st, state.ScopeInstance, r)
	if got := string(st.Snapshot().Slices[h.Namespace()].Value); got != `{"n":111111111111}` {
		t.Fatal("same-process hydration stale", got)
	}
	s.Close(context.Background())
	next := e.open(t, "notes", true, nil)
	ns := attach(t, next)
	nh := slice(t, ns, state.ScopeInstance, r)
	if got := string(ns.Snapshot().Slices[nh.Namespace()].Value); got != `{"n":111111111111}` {
		t.Fatal("frozen-clock generation selection stale", got)
	}
	set(t, nh, `{"n":2}`)
	ns.Flush()
	if err = next.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	value, ok := next.Hydrate(spec)
	if !ok || string(value) != `{"n":2}` {
		t.Fatal("generation clock reset after reopen", string(value), ok)
	}
}
func TestLoweredCapHydratesFallbackAndOwnerStillRuns(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", true, nil)
	st := attach(t, s)
	h := slice(t, st, state.ScopeInstance, state.Retention{Policy: state.KeepLatest})
	set(t, h, `{"old":"larger than new cap"}`)
	st.Flush()
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := e.open(t, "notes", true, nil)
	ns := attach(t, next)
	owner, err := ns.Register(state.Spec{Namespace: "plugin.state", Owner: "plugin", Scope: state.ScopeInstance, Persistent: true, Retention: state.Retention{Policy: state.KeepLatest}, MaxBytes: 8})
	if owner == nil || err == nil || !strings.Contains(err.Error(), "discarded persisted") {
		t.Fatalf("lowered cap = %v, %v", owner, err)
	}
	if entry := ns.Snapshot().Slices[owner.Namespace()]; entry.Hydrated || string(entry.Value) != "{}" {
		t.Fatal(entry)
	}
	set(t, owner, `{"x":1}`)
	ns.Flush()
	if err = next.flushNow(context.Background()); err != nil {
		t.Fatal("owner cannot replace oversized old state", err)
	}
}
