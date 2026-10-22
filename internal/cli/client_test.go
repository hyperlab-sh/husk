package cli

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func dialTest(t *testing.T, sock string, opts ClientOptions) *Client {
	t.Helper()
	c, err := Dial(t.Context(), sock, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func exitOf(t *testing.T, err error) int {
	t.Helper()
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v (%T) is not *cli.Error", err, err)
	}
	return ce.Exit
}

func TestClientCallReturnsRawResult(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", func(c *srvConn, req protocol.Message) {
		c.reply(req, map[string]any{"echo": req.Method})
	})
	c := dialTest(t, srv.Socket, ClientOptions{})
	for i := 0; i < 2; i++ { // two calls on one connection: ids must not collide
		got, err := c.Call(t.Context(), "x.y", map[string]any{"k": 1})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{"echo":"x.y"}` {
			t.Fatalf("result = %s", got)
		}
	}
	reqs := srv.requests()
	if len(reqs) != 2 || reqs[0].ID.String() == reqs[1].ID.String() {
		t.Fatalf("requests = %+v", reqs)
	}
	if string(reqs[0].Params) != `{"k":1}` {
		t.Fatalf("params = %s", reqs[0].Params)
	}
}

func TestClientSendsTokenOnlyWhenSet(t *testing.T) {
	for _, tc := range []struct{ name, token string }{{"user", ""}, {"plugin", "tok-1"}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{}) })
			c := dialTest(t, srv.Socket, ClientOptions{Token: tc.token})
			if _, err := c.Call(t.Context(), "x.y", nil); err != nil {
				t.Fatal(err)
			}
			if got := srv.requests()[0]; got.Token != tc.token || got.HasToken() != (tc.token != "") {
				t.Fatalf("token = %q (present %v), want %q", got.Token, got.HasToken(), tc.token)
			}
		})
	}
}

func TestClientMapsEveryErrorCodeToItsExit(t *testing.T) {
	for _, code := range protocol.Codes() {
		want := ExitRemote
		switch code {
		case protocol.CodeVersionMismatch, protocol.CodeParseError, protocol.CodeInvalidMessage, protocol.CodeLineTooLong:
			want = ExitProtocol
		}
		t.Run(string(code), func(t *testing.T) {
			srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) { c.fail(req, code, "boom") })
			c := dialTest(t, srv.Socket, ClientOptions{})
			_, err := c.Call(t.Context(), "x.y", nil)
			var ce *Error
			if !errors.As(err, &ce) || ce.Exit != want || ce.Code != string(code) || ce.Message != "boom" {
				t.Fatalf("err = %#v, want exit %d code %s", err, want, code)
			}
		})
	}
}

func TestClientErrorDataIsKept(t *testing.T) {
	srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) {
		c.send(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: protocol.CodeConflict, Message: "stale", Data: []byte(`{"revision":9}`)}})
	})
	c := dialTest(t, srv.Socket, ClientOptions{})
	_, err := c.Call(t.Context(), "x.y", nil)
	var ce *Error
	if !errors.As(err, &ce) || string(ce.Data) != `{"revision":9}` {
		t.Fatalf("err = %#v", err)
	}
}

func TestClientUnreadableReplyIsProtocolExit(t *testing.T) {
	srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) { c.raw("not json") })
	c := dialTest(t, srv.Socket, ClientOptions{})
	_, err := c.Call(t.Context(), "x.y", nil)
	if got := exitOf(t, err); got != ExitProtocol {
		t.Fatalf("exit = %d, want %d", got, ExitProtocol)
	}
}

func TestClientHostProtocolErrorNotificationIsProtocolExit(t *testing.T) {
	srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) {
		c.send(protocol.Message{Method: protocol.MethodProtocolError, Params: []byte(`{"code":"parse_error","message":"bad","method":""}`)})
	})
	c := dialTest(t, srv.Socket, ClientOptions{})
	_, err := c.Call(t.Context(), "x.y", nil)
	if got := exitOf(t, err); got != ExitProtocol {
		t.Fatalf("exit = %d, want %d", got, ExitProtocol)
	}
}

func TestClientConnectionDropIsTransportExit(t *testing.T) {
	srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) { c.close() })
	c := dialTest(t, srv.Socket, ClientOptions{})
	_, err := c.Call(t.Context(), "x.y", nil)
	if got := exitOf(t, err); got != ExitTransport {
		t.Fatalf("exit = %d, want %d", got, ExitTransport)
	}
}

func TestClientContextEndIsTransportExit(t *testing.T) {
	seen := make(chan struct{})
	srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) { close(seen) }) // never replies
	c := dialTest(t, srv.Socket, ClientOptions{})
	ctx, cancel := context.WithCancel(t.Context())
	go func() { <-seen; cancel() }()
	_, err := c.Call(ctx, "x.y", nil)
	if got := exitOf(t, err); got != ExitTransport || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v (exit %d)", err, got)
	}
}

func TestDialMissingSocketIsNoInstance(t *testing.T) {
	_, err := Dial(t.Context(), shortDir(t)+"/gone.sock", ClientOptions{})
	if got := exitOf(t, err); got != ExitNoInstance {
		t.Fatalf("exit = %d, want %d", got, ExitNoInstance)
	}
}

func TestStreamDeliversNotificationsThenWentAway(t *testing.T) {
	srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) {
		c.reply(req, map[string]any{"current": true})
		c.send(protocol.Message{Method: "state.changed", Params: []byte(`{"n":1}`)})
		c.send(protocol.Message{Method: "state.changed", Params: []byte(`{"n":2}`)})
		c.close()
	})
	c := dialTest(t, srv.Socket, ClientOptions{})
	st, err := c.Stream(t.Context(), "state.watch", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(st.Result) != `{"current":true}` {
		t.Fatalf("opening result = %s", st.Result)
	}
	for _, want := range []string{`{"n":1}`, `{"n":2}`} {
		m, err := st.Next(t.Context())
		if err != nil || m.Method != "state.changed" || string(m.Params) != want {
			t.Fatalf("Next = %+v, %v; want params %s", m, err, want)
		}
	}
	_, err = st.Next(t.Context())
	var ce *Error
	if !errors.As(err, &ce) || ce.Exit != ExitTransport || ce.Message != "instance went away" || !errors.Is(err, io.EOF) {
		t.Fatalf("end of stream = %#v", err)
	}
}

func TestStreamOpenErrorIsReturned(t *testing.T) {
	srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) { c.fail(req, protocol.CodeInvalidParams, "no") })
	c := dialTest(t, srv.Socket, ClientOptions{})
	if _, err := c.Stream(t.Context(), "state.watch", nil); exitOf(t, err) != ExitRemote {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamNextHonoursContext(t *testing.T) {
	srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{}) })
	c := dialTest(t, srv.Socket, ClientOptions{})
	st, err := c.Stream(t.Context(), "state.watch", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := st.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next = %v, want context.Canceled", err)
	}
}

func TestClientIgnoresRepliesToOtherIDs(t *testing.T) {
	srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) {
		other, _ := protocol.NewIntID(9999)
		c.send(protocol.Message{ID: &other, Result: []byte(`{"stray":true}`)})
		c.reply(req, map[string]any{"mine": true})
	})
	c := dialTest(t, srv.Socket, ClientOptions{})
	got, err := c.Call(t.Context(), "x.y", nil)
	if err != nil || string(got) != `{"mine":true}` {
		t.Fatalf("Call = %s, %v", got, err)
	}
}

func TestClientUnattributableErrorReplyIsAnErrorNotATimeout(t *testing.T) {
	// A reply without a usable id cannot be matched; the codec reports it as
	// a malformed line, which must end the call as a protocol failure rather
	// than leave it waiting for its own reply.
	for _, line := range []string{
		`{"id":null,"error":{"code":"internal","message":"boom"}}`,
		`{"error":{"code":"internal","message":"boom"}}`,
	} {
		srv := serve(t, shortDir(t), "a", func(c *srvConn, req protocol.Message) { c.raw(line) })
		c := dialTest(t, srv.Socket, ClientOptions{})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		_, err := c.Call(ctx, "x.y", nil)
		cancel()
		if got := exitOf(t, err); got != ExitProtocol {
			t.Fatalf("%s: exit = %d (%v), want %d", line, got, err, ExitProtocol)
		}
	}
}
