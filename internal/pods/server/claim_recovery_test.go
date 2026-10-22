//go:build linux || darwin

package server

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/pods/internal/testclock"
)

func TestTwoStaleRecoveriesCannotUnlinkTheWinningClaim(t *testing.T) {
	a := inertServer(t)
	b := New(a.cfg)
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: a.SocketPath(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	stale.Close()
	observed := make(chan error, 2)
	release := make(chan struct{})
	for _, s := range []*SocketServer{a, b} {
		original := s.probe
		var calls atomic.Int32
		s.probe = func(path string) error {
			err := original(path)
			if calls.Add(1) == 1 {
				observed <- err
				select {
				case <-release:
				case <-t.Context().Done():
				}
			}
			return err
		}
	}
	results := make(chan error, 2)
	go func() { results <- a.Start() }()
	go func() { results <- b.Start() }()
	for i := 0; i < 2; i++ {
		if err := testclock.Receive(t, observed); !errors.Is(err, instance.ErrSocketRefused) {
			t.Fatal("both probes must first observe the dead socket", err)
		}
	}
	close(release)
	wins, losses := 0, 0
	for i := 0; i < 2; i++ {
		err := testclock.Receive(t, results)
		if err == nil {
			wins++
		} else if strings.Contains(err.Error(), "already running") {
			losses++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("wins=%d losses=%d", wins, losses)
	}
	winner := a
	if !a.registered {
		winner = b
	}
	c, r := dial(t, winner)
	write(t, c, `{"id":1,"method":"session.ping"}`)
	if m := read(t, r); m.ID.String() != "1" {
		t.Fatal("loser unlinked winning socket")
	}
	files, err := os.ReadDir(a.cfg.RuntimeDir)
	if err != nil || len(files) != 2 {
		t.Fatal("recovery created an extra runtime file", files, err)
	}
}
