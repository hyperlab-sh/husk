package stem

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/stem/surfaces"
)

// TerminalPort crosses the loop for every terminal mutation. Sender captures
// a worker-safe Pod queue on the loop, never a mutable Pod lookup.
type TerminalPort struct {
	Engine  *surfaces.Engine
	Loop    Loop
	Size    func(string, protocol.SurfaceID) protocol.TerminalAttachResult
	Sender  func(string) func(protocol.Message) error
	Content contracts.ContentSink
	// Logf must be worker-safe: terminal drains never reenter the loop.
	Logf func(string, ...any)
}

// TerminalAttach implements contracts.TerminalPort.
func (p TerminalPort) TerminalAttach(ctx context.Context, c contracts.Caller, params protocol.TerminalAttachParams) (protocol.TerminalAttachResult, error) {
	var result protocol.TerminalAttachResult
	if err := allowCaller(protocol.MethodTerminalAttach, c); err != nil {
		return result, err
	}
	err := p.run(ctx, func() error {
		send := p.Sender(c.PluginID)
		var err error
		result, err = p.Engine.TerminalAttach(c.PluginID, params, p.Size(c.PluginID, params.Surface), func(b []byte) {
			raw, _ := json.Marshal(protocol.TerminalInputParams{Surface: params.Surface, Data: base64.StdEncoding.EncodeToString(b)})
			if err := send(protocol.Message{Method: protocol.MethodTerminalInput, Params: raw}); err != nil && p.Logf != nil {
				p.Logf("terminals: dropped input for %s: %v", params.Surface, err)
			}
		})
		if err == nil {
			p.Engine.Terminal(params.Surface).SetLinkHandler(func(url string, row, col int) {
				raw, _ := json.Marshal(protocol.TerminalLinkParams{Surface: params.Surface, URL: url, Row: row, Col: col})
				if err := send(protocol.Message{Method: protocol.MethodTerminalLink, Params: raw}); err != nil && p.Logf != nil {
					p.Logf("terminals: dropped link for %s: %v", params.Surface, err)
				}
			})
		}
		return err
	})
	return result, err
}

// TerminalWrite implements contracts.TerminalPort.
func (p TerminalPort) TerminalWrite(ctx context.Context, c contracts.Caller, params protocol.TerminalWriteParams) error {
	if err := allowCaller(protocol.MethodTerminalWrite, c); err != nil {
		return err
	}
	return p.run(ctx, func() error { return p.Engine.TerminalWrite(c.PluginID, params) })
}

// TerminalDetach implements contracts.TerminalPort.
func (p TerminalPort) TerminalDetach(ctx context.Context, c contracts.Caller, params protocol.TerminalDetachParams) error {
	if err := allowCaller(protocol.MethodTerminalDetach, c); err != nil {
		return err
	}
	return p.run(ctx, func() error {
		attached := p.Engine.Terminal(params.Surface) != nil
		if err := p.Engine.TerminalDetach(c.PluginID, params); err != nil {
			return err
		}
		if attached && p.Content != nil {
			p.Content(c.PluginID, protocol.SurfaceUpdateParams{Surface: params.Surface, Lines: []string{}})
		}
		return nil
	})
}

func (p TerminalPort) run(ctx context.Context, fn func() error) error {
	var err error
	if lerr := p.Loop.Do(ctx, func() { err = fn() }); lerr != nil {
		if ctx.Err() != nil {
			return ctxError(ctx)
		}
		return contracts.NewError(protocol.CodeUnavailable, "host loop is not running", nil)
	}
	return codedEngine(err)
}
