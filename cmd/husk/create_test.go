package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestCreateLocalFlagsUsageAndHiddenHelp(t *testing.T) {
	for _, args := range [][]string{
		{"create", "bundle", "app", "--instance", "x"},
		{"create", "bundle", "app", "--socket", "unused"},
		{"create", "bundle", "app", "--timeout", "1s"},
		{"create", "tui", "app"},
		{"create", "bundle", "My App"},
		{"--instance", "x", "create", "bundle", "app"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := t.TempDir()
			cmd := exec.Command(os.Args[0], args...)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "HUSK_TEST_RUN_MAIN=1", "HOME="+root)
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 2 {
				t.Fatalf("exit=%v out=%s", err, out)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("created %v err=%v", entries, err)
			}
		})
	}
	cmd := exec.Command(os.Args[0], "help")
	cmd.Env = append(os.Environ(), "HUSK_TEST_RUN_MAIN=1")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(out), "__guide-plugin") || !strings.Contains(string(out), "husk create bundle <name>") {
		t.Fatalf("help=%s err=%v", out, err)
	}
}

func TestCreateTUIStaysUnregisteredUntilUIScaffold(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		for _, args := range [][]string{{"tui"}, {"tui", "app"}, {"tui", "app", "example.test/app"}} {
			t.Run(format+"/"+strings.Join(args, " "), func(t *testing.T) {
				root := t.TempDir()
				argv := append([]string{"create", "--format", format}, args...)
				cmd := exec.CommandContext(t.Context(), os.Args[0], argv...)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "HUSK_TEST_RUN_MAIN=1", "HOME="+root)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				var ee *exec.ExitError
				if !errors.As(err, &ee) || ee.ExitCode() != 2 {
					t.Fatalf("exit=%v stdout=%s stderr=%s", err, &stdout, &stderr)
				}
				if stdout.Len() != 0 {
					t.Fatalf("unsupported verb wrote stdout: %s", &stdout)
				}
				const usage = "usage: husk create bundle <name>"
				if format == "json" {
					var reply struct {
						Error struct{ Code, Message string }
					}
					if err := json.Unmarshal(stderr.Bytes(), &reply); err != nil {
						t.Fatalf("stderr is not a JSON error: %s: %v", &stderr, err)
					}
					if reply.Error.Code != "usage" || !strings.Contains(reply.Error.Message, usage) {
						t.Fatalf("error=%+v, want usage", reply.Error)
					}
				} else if !strings.Contains(stderr.String(), usage) {
					t.Fatalf("usage missing from stderr: %s", &stderr)
				}
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Fatalf("unsupported verb created %v err=%v", entries, err)
				}
			})
		}
	}
}

func TestHiddenGuideVerbStartsWatchAndSpeaksOnPipes(t *testing.T) {
	// Short private Unix socket; all synchronization is by messages or EOF.
	root, err := os.MkdirTemp("/tmp", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "__guide-plugin")
	cmd.Env = append(os.Environ(), "HUSK_TEST_RUN_MAIN=1", "HUSK_SOCKET_PATH="+socket, "HUSK_PLUGIN_TOKEN=test-token")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill() })
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := listener.Accept(); accepted <- c }()
	var conn net.Conn
	select {
	case conn = <-accepted:
	case <-ctx.Done():
		t.Fatal("guide did not connect")
	}
	if conn == nil {
		t.Fatal("accept failed")
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var watch protocol.Message
	if err := json.Unmarshal(line, &watch); err != nil {
		t.Fatal(err)
	}
	if watch.Method != protocol.MethodStateWatch || watch.Token != "test-token" || watch.ID == nil {
		t.Fatalf("first request=%s", line)
	}
	var params struct {
		Slices []string `json:"slices"`
	}
	if err := json.Unmarshal(watch.Params, &params); err != nil {
		t.Fatal(err)
	}
	if strings.Join(params.Slices, ",") != "husk.geometry,husk.surfaces,husk.focus,husk.pods" {
		t.Fatalf("watch=%s", watch.Params)
	}
	writer := protocol.NewWriter(conn)
	if err := writer.Send(protocol.Message{ID: watch.ID, Result: json.RawMessage(`{"seq":1,"slices":[]}`)}); err != nil {
		t.Fatal(err)
	}
	line, err = reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var keys protocol.Message
	if err := json.Unmarshal(line, &keys); err != nil {
		t.Fatal(err)
	}
	if keys.Method != protocol.MethodKeysList {
		t.Fatalf("keys=%s", line)
	}
	if err := writer.Send(protocol.Message{ID: keys.ID, Result: json.RawMessage(`{"entries":[],"problems":[]}`)}); err != nil {
		t.Fatal(err)
	}
	id, _ := protocol.NewIntID(42)
	if err := protocol.NewWriter(stdin).Send(protocol.Message{ID: &id, Method: "unknown.test"}); err != nil {
		t.Fatal(err)
	}
	line, err = bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var reply protocol.Message
	if err := json.Unmarshal(line, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ID == nil || reply.ID.String() != id.String() || reply.Error == nil || reply.Error.Code != protocol.CodeUnknownMethod {
		t.Fatalf("reply=%s", line)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("guide exit=%v stderr=%s", err, stderr.String())
	}
}
