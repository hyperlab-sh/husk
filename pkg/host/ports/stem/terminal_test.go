package stem_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/ports/stem"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestTerminalPortPoliciesAndDetach(t *testing.T) {
	_, eng, _, loop, _ := newPort(t)
	t.Cleanup(func() { _ = loop.Do(context.Background(), func() { eng.CloseTerminals() }) })
	ctx := context.Background()
	plugin := contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "alpha.files"}
	lines := []string{"retained"}
	logs := make(chan string, 4)
	port := stem.TerminalPort{Engine: eng, Loop: loop, Size: func(string, protocol.SurfaceID) protocol.TerminalAttachResult {
		return protocol.TerminalAttachResult{Width: 20, Height: 5}
	}, Sender: func(string) func(protocol.Message) error {
		return func(protocol.Message) error { return protocol.ErrSendQueueFull }
	}, Logf: func(format string, args ...any) { logs <- format }, Content: func(_ string, p protocol.SurfaceUpdateParams) { lines = p.Lines }}
	attach := protocol.TerminalAttachParams{Surface: protocol.SurfaceMainStage, Capture: protocol.CaptureBool(false)}
	write := protocol.TerminalWriteParams{Surface: attach.Surface, Data: base64.StdEncoding.EncodeToString([]byte("\x1b[6n"))}
	detach := protocol.TerminalDetachParams{Surface: attach.Surface}
	for _, kind := range []contracts.CallerKind{contracts.CallerCLI, contracts.CallerHost, ""} {
		caller := contracts.Caller{Kind: kind}
		_, err := port.TerminalAttach(ctx, caller, attach)
		for _, err := range []error{err, port.TerminalWrite(ctx, caller, write), port.TerminalDetach(ctx, caller, detach)} {
			if contracts.CodeOf(err) != string(protocol.CodeDenied) {
				t.Fatalf("caller %s: %v", kind, err)
			}
		}
	}
	surface := stem.NewSurfacePort(eng, loop, stem.WithContent(port.Content))
	if err := surface.SurfaceUpdate(ctx, plugin, protocol.SurfaceUpdateParams{Surface: attach.Surface, Lines: []string{"retained"}}); err != nil {
		t.Fatal(err)
	}
	if err := port.TerminalDetach(ctx, plugin, detach); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "retained" {
		t.Fatal("unattached detach erased line content")
	}
	if _, err := port.TerminalAttach(ctx, plugin, attach); err != nil {
		t.Fatal(err)
	}
	if _, err := port.TerminalAttach(ctx, plugin, attach); contracts.CodeOf(err) != string(protocol.CodeConflict) {
		t.Fatalf("conflict %v", err)
	}
	if err := port.TerminalWrite(ctx, plugin, protocol.TerminalWriteParams{Surface: attach.Surface, Data: "!"}); contracts.CodeOf(err) != string(protocol.CodeInvalidParams) {
		t.Fatalf("invalid write %v", err)
	}
	if err := port.TerminalWrite(ctx, plugin, write); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(<-logs, "dropped input") {
		t.Fatal("queue refusal not logged")
	}
	if err := port.TerminalDetach(ctx, plugin, detach); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatal("attached detach did not reset content")
	}
}
