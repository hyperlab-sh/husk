package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateSocketDirIsPrivateUnderRuntimeDir(t *testing.T) {
	rt := t.TempDir()
	dir, err := createSocketDir(rt)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if filepath.Dir(dir) != rt {
		t.Fatalf("dir %q not under runtime dir %q", dir, rt)
	}
	st, _ := os.Stat(dir)
	if got := st.Mode().Perm(); got != 0o700 {
		t.Fatalf("dir mode = %o, want 700", got)
	}
}

func TestCreateSocketDirFallsBackToPrivateTemp(t *testing.T) {
	for _, rt := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		dir, err := createSocketDir(rt)
		if err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(dir)
		if got := st.Mode().Perm(); got != 0o700 {
			t.Fatalf("rt=%q dir mode = %o, want 700", rt, got)
		}
		if !strings.HasPrefix(filepath.Base(dir), "husk-") {
			t.Fatalf("unexpected dir name %q", dir)
		}
		os.RemoveAll(dir)
	}
}

func TestCreateSocketDirIsUniquePerCall(t *testing.T) {
	rt := t.TempDir()
	a, _ := createSocketDir(rt)
	b, _ := createSocketDir(rt)
	if a == b {
		t.Fatalf("expected distinct dirs, got %q twice", a)
	}
}

func TestListenPrivateSocketMode(t *testing.T) {
	l, path, dir, err := listenPrivate(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	defer l.Close()
	if filepath.Dir(path) != dir || filepath.Base(path) != socketName {
		t.Fatalf("unexpected socket path %q in %q", path, dir)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%q is not a socket: %v", path, st.Mode())
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 600", got)
	}
	ds, _ := os.Stat(dir)
	if got := ds.Mode().Perm(); got != 0o700 {
		t.Fatalf("dir mode = %o, want 700", got)
	}
}

func TestCheckPeerUID(t *testing.T) {
	if err := checkPeerUID(501, 501); err != nil {
		t.Fatalf("same uid rejected: %v", err)
	}
	err := checkPeerUID(0, 501)
	if err == nil || !strings.Contains(err.Error(), "peer uid 0") {
		t.Fatalf("foreign uid accepted or bad error: %v", err)
	}
	if checkPeerUID(1<<31+5, 5) == nil {
		t.Fatal("large uid must not wrap onto a small host uid")
	}
}

func TestAuthorizePeerAcceptsOwnUID(t *testing.T) {
	l, _, dir, err := listenPrivate(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	defer l.Close()
	done := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		done <- authorizePeer(c)
	}()
	c, err := net.Dial("unix", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := <-done; err != nil {
		t.Fatalf("own-uid peer rejected: %v", err)
	}
}

func TestAuthorizePeerRejectsNonUnixConn(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if authorizePeer(a) == nil {
		t.Fatal("non-unix connection must be refused")
	}
}

// shortTempDir avoids t.TempDir(), whose long path can exceed the sun_path
// limit (104 bytes on darwin) once the socket name is appended.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "hk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}
