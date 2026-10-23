package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

const testToken = "tok-123"

// logBuf is a stderr stand-in the test can wait on while the plugin writes.
type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
	ch chan string
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.b.Write(p)
	l.mu.Unlock()
	select {
	case l.ch <- string(p):
	default:
	}
	return len(p), nil
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// harness runs a plugin over pipes: the host's stdin, the plugin's stdout and
// a socket connection. Nothing sleeps; ordering comes from the plugin's
// single loop and from channel receives.
type harness struct {
	t     *testing.T
	in    *io.PipeWriter // host -> plugin stdin
	out   chan protocol.Message
	sockW net.Conn // host end of the socket
	sock  chan protocol.Message
	logs  *logBuf
	armed chan func() // retry callbacks handed to cfg.after
	done  chan error
	pings int
}

func start(t *testing.T, contributor bool) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	plug, host := net.Pipe()
	h := &harness{
		t: t, in: inW, sockW: host,
		out: make(chan protocol.Message, 64), sock: make(chan protocol.Message, 64),
		logs:  &logBuf{ch: make(chan string, 256)},
		armed: make(chan func(), 256), done: make(chan error, 1),
	}
	pump := func(r io.Reader, ch chan protocol.Message) {
		_ = protocol.ReadLoop(r, func(m protocol.Message) { ch <- m }, func(le *protocol.LineError) {
			t.Errorf("plugin wrote an unreadable line: %v", le)
		})
		close(ch)
	}
	go pump(outR, h.out)
	go pump(host, h.sock)
	go func() {
		err := run(config{contributor: contributor, token: testToken, after: func(f func()) { h.armed <- f }}, inR, outW, plug, h.logs)
		outW.Close()
		plug.Close()
		h.done <- err
	}()
	t.Cleanup(func() { inW.Close(); host.Close() })
	return h
}

func (h *harness) send(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.in, line+"\n"); err != nil {
		h.t.Fatalf("write to plugin: %v", err)
	}
}

func (h *harness) toSocket(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.sockW, line+"\n"); err != nil {
		h.t.Fatalf("write to plugin socket: %v", err)
	}
}

func recv(t *testing.T, ch chan protocol.Message, what string) protocol.Message {
	t.Helper()
	select {
	case m, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed while waiting for %s", what)
		}
		return m
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	return protocol.Message{}
}

// sockNext returns the next socket message, which must be method and carry the
// plugin's token.
func (h *harness) sockNext(method string) protocol.Message {
	h.t.Helper()
	m := recv(h.t, h.sock, method)
	if m.Method != method {
		h.t.Fatalf("socket got %q, want %q", m.Method, method)
	}
	if m.Token != testToken {
		h.t.Errorf("%s token = %q, want %q", method, m.Token, testToken)
	}
	return m
}

