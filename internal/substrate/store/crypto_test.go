package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
)

type memoryKeyring struct {
	mu          sync.Mutex
	value       string
	unavailable bool
	setFailure  bool
	sets        int
}

func (r *memoryKeyring) Get(service, account string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if service != "husk" || account != "store-identity" {
		return "", errors.New("wrong keyring key")
	}
	if r.unavailable {
		return "", errors.New("no keyring")
	}
	if r.value == "" {
		return "", keyring.ErrNotFound
	}
	return r.value, nil
}
func (r *memoryKeyring) Set(_, _, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unavailable || r.setFailure {
		return errors.New("no keyring")
	}
	r.value = value
	r.sets++
	return nil
}

func TestSecretRoundTripRandomCiphertextAndLazyIdentity(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "keyring", true: "fallback"}[fallback], func(t *testing.T) {
			e := fixture(t)
			ring := &memoryKeyring{setFailure: fallback}
			s := e.open(t, "notes", func(o *Options) { o.Keyring = ring })
			doc, err := s.Secrets("secrets")
			if err != nil {
				t.Fatal(err)
			}
			if _, found, err := doc.Get("git.token"); err != nil || found {
				t.Fatal(found, err)
			}
			if _, err = os.Stat(filepath.Join(s.paths.Data, "keys", "identity.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("read created identity", err)
			}
			if ring.sets != 0 {
				t.Fatal("read created keyring identity")
			}
			secret := []byte("private binary\x00token")
			if err = doc.Put("git.token", secret); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.paths.Data, "secrets", "secrets.age")
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(first, secret) {
				t.Fatal("plaintext stored")
			}
			if err = doc.Put("git.token", secret); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(path)
			if bytes.Equal(first, second) {
				t.Fatal("ciphertext reused")
			}
			value, found, err := doc.Get("git.token")
			if err != nil || !found || !bytes.Equal(value, secret) {
				t.Fatal("round trip failed", err)
			}
			value[0] = 'X'
			again, _, _ := doc.Get("git.token")
			if !bytes.Equal(again, secret) {
				t.Fatal("secret output aliased")
			}
			if keys, err := doc.Keys(); err != nil || !reflect.DeepEqual(keys, []string{"git.token"}) {
				t.Fatal(keys, err)
			}
			reopened := e.open(t, "other", func(o *Options) { o.Keyring = ring })
			other, _ := reopened.Secrets("secrets")
			if value, ok, err := other.Get("git.token"); err != nil || !ok || !bytes.Equal(value, secret) {
				t.Fatal("restart failed", err)
			}
			if fallback {
				info, err := os.Stat(filepath.Join(s.paths.Data, "keys", "identity.txt"))
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal(info, err)
				}
				if !reopened.health.KeyringFallback {
					t.Fatal("fallback not diagnosed on restart")
				}
			} else if ring.sets != 1 {
				t.Fatal("identity regenerated", ring.sets)
			}
			if err = doc.Delete("git.token"); err != nil {
				t.Fatal(err)
			}
			if err = doc.Delete("git.token"); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := doc.Get("git.token"); err != nil || ok {
				t.Fatal(ok, err)
			}
		})
	}
}
func TestUnreadableSecretsNeverOverwritten(t *testing.T) {
	e := fixture(t)
	ring := &memoryKeyring{}
	s := e.open(t, "notes", func(o *Options) { o.Keyring = ring })
	doc, _ := s.Secrets("secrets")
	if err := doc.Put("key", []byte("value")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.paths.Data, "secrets", "secrets.age")
	good, _ := os.ReadFile(path)
	bad := []byte("not encrypted")
	if err := atomicWrite(context.Background(), path, bad); err != nil {
		t.Fatal(err)
	}
	if err := doc.Put("new", []byte("do not overwrite")); err == nil {
		t.Fatal("overwrote corrupt secret")
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(current, bad) {
		t.Fatal("bad ciphertext replaced")
	}
	if err := atomicWrite(context.Background(), path, good); err != nil {
		t.Fatal(err)
	}
	wrong := e.open(t, "other", func(o *Options) { o.Keyring = &memoryKeyring{} })
	other, _ := wrong.Secrets("secrets")
	if err := other.Put("new", []byte("wrong-key")); err == nil {
		t.Fatal("overwrote without key")
	}
	current, _ = os.ReadFile(path)
	if !bytes.Equal(current, good) {
		t.Fatal("ciphertext changed without identity")
	}
	if _, err := s.Secrets("../outside"); err == nil {
		t.Fatal("unsafe secret namespace")
	}
	if err := doc.Put("too-big", make([]byte, 65537)); err == nil {
		t.Fatal("large secret accepted")
	}
}
func TestConcurrentSecretWritersDoNotLoseKeys(t *testing.T) {
	e := fixture(t)
	ring := &memoryKeyring{}
	a := e.open(t, "one", func(o *Options) { o.Keyring = ring })
	b := e.open(t, "two", func(o *Options) { o.Keyring = ring })
	ad, _ := a.Secrets("secrets")
	bd, _ := b.Secrets("secrets")
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := ad.Put("a", []byte("one")); err != nil {
			t.Error(err)
		}
	})
	wg.Go(func() {
		if err := bd.Put("b", []byte("two")); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	if keys, err := ad.Keys(); err != nil || !reflect.DeepEqual(keys, []string{"a", "b"}) {
		t.Fatal(keys, err)
	}
	if ring.sets != 1 {
		t.Fatal("multiple identities created", ring.sets)
	}
}
