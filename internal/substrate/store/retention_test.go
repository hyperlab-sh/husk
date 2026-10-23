package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

func TestDebounceQuietWindowAndMaximum(t *testing.T) {
	for _, maximum := range []bool{false, true} {
		t.Run(map[bool]string{false: "quiet", true: "maximum"}[maximum], func(t *testing.T) {
			e := fixture(t)
			s := e.open(t, "notes", nil)
			st := attach(t, s)
			h := slice(t, st, state.ScopeInstance, state.Retention{Policy: state.KeepLatest})
			set(t, h, `{"n":0}`)
			st.Flush()
			spec := st.Snapshot().Slices[h.Namespace()].Spec
			missing := func() {
				t.Helper()
				syncDue(t, s)
				if _, _, err := s.source.Get(context.Background(), s.sliceKey(spec, "current.json")); !errors.Is(err, ErrNotFound) {
					t.Fatal("persisted before deadline", err)
				}
			}
			missing()
			if !maximum {
				e.clock.Advance(499 * time.Millisecond)
				missing()
				set(t, h, `{"n":2}`)
				st.Flush()
				e.clock.Advance(499 * time.Millisecond)
				missing()
				e.clock.Advance(time.Millisecond)
				syncDue(t, s)
				if string(stored(t, s, spec, "current.json").Value) != `{"n":2}` {
					t.Fatal("quiet debounce did not keep latest")
				}
			} else {
				for i := 0; i < 12; i++ {
					e.clock.Advance(400 * time.Millisecond)
					set(t, h, `{"n":`+strings.Repeat("1", i+1)+`}`)
					st.Flush()
					missing()
				}
				e.clock.Advance(200 * time.Millisecond)
				syncDue(t, s)
				if _, _, err := s.source.Get(context.Background(), s.sliceKey(spec, "current.json")); err != nil {
					t.Fatal("5s maximum did not flush", err)
				}
			}
		})
	}
}
func TestRetentionPoliciesAndHydrateBeforeOwner(t *testing.T) {
	for _, r := range []state.Retention{{Policy: state.KeepLatest}, {Policy: state.Manual}, {Policy: state.KeepLastN, N: 3}, {Policy: state.TTL, TTL: time.Minute}} {
		t.Run(string(r.Policy), func(t *testing.T) {
			e := fixture(t)
			s := e.open(t, "notes", nil)
			st := attach(t, s)
			h := slice(t, st, state.ScopeInstance, r)
			for i := 1; i <= 5; i++ {
				e.clock.Advance(time.Second)
				set(t, h, `{"n":`+strings.Repeat("1", i)+`}`)
				st.Flush()
				if err := s.flushNow(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			spec := st.Snapshot().Slices[h.Namespace()].Spec
			entries, err := s.source.List(context.Background(), s.sliceKey(spec, ""))
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if r.Policy == state.KeepLastN {
				want = 3
			}
			if len(entries) != want {
				t.Fatalf("files %d want %d", len(entries), want)
			}
			if err = s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			next := e.open(t, "notes", nil)
			nextState := attach(t, next)
			owner := slice(t, nextState, state.ScopeInstance, r)
			entry := nextState.Snapshot().Slices[owner.Namespace()]
			if !entry.Hydrated || string(entry.Value) != `{"n":11111}` || entry.Revision != 1 {
				t.Fatalf("owner did not synchronously hydrate: %+v", entry)
			}
			if r.Policy == state.TTL {
				e.clock.Advance(time.Minute)
				if _, err = next.GC(context.Background()); err != nil {
					t.Fatal(err)
				}
				if value, ok := next.Hydrate(spec); ok {
					t.Fatalf("TTL loaded expired memory preload %s", value)
				}
				if _, _, err = next.source.Get(context.Background(), next.sliceKey(spec, "current.json")); !errors.Is(err, ErrNotFound) {
					t.Fatal("TTL not collected", err)
				}
			} else if r.Policy != state.KeepLastN {
				e.clock.Advance(400 * 24 * time.Hour)
				if _, err = next.GC(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, _, err = next.source.Get(context.Background(), next.sliceKey(spec, "current.json")); err != nil {
					t.Fatal("non-expiring policy collected", err)
				}
			}
		})
	}
}
func TestScopeIsolationAnonymousAndWorkspaceBatch(t *testing.T) {
	e := fixture(t)
	a := e.open(t, "one", nil)
	b := e.open(t, "two", nil)
	ast := attach(t, a)
	bst := attach(t, b)
	ah := slice(t, ast, state.ScopeWorkspace, state.Retention{Policy: state.KeepLatest})
	bh := slice(t, bst, state.ScopeWorkspace, state.Retention{Policy: state.KeepLatest})
	set(t, ah, `{"owner":"one"}`)
	set(t, bh, `{"owner":"two"}`)
	ast.Flush()
	bst.Flush()
	for _, s := range []*Store{a, b} {
		if err := s.flushNow(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if string(stored(t, a, ast.Snapshot().Slices[ah.Namespace()].Spec, "current.json").Value) != `{"owner":"one"}` || string(stored(t, b, bst.Snapshot().Slices[bh.Namespace()].Spec, "current.json").Value) != `{"owner":"two"}` {
		t.Fatal("workspace collision")
	}
	// Workspace batching preserves an unchanged peer without rewriting it.
	core, err := ast.Register(state.Spec{Namespace: "husk.workspace", Owner: "husk", Scope: state.ScopeWorkspace, Persistent: true, Retention: state.Retention{Policy: state.KeepLatest}, Initial: []byte(`{"core":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	ast.Flush()
	if err = a.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	coreSpec := ast.Snapshot().Slices[core.Namespace()].Spec
	_, coreBefore, err := a.source.Get(context.Background(), a.sliceKey(coreSpec, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)
	set(t, ah, `{"owner":"one-new"}`)
	ast.Flush()
	if err = a.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, coreAfter, err := a.source.Get(context.Background(), a.sliceKey(coreSpec, "current.json"))
	if err != nil || !coreAfter.Modified.Equal(coreBefore.Modified) {
		t.Fatal("unchanged workspace peer rewritten", err)
	}
	anonymous := e.open(t, "4242", nil)
	anonState := attach(t, anonymous)
	ih := slice(t, anonState, state.ScopeInstance, state.Retention{Policy: state.Manual})
	set(t, ih, `{"x":1}`)
	anonState.Flush()
	if err = anonymous.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(anonymous.paths.State, "instances", "4242")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pid instance accumulated state", err)
	}
	ih.Unregister()
	uh := slice(t, anonState, state.ScopeUser, state.Retention{Policy: state.Manual})
	set(t, uh, `{"shared":true}`)
	anonState.Flush()
	if err = anonymous.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored(t, anonymous, anonState.Snapshot().Slices[uh.Namespace()].Spec, "current.json").Value), "shared") {
		t.Fatal("anonymous user scope not persisted")
	}
}
func TestCorruptFutureAndUnknownRetentionSafety(t *testing.T) {
	for _, kind := range []string{"corrupt", "future", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			e := fixture(t)
			seed := e.open(t, "notes", nil)
			seed.Close(context.Background())
			key := Key{Scope: state.ScopeInstance, Instance: "notes", Path: []string{"slices", "plugin.state", "current.json"}}
			data := []byte("{")
			if kind != "corrupt" {
				version := 1
				if kind == "future" {
					version = 2
				}
				data, _ = json.Marshal(envelope{Version: version, Namespace: "plugin.state", Scope: state.ScopeInstance, Instance: "notes", WrittenAt: e.clock.Now(), Value: []byte(`{"saved":true}`)})
			}
			if err := seed.source.Put(context.Background(), key, data); err != nil {
				t.Fatal(err)
			}
			s := e.open(t, "notes", nil)
			st := attach(t, s)
			h := slice(t, st, state.ScopeInstance, state.Retention{Policy: state.Manual})
			entry := st.Snapshot().Slices[h.Namespace()]
			if kind == "legacy" {
				if !entry.Hydrated {
					t.Fatal("legacy value not hydrated")
				}
				return
			}
			if entry.Hydrated || string(entry.Value) != "{}" {
				t.Fatal(entry)
			}
			if kind == "corrupt" {
				check, err := s.Check(context.Background())
				if err != nil || len(check.Quarantined) != 1 {
					t.Fatal(check, err)
				}
				set(t, h, `{"fresh":true}`)
			}
			st.Flush()
			if err := s.flushNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			if kind == "future" {
				got, _, err := s.source.Get(context.Background(), key)
				if err != nil || string(got) != string(data) {
					t.Fatal("overwrote future file")
				}
				if err = s.ClearSlice(h.Namespace()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestClearUninstallAndUnregistrationRespectPersistence(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	st := attach(t, s)
	h := slice(t, st, state.ScopeInstance, state.Retention{Policy: state.Manual})
	set(t, h, `{"old":true}`)
	st.Flush()
	spec := st.Snapshot().Slices[h.Namespace()].Spec
	if err := s.ClearSlice(h.Namespace()); err != nil {
		t.Fatal(err)
	}
	if err := s.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.source.Get(context.Background(), s.sliceKey(spec, "current.json")); !errors.Is(err, ErrNotFound) {
		t.Fatal("pre-clear pending write resurrected", err)
	}
	if _, err := st.Clear(h.Namespace()); err != nil {
		t.Fatal(err)
	}
	st.Flush()
	if err := s.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(stored(t, s, spec, "current.json").Value) != "{}" {
		t.Fatal("clear reset not persisted")
	}
	set(t, h, `{"pending":true}`)
	st.Flush()
	h.Unregister()
	st.Flush()
	if err := s.flushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(stored(t, s, spec, "current.json").Value) != `{"pending":true}` {
		t.Fatal("unregistration dropped a pending committed value")
	}
	if err := s.RemovePlugin("plugin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.source.Get(context.Background(), s.sliceKey(spec, "current.json")); !errors.Is(err, ErrNotFound) {
		t.Fatal("uninstall left value")
	}
	if err := s.ClearSlice("../unsafe"); err == nil {
		t.Fatal("unsafe clear")
	}
}
