//go:build linux || darwin

package ptyhost

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func output(t *testing.T, c *Child) (<-chan string, <-chan error) {
	t.Helper()
	chunks := make(chan string, 32)
	done := make(chan error, 1)
	go func() {
		err := c.Pump(protocol.SurfaceMainStage, func(p protocol.TerminalWriteParams) error {
			b, err := base64.StdEncoding.DecodeString(p.Data)
			if err != nil {
				return err
			}
			chunks <- string(b)
			return nil
		})
		close(chunks)
		done <- err
	}()
	return chunks, done
}
func until(t *testing.T, ch <-chan string, text *string, want string) {
	t.Helper()
	for !strings.Contains(*text, want) {
		s, ok := <-ch
		if !ok {
			t.Fatalf("PTY ended before %q; output %q", want, *text)
		}
		*text += s
	}
}

func TestPTYOutputResizeInputAndEnvironment(t *testing.T) {
	command := `stty -echo; printf '\033[31mREADY\033[0m\n'; stty size; printf '%s|%s|%s|%s|%s\n' "$TERM" "$COLORTERM" "$TERM_PROGRAM" "$HUSK_SOCKET_PATH" "${HUSK_PLUGIN_TOKEN+credential-leak}"; read line; stty size; printf 'READ:%s\n' "$line"`
	c, err := Start([]string{"/bin/sh", "-c", command}, Options{Width: 80, Height: 24, Env: []string{"PATH=/usr/bin:/bin", "HUSK_SOCKET_PATH=/tmp/socket", "HUSK_PLUGIN_TOKEN=private"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	chunks, done := output(t, c)
	var text string
	until(t, chunks, &text, "xterm-256color|truecolor|husk|/tmp/socket|\r\n")
	if !strings.Contains(text, "\x1b[31mREADY\x1b[0m") || !strings.Contains(text, "24 80") {
		t.Fatalf("initial PTY %q", text)
	}
	if err = c.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if err = c.Input(protocol.TerminalInputParams{Surface: protocol.SurfaceMainStage, Data: base64.StdEncoding.EncodeToString([]byte("next\n"))}); err != nil {
		t.Fatal(err)
	}
	until(t, chunks, &text, "READ:next")
	for s := range chunks {
		text += s
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = c.Wait(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "30 100") {
		t.Fatalf("resized output %q", text)
	}
}

func TestPTYCloseHangsUpProcessGroup(t *testing.T) {
	c, err := Start([]string{os.Args[0], "-test.run=^TestPTYHelper$", "--", "parent"}, Options{Width: 80, Height: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	chunks, done := output(t, c)
	var text string
	until(t, chunks, &text, "CHILD:")
	// The complete line is the readiness acknowledgement after installing HUP.
	until(t, chunks, &text, "\r\n")
	marker := strings.Index(text, "CHILD:")
	line := strings.Split(text[marker+6:], "\r\n")[0]
	pid, err := strconv.Atoi(line)
	if err != nil {
		t.Fatalf("child readiness %q", text)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Wait(); err != nil {
		t.Fatal("session leader did not reap its child after HUP", err)
	}
	for range chunks {
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("child survived group hangup: %v", err)
	}
}

func TestPTYHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	hup := make(chan os.Signal, 2)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	switch os.Args[len(os.Args)-1] {
	case "parent":
		cmd := exec.Command(os.Args[0], "-test.run=^TestPTYHelper$", "--", "child")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		<-hup
		if err := cmd.Wait(); err != nil {
			os.Exit(4)
		}
		os.Exit(0)
	case "child":
		fmt.Printf("CHILD:%d\n", os.Getpid())
		<-hup
		os.Exit(0)
	}
}
