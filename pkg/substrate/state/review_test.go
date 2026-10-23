package state_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

func TestUntypedHydrationFallback(t *testing.T) {
	for _, tc := range []struct {
		name, saved string
		wantErr     error
	}{
		{"invalid JSON", `{`, state.ErrInvalidValue},
		{"array", `[]`, state.ErrInvalidValue},
		{"null", `null`, state.ErrInvalidValue},
		{"scalar", `42`, state.ErrInvalidValue},
		{"lowered manifest cap", `{"saved":"more than 16 bytes"}`, state.ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := state.New(state.Options{Hydrator: hydratorFunc(func(state.Spec) (json.RawMessage, bool) {
				return []byte(tc.saved), true
			})})
			spec := baseSpec("plugin.state", "plugin")
			spec.Persistent, spec.Retention.Policy = true, state.KeepLatest
			spec.Initial, spec.MaxBytes = []byte(`{"n":7}`), 16
			h, err := s.Register(spec)
			if h == nil || !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), "state: discarded persisted plugin.state:") {
				t.Fatalf("Register = %v, %v; want usable handle and wrapped %v", h, err, tc.wantErr)
			}
			entry := s.Snapshot().Slices[spec.Namespace]
			if entry.Hydrated || entry.Revision != 1 || s.Snapshot().Seq != 1 || string(entry.Value) != `{"n":7}` {
				t.Fatalf("fallback did not publish exactly Initial: %+v", entry)
			}
			var changes []state.Change
			s.OnFlush(func(c []state.Change, snap *state.Snapshot) {
				changes = append(changes, c...)
				if !snap.Slices[spec.Namespace].Persistent {
					t.Fatal("fallback lost persistent metadata")
				}
			})
			// Pod can continue startup and use the capability despite the warning.
			if rev, err := h.Set([]byte(`{"n":8}`)); err != nil || rev != 2 {
				t.Fatalf("fallback handle unusable: %d, %v", rev, err)
			}
			s.Flush()
			if len(changes) != 1 || changes[0].Revision != 2 || changes[0].Removed {
				t.Fatal(changes)
			}
		})
	}
}

func TestNormalizedEncodingAcrossEveryValueSource(t *testing.T) {
	const input = ` { "b": 1, "a": { "d": 2, "c": [ { "f": 3, "e": 9007199254740993 } ] } } `
	const normalized = `{"a":{"c":[{"e":9007199254740993,"f":3}],"d":2},"b":1}`
	for _, source := range []string{"initial", "hydrated", "set"} {
		t.Run(source, func(t *testing.T) {
			opts := state.Options{}
			if source == "hydrated" {
				opts.Hydrator = hydratorFunc(func(state.Spec) (json.RawMessage, bool) { return []byte(input), true })
			}
			s := state.New(opts)
			spec := baseSpec("plugin.state", "plugin")
			spec.MaxBytes = len(normalized) // Raw padding exceeds the cap; normalized bytes fit.
			if source == "initial" {
				spec.Initial = []byte(input)
			}
			if source == "hydrated" {
				spec.Persistent, spec.Retention.Policy = true, state.KeepLatest
			}
			h := mustRegister(t, s, spec)
			if source == "set" {
				if _, err := h.Set([]byte(input)); err != nil {
					t.Fatal(err)
				}
			}
			before := s.Snapshot()
			entry := before.Slices[spec.Namespace]
			if string(entry.Value) != normalized || entry.Bytes != len(normalized) || entry.Hydrated != (source == "hydrated") {
				t.Fatalf("not normalized: %+v, %s", entry, entry.Value)
			}
			if source == "initial" && string(entry.Initial) != normalized {
				t.Fatal("Initial not normalized")
			}
			s.Flush()
			calls := 0
			s.OnFlush(func([]state.Change, *state.Snapshot) { calls++ })
			if rev, err := h.Patch([]byte(`{}`), entry.Revision); err != nil || rev != entry.Revision || s.Snapshot() != before {
				t.Fatalf("empty patch changed normalized %s: %d, %v", source, rev, err)
			}
			if rev, err := h.Set([]byte(input)); err != nil || rev != entry.Revision || s.Snapshot() != before {
				t.Fatalf("padding/key ordering changed %s: %d, %v", source, rev, err)
			}
			if rev, err := h.Patch([]byte(`{"a":{"d":2}}`), 0); err != nil || rev != entry.Revision || s.Snapshot() != before {
				t.Fatalf("nested no-op patch changed %s: %d, %v", source, rev, err)
			}
			s.Flush()
			if calls != 0 {
				t.Fatal("no-op emitted changes")
			}
		})
	}
}

func TestClearUnknownIncludesNamespace(t *testing.T) {
	s := state.New(state.Options{})
	before := s.Snapshot()
	rev, err := s.Clear("missing.state")
	if rev != 0 || !errors.Is(err, state.ErrUnregistered) || !strings.Contains(err.Error(), "missing.state") || s.Snapshot() != before {
		t.Fatalf("Clear unknown = %d, %v", rev, err)
	}
}
