package instance

import (
	"os"
	"path/filepath"
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