func (h *harness) outNext() protocol.Message {
	h.t.Helper()
	return recv(h.t, h.out, "a reply on stdout")
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func (h *harness) update() protocol.SurfaceUpdateParams {
	h.t.Helper()
	return decode[protocol.SurfaceUpdateParams](h.t, h.sockNext(protocol.MethodSurfaceUpdate).Params)
}

// stdoutBarrier proves the plugin processed everything sent on stdin so far
// and wrote nothing to stdout in between: its single loop answers an unknown
// request after handling every earlier line.
func (h *harness) stdoutBarrier() {
	h.t.Helper()
	h.pings++
	id := fmt.Sprintf("barrier-%d", h.pings)
	h.send(`{"id":"` + id + `","method":"barrier.ping"}`)
	m := h.outNext()
	if m.ID == nil || m.ID.String() != `"`+id+`"` || m.Error == nil || m.Error.Code != protocol.CodeUnknownMethod {
		h.t.Fatalf("expected the barrier's unknown_method reply, got %+v", m)
	}
}

func (h *harness) key(surface, key string) {
	h.send(`{"method":"input.key","params":{"surface":"` + surface + `","key":"` + key + `","modifiers":{"alt":false,"ctrl":false,"shift":false,"super":false}}}`)
}

func resized(surface string, w, hgt int, focused, visible bool) string {
	return fmt.Sprintf(`{"method":"surface.resized","params":{"surfaces":[{"surface":"%s","width":%d,"height":%d,"focused":%t,"visible":%t}]}}`,
		surface, w, hgt, focused, visible)
}

func (h *harness) waitLog(substr string) {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for !strings.Contains(h.logs.String(), substr) {
		select {
		case <-h.logs.ch:
		case <-deadline:
			h.t.Fatalf("log never contained %q: %q", substr, h.logs.String())
		}
	}
}

func TestRenderUsesSurfaceSizeAndAnchors(t *testing.T) {
	h := start(t, false)
	h.send(resized("main-stage", 50, 20, true, true))
	u := h.update()
	if u.Surface != protocol.SurfaceMainStage || u.Title != "Reference plugin" {
		t.Fatalf("surface.update target = %s %q", u.Surface, u.Title)
	}
	if got := u.Lines[1]; got != strings.Repeat("─", 50) {
		t.Errorf("separator = %d cells, want 50", len([]rune(got)))
	}
	for i, row := range rows {
		anchor := "\x1b]husk;slot=" + rowActionsSlot + ";key=" + row + "\x1b\\"
		if line := u.Lines[2+i]; !strings.HasSuffix(line, anchor) {
			t.Errorf("row %d = %q, want anchor suffix %q", i, line, anchor)
		}
	}
	if !strings.HasPrefix(u.Lines[2], "> alpha") || !strings.HasPrefix(u.Lines[3], "  bravo") {
		t.Errorf("selection marker wrong: %q %q", u.Lines[2], u.Lines[3])
	}
	if !strings.Contains(strings.Join(u.Lines, "\n"), "size 50x20 · replies: none") {
		t.Errorf("status line = %q", u.Lines)
	}
	for i, l := range u.Lines {
		if i >= 2 && i < 2+len(rows) {
			continue
		}
		if n := len([]rune(l)); n > 50 {
			t.Errorf("line %d is %d cells, over the 50 cell surface", i, n)
		}
	}

	h.send(resized("main-stage", 30, 10, true, true))
	u = h.update()
	if got := u.Lines[1]; got != strings.Repeat("─", 30) {
		t.Errorf("after resize separator = %d cells, want 30", len([]rune(got)))
	}
	if !strings.Contains(strings.Join(u.Lines, "\n"), "size 30x10") {
		t.Errorf("status line does not report the new size: %q", u.Lines)
	}
}

func TestNothingRendersWhenNothingVisibleChanged(t *testing.T) {
	h := start(t, false)
	h.send(resized("main-stage", 30, 10, true, true))
	h.update()
	h.send(resized("main-stage", 30, 10, true, true))   // identical
	h.send(resized("main-stage", 30, 10, false, true))  // focus only
	h.send(resized("main-stage", 40, 12, false, false)) // hidden
	h.send(resized("side-panel", 40, 12, false, true))  // not ours
	h.send(resized("main-stage", 0, 0, true, true))     // no room
	h.send(`{"method":"input.key_state","params":{"phase":"down","key":"alt","held":true,"modifiers":{"alt":true,"ctrl":false,"shift":false,"super":false}}}`)
	h.send(`{"method":"theme.changed","params":{"palette":{"name":"x"}}}`)
	h.send(`{"method":"input.mouse","params":{"surface":"main-stage","x":1,"y":1,"button":"left"}}`)
	h.send(resized("main-stage", 40, 12, false, true)) // visible again: renders
	// The next socket message must be that render, not one of the above.
	if u := h.update(); u.Surface != protocol.SurfaceMainStage || !strings.Contains(strings.Join(u.Lines, "\n"), "size 40x12") {
		t.Errorf("first render after the quiet period = %q", u.Lines)
	}
	h.stdoutBarrier()
}

func TestKeysEmitEventAndCallHook(t *testing.T) {
	h := start(t, false)
	h.send(resized("main-stage", 60, 20, true, true))
	h.update()

	h.key("main-stage", "j")
	em := h.sockNext(protocol.MethodEventEmit)
	if em.ID == nil {
		t.Fatal("event.emit must be a request: the CLI-style caller needs the reply")
	}
	ev := decode[struct {
		Name string
		Data map[string]any
	}](t, em.Params)
	if ev.Name != selectionEvent || ev.Data["row"] != "bravo" {
		t.Errorf("event = %+v", ev)
	}
	if u := h.update(); !strings.HasPrefix(u.Lines[3], "> bravo") {
		t.Errorf("selected row not moved: %q", u.Lines[3])
	}
	h.toSocket(`{"id":` + em.ID.String() + `,"result":{}}`)

	h.key("main-stage", "k")
	h.key("main-stage", "up") // wraps from alpha to charlie
	h.sockNext(protocol.MethodEventEmit)
	h.update()
	last := decode[struct{ Data map[string]any }](t, h.sockNext(protocol.MethodEventEmit).Params)
	if last.Data["row"] != "charlie" {
		t.Errorf("selection must wrap to the last row, got %v", last.Data["row"])
	}
	h.update()
	h.key("main-stage", "down") // wraps back to alpha
	h.sockNext(protocol.MethodEventEmit)
	h.update()

	h.key("main-stage", "r")
	c1 := h.sockNext(protocol.MethodHookCall)
	h.key("main-stage", "r")
	c2 := h.sockNext(protocol.MethodHookCall)
	hp := decode[struct {
		Hook string
		Data map[string]any
	}](t, c1.Params)
	if hp.Hook != refreshHook || hp.Data["reason"] != "manual" {
		t.Errorf("hook.call params = %+v", hp)
	}
	if c1.ID == nil || c2.ID == nil || c1.ID.String() == c2.ID.String() {
		t.Fatalf("hook.call ids must be distinct: %v %v", c1.ID, c2.ID)
	}

	h.toSocket(`{"id":` + c1.ID.String() + `,"result":{"kind":"gate","allowed":true,"vetoes":[],"results":[{"plugin_id":"` + contributorID + `"}],"future":1}}`)
	if got := strings.Join(h.update().Lines, "\n"); !strings.Contains(got, "replies: refresh allowed by 1") {
		t.Errorf("allowed outcome not shown: %q", got)
	}
	h.toSocket(`{"id":` + c2.ID.String() + `,"result":{"kind":"gate","allowed":false,"vetoes":[{"plugin_id":"index","reason":"busy"}],"results":[{"plugin_id":"index","veto":true,"reason":"busy"}]}}`)
	if got := strings.Join(h.update().Lines, "\n"); !strings.Contains(got, "replies: refresh vetoed by index: busy") {
		t.Errorf("veto not shown: %q", got)
	}

	h.key("main-stage", "r")
	c3 := h.sockNext(protocol.MethodHookCall)
	h.toSocket(`{"id":` + c3.ID.String() + `,"result":{"kind":"gate","allowed":true,"vetoes":[],"results":[{"plugin_id":"` + contributorID + `","error":{"code":"timeout","message":"no reply in 10s"}}]}}`)
	if got := strings.Join(h.update().Lines, "\n"); !strings.Contains(got, "replies: refresh allowed by 0") {
		t.Errorf("subscriber timeout must not count as an answer: %q", got)
	}
	h.key("main-stage", "r")
	c4 := h.sockNext(protocol.MethodHookCall)
	h.toSocket(`{"id":` + c4.ID.String() + `,"result":{"kind":"gate","allowed":true,"vetoes":[],"results":[]}}`)
	if got := strings.Join(h.update().Lines, "\n"); !strings.Contains(got, "replies: refresh allowed by 0") {
		t.Errorf("empty outcome not shown: %q", got)
	}
	for _, c := range []struct{ name, result, want string }{
		{"collect outcome", `{"kind":"collect","results":[]}`, `refresh outcome of kind`},
		{"gate without allowed", `{"kind":"gate","vetoes":[],"results":[]}`, `refresh outcome unreadable`},
		{"two vetoes", `{"kind":"gate","allowed":false,"vetoes":[{"plugin_id":"a","reason":"x"},{"plugin_id":"b","reason":"y"}],"results":[]}`, `refresh vetoed by a: x (+1 more)`},
	} {
		h.key("main-stage", "r")
		call := h.sockNext(protocol.MethodHookCall)
		h.toSocket(`{"id":` + call.ID.String() + `,"result":` + c.result + `}`)
		got := strings.Join(h.update().Lines, "\n")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: want %q in %q", c.name, c.want, got)
		}
		if c.name != "two vetoes" && strings.Contains(got, "vetoed") {
			t.Errorf("%s must not render as a veto: %q", c.name, got)
		}
	}
	h.key("main-stage", "r")
	c5 := h.sockNext(protocol.MethodHookCall)
	h.toSocket(`{"id":` + c5.ID.String() + `,"error":{"code":"denied","message":"no"}}`)
	if got := strings.Join(h.update().Lines, "\n"); !strings.Contains(got, "replies: refresh not asked: denied") {
		t.Errorf("error reply not shown: %q", got)
	}
}

