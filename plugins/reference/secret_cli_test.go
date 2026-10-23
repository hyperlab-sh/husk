package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/cli"
	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Drive the real CLI over a Unix socket, forwarding its action through the
// plugin harness. The fake host dismisses the secret dialog with user_denied.
// The resulting action error must reach stderr and a nonzero CLI exit status.
func TestSecretCheckDeniedCLIExitsNonzero(t *testing.T) {
	h := start(t, false)
	dir, err := os.MkdirTemp("/tmp", "href")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "host.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	type invocation struct {
		req protocol.Message
		w   *protocol.Writer
	}
	invoked := make(chan invocation, 1)
	served := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		w := protocol.NewWriter(conn)
		served <- protocol.ReadLoop(conn, func(req protocol.Message) {
			if req.Method == protocol.MethodSessionPing {
				raw, _ := json.Marshal(protocol.SessionPingResult{ID: "reference-test", PID: 1, ProtocolVersion: protocol.Version})
				if err := w.Send(protocol.Message{ID: req.ID, Result: raw}); err != nil {
					t.Error(err)
				}
				return
			}
			invoked <- invocation{req, w}
		}, func(e *protocol.LineError) { t.Errorf("CLI wrote an invalid message: %v", e) })
	}()
	var stdout, stderr bytes.Buffer
	exited := make(chan int, 1)
	go func() {
		exited <- cli.Main(t.Context(), []string{"action", "invoke", secretCheckAction, "--socket", socket, "--format", "json"}, cli.Env{
			Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(""),
			Getenv:     func(string) string { return "" },
			RuntimeDir: func() (string, bool, error) { return "", false, nil },
		})
	}()
	call := <-invoked
	if call.req.Method != protocol.MethodActionInvoke {
		t.Fatalf("CLI method = %q", call.req.Method)
	}
	raw, err := json.Marshal(call.req)
	if err != nil {
		t.Fatal(err)
	}
	h.send(string(raw))
	get := h.sockNext(protocol.MethodSecretsGet)
	h.stdoutBarrier() // no action reply until the secret read is answered
	h.toSocket(`{"id":` + get.ID.String() + `,"error":{"code":"denied","message":"denied: user_denied","data":{"reason":"user_denied"}}}`)
	if err := call.w.Send(h.outNext()); err != nil {
		t.Fatal(err)
	}
	code := <-exited
	<-served // the CLI closed the connection; leave no server worker behind
	if code != output.ExitRemote || stdout.Len() != 0 {
		t.Fatalf("denied action: exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	var failure struct {
		Error *protocol.Error `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &failure); err != nil || failure.Error == nil || failure.Error.Code != protocol.CodeDenied {
		t.Fatalf("stderr = %q (%v), want a denied error", stderr.String(), err)
	}
	if data := decode[map[string]string](t, failure.Error.Data); data["reason"] != "user_denied" {
		t.Fatalf("error data = %v, want user_denied", data)
	}
}
