package host

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/services"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

type delayedCatalog struct {
	resolves atomic.Int32
	layers   atomic.Int32
	started  chan struct{}
	release  chan struct{}
}

func (s *delayedCatalog) Resolve(store.DocName, string) (store.Resolved, error) {
	if s.resolves.Add(1) == 2 {
		close(s.started)
		<-s.release
	}
	return store.Resolved{}, nil
}
func (s *delayedCatalog) Layers(store.DocName, string) ([]store.LayerDoc, error) {
	id := fmt.Sprintf("entry%d", s.layers.Add(1))
	return []store.LayerDoc{{Layer: store.LayerUser, Doc: store.Doc{"version": float64(1), "entries": map[string]any{id: map[string]any{"source": "/opt/plugin"}}}}}, nil
}

func TestSlowCatalogNeverDelaysReloadOrOverwritesNewerCache(t *testing.T) {
	reader := &delayedCatalog{started: make(chan struct{}), release: make(chan struct{})}
	h, err := boot(t, nil, func(o *options) { o.catalogReader = reader })
	if err != nil {
		t.Fatal(err)
	}
	var slow <-chan struct{}
	h.loop(func() { slow = h.root.warmCatalog() })
	<-reader.started
	// A real flight completes while the earlier catalog read is blocked. Its
	// event schedules a newer cache read, without joining the blocked worker.
	reports, err := h.root.rt.Coordinator.Reload(t.Context(), services.ReasonCLI)
	if err != nil || len(reports) != 5 {
		close(reader.release)
		t.Fatalf("reload: %v %v", reports, err)
	}
	var fresh <-chan struct{}
	h.loop(func() { fresh = h.root.warmCatalog() })
	<-fresh
	h.loop(func() {})
	before := h.root.Catalog()
	revision := h.root.CatalogRevision()
	close(reader.release)
	<-slow
	h.root.wg.Wait()
	h.loop(func() {})
	after := h.root.Catalog()
	if h.root.CatalogRevision() != revision {
		t.Fatal("rejected stale worker advanced catalog revision")
	}
	if len(before.Entries) != 1 || len(after.Entries) != 1 || before.Entries[0].ID != after.Entries[0].ID {
		t.Fatalf("late worker replaced fresh cache: %v -> %v", before, after)
	}
}