func TestRefreshActionFiresTheGateLikeTheRKey(t *testing.T) {
	h := start(t, false)
	h.send(resized("main-stage", 60, 20, true, true))
	h.update()

	h.send(`{"id":21,"method":"action.invoke","params":{"action":"` + refreshAction + `","args":{},"caller":{"kind":"cli"}}}`)
	call := h.sockNext(protocol.MethodHookCall)
	hp := decode[struct {
		Hook string
		Data map[string]any
	}](t, call.Params)
	if hp.Hook != refreshHook || hp.Data["reason"] != "manual" {
		t.Errorf("hook.call params = %+v", hp)
	}
	if m := h.outNext(); m.ID.String() != "21" || m.Error != nil {
		t.Errorf("action reply = %+v", m)
	}
	h.toSocket(`{"id":` + call.ID.String() + `,"result":{"kind":"gate","allowed":true,"vetoes":[],"results":[{"plugin_id":"` + contributorID + `"}]}}`)
	if got := strings.Join(h.update().Lines, "\n"); !strings.Contains(got, "replies: refresh allowed by 1") {
		t.Errorf("outcome not shown: %q", got)
	}
}

func TestRefreshActionBelongsToThePublisherOnly(t *testing.T) {
	h := start(t, true)
	h.sockNext(protocol.MethodSlotContribute)
	h.send(`{"id":22,"method":"action.invoke","params":{"action":"` + refreshAction + `","args":{}}}`)
	if m := h.outNext(); m.ID.String() != "22" || m.Error == nil || m.Error.Code != protocol.CodeNotFound {
		t.Errorf("contributor refresh reply = %+v", m)
	}
}

