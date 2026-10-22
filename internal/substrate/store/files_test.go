package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

func TestLocalSourceAtomicPrivateAndCrashTemp(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	source, err := NewLocalSource(root)
	if err != nil {
		t.Fatal(err)
	}
	key := Key{Scope: state.ScopeInstance, Instance: "notes", Path: []string{"slices", "p.state", "current.json"}}
	ctx := context.Background()
	if err = source.Put(ctx, key, []byte("old")); err != nil {
		t.Fatal(err)
	}
	path, _ := source.path(key, false)
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	// Simulated power loss before rename: only the previous committed record is visible.
	partial, err := os.CreateTemp(filepath.Dir(path), ".current.json.tmp-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = partial.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err = partial.Close(); err != nil {
		t.Fatal(err)
	}
	records, err := source.List(ctx, Key{Scope: key.Scope, Instance: key.Instance})
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	data, _, err := source.Get(ctx, key)
	if err != nil || string(data) != "old" {
		t.Fatal(string(data), err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = source.Put(canceled, key, []byte("new")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	data, _, _ = source.Get(ctx, key)
	if string(data) != "old" {
		t.Fatal("canceled write replaced value")
	}
	if err = source.Put(ctx, key, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, _, _ = source.Get(ctx, key)
	if string(data) != "new" {
		t.Fatal(string(data))
	}
	if err = source.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err = source.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, _, err = source.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestLocalSourceRejectsUnsafePathsAndPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	source, err := NewLocalSource(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []Key{{Scope: state.ScopeUser, Path: []string{"..", "escape"}}, {Scope: state.ScopeUser, Path: []string{"x/y"}}, {Scope: state.ScopeUser, Instance: "notes", Path: []string{"a"}}, {Scope: state.ScopeInstance, Instance: "123", Path: []string{"a"}}, {Scope: "bad", Path: []string{"a"}}} {
		if err = source.Put(context.Background(), key, nil); err == nil {
			t.Fatal("unsafe key accepted", key)
		}
	}
	key := Key{Scope: state.ScopeUser, Path: []string{"a"}}
	if err = source.Put(context.Background(), key, []byte("safe")); err != nil {
		t.Fatal(err)
	}
	path, _ := source.path(key, false)
	os.Chmod(path, 0644)
	if _, _, err = source.Get(context.Background(), key); err == nil {
		t.Fatal("public record read")
	}
	os.Chmod(path, 0600)
	os.Remove(path)
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("outside"), 0600)
	if err = os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err = source.Get(context.Background(), key); err == nil {
		t.Fatal("followed file symlink")
	}
	if err = source.Put(context.Background(), key, []byte("bad")); err == nil {
		t.Fatal("replaced file symlink")
	}
	if _, err = source.List(context.Background(), Key{Scope: state.ScopeUser}); err == nil {
		t.Fatal("listed symlink")
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "outside" {
		t.Fatal("changed external file")
	}
	os.Chmod(root, 0755)
	if _, err = NewLocalSource(root); err == nil {
		t.Fatal("accepted public source root")
	}
	symlink := filepath.Join(t.TempDir(), "alias")
	os.Symlink(root, symlink)
	if _, err = NewLocalSource(symlink); err == nil {
		t.Fatal("accepted symlink root")
	}
	if _, err = NewLocalSource("relative"); err == nil {
		t.Fatal("accepted relative root")
	}
}
func TestNoopSyncerCancellation(t *testing.T) {
	report, err := (NoopSyncer{}).Reconcile(context.Background(), nil, nil, Key{})
	if err != nil || report.Copied != 0 || report.Deleted != 0 || !strings.Contains(ConflictLastWriterWins, "writer") {
		t.Fatal(report, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = (NoopSyncer{}).Reconcile(ctx, nil, nil, Key{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
