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
	err = take(t, r.exit)
	r.exit <- nil
	if err == nil || !strings.Contains(err.Error(), "herdr_too_old") {
		t.Fatalf("terminal failure=%v", err)
	}
	r.host.until(t, protocol.MethodStateSet, "too_old")
}

func TestMissingBinaryFailsBeforeCommands(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.o.Binary = ""
	if err := Run(context.Background(), r.o, strings.NewReader(""), io.Discard, io.Discard); err == nil || err.Error() != "herdr binary not found" {
		t.Fatalf("binary failure=%v", err)
	}
}

func TestStartFailureIsTerminal(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.server.Close()
	r.o.Settings.Server = "start"
	r.o.StartServer = func(context.Context) error { return os.ErrPermission }
	r.start(t)
	err := take(t, r.exit)
	r.exit <- nil
	if err == nil || !strings.Contains(err.Error(), "herdr server did not start") {
		t.Fatalf("start failure=%v", err)
	}
}

func TestCommandCapReturnsBusy(t *testing.T) {
	r := newRig(t, "never", false, "")
	r.start(t)
	r.host.until(t, protocol.MethodStateSet, "attached")
	for range 32 {
		if reply := r.invoke(t, "demo.go"); reply.Error != nil {
			t.Fatal(reply.Error)
		}
	}
	if reply := r.invoke(t, "demo.go"); reply.Error == nil || reply.Error.Code != protocol.CodeBusy {
		t.Fatalf("cap=%+v", reply)
	}
}
