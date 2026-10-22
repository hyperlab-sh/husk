package state_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

func baseSpec(namespace, owner string) state.Spec {
	return state.Spec{Namespace: namespace, Owner: owner, Scope: state.ScopeInstance}
}

func mustRegister(t *testing.T, s *state.State, spec state.Spec) *state.Handle {
	t.Helper()
	h, err := s.Register(spec)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestNamespace(t *testing.T) {
	for _, ns := range []string{"husk.focus", "example.notes.state", "A_1-B.state", strings.Repeat("a", 126) + ".b"} {
		if !state.ValidNamespace(ns) {
			t.Errorf("valid namespace rejected: %q", ns)
		}
	}
	for _, ns := range []string{"", "husk", ".state", "husk.", "a..b", "a...state", "a/b.state", "a.b/c", "a.é", "a. b", "a.:b", "a.\x00", "a.\\b", strings.Repeat("a", 127) + ".b"} {
		if state.ValidNamespace(ns) {
			t.Errorf("invalid namespace accepted: %q", ns)
		}
	}
}

func TestInitialNamespaces(t *testing.T) {
	// Deliberately enumerate the documented table: adding or renaming a core
	// slice must update this test as well as its public constants.
	for _, item := range []struct{ namespace, owner string }{
		{state.SurfacesNamespace, state.SurfacesOwner},
		{state.FocusNamespace, state.FocusOwner},
		{state.LayoutNamespace, state.LayoutOwner},
		{state.GeometryNamespace, state.GeometryOwner},
		{state.PodsNamespace, state.PodsOwner},
		{state.ProfileNamespace, state.ProfileOwner},
		{state.GrantsNamespace, state.GrantsOwner},
		{state.StoreNamespace, state.StoreOwner},
		{"example.notes.state", "example.notes"},
		{"example.notes.workspace", "example.notes"},
	} {
		s := state.New(state.Options{})
		h := mustRegister(t, s, baseSpec(item.namespace, item.owner))
		if h.Namespace() != item.namespace || !state.ValidNamespace(item.namespace) {
			t.Fatalf("invalid documented name: %s", item.namespace)
		}
	}
}

func TestInvalidSpecAtomic(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		change      func(*state.Spec)
	}{
		{"namespace", "namespace", func(s *state.Spec) { s.Namespace = "husk" }},
		{"foreign owner", "owner", func(s *state.Spec) { s.Owner = "other" }},
		{"parent owner", "owner", func(s *state.Spec) { s.Namespace = "husk.child.focus" }},
		{"unknown scope", "scope", func(s *state.Spec) { s.Scope = "wrong" }},
		{"nonpersistent policy", "retention", func(s *state.Spec) { s.Retention.Policy = state.Manual }},
		{"nonpersistent N", "retention", func(s *state.Spec) { s.Retention.N = 2 }},
		{"nonpersistent TTL", "retention", func(s *state.Spec) { s.Retention.TTL = time.Minute }},
		{"missing policy", "retention", func(s *state.Spec) { s.Persistent = true }},
		{"unknown policy", "retention", func(s *state.Spec) { s.Persistent = true; s.Retention.Policy = "bad" }},
		{"negative cap", "max_bytes", func(s *state.Spec) { s.MaxBytes = -1 }},
		{"raise core cap", "max_bytes", func(s *state.Spec) { s.MaxBytes = state.CoreMaxBytes + 1 }},
		{"raise plugin cap", "max_bytes", func(s *state.Spec) {
			s.Namespace, s.Owner, s.MaxBytes = "plugin.state", "plugin", state.PluginMaxBytes+1
		}},
		{"raise hard cap", "max_bytes", func(s *state.Spec) { s.Scope = state.ScopeWorkspace; s.MaxBytes = state.HardMaxBytes + 1 }},
		{"initial array", "initial", func(s *state.Spec) { s.Initial = []byte("[]") }},
		{"initial malformed", "initial", func(s *state.Spec) { s.Initial = []byte("{") }},
		{"initial over cap", "initial", func(s *state.Spec) { s.MaxBytes = 2; s.Initial = []byte(`{"a":1}`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := state.New(state.Options{})
			spec := baseSpec("husk.focus", "husk")
			tc.change(&spec)
			before := s.Snapshot()
			if h, err := s.Register(spec); h != nil || !errors.Is(err, state.ErrInvalidSpec) || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Register = %v, %v; want invalid %s", h, err, tc.field)
			}
			if s.Snapshot() != before {
				t.Fatal("failed registration published a snapshot")
			}
		})
	}
}

