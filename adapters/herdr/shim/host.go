package shim

import (
	"context"
	"encoding/json"
	"net"
	"sync"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Host is the plugin-side socket seam. Call awaits a reply; Notify sends only.
type Host interface {
	Call(context.Context, string, any) (json.RawMessage, error)
	Notify(context.Context, string, any) error
}

// SocketHost speaks the public protocol with a per-launch credential. Each
// independent request owns its connection, so cross-plugin actions cannot block
// state writes or notifications behind a shared reader.
type SocketHost struct {
	Path, Token   string
	mu            sync.Mutex
	notifications net.Conn
	notifyDone    chan struct{}
	notifyStop    func() bool
}

func (h *SocketHost) send(ctx context.Context, method string, params any, reply bool) (json.RawMessage, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", h.Path)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	m := protocol.Message{Method: method, Params: raw, Token: h.Token}
	if reply {
		id := protocol.NewStringID("1")
		m.ID = &id
	}
	if err = protocol.NewWriter(c).Send(m); err != nil {
		return nil, err
	}
	if !reply {
		return nil, nil
	}
	var result json.RawMessage
	var failure error
	// Read only one reply: ReadLoop normally lives until EOF, so decode the
	// public envelope directly on this single-request connection.
	var response protocol.Message
	if err = json.NewDecoder(c).Decode(&response); err != nil {
		return nil, err
	}
	if response.Error != nil {
		failure = response.Error
	} else {
		result = response.Result
	}
	return result, failure
}

// Call sends an authenticated request.
func (h *SocketHost) Call(ctx context.Context, m string, p any) (json.RawMessage, error) {
	return h.send(ctx, m, p, true)
}

// Notify sends on a lifetime connection. Closing immediately after writing a
// notification would race the host's ordered notification worker at EOF.
func (h *SocketHost) Notify(ctx context.Context, m string, p any) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.notifications != nil {
		select {
		case <-h.notifyDone:
			h.notifyStop()
			h.notifications = nil
		default:
		}
	}
	if h.notifications == nil {
		c, err := (&net.Dialer{}).DialContext(ctx, "unix", h.Path)
		if err != nil {
			return err
		}
		h.notifications = c
		h.notifyDone = make(chan struct{})
		done := h.notifyDone
		h.notifyStop = context.AfterFunc(ctx, func() { _ = c.Close() })
		go func() {
			defer close(done)
			defer c.Close()
			_ = protocol.ReadLoop(c, func(protocol.Message) {}, func(*protocol.LineError) {})
		}()
	}
	return protocol.NewWriter(h.notifications).Send(protocol.Message{Method: m, Params: raw, Token: h.Token})
}

// Close joins the notification reader. Run calls this on shutdown.
func (h *SocketHost) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.notifications != nil {
		h.notifyStop()
		_ = h.notifications.Close()
		<-h.notifyDone
		h.notifications = nil
	}
	return nil
}
