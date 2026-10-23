package host

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestCatalogRejectedWorkerClosesCompletion(t *testing.T) {
	spy := &catalogSpy{}
	h, err := boot(t, nil, func(o *options) { o.catalogReader = spy })
	if err != nil {
		t.Fatal(err)
	}
	h.loop(func() {
		h.root.mu.Lock()
		h.root.closing = true
		h.root.mu.Unlock()
		done := h.root.warmCatalog()
		h.root.mu.Lock()
		h.root.closing = false
		h.root.mu.Unlock()
		select {
		case <-done:
		default:
			t.Error("rejected catalog worker left completion open")
		}
	})
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.calls) != 2 {
		t.Fatalf("rejected worker read Store: %v", spy.calls)
	}
}

func TestMalformedReloadStillRefreshesCatalog(t *testing.T) {
	spy := &catalogSpy{}
	h, err := boot(t, nil, func(o *options) { o.catalogReader = spy })
	if err != nil {
		t.Fatal(err)
	}
	h.loop(func() {
		if err := (reloadWatcher{h.root}).Notify(protocol.Message{Params: json.RawMessage("{")}); err == nil {
			t.Error("malformed payload accepted")
		}
	})
	h.root.wg.Wait()
	h.loop(func() {})
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.calls) != 4 {
		t.Fatalf("malformed reload skipped refresh: %v", spy.calls)
	}
}

func TestEmptyCatalogUsesJSONArrayFields(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*Root{{}, h.root} {
		raw, err := json.Marshal(r.Catalog())
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != `{"entries":[],"problems":[]}` {
			t.Fatalf("empty catalog: %s", raw)
		}
	}
}
