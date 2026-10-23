package handlers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/server"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// server.Notifier must satisfy contracts.Notifier for ServerHandler.Serve to
// pass one through unchanged.
var _ contracts.Notifier = server.Notifier(nil)

// recordingContractsHandler records the contracts caller and request it saw.
type recordingContractsHandler struct {
	caller contracts.Caller
	method string
}

func (h *recordingContractsHandler) Serve(_ context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	h.caller, h.method = c, req.Method
	return Reply(req, protocol.EmptyResult{})
}

func TestServerHandlerServes(t *testing.T) {
	h := &recordingContractsHandler{}
	adapted := ServerHandler{Handler: h}
	id := protocol.NewStringID("11")
	notifier := server.Notifier(nil)

	reply := adapted.Serve(context.Background(),
		server.Caller{Kind: server.CallerPlugin, PluginID: "atlas", ConnID: 42},
		protocol.Message{Method: protocol.MethodStateGet, ID: &id}, notifier)

	if reply.Error != nil {
		t.Fatalf("reply error = %+v", reply.Error)
	}
	if h.method != protocol.MethodStateGet {
		t.Fatalf("method = %s", h.method)
	}
	if h.caller.Kind != contracts.CallerPlugin || h.caller.PluginID != "atlas" || h.caller.ConnID != 42 {
		t.Fatalf("caller = %+v", h.caller)
	}
}

func TestFromServerCaller(t *testing.T) {
	cases := []struct {
		in   server.Caller
		want contracts.CallerKind
	}{
		{server.Caller{Kind: server.CallerCLI}, contracts.CallerCLI},
		{server.Caller{Kind: server.CallerCLI, PluginID: "", ConnID: 3}, contracts.CallerCLI},
		{server.Caller{Kind: server.CallerPlugin, PluginID: "p", ConnID: 4}, contracts.CallerPlugin},
		{server.Caller{Kind: 0, ConnID: 5}, contracts.CallerKind(0)},
	}
	for _, tc := range cases {
		got := FromServerCaller(tc.in)
		if got.Kind != tc.want || got.PluginID != tc.in.PluginID || got.ConnID != tc.in.ConnID {
			t.Fatalf("FromServerCaller(%+v) = %+v", tc.in, got)
		}
		if got.Kind == contracts.CallerStem {
			t.Fatal("a Stem caller never crosses the socket")
		}
	}
}

// ctxHandler keeps the request context it was served with.
type ctxHandler struct{ ctx context.Context }

func (h *ctxHandler) Serve(ctx context.Context, _ contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	h.ctx = ctx
	return Reply(req, protocol.EmptyResult{})
}

// A watch keeps its request context for the life of the connection, so the
// Closing wrapper must not end it when Serve returns; every other method's
// context is released on return.
func TestServerHandlerContextLifetime(t *testing.T) {
	serve := func(method string, parent, closing context.Context) context.Context {
		id := protocol.NewStringID("1")
		h := &ctxHandler{}
		ServerHandler{Handler: h, Closing: closing}.Serve(parent, server.Caller{}, protocol.Message{Method: method, ID: &id}, nil)
		return h.ctx
	}

	t.Run("a plain request's context ends on return", func(t *testing.T) {
		if ctx := serve(protocol.MethodStateGet, context.Background(), context.Background()); ctx.Err() == nil {
			t.Fatal("request context still live after Serve returned")
		}
	})

	for _, m := range []string{protocol.MethodEventWatch, protocol.MethodStateWatch} {
		t.Run(m+" outlives Serve and ends with the connection", func(t *testing.T) {
			parent, conn := context.WithCancel(context.Background())
			ctx := serve(m, parent, context.Background())
			if err := ctx.Err(); err != nil {
				t.Fatalf("watch context ended after Serve returned: %v", err)
			}
			conn()
			if ctx.Err() == nil {
				t.Fatal("watch context did not end with the connection")
			}
		})
	}

	t.Run("shutdown cancels a request being served with ErrShutdown", func(t *testing.T) {
		for _, m := range []string{protocol.MethodStateGet, protocol.MethodStateWatch} {
			closing, shut := context.WithCancel(context.Background())
			var got context.Context
			id := protocol.NewStringID("1")
			blocked := handlerFunc(func(ctx context.Context) { got = ctx; shut(); <-ctx.Done() })
			ServerHandler{Handler: blocked, Closing: closing}.Serve(context.Background(), server.Caller{}, protocol.Message{Method: m, ID: &id}, nil)
			if cause := context.Cause(got); cause != contracts.ErrShutdown {
				t.Fatalf("%s: cause = %v, want ErrShutdown", m, cause)
			}
		}
	})

	t.Run("shutdown cancels a retained watch context with ErrShutdown", func(t *testing.T) {
		closing, shut := context.WithCancel(context.Background())
		ctx := serve(protocol.MethodStateWatch, context.Background(), closing)
		shut()
		<-ctx.Done() // ends only if the shutdown hook outlived Serve
	})
}

// Serving many requests on one long-lived connection leaves no children on
// its context.
func TestServerHandlerLeavesNoChildrenOnTheConnection(t *testing.T) {
	parent := &countingCtx{Context: context.Background(), done: make(chan struct{})}
	h := &ctxHandler{}
	sh := ServerHandler{Handler: h, Closing: context.Background()}
	id := protocol.NewStringID("1")
	for i := 0; i < 10000; i++ {
		sh.Serve(parent, server.Caller{}, protocol.Message{Method: protocol.MethodStateGet, ID: &id}, nil)
	}
	if n := parent.live.Load(); n != 0 {
		t.Fatalf("%d request contexts still registered on the connection context", n)
	}
}

// countingCtx is a parent that is not a standard cancelCtx (its Done channel is
// its own), so context.WithCancelCause registers each child through the
// AfterFunc method the context package looks for. live counts the children
// registered and not yet released by their cancel.
type countingCtx struct {
	context.Context
	done chan struct{}
	live atomic.Int64
}

func (c *countingCtx) Done() <-chan struct{} { return c.done }

func (c *countingCtx) AfterFunc(func()) (stop func() bool) {
	c.live.Add(1)
	var once sync.Once
	return func() bool {
		released := false
		once.Do(func() { c.live.Add(-1); released = true })
		return released
	}
}

type handlerFunc func(ctx context.Context)

func (f handlerFunc) Serve(ctx context.Context, _ contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	f(ctx)
	return Reply(req, protocol.EmptyResult{})
}
