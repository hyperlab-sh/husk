package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResetSecretsDeletesSealedFilesAndIdentityOnly seals two namespaces under a
// keyring identity and a fallback identity file, then proves the reset removes
// exactly those and leaves every other file in the data, state and config trees.
func TestResetSecretsDeletesSealedFilesAndIdentityOnly(t *testing.T) {
	e := fixture(t)
	ring := &memoryKeyring{}
	s := e.open(t, "notes", func(o *Options) { o.Keyring = ring })
	for _, ns := range []string{"alpha", "beta"} {
		doc, err := s.Secrets(ns)
		if err != nil {
			t.Fatal(err)
		}
		if err = doc.Put("k", []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	paths := s.Paths()
	identity := filepath.Join(paths.Data, "keys", "identity.txt")
	if err := privateDir(filepath.Dir(identity)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identity, []byte("AGE-SECRET-KEY-x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(paths.Data, "secrets", ".alpha.age.tmp-123456")
	if err := os.WriteFile(temp, []byte("crash"), 0o600); err != nil {
		t.Fatal(err)
	}
	keep := []string{
		filepath.Join(paths.Data, "secrets", ".alpha.lock"),
		filepath.Join(paths.Data, "other.txt"),
		filepath.Join(paths.State, "other.txt"),
		filepath.Join(paths.Config, "other.txt"),
		filepath.Join(paths.Data, "secrets", "notes.txt"),
	}
	for _, p := range keep[1:] {
		if err := os.WriteFile(p, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(keep[0]); err != nil {
		t.Fatal("expected the namespace lock file to exist:", err)
	}

	if err := s.ResetSecrets(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(paths.Data, "secrets", "alpha.age"),
		filepath.Join(paths.Data, "secrets", "beta.age"),
		identity,
		temp,
	} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the reset (err %v)", p, err)
		}
	}
	if _, err := ring.Get("husk", "store-identity"); err == nil {
		t.Error("keyring identity survived the reset")
	}
	for _, p := range keep {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("reset touched %s: %v", p, err)
		}
	}
	// A second reset on the now empty store is a no-op, and a new identity can
	// be created: the cached sealer was dropped.
	if err := s.ResetSecrets(); err != nil {
		t.Fatal("second reset:", err)
	}
	doc, _ := s.Secrets("gamma")
	if err := doc.Put("k", []byte("v")); err != nil {
		t.Fatal("secrets unusable after reset:", err)
	}
}

// TestResetSecretsOnPristineStore does nothing and succeeds.
func TestResetSecretsOnPristineStore(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", func(o *Options) { o.Keyring = &memoryKeyring{} })
	if err := s.ResetSecrets(); err != nil {
		t.Fatal(err)
	}
}

// TestResetSecretsKeyringFailureIsReported: an unreadable keyring is not
// "absent", so the reset reports it instead of claiming success.
func TestResetSecretsKeyringFailureIsReported(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", func(o *Options) { o.Keyring = &memoryKeyring{unavailable: true} })
	if err := s.ResetSecrets(); err == nil {
		t.Fatal("expected the keyring failure")
	}
}
