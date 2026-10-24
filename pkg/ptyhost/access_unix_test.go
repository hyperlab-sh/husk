//go:build linux || darwin

package ptyhost

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Watchdogs bound failed process barriers; readiness and shutdown use I/O/signals,
// never elapsed time or sleeps.
func awaitPTY[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("PTY barrier timed out")
		var zero T
		return zero
	}
}

func accessChild(t *testing.T, mode string, options Options) *Child {
	t.Helper()
	options.Width, options.Height = 40, 10
	options.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	c, err := Start([]string{os.Args[0], "-test.run=^TestPTYAccessHelper$", "--", mode}, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Cleanup deliberately avoids the APIs under test, including PID/Signal.
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		_ = c.file.Close()
		_ = c.Wait()
	})
	return c
}

func untilPTY(t *testing.T, chunks <-chan string, text *string, want string) {
	t.Helper()
	for !strings.Contains(*text, want) {
		chunk := awaitPTY(t, chunks)
		if chunk == "" {
			t.Fatalf("PTY ended before %q: %q", want, *text)
		}
		*text += chunk
	}
}

func TestPTYDirectoryPIDAndRawInput(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := accessChild(t, "directory", Options{Dir: dir})
	chunks, pump := output(t, c)
	var text string
	untilPTY(t, chunks, &text, "CWD:"+realDir+"\r\n")
	if c.PID() <= 0 || !strings.Contains(text, fmt.Sprintf("PID:%d\r\n", c.PID())) {
		t.Fatalf("session leader PID mismatch: %d, %q", c.PID(), text)
	}
	if !c.GroupAlive() {
		t.Fatal("live session group reported absent")
	}
	input := []byte("raw-input\n")
	if n, err := c.Write(input); err != nil || n != len(input) {
		t.Fatalf("Write=%d,%v", n, err)
	}
	untilPTY(t, chunks, &text, "READ:raw-input\r\n")
	if err := c.Wait(); err != nil {
		t.Fatal(err)
	}
	for range chunks {
	}
	if err := awaitPTY(t, pump); err != nil {
		t.Fatal(err)
	}
	if c.GroupAlive() {
		t.Fatal("exited and reaped group still reported alive")
	}
}

func TestPTYSignalReachesGrandchild(t *testing.T) {
	c := accessChild(t, "signal-parent", Options{})
	chunks, pump := output(t, c)
	var text string
	untilPTY(t, chunks, &text, "DESCENDANT-READY\r\n")
	if !c.GroupAlive() {
		t.Fatal("live descendant group reported absent")
	}
	if err := c.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	untilPTY(t, chunks, &text, "DESCENDANT-TERM\r\n")
	untilPTY(t, chunks, &text, "GROUP-EXIT\r\n")
	if err := c.Wait(); err != nil {
		t.Fatal(err)
	}
	for range chunks {
	}
	if err := awaitPTY(t, pump); err != nil {
		t.Fatal(err)
	}
	if c.GroupAlive() {
		t.Fatal("group survived leader reaping its descendant")
	}
}

func TestPTYCloseTerminalUnblocksPumpWithoutReaping(t *testing.T) {
	// If CloseTerminal accidentally uses Close, its injected grace never fires.
	c := accessChild(t, "hold", Options{After: func(time.Duration) <-chan time.Time { return make(chan time.Time) }})
	chunks, pump := output(t, c)
	var text string
	untilPTY(t, chunks, &text, "HOLD-READY\r\n")
	closed := make(chan error, 1)
	go func() { closed <- c.CloseTerminal() }()
	if err := awaitPTY(t, closed); err != nil {
		t.Fatal(err)
	}
	if err := awaitPTY(t, pump); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.done:
		t.Fatal("closing the terminal reaped the HUP-ignoring leader")
	default:
	}
	if !c.GroupAlive() {
		t.Fatal("closing the terminal stopped its process group")
	}
	waited := make(chan error, 1)
	go func() { waited <- c.Wait() }()
	select {
	case <-waited:
		t.Fatal("Wait returned before process exit")
	default:
	}
	if err := c.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := awaitPTY(t, waited); err != nil {
		t.Fatal(err)
	}
	if c.GroupAlive() {
		t.Fatal("group remained after Wait")
	}
}

func TestPTYAccessHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "directory":
		cwd, err := os.Getwd()
		if err != nil {
			os.Exit(2)
		}
		fmt.Printf("PID:%d\nCWD:%s\n", os.Getpid(), cwd)
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			os.Exit(3)
		}
		fmt.Printf("READ:%s\n", strings.TrimSpace(line))
	case "hold", "signal-parent", "signal-child":
		signal.Ignore(syscall.SIGHUP)
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		switch os.Args[len(os.Args)-1] {
		case "hold":
			fmt.Println("HOLD-READY")
			<-term
		case "signal-child":
			fmt.Println("DESCENDANT-READY")
			<-term
			fmt.Println("DESCENDANT-TERM")
		case "signal-parent":
			child := exec.Command(os.Args[0], "-test.run=^TestPTYAccessHelper$", "--", "signal-child")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err := child.Run(); err != nil {
				os.Exit(4)
			}
			fmt.Println("GROUP-EXIT")
		}
	default:
		os.Exit(5)
	}
	os.Exit(0)
}