func TestUnboundOrForeignKeysDoNothing(t *testing.T) {
	h := start(t, false)
	h.send(resized("main-stage", 60, 20, true, true))
	h.update()
	h.key("main-stage", "x")                                                                   // unbound
	h.key("side-panel", "j")                                                                   // surface the plugin does not occupy
	h.send(`{"method":"input.key","params":{"surface":"main-stage","key":"","modifiers":{}}}`) // invalid
	h.send(`{"method":"input.key","params":"nope"}`)
	// The barrier's event.emit is the first thing on the socket, and it moved
	// the selection exactly once.
	h.key("main-stage", "j")
	if ev := decode[struct{ Data map[string]any }](t, h.sockNext(protocol.MethodEventEmit).Params); ev.Data["row"] != "bravo" {
		t.Errorf("selection = %v after one effective key, want bravo", ev.Data["row"])
	}
	h.update()
}

func TestRepliesToUnknownReplyIdsAreIgnored(t *testing.T) {
	h := start(t, false)
	h.toSocket(`{"id":999,"result":{}}`)
	h.waitLog("reply 999 matches no request")
}

func TestHostRequestsGetErrorRepliesAndErrorsAreNeverAnswered(t *testing.T) {
	h := start(t, false)
	h.send(`{"id":5,"method":"action.invoke","params":{"action":"go.reference.nope","args":{}}}`)
	if m := h.outNext(); m.ID.String() != "5" || m.Error == nil || m.Error.Code != protocol.CodeNotFound {
		t.Errorf("action.invoke reply = %+v", m)
	}
	h.send(`{"id":6,"method":"hook.call","params":{"hook":"` + refreshHook + `","data":{}}}`)
	if m := h.outNext(); m.ID.String() != "6" || m.Error == nil || m.Error.Code != protocol.CodeNotFound {
		t.Errorf("hook.call on the publisher = %+v", m)
	}
	h.send(`{"method":"protocol.error","params":{"code":"invalid_message","message":"nope","method":"surface.update"}}`)
	h.waitLog(`host rejected "surface.update": invalid_message: nope`)
	h.toSocket(`{"method":"protocol.error","params":{"code":"denied","message":"bad token","method":"surface.update"}}`)
	h.waitLog("denied: bad token")
	h.stdoutBarrier() // nothing was written to stdout for either error
}

func TestEOFEndsTheRun(t *testing.T) {
	h := start(t, false)
	h.in.Close()
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("run returned %v at EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return at EOF")
	}
}

