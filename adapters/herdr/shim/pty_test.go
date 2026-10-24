package shim

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/creack/pty"
)

func paneIOHelper() int {
	size, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		return 2
	}
	fmt.Printf("\x1b[31mready\x1b[0m size=%dx%d\n", size.Cols, size.Rows)
	input, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return 3
	}
	size, err = pty.GetsizeFull(os.Stdin)
	if err != nil {
		return 4
	}
	fmt.Printf("input=%s resized=%dx%d\n", strings.TrimSpace(input), size.Cols, size.Rows)
	return 0
}

func TestPaneRealPTYInputResizeAndExit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := SpawnTerminal(Command{Argv: []string{exe, "--pane-io"}, Env: []string{"GORACE=atexit_sleep_ms=0"}}, 41, 9)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	defer p.Signal(syscall.SIGKILL)
	if pgid, err := syscall.Getpgid(p.PID()); err != nil || pgid != p.PID() {
		t.Fatal("PTY leader has no independent process group")
	}
	chunks := make(chan string, 16)
	go func() {
		defer close(chunks)
		b := make([]byte, 4096)
		for {
			n, err := p.Read(b)
			if n > 0 {
				chunks <- string(b[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	output := ""
	for !strings.Contains(output, "size=41x9") {
		chunk := take(t, chunks)
		if chunk == "" {
			t.Fatal("PTY closed before initial output")
		}
		output += chunk
	}
	if !strings.Contains(output, "\x1b[31mready\x1b[0m") {
		t.Fatal("scripted ANSI bytes lost")
	}
	if err = p.Resize(73, 19); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	for !strings.Contains(output, "resized=73x19") {
		chunk := take(t, chunks)
		if chunk == "" {
			t.Fatal("PTY closed before resized output")
		}
		output += chunk
	}
	if !strings.Contains(output, "input=hello") {
		t.Fatal("input did not reach PTY")
	}
	done := make(chan int, 1)
	go func() { status, _ := p.Wait(); done <- status }()
	if status := take(t, done); status != 0 {
		t.Fatalf("PTY program exited %d", status)
	}
}
