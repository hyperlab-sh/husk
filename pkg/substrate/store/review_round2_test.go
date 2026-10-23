package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
	"github.com/zalando/go-keyring"
)

func TestPremergeTTLIdenticalValueAfterExpiry(t *testing.T) {
	for _, mode := range []string{"hydrate", "gc", "before-gc"} {
		t.Run(mode, func(t *testing.T) {
			e := fixture(t)
			s := e.open(t, "notes", nil)
			st := attach(t, s)
			r := state.Retention{Policy: state.TTL, TTL: time.Minute}
			h := slice(t, st, state.ScopeInstance, r)
			set(t, h, `{"n":1}`)
			st.Flush()
			if err := s.flushNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			spec := st.Snapshot().Slices[h.Namespace()].Spec
			// Both Hydrate and a successful write can install the old equality baseline.
			if _, ok := s.Hydrate(spec); !ok {
				t.Fatal("fresh value not hydrated")
			}
			e.clock.Advance(time.Minute)
			if mode != "hydrate" {
				if mode == "gc" {
					if _, err := s.GC(context.Background()); err != nil {
						t.Fatal(err)
					}
					id := storageID(s.sliceKey(spec, ""), spec.Namespace)
					s.mu.Lock()
					_, staleBaseline := s.baseline[id]
					cached := len(s.loaded[id])
					s.mu.Unlock()
					if staleBaseline || cached != 0 {
						t.Fatal("GC retained deleted current record or its baseline")
					}
				}
				set(t, h, `{"n":2}`)
				set(t, h, `{"n":1}`)
			} else {
				h.Unregister()
				st.Flush()
				h = slice(t, st, state.ScopeInstance, r)
				id := storageID(s.sliceKey(spec, ""), spec.Namespace)
				s.mu.Lock()
				_, staleBaseline := s.baseline[id]
				s.mu.Unlock()
				if staleBaseline {
					t.Fatal("failed TTL hydration installed an expired baseline")
				}
				set(t, h, `{"n":1}`)
			}
			st.Flush()
			if err := s.flushNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, info, err := s.source.Get(context.Background(), s.sliceKey(spec, "current.json"))
			if err != nil || !info.Modified.Equal(e.clock.Now()) {
				t.Fatalf("expired equality baseline suppressed write: %v, mtime=%v now=%v", err, info.Modified, e.clock.Now())
			}
			if value, ok := s.Hydrate(spec); !ok || string(value) != `{"n":1}` {
				t.Fatalf("newly committed identical value will be lost: %s %v", value, ok)
			}
		})
	}
}

type classifiedKeyring struct {
	getErr error
	sets   int
}

func (r *classifiedKeyring) Get(string, string) (string, error) { return "", r.getErr }
func (r *classifiedKeyring) Set(string, string, string) error {
	r.sets++
	return errors.New("no writable keyring")
}

func (r *classifiedKeyring) Delete(string, string) error { return r.getErr }

