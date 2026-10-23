package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Finding 8: each Store has its own mutex, so the file lock protects the
// shared document across instances. Concurrent disjoint patches must all survive.
func TestReviewConfigPatchAcrossInstancesPreservesAllKeys(t *testing.T) {
	e := fixture(t)
	a := e.open(t, "one", nil)
	b := e.open(t, "two", nil)
	writeConfig(t, a, []string{"config.json"}, `{}`)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker, s := range []*Store{a, b} {
		wg.Go(func() {
			<-start
			for i := 0; i < 32; i++ {
				if err := s.Patch("config", LayerUser, "", Doc{fmt.Sprintf("worker%d-key%d", worker, i): i}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	close(start)
	wg.Wait()
	raw, err := os.ReadFile(filepath.Join(a.paths.Config, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseDoc(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc) != 64 {
		t.Fatalf("concurrent patches lost keys: got %d, want 64", len(doc))
	}
}
