//go:build linux || darwin

package ptyhost

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestPTYCloseEscalatesAfterInjectedGrace(t *testing.T) {
	tick := make(chan time.Time, 1)
	armed := make(chan time.Duration, 1)
	c, err := Start([]string{os.Args[0], "-test.run=^TestPTYHelper$", "--", "stubborn"}, Options{Width: 80, Height: 24, After: func(d time.Duration) <-chan time.Time { armed <- d; return tick }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-c.done:
			return
		default:
		}
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		select {
		case tick <- time.Time{}:
		default:
		}
		_ = c.Close()
	})
	chunks, pump := output(t, c)
	var text string
	until(t, chunks, &text, "STUBBORN")
	finished := make(chan error, 1)
	go func() { finished <- c.Close() }()
	if d := <-armed; d != 500*time.Millisecond {
		t.Fatalf("grace %v", d)
	}
	select {
	case <-finished:
		t.Fatal("Close did not allow the HUP grace")
	default:
	}
	tick <- time.Time{}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if c.Wait() == nil {
		t.Fatal("stubborn child not killed")
	}
	if err := syscall.Kill(c.cmd.Process.Pid, 0); err != syscall.ESRCH {
		t.Fatalf("child not reaped: %v", err)
	}
	for range chunks {
	}
	if err := <-pump; err != nil {
		t.Fatal(err)
	}
}