func TestSocketCloseEndsTheRunWithAnError(t *testing.T) {
	h := start(t, false)
	h.sockW.Close()
	select {
	case err := <-h.done:
		if err == nil || !strings.Contains(err.Error(), "socket closed") {
			t.Errorf("run returned %v, want a socket closed error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return when the socket closed")
	}
}

func TestContributorFillsSlotAndAnswersHook(t *testing.T) {
	h := start(t, true)
	c := h.sockNext(protocol.MethodSlotContribute)
	if c.ID == nil {
		t.Fatal("slot.contribute must be a request")
	}
	p := decode[protocol.SlotContributeParams](t, c.Params)
	if p.SlotID != rowActionsSlot || p.ID != inspectContrib || p.OnClick != inspectAction || !strings.Contains(p.Content, "[inspect]") {
		t.Errorf("contribution = %+v", p)
	}
	h.toSocket(`{"id":` + c.ID.String() + `,"result":{}}`)

	h.send(`{"id":7,"method":"hook.call","params":{"hook":"` + refreshHook + `","kind":"gate","data":{"reason":"manual"}}}`)
	r := h.outNext()
	if r.ID.String() != "7" || r.Error != nil || r.Token != "" {
		t.Fatalf("hook reply = %+v", r)
	}
	if got := string(r.Result); got != "{}" {
		t.Errorf("gate reply = %s, want exactly {}", got)
	}
	h.send(`{"id":"h","method":"hook.call","params":{"hook":"someone.else","data":{}}}`)
	if m := h.outNext(); m.ID.String() != `"h"` || m.Error == nil || m.Error.Code != protocol.CodeNotFound {
		t.Errorf("foreign hook reply = %+v", m)
	}

	h.send(`{"id":8,"method":"action.invoke","params":{"action":"` + inspectAction + `","args":{"row_key":"bravo"},"caller":{"kind":"input"}}}`)
	toast := decode[protocol.ToastShowParams](t, h.sockNext(protocol.MethodToastShow).Params)
	if toast.Message != "row bravo" || toast.Title != "Inspect" || toast.Level != "info" {
		t.Errorf("toast = %+v", toast)
	}
	if m := h.outNext(); m.ID.String() != "8" || m.Error != nil {
		t.Errorf("action reply = %+v", m)
	}
	h.send(`{"id":9,"method":"action.invoke","params":{"action":"someone.else"}}`)
	if m := h.outNext(); m.Error == nil || m.Error.Code != protocol.CodeNotFound {
		t.Errorf("foreign action reply = %+v", m)
	}

	h.send(`{"method":"event.deliver","params":{"name":"` + selectionEvent + `","source":"` + publisherID + `","data":{"row":"charlie"}}}`)
	h.waitLog("event " + selectionEvent + " row=charlie")

	// The contributor never renders: resizes and keys are ignored.
	h.send(resized("main-stage", 50, 20, true, true))
	h.key("main-stage", "j")
	h.stdoutBarrier()
	h.send(`{"id":10,"method":"action.invoke","params":{"action":"` + inspectAction + `","args":{}}}`)
	// The next socket message is this toast, so the resize and key sent nothing.
	if got := decode[protocol.ToastShowParams](t, h.sockNext(protocol.MethodToastShow).Params); got.Message != "row " {
		t.Errorf("toast = %+v", got)
	}
	h.outNext()

	// A non-string args value is absent, not an error.
	h.send(`{"id":11,"method":"action.invoke","params":{"action":"` + inspectAction + `","args":{"row_key":7,"extra":{"x":1}}}}`)
	if got := decode[protocol.ToastShowParams](t, h.sockNext(protocol.MethodToastShow).Params); got.Message != "row " {
		t.Errorf("non-string row_key toast = %+v", got)
	}
	if m := h.outNext(); m.ID.String() != "11" || m.Error != nil {
		t.Errorf("non-string args reply = %+v", m)
	}
}

func TestContributorRetriesWhenPublisherIsNotInTheCatalogYet(t *testing.T) {
	h := start(t, true)
	c := h.sockNext(protocol.MethodSlotContribute)
	h.toSocket(`{"id":` + c.ID.String() + `,"error":{"code":"not_found","message":"unknown slot"}}`)
	fire := recvFn(t, h.armed)
	select {
	case <-h.armed:
		t.Fatal("one refusal must arm exactly one retry")
	default:
	}
	fire()
	c2 := h.sockNext(protocol.MethodSlotContribute)
	if c2.ID.String() == c.ID.String() {
		t.Errorf("a retry must use a fresh request id")
	}
	h.toSocket(`{"id":` + c2.ID.String() + `,"result":{}}`)
	h.stdoutBarrier()
	select {
	case <-h.armed:
		t.Fatal("an accepted contribution must not retry")
	default:
	}
	if !strings.Contains(h.logs.String(), "refused a contribution: not_found") {
		t.Errorf("log = %q", h.logs.String())
	}
}

func recvFn(t *testing.T, ch chan func()) func() {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("no retry was armed")
	}
	return nil
}

func TestContributorRetriesAreBounded(t *testing.T) {
	h := start(t, true)
	for i := 0; i < maxRetries; i++ {
		c := h.sockNext(protocol.MethodSlotContribute)
		h.toSocket(`{"id":` + c.ID.String() + `,"error":{"code":"not_found","message":"unknown slot"}}`)
		recvFn(t, h.armed)()
	}
	c := h.sockNext(protocol.MethodSlotContribute)
	h.toSocket(`{"id":` + c.ID.String() + `,"error":{"code":"not_found","message":"unknown slot"}}`)
	h.waitLog(fmt.Sprintf("giving up on contributing after %d retries", maxRetries))
	h.stdoutBarrier()
	select {
	case <-h.armed:
		t.Fatal("retried past the bound")
	default:
	}
}

