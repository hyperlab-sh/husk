package instance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRuntimeDirSharesPrivateDirectoryRule(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "private")
	if err := CheckRuntimeDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckRuntimeDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := CheckRuntimeDir(dir); err == nil {
		t.Fatal("public directory accepted")
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := CheckRuntimeDir(link); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestLookupRuntimeDirNeverCreates(t *testing.T) {
	root := t.TempDir()
	uid := os.Getuid()

	t.Run("absent xdg directory is not found and not created", func(t *testing.T) {
		dir, found, err := lookupRuntimeDir(filepath.Join(root, "run/"), "/nonexistent", uid)
		if err != nil || found {
			t.Fatalf("found=%v err=%v, want absent", found, err)
		}
		if dir != filepath.Join(root, "run", "husk") {
			t.Fatalf("dir = %q, want the cleaned path", dir)
		}
		if _, serr := os.Lstat(filepath.Join(root, "run")); serr == nil {
			t.Fatal("lookup created a directory")
		}
	})
	t.Run("absent fallback directory is not found and not created", func(t *testing.T) {
		dir, found, err := lookupRuntimeDir("", root, uid)
		want := filepath.Join(root, fmt.Sprintf("husk-%d", uid))
		if err != nil || found || dir != want {
			t.Fatalf("dir=%q found=%v err=%v", dir, found, err)
		}
		if _, serr := os.Lstat(want); serr == nil {
			t.Fatal("lookup created the fallback directory")
		}
	})
	t.Run("a private directory is found", func(t *testing.T) {
		xdg := filepath.Join(root, "ok")
		if err := os.MkdirAll(filepath.Join(xdg, "husk"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, found, err := lookupRuntimeDir(xdg, "/nonexistent", uid); err != nil || !found {
			t.Fatalf("found=%v err=%v", found, err)
		}
	})
	t.Run("unusable directories are errors with the spec's wording", func(t *testing.T) {
		loose := filepath.Join(root, "loose")
		if err := os.MkdirAll(filepath.Join(loose, "husk"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(loose, "husk"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, err := lookupRuntimeDir(loose, "/nonexistent", uid)
		if err == nil || !strings.Contains(err.Error(), "is not private to uid") || strings.HasPrefix(err.Error(), "runtime dir: ") {
			t.Fatalf("err = %v", err)
		}
		link := filepath.Join(root, "link")
		if err := os.MkdirAll(link, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(loose, filepath.Join(link, "husk")); err != nil {
			t.Fatal(err)
		}
		_, _, err = lookupRuntimeDir(link, "/nonexistent", uid)
		if err == nil || !strings.Contains(err.Error(), "is a symlink") {
			t.Fatalf("err = %v", err)
		}
	})
}
