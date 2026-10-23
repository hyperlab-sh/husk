package host

import (
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/protocol"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/services/catalog"
)

// catalogCache is published only on the loop and read from any goroutine.
type catalogCache struct {
	value      atomic.Pointer[contracts.CatalogResult]
	revision   atomic.Uint64
	generation uint64 // loop-only; discards late workers after a newer reload
}

// CatalogRevision is an any-goroutine cache-publication token; it never does I/O.
func (r *Root) CatalogRevision() uint64 { return r.pluginCatalog.revision.Load() }

// Catalog implements contracts.Frontend without calling Store.
func (r *Root) Catalog() contracts.CatalogResult {
	out := contracts.CatalogResult{Entries: []contracts.CatalogEntry{}, Problems: []protocol.Problem{}}
	if p := r.pluginCatalog.value.Load(); p != nil {
		out.Entries = append(out.Entries, p.Entries...)
		out.Problems = append(out.Problems, p.Problems...)
		for i := range out.Entries {
			out.Entries[i].Surfaces = append([]protocol.SurfaceID(nil), out.Entries[i].Surfaces...)
			out.Entries[i].Tags = append([]string(nil), out.Entries[i].Tags...)
		}
		for i := range out.Problems {
			if src := out.Problems[i].Source; src != nil {
				copied := *src
				out.Problems[i].Source = &copied
			}
		}
	}
	return out
}

// warmCatalog captures the serving profile on the loop (or builder), starts a
// tracked worker, and posts publication. No reload ever waits for the reader.
func (r *Root) warmCatalog() <-chan struct{} {
	done := make(chan struct{})
	if r.store == nil || r.rt == nil {
		close(done)
		return done
	}
	r.pluginCatalog.generation++
	generation := r.pluginCatalog.generation
	profile := r.rt.Profile.Active()
	reader := catalog.Reader(r.store)
	if r.opts.catalogReader != nil {
		reader = r.opts.catalogReader
	}
	if !r.spawn(func() tea.Msg {
		defer close(done)
		result := catalog.Read(reader, profile, r.store.Paths().Config)
		out := contracts.CatalogResult{Entries: make([]contracts.CatalogEntry, len(result.Entries)), Problems: result.Problems}
		for i, e := range result.Entries {
			out.Entries[i] = contracts.CatalogEntry(e)
		}
		r.post(func() {
			if generation == r.pluginCatalog.generation {
				r.pluginCatalog.value.Store(&out)
				r.pluginCatalog.revision.Add(1)
			}
		})
		return nil
	}) {
		close(done)
	}
	return done
}