func TestRetention(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    state.Retention
		ok   bool
	}{
		{"latest", state.Retention{Policy: state.KeepLatest}, true},
		{"manual", state.Retention{Policy: state.Manual}, true},
		{"n min", state.Retention{Policy: state.KeepLastN, N: 2}, true},
		{"n max", state.Retention{Policy: state.KeepLastN, N: 20}, true},
		{"ttl min", state.Retention{Policy: state.TTL, TTL: time.Minute}, true},
		{"ttl max", state.Retention{Policy: state.TTL, TTL: 8760 * time.Hour}, true},
		{"n zero", state.Retention{Policy: state.KeepLastN}, false},
		{"n one", state.Retention{Policy: state.KeepLastN, N: 1}, false},
		{"n high", state.Retention{Policy: state.KeepLastN, N: 21}, false},
		{"n with ttl", state.Retention{Policy: state.KeepLastN, N: 2, TTL: time.Minute}, false},
		{"ttl low", state.Retention{Policy: state.TTL, TTL: time.Minute - 1}, false},
		{"ttl high", state.Retention{Policy: state.TTL, TTL: 8760*time.Hour + 1}, false},
		{"ttl with n", state.Retention{Policy: state.TTL, TTL: time.Minute, N: 2}, false},
		{"latest with n", state.Retention{Policy: state.KeepLatest, N: 2}, false},
		{"manual with ttl", state.Retention{Policy: state.Manual, TTL: time.Minute}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseSpec("plugin.state", "plugin")
			spec.Persistent, spec.Retention = true, tc.r
			s := state.New(state.Options{})
			_, err := s.Register(spec)
			if (err == nil) != tc.ok || err != nil && !errors.Is(err, state.ErrInvalidSpec) {
				t.Fatalf("Register = %v, want success %v", err, tc.ok)
			}
		})
	}
}

func TestScopeAndClassCaps(t *testing.T) {
	for _, tc := range []struct {
		owner, workspaceOwner string
		scope                 state.Scope
		cap                   int
		ok                    bool
	}{
		{"husk", "", state.ScopeInstance, state.CoreMaxBytes, true},
		{"husk", "", state.ScopeUser, state.CoreMaxBytes, true},
		{"plugin", "", state.ScopeInstance, state.PluginMaxBytes, true},
		{"plugin", "", state.ScopeUser, state.PluginMaxBytes, true},
		{"husk", "", state.ScopeWorkspace, state.WorkspaceMaxBytes, true},
		{"plugin", "plugin", state.ScopeWorkspace, state.WorkspaceMaxBytes, true},
		{"plugin", "", state.ScopeWorkspace, 0, false},
		{"plugin", "other", state.ScopeWorkspace, 0, false},
	} {
		t.Run(tc.owner+"/"+string(tc.scope)+"/"+tc.workspaceOwner, func(t *testing.T) {
			s := state.New(state.Options{WorkspaceOwner: tc.workspaceOwner})
			spec := baseSpec(tc.owner+".state", tc.owner)
			spec.Scope = tc.scope
			h, err := s.Register(spec)
			if !tc.ok {
				if !errors.Is(err, state.ErrInvalidSpec) {
					t.Fatalf("Register = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			entry, _ := s.Get(h.Namespace())
			if entry.MaxBytes != tc.cap {
				t.Fatalf("cap = %d, want %d", entry.MaxBytes, tc.cap)
			}
			value := []byte(`{"a":"` + strings.Repeat("x", tc.cap-8) + `"}`)
			if len(value) != tc.cap {
				t.Fatal("bad test boundary")
			}
			if _, err := h.Set(value); err != nil {
				t.Fatalf("at cap: %v", err)
			}
			before := s.Snapshot()
			if _, err := h.Set(append(value, ' ')); !errors.Is(err, state.ErrTooLarge) {
				t.Fatalf("above cap: %v", err)
			}
			if s.Snapshot() != before {
				t.Fatal("failed size check published")
			}
		})
	}
}
