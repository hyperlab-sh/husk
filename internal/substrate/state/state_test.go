package state_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

func TestRegistrationAndLimits(t *testing.T) {
	s := state.New(state.Options{})
	if snap := s.Snapshot(); snap.Seq != 0 || len(snap.Slices) != 0 {
		t.Fatal(snap)
	}
	h := mustRegister(t, s, baseSpec("husk.focus", "husk"))
	entry, ok := s.Get(h.Namespace())
	if !ok || entry.Revision != 1 || entry.Bytes != 2 || entry.Hydrated || string(entry.Value) != "{}" {
		t.Fatalf("initial entry: %+v", entry)
	}
	before := s.Snapshot()
	if duplicate, err := s.Register(baseSpec(h.Namespace(), "husk")); duplicate != nil || !errors.Is(err, state.ErrDuplicate) {
		t.Fatalf("duplicate = %v, %v", duplicate, err)
	}
	if s.Snapshot() != before {
		t.Fatal("duplicate changed snapshot")
	}
	if _, ok := s.Get("missing.state"); ok {
		t.Fatal("missing slice exists")
	}
	for i := 1; i < state.MaxSlices; i++ {
		mustRegister(t, s, baseSpec(fmt.Sprintf("p%d.state", i), fmt.Sprintf("p%d", i)))
	}
	before = s.Snapshot()
	if _, err := s.Register(baseSpec("overflow.state", "overflow")); !errors.Is(err, state.ErrTooMany) {
		t.Fatalf("cap: %v", err)
	}
	if s.Snapshot() != before {
		t.Fatal("limit failure changed snapshot")
	}
	h.Unregister()
	mustRegister(t, s, baseSpec("replacement.state", "replacement"))
	if len(s.Snapshot().Slices) != state.MaxSlices {
		t.Fatal("unregister did not free capacity")
	}
}

func TestWritesAreAtomicAndReadYourWrites(t *testing.T) {
	s := state.New(state.Options{})
	spec := baseSpec("p.state", "p")
	spec.MaxBytes = 16
	h := mustRegister(t, s, spec)
	for _, value := range []string{`{"n":1}`, `{"n":2}`, ` {"n":2} `} {
		before := s.Snapshot()
		rev, err := h.Set([]byte(value))
		if err != nil {
			t.Fatal(err)
		}
		entry := s.Snapshot().Slices[h.Namespace()]
		if entry.Revision != rev || rev != before.Slices[h.Namespace()].Revision+1 || string(entry.Value) != value || entry.Bytes != len(value) || s.Snapshot().Seq != before.Seq+1 {
			t.Fatalf("not published synchronously: %+v", entry)
		}
	}
	before := s.Snapshot()
	rev := before.Slices[h.Namespace()].Revision
	if got, err := h.Set([]byte(` {"n":2} `)); err != nil || got != rev || s.Snapshot() != before {
		t.Fatal("identical encoding must be no-op")
	}
	for _, tc := range []struct {
		value string
		err   error
	}{
		{"", state.ErrInvalidValue}, {"null", state.ErrInvalidValue}, {"[]", state.ErrInvalidValue},
		{"1", state.ErrInvalidValue}, {`"str"`, state.ErrInvalidValue}, {"true", state.ErrInvalidValue},
		{"{", state.ErrInvalidValue}, {"{}{}", state.ErrInvalidValue}, {`{"long":"123456789"}`, state.ErrTooLarge},
	} {
		t.Run(tc.value, func(t *testing.T) {
			if got, err := h.Set([]byte(tc.value)); !errors.Is(err, tc.err) || got != rev {
				t.Fatalf("Set = %d, %v", got, err)
			}
			if s.Snapshot() != before {
				t.Fatal("rejected write changed snapshot")
			}
		})
	}
}

