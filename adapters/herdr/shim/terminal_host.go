package shim

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// TerminalSession opens a dedicated acknowledged output connection. The caller
// owns its lifetime; one Call completes before the next PTY chunk is read.
func (h *SocketHost) TerminalSession(ctx context.Context) (Host, io.Closer, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", h.Path)
	if err != nil {
		return nil, nil, err
	}
	s := &terminalHost{conn: c, token: h.Token, decoder: json.NewDecoder(c)}
	s.stop = context.AfterFunc(ctx, func() { _ = c.Close() })
	return s, s, nil
}

type terminalHost struct {
	mu      sync.Mutex
	conn    net.Conn
	token   string
	decoder *json.Decoder
	stop    func() bool
}

func (h *terminalHost) Call(ctx context.Context, m string, p any) (json.RawMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { _ = h.conn.Close() })
	defer stop()
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	id := protocol.NewStringID("output")
	if err = protocol.NewWriter(h.conn).Send(protocol.Message{ID: &id, Method: m, Params: raw, Token: h.token}); err != nil {
		return nil, err
	}
	var reply protocol.Message
	if err = h.decoder.Decode(&reply); err != nil {
		return nil, err
	}
	if reply.Error != nil {
		return nil, reply.Error
	}
	return reply.Result, nil
}
func (h *terminalHost) Notify(ctx context.Context, m string, p any) error {
	_, err := h.Call(ctx, m, p)
	return err
}
func (h *terminalHost) Close() error { h.stop(); return h.conn.Close() }
