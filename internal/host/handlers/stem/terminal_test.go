package stem

import (
	"context"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"testing"
)

type terminalPortFake struct {
	calls int
	err   error
}

func (p *terminalPortFake) TerminalAttach(context.Context, contracts.Caller, protocol.TerminalAttachParams) (protocol.TerminalAttachResult, error) {
	p.calls++
	return protocol.TerminalAttachResult{Width: 80, Height: 24}, p.err
}
func (p *terminalPortFake) TerminalWrite(context.Context, contracts.Caller, protocol.TerminalWriteParams) error {
	p.calls++
	return p.err
}
func (p *terminalPortFake) TerminalDetach(context.Context, contracts.Caller, protocol.TerminalDetachParams) error {
	p.calls++
	return p.err
}
func terminalRequest(method, params string) protocol.Message {
	m := req(method, params)
	id := protocol.NewStringID("terminal")
	m.ID = &id
	return m
}
func TestTerminalHandlerAdmissionDecodeAndDrop(t *testing.T) {
	p := &terminalPortFake{}
	logged := 0
	h := Terminal{Port: p, Logf: func(string, ...any) { logged++ }}
	for _, method := range []string{protocol.MethodTerminalAttach, protocol.MethodTerminalWrite, protocol.MethodTerminalDetach} {
		for _, c := range []contracts.Caller{cli, stemSide, nobody} {
			wantCode(t, h.Serve(ctx, c, terminalRequest(method, `{"surface":"main-stage","data":"eA=="}`), nil), protocol.CodeDenied)
		}
		wantCode(t, h.Serve(ctx, plugin, terminalRequest(method, `{"surface":42}`), nil), protocol.CodeInvalidParams)
	}
	if p.calls != 0 {
		t.Fatal("invalid input reached port")
	}
	for _, raw := range []string{`{"surface":42}`, `{"surface":"main-stage","data":"!"}`} {
		bad := terminalRequest(protocol.MethodTerminalWrite, raw)
		bad.ID = nil
		if reply := h.Serve(ctx, plugin, bad, nil); reply.Error != nil {
			t.Fatal("invalid notification produced a reply")
		}
	}
	if logged != 2 || p.calls != 0 {
		t.Fatal("invalid notifications not logged and dropped")
	}
	for _, tc := range []struct{ method, raw string }{{protocol.MethodTerminalAttach, `{"surface":"main-stage","capture":true}`}, {protocol.MethodTerminalWrite, `{"surface":"main-stage","data":"eA=="}`}, {protocol.MethodTerminalDetach, `{"surface":"main-stage"}`}} {
		if out := h.Serve(ctx, plugin, terminalRequest(tc.method, tc.raw), nil); out.Error != nil {
			t.Fatalf("valid call: %+v", out.Error)
		}
	}
	if p.calls != 3 {
		t.Fatal("valid methods did not reach port")
	}
	p.err = contracts.NewError(protocol.CodeConflict, "conflict", nil)
	wantCode(t, h.Serve(ctx, plugin, terminalRequest(protocol.MethodTerminalAttach, `{"surface":"main-stage"}`), nil), protocol.CodeConflict)
}
