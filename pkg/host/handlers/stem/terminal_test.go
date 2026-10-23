package stem

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
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
func TestTerminalHandlerAdmissionDecodeAndErrors(t *testing.T) {
	p := &terminalPortFake{}
	h := Terminal{Port: p}
	for _, method := range []string{protocol.MethodTerminalAttach, protocol.MethodTerminalWrite, protocol.MethodTerminalDetach} {
		for _, c := range []contracts.Caller{cli, stemSide, nobody} {
			wantCode(t, h.Serve(ctx, c, terminalRequest(method, `{"surface":"main-stage","data":"eA=="}`), nil), protocol.CodeDenied)
		}
		wantCode(t, h.Serve(ctx, plugin, terminalRequest(method, `{"surface":42}`), nil), protocol.CodeInvalidParams)
	}
	if p.calls != 0 {
		t.Fatal("invalid input reached port")
	}
	oversized := base64.StdEncoding.EncodeToString(make([]byte, protocol.MaxTerminalBytes+1))
	for _, raw := range []string{`{"surface":42}`, `{"surface":"main-stage","data":"!"}`, `{"data":"eA=="}`, `{"surface":"main-stage","data":"` + oversized + `"}`} {
		for _, notification := range []bool{false, true} {
			bad := terminalRequest(protocol.MethodTerminalWrite, raw)
			if notification {
				bad.ID = nil
			}
			// The server converts a notification's error into protocol.error.
			wantCode(t, h.Serve(ctx, plugin, bad, nil), protocol.CodeInvalidParams)
		}
	}
	if p.calls != 0 {
		t.Fatal("invalid write reached port")
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