func TestClip(t *testing.T) {
	for _, c := range []struct {
		in    string
		width int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"héllo wörld", 6, "héllo…"},
		{"hello", 0, ""},
	} {
		if got := clip(c.in, c.width); got != c.want {
			t.Errorf("clip(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestLaunchRequiresTheHostEnvironment(t *testing.T) {
	t.Setenv("HUSK_SOCKET_PATH", "")
	t.Setenv("HUSK_PLUGIN_TOKEN", "x")
	if err := launch(false); err == nil || !strings.Contains(err.Error(), "HUSK_SOCKET_PATH") {
		t.Errorf("missing socket path: %v", err)
	}
	t.Setenv("HUSK_SOCKET_PATH", "/nonexistent/husk.sock")
	t.Setenv("HUSK_PLUGIN_TOKEN", "")
	if err := launch(false); err == nil {
		t.Error("missing token must fail")
	}
}

func TestPublisherHookNeedsItsKind(t *testing.T) {
	raw, err := os.ReadFile("husk-plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	noKind := strings.Replace(string(raw), `"kind": "gate", `, "", 1)
	if noKind == string(raw) {
		t.Fatal("the manifest no longer declares the hook kind this test strips")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "husk-plugin.json"), []byte(noKind), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manifest.NewRegistry().Load(dir); err == nil {
		t.Error("a hook without a kind must fail conversion")
	}
}

func TestManifestsAreValidV2AndMatchThePlugin(t *testing.T) {
	reg := manifest.NewRegistry()
	pub, warns, err := reg.Load(".")
	if err != nil || len(warns) != 0 {
		t.Fatalf("publisher manifest: %v %v", err, warns)
	}
	act, warns, err := reg.Load("../reference-actions")
	if err != nil || len(warns) != 0 {
		t.Fatalf("contributor manifest: %v %v", err, warns)
	}
	if pub.ID != publisherID || act.ID != contributorID || pub.Protocol != protocol.Major || act.Protocol != protocol.Major {
		t.Errorf("identities = %q/%d, %q/%d", pub.ID, pub.Protocol, act.ID, act.Protocol)
	}
	if len(pub.Surfaces) != 1 || pub.Surfaces[0].ID != protocol.SurfaceMainStage {
		t.Fatalf("publisher surfaces = %+v", pub.Surfaces)
	}
	if want := (protocol.FrameSpec{Border: true, Label: "Reference plugin", Close: true}); pub.Surfaces[0].Frame == nil || *pub.Surfaces[0].Frame != want {
		t.Errorf("frame = %+v, want %+v", pub.Surfaces[0].Frame, want)
	}
	if len(surfaces) != 1 || surfaces[0] != pub.Surfaces[0].ID {
		t.Errorf("code renders %v, manifest claims %v", surfaces, pub.Surfaces)
	}
	if len(pub.SecretRequests) != 1 || pub.SecretRequests[0].Name != secretName || pub.SecretRequests[0].Required || pub.SecretRequests[0].Purpose == "" {
		t.Errorf("publisher secrets = %+v", pub.SecretRequests)
	}
	if len(pub.RailItems) != 0 || len(act.Surfaces) != 0 {
		t.Errorf("no rail items or contributor surfaces expected: %+v %+v", pub.RailItems, act.Surfaces)
	}
	if len(pub.PublishedSlots) != 1 || pub.PublishedSlots[0].ID != rowActionsSlot || pub.PublishedSlots[0].Kind != "inline" {
		t.Errorf("published slots = %+v", pub.PublishedSlots)
	}
	if len(pub.HooksDeclared) != 1 || pub.HooksDeclared[0].ID != refreshHook || pub.HooksDeclared[0].Title == "" || pub.HooksDeclared[0].Kind != manifest.HookGate {
		t.Errorf("hooks = %+v", pub.HooksDeclared)
	}
	if len(pub.Actions) != 2 || pub.Actions[0].ID != refreshAction || pub.Actions[0].Title != "Refresh" || pub.Actions[1].ID != secretCheckAction {
		t.Errorf("publisher actions = %+v, want {%s, %s}", pub.Actions, refreshAction, secretCheckAction)
	}
	if len(pub.EventsDeclared) != 1 || pub.EventsDeclared[0].Name != selectionEvent || pub.EventsDeclared[0].Title == "" {
		t.Errorf("events = %+v", pub.EventsDeclared)
	}
	// The contributor's subscriptions and on_click action are what the code
	// answers and contributes.
	if len(act.HookRegistrations) != 1 || act.HookRegistrations[0].ID != refreshHook || act.HookRegistrations[0].Priority != 10 {
		t.Errorf("hook registrations = %+v", act.HookRegistrations)
	}
	if len(act.Subscriptions) != 1 || act.Subscriptions[0] != selectionEvent {
		t.Errorf("event subscriptions = %v", act.Subscriptions)
	}
	if len(act.Actions) != 1 || act.Actions[0].ID != inspectAction {
		t.Errorf("actions = %+v", act.Actions)
	}
	pc, ac := pub.Argv(), act.Argv()
	if filepath.Clean(pc[0]) != filepath.Clean(ac[0]) {
		t.Errorf("both plugins must run the same binary: %q vs %q", pc, ac)
	}
	if len(pc) != 1 || len(ac) != 2 || ac[1] != "--contributor" {
		t.Errorf("argv = %q, %q", pc, ac)
	}
}

// captureOf reports the wire state of a frame's capture member: nil when the
// member is absent, otherwise the value.
func captureOf(u protocol.SurfaceUpdateParams) *protocol.CaptureValue { return u.Capture }

func TestTextModeTogglesCaptureOnFrames(t *testing.T) {
	h := start(t, false)
	h.send(resized("main-stage", 60, 20, true, true))
	if u := h.update(); captureOf(u) != nil {
		t.Fatalf("first frame must omit capture, got %v", *u.Capture)
	}

	// A selection frame outside text mode omits the member too.
	h.key("main-stage", "j")
	h.sockNext(protocol.MethodEventEmit)
	if u := h.update(); captureOf(u) != nil {
		t.Fatalf("frame outside text mode must omit capture, got %v", *u.Capture)
	}

	h.key("main-stage", "i")
	on := h.update()
	if on.Capture == nil || *on.Capture != protocol.CaptureTrue {
		t.Fatalf("entering text mode: capture = %v, want true", on.Capture)
	}
	if last := on.Lines[len(on.Lines)-1]; last != "> " {
		t.Errorf("echo line on entry = %q, want %q", last, "> ")
	}

	// j, k and r are text here, not shortcuts: no event, no hook, one frame each.
	for i, step := range []struct{ key, echo string }{
		{"q", "> q"}, {"j", "> qj"}, {"r", "> qjr"}, {" ", "> qjr "}, {"ö", "> qjr ö"},
		{"backspace", "> qjr "}, {"tab", "> qjr "},
	} {
		h.key("main-stage", step.key)
		if step.key == "tab" {
			continue // not text: ignored, no frame
		}
		u := h.update()
		if u.Capture == nil || *u.Capture != protocol.CaptureTrue {
			t.Errorf("step %d: frame in text mode must carry capture=true, got %v", i, u.Capture)
		}
		if got := u.Lines[len(u.Lines)-1]; got != step.echo {
			t.Errorf("step %d: echo = %q, want %q", i, got, step.echo)
		}
	}

	// A frame rendered for another reason (resize) still carries true.
	h.send(resized("main-stage", 40, 12, true, true))
	if u := h.update(); u.Capture == nil || *u.Capture != protocol.CaptureTrue {
		t.Errorf("resize frame in text mode: capture = %v, want true", u.Capture)
	}

	h.key("main-stage", "enter")
	off := h.update()
	if off.Capture == nil || *off.Capture != protocol.CaptureFalse {
		t.Fatalf("leaving text mode: capture = %v, want false", off.Capture)
	}
	if got := off.Lines[len(off.Lines)-1]; got != "> qjr " {
		t.Errorf("echo kept after leaving = %q", got)
	}

	// The false rides one frame only; the next one omits the member again.
	h.send(resized("main-stage", 50, 15, true, true))
	if u := h.update(); u.Capture != nil {
		t.Errorf("frame after the toggle must omit capture, got %v", *u.Capture)
	}

	// j is a shortcut again, and esc also leaves text mode.
	h.key("main-stage", "i")
	if u := h.update(); u.Capture == nil || *u.Capture != protocol.CaptureTrue {
		t.Fatalf("re-entering: capture = %v", u.Capture)
	}
	h.key("main-stage", "esc")
	if u := h.update(); u.Capture == nil || *u.Capture != protocol.CaptureFalse {
		t.Fatalf("esc must leave text mode with capture=false, got %v", u.Capture)
	}
	h.key("main-stage", "j")
	h.sockNext(protocol.MethodEventEmit)
	h.update()
}

func TestCaptureFieldWireShape(t *testing.T) {
	for name, tc := range map[string]struct {
		textMode, sendOff bool
		want              string
	}{
		"outside":    {false, false, ""},
		"in mode":    {true, false, "true"},
		"after exit": {false, true, "false"},
	} {
		t.Run(name, func(t *testing.T) {
			s := protocol.SurfaceMainStage
			p := &plugin{textMode: map[protocol.SurfaceID]bool{s: tc.textMode}, sendOff: map[protocol.SurfaceID]bool{s: tc.sendOff}}
			b, err := json.Marshal(protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage, Lines: []string{}, Capture: p.captureField(s)})
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]json.RawMessage
			_ = json.Unmarshal(b, &m)
			got, has := m["capture"]
			if tc.want == "" && has || tc.want != "" && string(got) != tc.want {
				t.Errorf("capture on the wire = %q (present %v), want %q", got, has, tc.want)
			}
		})
	}
}

// secret-check asks the host for the declared secret and replies to the action
// once the read is decided: set:true on a value, an action error preserving
// the error's code and reason otherwise, and never the value or backend prose.
func TestSecretCheckRepliesWithoutTheValue(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reply      string
		wantCode   protocol.Code
		wantReason string
	}{
		{"value", `{"id":%s,"result":{"name":"go.reference.token","value":"s3cret"}}`, "", ""},
		{"declined", `{"id":%s,"error":{"code":"denied","message":"s3cret","data":{"reason":"user_denied","extra":"s3cret"}}}`, protocol.CodeDenied, "user_denied"},
		{"unset", `{"id":%s,"error":{"code":"not_found","message":"not_found: unset","data":{"reason":"unset"}}}`, protocol.CodeNotFound, "unset"},
		{"storage", `{"id":%s,"error":{"code":"unavailable","message":"s3cret","data":{"reason":"storage"}}}`, protocol.CodeUnavailable, "storage"},
		{"no reason", `{"id":%s,"error":{"code":"internal","message":"s3cret"}}`, protocol.CodeInternal, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := start(t, false)
			h.send(`{"id":"inv-1","method":"action.invoke","params":{"action":"` + secretCheckAction + `"}}`)
			get := h.sockNext(protocol.MethodSecretsGet)
			if got := decode[map[string]string](t, get.Params); got["name"] != secretName {
				t.Fatalf("secrets.get params = %v", got)
			}
			// The action is not answered until the read is.
			h.stdoutBarrier()
			h.toSocket(fmt.Sprintf(tc.reply, get.ID.String()))
			rep := h.outNext()
			if rep.ID == nil || rep.ID.String() != `"inv-1"` {
				t.Fatalf("action reply = %+v", rep)
			}
			raw, err := json.Marshal(rep)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "s3cret") || strings.Contains(h.logs.String(), "s3cret") {
				t.Fatal("secret value or backend prose leaked into a reply or log")
			}
			if tc.wantCode == "" {
				if rep.Error != nil || string(rep.Result) != `{"set":true}` {
					t.Fatalf("successful action = %+v", rep)
				}
				return
			}
			if rep.Error == nil || rep.Error.Code != tc.wantCode || rep.Result != nil {
				t.Fatalf("action reply = %+v, want error %s without a result", rep, tc.wantCode)
			}
			want := map[string]string{"reason": tc.wantReason}
			if got := decode[map[string]string](t, rep.Error.Data); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("error data = %v, want %v", got, want)
			}
		})
	}
}

