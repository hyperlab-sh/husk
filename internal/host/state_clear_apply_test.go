package host

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/internal/pods/server"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// clearContextObserver keeps the real mux and observes server cancellation so
// the disconnect test releases the loop only after the request has ended.
type clearContextObserver struct {
	next  contracts.Handler
	ended chan struct{}
}

func (o clearContextObserver) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, n contracts.Notifier) protocol.Message {
	if req.Method == protocol.MethodStateClear {
		context.AfterFunc(ctx, func() { close(o.ended) })
	}
	return o.next.Serve(ctx, c, req, n)
}

// observingClearServer intercepts registration, retaining the production
// ServerHandler and mux while observing the connection's context.
type observingClearServer struct {
	server.Server
	ended chan struct{}
}

func (s observingClearServer) Handle(prefix string, next server.Handler) {
	if prefix == "state" {
		adapted := next.(handlers.ServerHandler)
		adapted.Handler = clearContextObserver{next: adapted.Handler, ended: s.ended}
		next = adapted
	}
	s.Server.Handle(prefix, next)
}

func TestStateClearApplySocketShutdown(t *testing.T) {
	spec := state.Spec{Namespace: "atlas.state", Owner: "atlas", Scope: state.ScopeUser, Persistent: true, Retention: state.Retention{Policy: state.KeepLatest}, Initial: json.RawMessage(`{"fresh":true}`)}
	dir := t.TempDir()
	// Persist through a real Store shutdown, then hydrate that copy in the host
	// whose queued reset will be abandoned. Its dirty registration also exercises
	// the epoch fence against the final flush resurrecting the old value.
	seed, err := bootIn(t, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed.loop(func() {
		handle, e := seed.root.st.Register(spec)
		if e != nil {
			t.Error(e)
			return
		}
		if _, e = handle.Set([]byte(`{"dirty":1}`)); e != nil {
			t.Error(e)
		}
	})
	seed.shutdown()
	hook, ready := readyHook("")
	h, err := bootIn(t, dir, nil, withHook(hook))
	if err != nil {
		t.Fatal(err)
	}
	h.loop(func() {
		if _, e := h.root.st.Register(spec); e != nil {
			t.Error(e)
		}
	})
	// Locate by namespace rather than depending on Store's scope directory layout.
	path := ""
	if err := filepath.WalkDir(h.root.store.Paths().State, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() && d.Name() == "current.json" && filepath.Base(filepath.Dir(p)) == spec.Namespace {
			path = p
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("persisted slice missing before clear")
	}
	if got := string(h.root.st.Snapshot().Slices[spec.Namespace].Value); got != `{"dirty":1}` {
		t.Fatalf("hydrated %s", got)
	}
	runHost(t, ready, h)
	conn := dial(t, h.root.srv.SocketPath())
	entered, proceed := make(chan struct{}), make(chan struct{})
	closed := make(chan error, 1)
	h.post(fnMsg{fn: func() { close(entered); <-proceed; closed <- h.root.Close() }})
	<-entered
	queued := len(h.in)
	if _, err := conn.c.Write([]byte(`{"id":1,"method":"state.clear","params":{"namespace":"atlas.state","force":true}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	for len(h.in) <= queued {
		runtime.Gosched()
	}
	close(proceed)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close exceeded the bound for a queued clear")
	}
	line, err := conn.r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var reply protocol.Message
	if err := json.Unmarshal(line, &reply); err != nil || reply.Error != nil || string(reply.Result) != "{}" {
		t.Fatalf("reply = %s (%v)", line, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("persisted copy survived clear: %v", err)
	}
}

func TestStateClearApplySocketDisconnect(t *testing.T) {
	hook, ready := readyHook("")
	ended := make(chan struct{})
	h, err := boot(t, nil, withHook(hook), func(o *options) {
		o.newServer = func(cfg server.Config) server.Server {
			return observingClearServer{Server: server.New(cfg), ended: ended}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	h.loop(func() {
		handle, e := h.root.st.Register(state.Spec{Namespace: "atlas.state", Owner: "atlas", Scope: state.ScopeUser, Initial: json.RawMessage(`{"fresh":true}`)})
		if e != nil {
			t.Error(e)
			return
		}
		if _, e = handle.Set([]byte(`{"dirty":1}`)); e != nil {
			t.Error(e)
		}
	})
	runHost(t, ready, h)
	conn := dial(t, h.root.srv.SocketPath())
	entered, proceed := make(chan struct{}), make(chan struct{})
	h.post(fnMsg{fn: func() { close(entered); <-proceed }})
	<-entered
	queued := len(h.in)
	if _, err := conn.c.Write([]byte(`{"id":1,"method":"state.clear","params":{"namespace":"atlas.state","force":true}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	for len(h.in) <= queued {
		runtime.Gosched()
	}
	if err := conn.c.Close(); err != nil {
		t.Fatal(err)
	}
	<-ended
	close(proceed)
	h.loop(func() {}) // ordered behind the reset
	second := dial(t, h.root.srv.SocketPath())
	reply := second.callParams(t, "read", protocol.MethodStateGet, map[string]any{"slice": "atlas.state"})
	var result contracts.StateReadResult
	if err := json.Unmarshal(reply.Result, &result); err != nil || reply.Error != nil || string(result.Value) != `{"fresh":true}` || result.Revision != 3 {
		t.Fatalf("get = %+v (%v), reply %+v", result, err, reply)
	}
}