func TestCapabilitiesAndTombstones(t *testing.T) {
	s := state.New(state.Options{})
	a := mustRegister(t, s, baseSpec("a.state", "a"))
	b := mustRegister(t, s, baseSpec("b.state", "b"))
	if a.Namespace() != "a.state" || b.Namespace() != "b.state" {
		t.Fatal("capability namespace")
	}
	if _, err := a.Set([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if string(s.Snapshot().Slices[b.Namespace()].Value) != "{}" {
		t.Fatal("foreign handle wrote b")
	}
	zero := new(state.Handle)
	var nilHandle *state.Handle
	for _, invalid := range []*state.Handle{zero, nilHandle} {
		if invalid.Namespace() != "" {
			t.Fatal("zero namespace")
		}
		if _, err := invalid.Set([]byte(`{}`)); !errors.Is(err, state.ErrUnregistered) {
			t.Fatal(err)
		}
		if _, err := invalid.Patch([]byte(`{}`), 0); !errors.Is(err, state.ErrUnregistered) {
			t.Fatal(err)
		}
		invalid.Unregister()
	}
	oldRev := s.Snapshot().Slices[a.Namespace()].Revision
	a.Unregister()
	afterRemoval := s.Snapshot()
	a.Unregister()
	if s.Snapshot() != afterRemoval {
		t.Fatal("repeated unregister changed snapshot")
	}
	if _, ok := s.Get(a.Namespace()); ok {
		t.Fatal("removed slice exists")
	}
	replacement := mustRegister(t, s, baseSpec(a.Namespace(), "a"))
	if got := s.Snapshot().Slices[a.Namespace()].Revision; got <= oldRev {
		t.Fatalf("revision went backwards: %d <= %d", got, oldRev)
	}
	if _, err := a.Set([]byte(`{"evil":true}`)); !errors.Is(err, state.ErrUnregistered) {
		t.Fatal("stale handle wrote replacement", err)
	}
	if _, err := a.Patch([]byte(`{}`), 0); !errors.Is(err, state.ErrUnregistered) {
		t.Fatal(err)
	}
	a.Unregister()
	if _, err := replacement.Set([]byte(`{"new":true}`)); err != nil {
		t.Fatal("stale handle removed replacement", err)
	}
}

func TestFlushCoalescesOrdersAndRemoves(t *testing.T) {
	s := state.New(state.Options{})
	var batches [][]state.Change
	var snapshots []*state.Snapshot
	s.OnFlush(nil)
	s.OnFlush(func(changes []state.Change, snap *state.Snapshot) {
		batches = append(batches, append([]state.Change(nil), changes...))
		snapshots = append(snapshots, snap)
	})
	secondCalls := 0
	s.OnFlush(func(changes []state.Change, snap *state.Snapshot) {
		secondCalls++
		if !reflect.DeepEqual(changes, batches[len(batches)-1]) || snap != snapshots[len(snapshots)-1] {
			t.Fatal("listeners saw different batches")
		}
	})
	s.Flush()
	b := mustRegister(t, s, baseSpec("b.state", "b"))
	a := mustRegister(t, s, baseSpec("a.state", "a"))
	for i := 1; i <= 10; i++ {
		if _, err := b.Set(fmt.Appendf(nil, `{"n":%d}`, i)); err != nil {
			t.Fatal(err)
		}
	}
	s.Flush()
	want := []state.Change{{Namespace: "a.state", Owner: "a", Revision: 1}, {Namespace: "b.state", Owner: "b", Revision: 11}}
	if len(batches) != 1 || !reflect.DeepEqual(batches[0], want) || secondCalls != 1 {
		t.Fatalf("batch: %+v", batches)
	}
	if snapshots[0] != s.Snapshot() {
		t.Fatal("listener snapshot not current")
	}
	s.Flush()
	if len(batches) != 1 {
		t.Fatal("empty flush replayed changes")
	}
	if _, err := a.Set([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	s.Flush()
	if len(batches) != 1 {
		t.Fatal("no-op emitted change")
	}
	if _, err := a.Set([]byte(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	a.Unregister()
	s.Flush()
	want = []state.Change{{Namespace: "a.state", Owner: "a", Revision: 2, Removed: true}}
	if !reflect.DeepEqual(batches[1], want) {
		t.Fatalf("removed batch: %+v", batches[1])
	}
	if _, ok := snapshots[1].Slices["a.state"]; ok {
		t.Fatal("removed entry in flush snapshot")
	}
	// Unregister then re-register within a single Update is one net creation,
	// not a removal followed by a second event.
	b.Unregister()
	mustRegister(t, s, baseSpec("b.state", "b"))
	s.Flush()
	if len(batches[2]) != 1 || batches[2][0].Removed || batches[2][0].Revision != 12 {
		t.Fatal(batches[2])
	}
	if snapshots[0].Slices["a.state"].Revision != 1 {
		t.Fatal("retained flush snapshot changed")
	}
}

func TestSnapshotsAndInputAreIsolated(t *testing.T) {
	s := state.New(state.Options{})
	initial := []byte(`{"initial":1}`)
	spec := baseSpec("p.state", "p")
	spec.Initial = initial
	h := mustRegister(t, s, spec)
	first := s.Snapshot()
	initial[2] = 'X'
	value := []byte(`{"next":2}`)
	if _, err := h.Set(value); err != nil {
		t.Fatal(err)
	}
	second := s.Snapshot()
	value[2] = 'X'
	get, _ := s.Get(h.Namespace())
	get.Initial[2], get.Value[2] = 'X', 'X'
	if string(first.Slices[h.Namespace()].Value) != `{"initial":1}` || string(second.Slices[h.Namespace()].Value) != `{"next":2}` || string(second.Slices[h.Namespace()].Initial) != `{"initial":1}` {
		t.Fatal("alias corrupted snapshot")
	}
	if _, err := s.Clear(h.Namespace()); err != nil {
		t.Fatal(err)
	}
	if string(s.Snapshot().Slices[h.Namespace()].Value) != `{"initial":1}` {
		t.Fatal("Initial was not isolated")
	}
	h.Unregister()
	if len(first.Slices) != 1 || len(second.Slices) != 1 || len(s.Snapshot().Slices) != 0 {
		t.Fatal("snapshots not independent")
	}
}

func TestClearBothPersistenceClasses(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint(persistent), func(t *testing.T) {
			s := state.New(state.Options{})
			spec := baseSpec("p.state", "p")
			spec.Initial = []byte(`{"n":0}`)
			spec.Persistent = persistent
			if persistent {
				spec.Retention.Policy = state.Manual
			}
			h := mustRegister(t, s, spec)
			if _, err := h.Set([]byte(`{"n":1}`)); err != nil {
				t.Fatal(err)
			}
			s.Flush()
			var changes []state.Change
			s.OnFlush(func(c []state.Change, _ *state.Snapshot) { changes = append(changes, c...) })
			for wantRev := uint64(3); wantRev <= 4; wantRev++ {
				rev, err := s.Clear(h.Namespace())
				entry := s.Snapshot().Slices[h.Namespace()]
				if err != nil || rev != wantRev || entry.Revision != rev || string(entry.Value) != `{"n":0}` {
					t.Fatalf("Clear = %d, %v, %+v", rev, err, entry)
				}
				s.Flush()
			}
			if len(changes) != 2 || changes[1].Revision != 4 {
				t.Fatal(changes)
			}
			h.Unregister()
			before := s.Snapshot()
			if _, err := s.Clear(h.Namespace()); !errors.Is(err, state.ErrUnregistered) || s.Snapshot() != before {
				t.Fatal("clear missing slice", err)
			}
		})
	}
}

type hydratorFunc func(state.Spec) (json.RawMessage, bool)

func (f hydratorFunc) Hydrate(spec state.Spec) (json.RawMessage, bool) { return f(spec) }

func TestHydrationAndPersistentEnumeration(t *testing.T) {
	loaded := []byte(`{"restored":true}`)
	calls := 0
	s := state.New(state.Options{Hydrator: hydratorFunc(func(spec state.Spec) (json.RawMessage, bool) {
		calls++
		if spec.Namespace != "p.state" || !spec.Persistent || spec.Scope != state.ScopeUser || spec.MaxBytes != state.PluginMaxBytes {
			t.Fatalf("hydration spec: %+v", spec)
		}
		spec.Initial[0] = '!'
		return loaded, true
	})})
	mustRegister(t, s, baseSpec("husk.store", "husk"))
	spec := baseSpec("p.state", "p")
	spec.Scope, spec.Persistent, spec.Retention.Policy = state.ScopeUser, true, state.KeepLatest
	h := mustRegister(t, s, spec)
	loaded[2] = 'X'
	entry, _ := s.Get(h.Namespace())
	if calls != 1 || !entry.Hydrated || string(entry.Value) != `{"restored":true}` || string(entry.Initial) != `{}` || entry.Revision != 1 {
		t.Fatalf("hydration: %+v, calls %d", entry, calls)
	}
	var dirty []string
	s.OnFlush(func(changes []state.Change, snap *state.Snapshot) {
		for _, change := range changes {
			if entry, ok := snap.Slices[change.Namespace]; ok && entry.Persistent {
				if entry.Retention.Policy != state.KeepLatest {
					t.Fatal("lost retention")
				}
				dirty = append(dirty, entry.Namespace)
			}
		}
	})
	s.Flush()
	if !reflect.DeepEqual(dirty, []string{"p.state"}) {
		t.Fatal(dirty)
	}
	if _, err := s.Clear(h.Namespace()); err != nil {
		t.Fatal(err)
	}
	if string(s.Snapshot().Slices[h.Namespace()].Value) != "{}" {
		t.Fatal("Clear reset to hydrated value")
	}
	for _, raw := range []string{"[]", "{", `{"too-long":"` + strings.Repeat("x", 20) + `"}`} {
		bad := state.New(state.Options{Hydrator: hydratorFunc(func(state.Spec) (json.RawMessage, bool) { return []byte(raw), true })})
		sp := spec
		sp.MaxBytes = 16
		before := bad.Snapshot()
		h, err := bad.Register(sp)
		if h != nil || err == nil || bad.Snapshot() != before {
			t.Fatalf("invalid hydration committed: %v, %v", h, err)
		}
	}
	missing := state.New(state.Options{Hydrator: hydratorFunc(func(state.Spec) (json.RawMessage, bool) { return nil, false })})
	h = mustRegister(t, missing, spec)
	if entry := missing.Snapshot().Slices[h.Namespace()]; entry.Hydrated || string(entry.Value) != "{}" {
		t.Fatal(entry)
	}
}

func TestConcurrentSnapshotReadsDuringWrites(t *testing.T) {
	s := state.New(state.Options{})
	h := mustRegister(t, s, baseSpec("p.state", "p"))
	baseline := s.Snapshot()
	stop := make(chan struct{})
	var readers sync.WaitGroup
	var ready sync.WaitGroup
	ready.Add(4)
	for range 4 {
		readers.Go(func() {
			lastSeq := uint64(0)
			ready.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := s.Snapshot()
				if snap.Seq < lastSeq {
					t.Error("snapshot sequence went backwards")
					return
				}
				lastSeq = snap.Seq
				for _, entry := range snap.Slices {
					if !json.Valid(entry.Value) || len(entry.Value) != entry.Bytes || entry.Revision == 0 {
						t.Error("inconsistent snapshot")
						return
					}
					if _, ok := entry.At(""); !ok {
						t.Error("unreadable entry")
						return
					}
				}
				if string(baseline.Slices["p.state"].Value) != "{}" {
					t.Error("old snapshot mutated")
					return
				}
			}
		})
	}
	ready.Wait()
	for i := 0; i < 300; i++ {
		if _, err := h.Set(fmt.Appendf(nil, `{"n":%d}`, i)); err != nil {
			t.Error(err)
			break
		}
		if i%10 == 0 {
			h.Unregister()
			h = mustRegister(t, s, baseSpec("p.state", "p"))
		}
		s.Flush()
	}
	close(stop)
	readers.Wait()
}
