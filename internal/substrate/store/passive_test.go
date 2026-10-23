package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func passive(o *Options) { o.Passive = true }

// corruptSiblings lists quarantine copies next to a record.
func corruptSiblings(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestPassiveOpenSkipsGC: an expired temp survives a passive Open and an
// explicit GC removes it; a normal Open's own GC removes it.
func TestPassiveOpenSkipsGC(t *testing.T) {
	for _, tc := range []struct {
		name         string
		passive      bool
		goneAfterOpn bool
	}{{"passive", true, false}, {"default", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := fixture(t)
			dir := filepath.Join(e.root, "state", "husk", "user", "slices", "old.ns")
			if err := privateDir(dir); err != nil {
				t.Fatal(err)
			}
			temp := filepath.Join(dir, ".current.json.tmp-1000000000")
			if err := os.WriteFile(temp, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			old := e.clock.Now().Add(-100 * 24 * time.Hour)
			if err := os.Chtimes(temp, old, old); err != nil {
				t.Fatal(err)
			}
			s := e.open(t, "notes", func(o *Options) { o.Passive = tc.passive })
			if _, err := os.Stat(temp); os.IsNotExist(err) != tc.goneAfterOpn {
				t.Fatalf("after Open: removed=%v, want %v", os.IsNotExist(err), tc.goneAfterOpn)
			}
			report, err := s.GC(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if (len(report.Removed) > 0) == tc.goneAfterOpn {
				t.Fatalf("explicit GC removed %v", report.Removed)
			}
			if _, err := os.Stat(temp); !os.IsNotExist(err) {
				t.Fatalf("temp survived explicit GC: %v", err)
			}
		})
	}
}

// TestPassiveOpenLeavesCorruptSliceInPlace: the unparsable record is not
// renamed, its slice is blocked, and Check reports it; a normal Open
// quarantines it.
func TestPassiveOpenLeavesCorruptSliceInPlace(t *testing.T) {
	for _, isPassive := range []bool{true, false} {
		e := fixture(t)
		dir := filepath.Join(e.root, "state", "husk", "user", "slices", "x.y")
		if err := privateDir(dir); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "current.json")
		if err := os.WriteFile(file, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		s := e.open(t, "notes", func(o *Options) { o.Passive = isPassive })
		_, statErr := os.Stat(file)
		moved := os.IsNotExist(statErr)
		if moved == isPassive || (len(corruptSiblings(t, dir)) > 0) == isPassive {
			t.Fatalf("passive=%v: moved=%v siblings=%v", isPassive, moved, corruptSiblings(t, dir))
		}
		if !isPassive {
			continue
		}
		if got, _ := os.ReadFile(file); string(got) != "{not json" {
			t.Fatalf("record changed: %q", got)
		}
		s.mu.Lock()
		blocked := s.blocked["user//x.y"]
		s.mu.Unlock()
		if !blocked {
			t.Fatal("slice persistence was not blocked")
		}
		report, err := s.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Problems) == 0 {
			t.Fatalf("Check did not report the corrupt record: %+v", report)
		}
	}
}

// TestPassiveOpenResetsNoMetadata: a malformed named-instance meta.json stays
// put, none is written for a fresh one, and Close writes none.
func TestPassiveOpenResetsNoMetadata(t *testing.T) {
	e := fixture(t)
	dir := filepath.Join(e.root, "state", "husk", "instances", "notes")
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	meta := filepath.Join(dir, "meta.json")
	if err := os.WriteFile(meta, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := e.open(t, "notes", passive)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(meta); string(got) != "garbage" || len(corruptSiblings(t, dir)) != 0 {
		t.Fatalf("meta.json touched: %q %v", got, corruptSiblings(t, dir))
	}

	f := fixture(t)
	g := f.open(t, "fresh", passive)
	if err := g.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "state", "husk", "instances", "fresh", "meta.json")); !os.IsNotExist(err) {
		t.Fatalf("passive Open or Close wrote meta.json: %v", err)
	}
}
