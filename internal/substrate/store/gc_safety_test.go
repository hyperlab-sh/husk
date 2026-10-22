package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

func TestGCDoesNotMistakeNamesForQuarantine(t *testing.T) {
	e := fixture(t)
	e.root = filepath.Join(e.root, "parent.corrupt-directory")
	s := e.open(t, "notes", true, nil)
	// Neither parent directories nor legitimate secret names identify quarantine files.
	names := []string{"a.age", "b.age", "c.age", "d.age", "e.age", "token.corrupt-20200101T000000.000000000Z.age"}
	old := e.clock.Now().Add(-400 * 24 * time.Hour)
	for _, name := range names {
		p := filepath.Join(s.paths.Data, name)
		if err := os.WriteFile(p, []byte("legitimate data"), 0600); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, old, old)
	}
	if _, err := s.GC(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(s.paths.Data, name)); err != nil {
			t.Fatal("GC deleted legitimate data", name, err)
		}
	}
}
func TestGCLogAgeAndDirectoryCap(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", true, nil)
	active, err := s.OpenPluginLog("plugin")
	if err != nil {
		t.Fatal(err)
	}
	active.Close()
	for i := 0; i < 22; i++ {
		name := fmt.Sprintf("old-%02d", i)
		p, err := pathInside(s.paths.State, []string{"logs", name, "plugin.log"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, []byte("kept diagnostic"), 0600); err != nil {
			t.Fatal(err)
		}
		at := e.clock.Now().Add(-time.Duration(i) * time.Hour)
		if i == 21 {
			at = e.clock.Now().Add(-15 * 24 * time.Hour)
		}
		os.Chtimes(p, at, at)
		os.Chtimes(filepath.Dir(p), at, at)
	}
	if _, err = s.GC(context.Background()); err != nil {
		t.Fatal(err)
	}
	dirs, err := os.ReadDir(filepath.Join(s.paths.State, "logs"))
	if err != nil || len(dirs) > 20 {
		t.Fatal(len(dirs), err)
	}
	if _, err = os.Stat(active.Name()); err != nil {
		t.Fatal("GC removed current instance log", err)
	}
}
func TestSameInstantCurrentWritesRefreshHydration(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", true, nil)
	st := attach(t, s)
	r := state.Retention{Policy: state.Manual}
	h := slice(t, st, state.ScopeInstance, r)
	for i := 1; i <= 2; i++ {
		set(t, h, fmt.Sprintf(`{"n":%d}`, i))
		st.Flush()
		if err := s.flushNow(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	h.Unregister()
	st.Flush()
	h = slice(t, st, state.ScopeInstance, r)
	if got := string(st.Snapshot().Slices[h.Namespace()].Value); got != `{"n":2}` {
		t.Fatal("same-instant current cache stale", got)
	}
}
