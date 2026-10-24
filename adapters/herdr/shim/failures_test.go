package shim

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestTerminalVersionFailurePublishesTooOld(t *testing.T) {
	r := newRig(t, "never", false, "")
	path := filepath.Join(r.o.Dir, "herdr-plugin.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "min_herdr_version='0.9.0'", "min_herdr_version='9.0.0'", 1))
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	r.start(t)
	reply := r.invoke(t, "demo.go")
	if reply.Error == nil || reply.Error.Code != protocol.CodeFailed || !strings.Contains(reply.Error.Message, "herdr_too_old") {
		t.Fatalf("version failure=%+v", reply)
	}
}

func TestMissingBinaryFailsBeforeCommands(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.o.Binary = ""
	if err := Run(context.Background(), r.o, strings.NewReader(""), io.Discard, io.Discard); err == nil || err.Error() != "herdr binary not found" {
		t.Fatalf("binary failure=%v", err)
	}
}

func TestCommandCapReturnsBusy(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "")
	for range 32 {
		if reply := r.invoke(t, "demo.go"); reply.Error != nil {
			t.Fatal(reply.Error)
		}
	}
	if reply := r.invoke(t, "demo.go"); reply.Error == nil || reply.Error.Code != protocol.CodeBusy {
		t.Fatalf("cap=%+v", reply)
	}
}