// Text mode belongs to the surface that received `i`: another surface of the
// same plugin neither captures nor consumes the one-shot capture=false.
func TestCaptureFieldIsPerSurface(t *testing.T) {
	other := protocol.SurfaceSidebarList
	p := &plugin{textMode: map[protocol.SurfaceID]bool{protocol.SurfaceMainStage: true}}
	if c := p.captureField(other); c != nil {
		t.Errorf("other surface capture = %v, want absent", *c)
	}
	p = &plugin{sendOff: map[protocol.SurfaceID]bool{protocol.SurfaceMainStage: true}}
	if c := p.captureField(other); c != nil {
		t.Errorf("other surface consumed or sent the off frame: %v", *c)
	}
	if c := p.captureField(protocol.SurfaceMainStage); c == nil || *c != protocol.CaptureFalse {
		t.Errorf("main-stage off frame = %v, want false", c)
	}
	if c := p.captureField(protocol.SurfaceMainStage); c != nil {
		t.Errorf("off frame sent twice: %v", *c)
	}
}

func TestSecretCheckIsNotTheContributorsAction(t *testing.T) {
	h := start(t, true)
	h.sockNext(protocol.MethodSlotContribute)
	h.send(`{"id":"inv-1","method":"action.invoke","params":{"action":"` + secretCheckAction + `"}}`)
	if m := h.outNext(); m.Error == nil || m.Error.Code != protocol.CodeNotFound {
		t.Fatalf("contributor answered secret-check: %+v", m)
	}
}
