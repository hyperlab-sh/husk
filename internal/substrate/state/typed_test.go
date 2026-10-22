package state_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

type viewState struct {
	Count int            `json:"count"`
	Tabs  map[string]int `json:"tabs,omitempty"`
}

func TestTypedSliceHydratesUpdatesAndSeesClear(t *testing.T) {
	s := state.New(state.Options{Hydrator: hydratorFunc(func(state.Spec) (json.RawMessage, bool) {
		return []byte(`{"count":5,"tabs":{"a":1},"unknown":"keep"}`), true
	})})
	spec := baseSpec("husk.layout", "husk")
	spec.Persistent, spec.Retention.Policy = true, state.KeepLatest
	initial := viewState{Count: 1, Tabs: map[string]int{"initial": 1}}
	slice, err := state.Register(s, spec, initial)
	if err != nil {
		t.Fatal(err)
	}
	initial.Tabs["initial"] = 99
	if got := slice.Get(); got.Count != 5 || got.Tabs["a"] != 1 {
		t.Fatal(got)
	}
	before := s.Snapshot()
	if err := slice.Update(func(*viewState) {}); err != nil || s.Snapshot() != before {
		t.Fatal("typed no-op wrote state", err)
	}
	if !strings.Contains(string(before.Slices[spec.Namespace].Value), "unknown") {
		t.Fatal("lost hydrated field")
	}
	get := slice.Get()
	get.Tabs["a"] = 99
	if slice.Get().Tabs["a"] != 1 {
		t.Fatal("Get aliases State")
	}
	if err := slice.Update(func(v *viewState) { v.Count++; v.Tabs["a"]++ }); err != nil {
		t.Fatal(err)
	}
	if got := slice.Get(); got.Count != 6 || got.Tabs["a"] != 2 {
		t.Fatal(got)
	}
	if string(before.Slices[spec.Namespace].Value) != `{"count":5,"tabs":{"a":1},"unknown":"keep"}` {
		t.Fatal("Update mutated old nested map")
	}
	if s.Snapshot().Slices[spec.Namespace].Revision != 2 {
		t.Fatal("wrong typed revision")
	}
	if _, err := s.Clear(spec.Namespace); err != nil {
		t.Fatal(err)
	}
	if got := slice.Get(); got.Count != 1 || got.Tabs["initial"] != 1 {
		t.Fatal("typed Get did not see Clear", got)
	}
	v := viewState{Count: 3, Tabs: map[string]int{"x": 1}}
	if err := slice.Set(v); err != nil {
		t.Fatal(err)
	}
	v.Tabs["x"] = 99
	if got := slice.Get(); got.Tabs["x"] != 1 {
		t.Fatal("Set retained alias", got)
	}
}

func TestTypedHydrationDecodeFailureReturnsUsableSlice(t *testing.T) {
	for _, persisted := range []string{`{"count":"bad"}`, `[]`, `{`, `{"oversized":"` + strings.Repeat("x", 100) + `"}`} {
		t.Run(persisted, func(t *testing.T) {
			s := state.New(state.Options{Hydrator: hydratorFunc(func(state.Spec) (json.RawMessage, bool) { return []byte(persisted), true })})
			spec := baseSpec("p.state", "p")
			spec.Persistent, spec.Retention.Policy, spec.MaxBytes = true, state.KeepLatest, 64
			slice, err := state.Register(s, spec, viewState{Count: 7})
			if slice == nil || err == nil || !strings.Contains(err.Error(), "discarded persisted p.state") {
				t.Fatalf("Register = %v, %v", slice, err)
			}
			if slice.Get().Count != 7 {
				t.Fatal("did not fall back")
			}
			entry := s.Snapshot().Slices[spec.Namespace]
			if entry.Hydrated || entry.Revision != 1 || s.Snapshot().Seq != 1 {
				t.Fatalf("fallback published extra mutation: %+v", entry)
			}
			if err := slice.Set(viewState{Count: 8}); err != nil || slice.Get().Count != 8 {
				t.Fatal("fallback slice unusable", err)
			}
		})
	}
}

func TestTypedFailuresAreAtomic(t *testing.T) {
	s := state.New(state.Options{})
	before := s.Snapshot()
	if slice, err := state.Register(s, baseSpec("p.state", "p"), 1); slice != nil || !errors.Is(err, state.ErrInvalidValue) {
		t.Fatal("non-object type accepted", err)
	}
	if slice, err := state.Register(s, baseSpec("p.state", "p"), map[string]any{"fn": func() {}}); slice != nil || !errors.Is(err, state.ErrInvalidValue) {
		t.Fatal("unencodable type accepted", err)
	}
	if s.Snapshot() != before {
		t.Fatal("invalid initial type changed State")
	}
	spec := baseSpec("p.state", "p")
	spec.MaxBytes = 20
	slice, err := state.Register(s, spec, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	before = s.Snapshot()
	if err := slice.Update(func(v *map[string]any) { (*v)["huge"] = strings.Repeat("x", 30) }); !errors.Is(err, state.ErrTooLarge) {
		t.Fatal(err)
	}
	if len(slice.Get()) != 0 || s.Snapshot() != before {
		t.Fatal("failed nested update changed current state")
	}
	if err := slice.Set(map[string]any{"fn": func() {}}); !errors.Is(err, state.ErrInvalidValue) {
		t.Fatal(err)
	}
	if err := slice.Set(nil); !errors.Is(err, state.ErrInvalidValue) {
		t.Fatal(err)
	}
	if s.Snapshot() != before {
		t.Fatal("failed set changed State")
	}
	var zero state.Slice[viewState]
	var nilSlice *state.Slice[viewState]
	for _, invalid := range []*state.Slice[viewState]{&zero, nilSlice} {
		if err := invalid.Set(viewState{}); !errors.Is(err, state.ErrUnregistered) {
			t.Fatal(err)
		}
		if err := invalid.Update(func(*viewState) { t.Fatal("called callback on invalid slice") }); !errors.Is(err, state.ErrUnregistered) {
			t.Fatal(err)
		}
		if invalid.Get().Count != 0 {
			t.Fatal("invalid Get")
		}
	}
}
