package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	portsservices "github.com/hyperlab-sh/husk/internal/host/ports/services"
	"github.com/hyperlab-sh/husk/internal/services"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type catalogSpy struct {
	mu    sync.Mutex
	calls []string
	gids  []int64
}

func (s *catalogSpy) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
	s.gids = append(s.gids, goroutineID())
}
func (s *catalogSpy) Resolve(n store.DocName, p string) (store.Resolved, error) {
	s.record("resolve:" + string(n) + ":" + p)
	return store.Resolved{}, nil
}
func (s *catalogSpy) Layers(n store.DocName, p string) ([]store.LayerDoc, error) {
	s.record("layers:" + string(n) + ":" + p)
	return []store.LayerDoc{{Layer: store.LayerUser, File: "catalog.json", Doc: store.Doc{"version": float64(1), "entries": map[string]any{"alpha": map[string]any{"source": "/opt/alpha", "tags": []any{"editor"}, "surfaces": []any{"main-stage"}}, "bad": false}}}}, nil
}

func TestCatalogCacheWarmsOffLoopAtStartupAndAfterReload(t *testing.T) {
	spy := &catalogSpy{}
	builder := goroutineID()
	h, err := boot(t, nil, func(o *options) { o.catalogReader = spy })
	if err != nil {
		t.Fatal(err)
	}
	settle := func() { h.root.wg.Wait(); h.loop(func() {}) }
	settle()
	original := h.root.Frontend().Catalog()
	if len(original.Entries) != 1 || len(original.Problems) != 1 {
		t.Fatalf("startup cache: %+v", original)
	}
	// Mutating a read must not alter any cached nested slice or source pointer.
	got := h.root.Catalog()
	got.Entries[0].Tags[0] = "changed"
	got.Entries[0].Surfaces[0] = protocol.SurfaceModal
	got.Problems[0].Source.File = "changed"
	if !reflect.DeepEqual(original, h.root.Catalog()) {
		t.Fatal("cache read aliases stored data")
	}
	h.loop(func() { _ = h.root.Catalog() })
	spy.mu.Lock()
	before := len(spy.calls)
	spy.mu.Unlock()
	if before != 2 {
		t.Fatalf("Catalog performed I/O: %d calls", before)
	}
	// The event hook, rather than a frontend call, schedules the next worker.
	h.loop(func() {
		raw, _ := json.Marshal(map[string]any{"data": map[string]any{"reason": "cli"}})
		if err := (reloadWatcher{h.root}).Notify(protocol.Message{Params: raw}); err != nil {
			t.Error(err)
		}
	})
	settle()
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.calls) != 4 {
		t.Fatalf("reload did not warm: %v", spy.calls)
	}
	for _, id := range spy.gids {
		if id == builder || id == h.root.loopGID.Load() {
			t.Fatalf("Store called on builder/loop: %d", id)
		}
	}
}

func TestBrokenCatalogDoesNotFailOrDelayReload(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config", "husk")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(config, "catalog.json")
	if err := os.WriteFile(file, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := bootIn(t, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	reports, err := h.root.rt.Coordinator.Reload(context.Background(), services.ReasonCLI)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 5 {
		t.Fatal(reports)
	}
	for _, r := range reports {
		if !r.Applied {
			t.Fatalf("catalog blocked report: %+v", r)
		}
		for _, p := range r.Problems {
			if p.Code == "store_error" {
				t.Fatal(p)
			}
		}
	}
	h.loop(func() {}) // flush the reload event before waiting for its worker
	h.root.wg.Wait()
	h.loop(func() {})
	got := h.root.Catalog()
	if len(got.Problems) != 1 || got.Problems[0].Code != "store_error" {
		t.Fatal(got)
	}
	if err := os.WriteFile(file, []byte(`{"version":1,"entries":{"alpha":{"source":"/opt/alpha"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = h.root.rt.Coordinator.Reload(t.Context(), services.ReasonCLI)
	if err != nil {
		t.Fatal(err)
	}
	h.loop(func() {})
	h.root.wg.Wait()
	h.loop(func() {})
	if got := h.root.Catalog(); len(got.Entries) != 1 || len(got.Problems) != 0 {
		t.Fatalf("reload cache stale: %+v", got)
	}
}

func TestCatalogUsesServingProfile(t *testing.T) {
	spy := &catalogSpy{}
	dir := t.TempDir()
	for _, name := range []string{"work", "dev"} {
		if err := os.MkdirAll(filepath.Join(dir, "config", "husk", "profiles", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	h, err := bootIn(t, dir, func(c *contracts.Config) { name := "work"; c.ProfileFlag = &name }, func(o *options) { o.catalogReader = spy })
	if err != nil {
		t.Fatal(err)
	}
	h.root.wg.Wait()
	h.loop(func() {})
	spy.mu.Lock()
	initial := append([]string(nil), spy.calls...)
	spy.mu.Unlock()
	if !reflect.DeepEqual(initial, []string{"resolve:catalog:work", "layers:catalog:work"}) {
		t.Fatal(initial)
	}
	result, err := (portsservices.Profile{Svc: h.root.rt.Profile}).Use(t.Context(), protocol.Caller{Kind: protocol.CallerCLI}, "dev")
	if err != nil || result.Active != "dev" {
		t.Fatalf("profile.use: %+v, %v", result, err)
	}
	h.loop(func() {})
	h.loop(func() {}) // the preceding Update's event flush has finished
	h.root.wg.Wait()
	h.loop(func() {})
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.calls) != 4 || !reflect.DeepEqual(spy.calls[len(spy.calls)-2:], []string{"resolve:catalog:dev", "layers:catalog:dev"}) {
		t.Fatal(spy.calls)
	}
}