func TestPremergeKeyringDefiniteAbsenceVersusTransient(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		absent bool
	}{
		{"missing", keyring.ErrNotFound, true},
		{"unsupported", fmt.Errorf("wrapped: %w", keyring.ErrUnsupportedPlatform), true},
		{"service-unknown", dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown", Body: []any{"no Secret Service"}}, true},
		{"name-unowned", fmt.Errorf("wrapped: %w", dbus.NewError("org.freedesktop.DBus.Error.NameHasNoOwner", nil)), true},
		{"no-session-address", fmt.Errorf("wrapped: %w", errors.New("dbus: couldn't determine address of session bus")), true},
		{"no-session-launcher", &exec.Error{Name: "dbus-launch", Err: exec.ErrNotFound}, true},
		{"missing-session-socket", &net.OpError{Op: "dial", Net: "unix", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ENOENT}}, true},
		{"locked", dbus.NewError("org.freedesktop.Secret.Error.IsLocked", nil), false},
		{"timeout", dbus.NewError("org.freedesktop.DBus.Error.Timeout", nil), false},
		{"no-reply", dbus.Error{Name: "org.freedesktop.DBus.Error.NoReply"}, false},
		{"io", fmt.Errorf("wrapped: %w", syscall.EIO), false},
		{"bus-permission", &net.OpError{Op: "dial", Net: "unix", Err: syscall.EACCES}, false},
		{"connection-refused", &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}, false},
		{"unknown-command", &exec.Error{Name: "other-tool", Err: exec.ErrNotFound}, false},
		{"uncertain-exit", errors.New("exit status 1"), false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			e := fixture(t)
			ring := &classifiedKeyring{getErr: tt.err}
			s := e.open(t, "notes", func(o *Options) { o.Keyring = ring })
			doc, _ := s.Secrets("example")
			err := doc.Put("token", []byte("value"))
			_, statErr := os.Stat(filepath.Join(s.paths.Data, "keys", "identity.txt"))
			if tt.absent {
				if err != nil || statErr != nil || ring.sets != 1 {
					t.Fatalf("definite absence did not fall back: put=%v file=%v sets=%d", err, statErr, ring.sets)
				}
				s.mu.Lock()
				fallback := s.health.KeyringFallback
				s.mu.Unlock()
				if !fallback {
					t.Fatal("fallback not diagnosed")
				}
				value, ok, getErr := doc.Get("token")
				if getErr != nil || !ok || string(value) != "value" {
					t.Fatal("fallback round trip failed", getErr)
				}
			} else {
				if !errors.Is(err, ErrUnavailable) || !errors.Is(statErr, fs.ErrNotExist) || ring.sets != 0 {
					t.Fatalf("transient uncertainty minted identity: put=%v file=%v sets=%d", err, statErr, ring.sets)
				}
			}
		})
	}
}

func TestPremergePatchLockIsHidden(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", nil)
	if err := s.Patch("config", LayerUser, "", Doc{"key": "value"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.paths.Config, ".config.json.lock")); err != nil {
		t.Fatal("missing hidden lock", err)
	}
	if _, err := os.Stat(filepath.Join(s.paths.Config, "config.json.lock")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("visible lock remains", err)
	}
}

// A late-starting Secret Service is not permission to shadow an existing key.
func TestPremergeSealedNamespacesPreventNewIdentity(t *testing.T) {
	e := fixture(t)
	ring := &memoryKeyring{}
	first := e.open(t, "one", func(o *Options) { o.Keyring = ring })
	original, _ := first.Secrets("old")
	if err := original.Put("token", []byte("original")); err != nil {
		t.Fatal(err)
	}
	sealedPath := filepath.Join(first.paths.Data, "secrets", "old.age")
	before, err := os.ReadFile(sealedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	absent := &classifiedKeyring{getErr: dbus.NewError("org.freedesktop.DBus.Error.ServiceUnknown", nil)}
	outage := e.open(t, "two", func(o *Options) { o.Keyring = absent })
	fresh, _ := outage.Secrets("new")
	if err = fresh.Put("token", []byte("new")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("late provider must fail unavailable, got %v", err)
	}
	if absent.sets != 0 {
		t.Error("attempted to replace an identity while sealed files exist")
	}
	if _, err = os.Stat(filepath.Join(outage.paths.Data, "keys", "identity.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("minted shadow fallback: %v", err)
	}
	after, err := os.ReadFile(sealedPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing sealed bytes changed", err)
	}
	outage.mu.Lock()
	diagnosed := false
	for _, problem := range outage.health.Diagnostics {
		if problem.Code == "unavailable" && strings.Contains(problem.Message, "sealed namespaces") {
			diagnosed = true
		}
	}
	outage.mu.Unlock()
	if !diagnosed {
		t.Error("missing diagnostic explaining sealed-namespace guard")
	}
	if err = outage.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered := e.open(t, "three", func(o *Options) { o.Keyring = ring })
	old, _ := recovered.Secrets("old")
	value, ok, err := old.Get("token")
	if err != nil || !ok || string(value) != "original" {
		t.Fatal("existing secret unreadable when provider recovered", err)
	}
}
