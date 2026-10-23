package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNoBackgroundGCSkipsStartupGC: an expired temp survives Open with the
// flag and an explicit GC removes it; without the flag Open's own GC does.
func TestNoBackgroundGCSkipsStartupGC(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flag   bool
		gone   bool // removed by Open alone
		wantGC bool // an explicit GC removed something
	}{
		{"flag", true, false, true},
		{"default", false, true, false},
	} {
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
			s := e.open(t, "notes", func(o *Options) { o.NoBackgroundGC = tc.flag })
			_, err := os.Stat(temp)
			if gone := os.IsNotExist(err); gone != tc.gone {
				t.Fatalf("after Open: removed=%v, want %v (%v)", gone, tc.gone, err)
			}
			report, err := s.GC(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if (len(report.Removed) > 0) != tc.wantGC {
				t.Fatalf("explicit GC removed %v, want removal=%v", report.Removed, tc.wantGC)
			}
			if _, err := os.Stat(temp); !os.IsNotExist(err) {
				t.Fatalf("temp survived explicit GC: %v", err)
			}
		})
	}
}
