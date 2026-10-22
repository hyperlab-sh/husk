package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Finding 2: transient keyring errors must never shadow an existing identity.
func TestReviewKeyringOutageDoesNotMintFallback(t *testing.T) {
	e := fixture(t)
	ring := &memoryKeyring{}
	first := e.open(t, "one", true, func(o *Options) { o.Keyring = ring })
	doc, _ := first.Secrets("old")
	if err := doc.Put("token", []byte("original")); err != nil {
		t.Fatal(err)
	}
	first.Close(context.Background())
	ring.mu.Lock()
	ring.unavailable = true
	ring.mu.Unlock()
	outage := e.open(t, "two", true, func(o *Options) { o.Keyring = ring })
	newDoc, _ := outage.Secrets("new")
	if err := newDoc.Put("token", []byte("new")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("keyring outage must be unavailable, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outage.paths.Data, "keys", "identity.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("outage minted a shadow identity: %v", err)
	}
	outage.mu.Lock()
	diagnosed := false
	for _, p := range outage.health.Diagnostics {
		if p.Code == "unavailable" {
			diagnosed = true
		}
	}
	outage.mu.Unlock()
	if !diagnosed {
		t.Error("keyring outage not diagnosed")
	}
	outage.Close(context.Background())
	ring.mu.Lock()
	ring.unavailable = false
	ring.mu.Unlock()
	recovered := e.open(t, "three", true, func(o *Options) { o.Keyring = ring })
	oldDoc, _ := recovered.Secrets("old")
	value, ok, err := oldDoc.Get("token")
	if err != nil || !ok || !bytes.Equal(value, []byte("original")) {
		t.Fatal("original identity was shadowed", err)
	}
}

// Finding 7: the warning exists at startup, without accessing any secret.
func TestReviewFallbackDiagnosedAtOpen(t *testing.T) {
	e := fixture(t)
	ring := &memoryKeyring{setFailure: true}
	first := e.open(t, "one", true, func(o *Options) { o.Keyring = ring })
	doc, _ := first.Secrets("secrets")
	if err := doc.Put("token", []byte("value")); err != nil {
		t.Fatal(err)
	}
	first.Close(context.Background())
	next := e.open(t, "two", true, func(o *Options) { o.Keyring = ring })
	next.mu.Lock()
	fallback := next.health.KeyringFallback
	next.mu.Unlock()
	if !fallback {
		t.Fatal("fallback not reported at Open")
	}
	st := attach(t, next)
	if !bytes.Contains(st.Snapshot().Slices["husk.store"].Value, []byte(`"keyring_fallback":true`)) {
		t.Fatal("startup warning not visible in State")
	}
}
