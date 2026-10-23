package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	host "github.com/hyperlab-sh/husk/pkg/host/contracts"
	services "github.com/hyperlab-sh/husk/pkg/services"
	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

// doc decodes raw JSON into a store.Doc, json.Number-first like Store.
func doc(t *testing.T, raw string) store.Doc {
	t.Helper()
	var out store.Doc
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		t.Fatalf("doc: %v", err)
	}
	return out
}

// applyTo prepares and publishes one reload input on a real service.
func applyTo(t *testing.T, s services.Reloadable, in services.ReloadInput) c.Report {
	t.Helper()
	cand, r, err := s.Rebuild(context.Background(), in)
	if err != nil {
		t.Fatalf("prepare %s: %v %+v", r.Service, err, r.Problems)
	}
	if cand == nil {
		t.Fatal("nil candidate")
	}
	r = cand.Apply()
	if !r.Applied {
		t.Fatalf("not applied: %+v", r)
	}
	return r
}

// staticInputs is an InputSource stub returning one fixed input; it stands in
// for the coordinator's user-scoped Inputs seam.
type staticInputs struct {
	in  services.ReloadInput
	err error
}

func (s staticInputs) Inputs(context.Context, services.Caller) (services.ReloadInput, error) {
	return s.in, s.err
}

var (
	userCaller  = host.Caller{Kind: host.CallerCLI}
	filesCaller = host.Caller{Kind: host.CallerPlugin, PluginID: "files"}
	stemHost    = host.Caller{Kind: host.CallerHost}
)

// hostState builds a fresh state tree for secrets tests.
func hostState() *state.State { return state.New(state.Options{}) }

// memoryCrypto is an in-memory store.SecretDoc. getErr, when set, is
// returned by Get — the leak test injects an error whose text carries the
// secret to prove services never passes the value through.
type memoryCrypto struct {
	data   map[string][]byte
	getErr error
}

func (m *memoryCrypto) Get(k string) ([]byte, bool, error) {
	if m.getErr != nil {
		return nil, false, m.getErr
	}
	v, ok := m.data[k]
	return append([]byte(nil), v...), ok, nil
}
func (m *memoryCrypto) Put(k string, v []byte) error {
	m.data[k] = append([]byte(nil), v...)
	return nil
}
func (m *memoryCrypto) Delete(k string) error { delete(m.data, k); return nil }
func (m *memoryCrypto) Keys() ([]string, error) {
	var out []string
	for k := range m.data {
		out = append(out, k)
	}
	return out, nil
}

// fakeClock satisfies the services Clock seam without real timers: NewTimer
// returns a timer whose channel never fires but whose Stop signals, giving
// tests a happens-after sync for prompt goroutines (Stop runs deferred,
// after any pending-marker write).
type fakeClock struct {
	now    time.Time
	timers []*signTimer
}

func (f *fakeClock) Now() time.Time { return f.now }
func (f *fakeClock) NewTimer(time.Duration) c.Timer {
	t := &signTimer{stopped: make(chan struct{})}
	f.timers = append(f.timers, t)
	return t
}

type signTimer struct {
	stopped chan struct{}
	done    bool
}

func (s *signTimer) C() <-chan time.Time { return nil }
func (s *signTimer) Stop() bool {
	if !s.done {
		s.done = true
		close(s.stopped)
	}
	return true
}

// fakeStoreReader feeds the coordinator exactly one resolved config doc.
type fakeStoreReader struct {
	resolved store.Resolved
	layers   []store.LayerDoc
	declared map[store.DocName]map[string]store.Doc
	profiles []string
	err      error // Resolve fails with it when set
}

func (s *fakeStoreReader) Invalidate() {}
func (s *fakeStoreReader) Resolve(store.DocName, string) (store.Resolved, error) {
	if s.err != nil {
		return store.Resolved{}, s.err
	}
	return s.resolved, nil
}
func (s *fakeStoreReader) Layers(store.DocName, string) ([]store.LayerDoc, error) {
	return s.layers, nil
}
func (s *fakeStoreReader) Declared(n store.DocName) map[string]store.Doc { return s.declared[n] }
func (s *fakeStoreReader) Profiles() []string {
	return append([]string(nil), s.profiles...)
}
