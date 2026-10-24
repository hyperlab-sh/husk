package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

type spellingClock struct{}

func (spellingClock) Now() time.Time                     { return time.Unix(1, 0) }
func (spellingClock) NewTimer(time.Duration) store.Timer { return spellingTimer{} }

type spellingTimer struct{}

func (spellingTimer) C() <-chan time.Time { return nil }
func (spellingTimer) Stop() bool          { return true }

// spellingInput exercises Store's actual layer merge and provenance rather
// than manufacturing a Resolved document that only resembles production.
func spellingInput(t *testing.T, lower, upper string) c.ReloadInput {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, ".config", "husk")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(upper), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{
		Passive: true, Clock: spellingClock{},
		Env:          store.Env{LookupEnv: func(string) (string, bool) { return "", false }, HomeDir: func() (string, error) { return home, nil }},
		Distribution: fstest.MapFS{"config.json": {Data: []byte(lower)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	resolved, err := st.Resolve("config", "")
	if err != nil {
		t.Fatal(err)
	}
	layers, err := st.Layers("config", "")
	if err != nil {
		t.Fatal(err)
	}
	return c.ReloadInput{Doc: resolved, ConfigLayers: layers}
}

func TestDuplicateSettingSpellings(t *testing.T) {
	for _, tc := range []struct {
		name, lower, upper, key, source string
		want                            any
		bad                             []string
		additions                       map[string][]string
	}{
		{name: "dotted first", lower: `{"dist":{"a":{"b":7}}}`, upper: `{"dist":{"a.b":1,"a":{"b":2},"other":9}}`, want: float64(7), bad: []string{"user"}},
		{name: "nested first", lower: `{"dist":{"a":{"b":7}}}`, upper: `{"dist":{"a":{"b":2},"a.b":1,"other":9}}`, want: float64(7), bad: []string{"user"}},
		{name: "equal values", lower: `{"dist":{"a":{"b":7}}}`, upper: `{"dist":{"a.b":2,"a":{"b":2},"other":9}}`, want: float64(7), bad: []string{"user"}},
		{name: "dotted only", lower: `{"dist":{"a":{"b":7}}}`, upper: `{"dist":{"a.b":1,"other":9}}`, want: float64(1), source: "user"},
		{name: "nested only", lower: `{"dist":{"a":{"b":7}}}`, upper: `{"dist":{"a":{"b":2},"other":9}}`, want: float64(2), source: "user"},
		{name: "lower duplicate higher valid", lower: `{"dist":{"a.b":1,"a":{"b":2}}}`, upper: `{"dist":{"a":{"b":8},"other":9}}`, want: float64(8), source: "user", bad: []string{"distribution"}},
		{name: "all layers duplicate", lower: `{"dist":{"a.b":1,"a":{"b":2}}}`, upper: `{"dist":{"a.b":5,"a":{"b":6},"other":9}}`, want: float64(3), source: "default", bad: []string{"distribution", "user"}},
		{name: "literal plugins example", lower: `{"plugins":{"a":{"b":7}}}`, upper: `{"plugins":{"a.b":1,"a":{"b":2}},"dist":{"other":9}}`, key: "plugins.a.b", want: float64(7), bad: []string{"user"}},
		{name: "bundle reader", lower: `{"husk":{"bundle":"base"}}`, upper: `{"husk.bundle":"first","husk":{"bundle":"second"},"dist":{"other":9}}`, key: "husk.bundle", want: "", source: "default", bad: []string{"user"}},
		{name: "additive reader", lower: `{"husk":{"plugins":{"paths":["base"]}}}`, upper: `{"husk.plugins.paths":["first"],"husk":{"plugins":{"paths":["second"]}},"dist":{"other":9}}`, key: "husk.plugins.paths", want: []any{"base", "added"}, source: "instance", bad: []string{"user"}, additions: map[string][]string{"paths": {"added"}}},
		{name: "deep prefix spellings", lower: `{"dist":{"a":{"b":7}}}`, upper: `{"dist.a":{"b":1},"dist":{"a":{"b":2},"other":9}}`, want: float64(7), bad: []string{"user"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := spellingInput(t, tc.lower, tc.upper)
			// ParseDoc discards appearance order. Keep both source orders and
			// repeat to exercise map traversal; flattening itself is lexical.
			for range 20 {
				s, err := New(Options{PluginAdditions: tc.additions, Schema: []Entry{
					{Key: "dist.a.b", Owner: "dist", Type: TypeInt, Default: int64(3)},
					{Key: "dist.other", Owner: "dist", Type: TypeInt, Default: int64(4)},
				}})
				if err != nil {
					t.Fatal(err)
				}
				before := s.Snapshot()
				cand, report, err := s.Rebuild(t.Context(), in)
				if len(tc.bad) != 0 {
					if !errors.Is(err, c.ErrInvalid) || cand != nil || s.Snapshot() != before {
						t.Fatalf("duplicate reload: %+v %v", report, err)
					}
					cand, report, err = s.RebuildStartup(t.Context(), in)
					if !errors.Is(err, c.ErrInvalid) || cand == nil {
						t.Fatalf("startup: %+v %v", report, err)
					}
				} else if err != nil || cand == nil {
					t.Fatalf("single spelling: %+v %v", report, err)
				}
				key := tc.key
				if key == "" {
					key = "dist.a.b"
				}
				seen := map[string]int{}
				for _, p := range report.Problems {
					if p.Code != "duplicate_key" {
						continue
					}
					seen[p.Layer]++
					if p.Severity != "error" || p.Key != key || p.Source == nil || p.Source.Layer != p.Layer || p.Source.File != "config.json" {
						t.Fatalf("duplicate diagnostic=%+v", p)
					}
				}
				if len(seen) != len(tc.bad) {
					t.Fatalf("duplicate layers=%v want %v", seen, tc.bad)
				}
				for _, layer := range tc.bad {
					if seen[layer] != 1 {
						t.Fatalf("duplicate count=%v", seen)
					}
				}
				if !cand.Apply().Applied {
					t.Fatal("candidate did not apply")
				}
				snap := s.Snapshot()
				source := tc.source
				if source == "" {
					source = "distribution"
				}
				raw, ok := snap.Raw(key)
				if !ok || !reflect.DeepEqual(raw, c.Clone(tc.want)) || snap.Source(key) != source {
					t.Errorf("%s=%v source=%s; want %v from %s", key, raw, snap.Source(key), tc.want, source)
				}
				if snap.Int("dist.other") != 9 || snap.Source("dist.other") != "user" {
					t.Error("unrelated key was reset")
				}
			}
		})
	}
}

func TestMergedOnlySpellingsAreDeterministic(t *testing.T) {
	for range 20 {
		s, err := New(Options{})
		if err != nil {
			t.Fatal(err)
		}
		doc, err := store.ParseDoc([]byte(`{"theme.name":"dotted","theme":{"name":"nested"}}`))
		if err != nil {
			t.Fatal(err)
		}
		candidate, report, err := s.Rebuild(t.Context(), c.ReloadInput{Doc: store.Resolved{Doc: doc}})
		if err != nil || candidate == nil || !candidate.Apply().Applied {
			t.Fatalf("rebuild=%+v %v", report, err)
		}
		if got := s.Snapshot().String("theme.name"); got != "dotted" {
			t.Fatalf("Doc-only value=%s", got)
		}
	}
}
