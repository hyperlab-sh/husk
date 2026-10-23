package handlers

import (
	"context"
	"strings"
	"sync"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Mux routes inbound protocol messages to port adapters by method prefix: the
// first dot-separated segment of the method name. Register every prefix once
// at composition, before the Server starts serving; Serve is then read-only.
type Mux struct {
	mu     sync.RWMutex
	routes map[string]contracts.Handler
}

// NewMux returns an empty Mux.
func NewMux() *Mux { return &Mux{routes: map[string]contracts.Handler{}} }

// Register serves prefix with h. It panics on a nil handler or a malformed,
// unknown or duplicate prefix: those are composition bugs, not input errors,
// the same line the Server's own Handle takes.
func (m *Mux) Register(prefix string, h contracts.Handler) {
	if h == nil || prefix == "" || strings.ContainsAny(prefix, "./") {
		panic("handlers: invalid mux prefix " + prefix)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.routes[prefix]; dup {
		panic("handlers: duplicate mux prefix " + prefix)
	}
	m.routes[prefix] = h
}

// Serve routes req to the adapter registered for its prefix. An unregistered
// prefix gets unknown_method; a registered adapter answers the rest (unknown
// methods within the prefix, Outbound methods arriving inbound, wrong caller
// kinds) through Admit. Serve returns exactly one reply per request —
// notifications get the same message shape and the Server drops their result,
// turning an error reply into a protocol.error diagnostic.
func (m *Mux) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, n contracts.Notifier) protocol.Message {
	prefix, _, _ := strings.Cut(req.Method, ".")
	m.mu.RLock()
	h := m.routes[prefix]
	m.mu.RUnlock()
	if h == nil {
		return UnknownMethod(req)
	}
	return h.Serve(ctx, c, req, n)
}
