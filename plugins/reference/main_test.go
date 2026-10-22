package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/pods/manifest"
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

	h.toSocket(`{"id":` + c1.ID.String() + `,"result":{"results":[{"plugin_id":"` + contributorID + `","data":{"ready":true}}]}}`)
	if got := strings.Join(h.update().Lines, "\n"); !strings.Contains(got, "replies: 1, last "+contributorID) {
		t.Errorf("hook result not shown: %q", got)
	}
	h.toSocket(`{"id":` + c2.ID.String() + `,"result":{"results":[{"plugin_id":"` + contributorID + `","error":{"code":"timeout","message":"no reply in 10s"}}]}}`)
	if got := strings.Join(h.update().Lines, "\n"); !strings.Contains(got, "replies: 2, last timeout") {
		t.Errorf("subscriber timeout not shown: %q", got)
	}

	h.key("main-stage", "r")
	c3 := h.sockNext(protocol.MethodHookCall)
	h.toSocket(`{"id":` + c3.ID.String() + `,"result":{"results":[]}}`)
	if got := strings.Join(h.update().Lines, "\n"); !strings.Contains(got, "replies: 2, last no subscribers") {
		t.Errorf("empty result not shown: %q", got)
	}
	h.key("main-stage", "r")
	c4 := h.sockNext(protocol.MethodHookCall)
	h.toSocket(`{"id":` + c4.ID.String() + `,"error":{"code":"denied","message":"no"}}`)
	if got := strings.Join(h.update().Lines, "\n"); !strings.Contains(got, "replies: 3, last denied") {
		t.Errorf("error reply not shown: %q", got)
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

	h.send(`{"id":7,"method":"hook.call","params":{"hook":"` + refreshHook + `","data":{"reason":"manual"}}}`)
	r := h.outNext()
	if r.ID.String() != "7" || r.Error != nil || r.Token != "" {
		t.Fatalf("hook reply = %+v", r)
	}
	if got := decode[struct{ Data map[string]any }](t, r.Result); got.Data["ready"] != true {
		t.Errorf("hook reply data = %v", got.Data)
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
	if len(pub.RailItems) != 0 || len(act.Surfaces) != 0 {
		t.Errorf("no rail items or contributor surfaces expected: %+v %+v", pub.RailItems, act.Surfaces)
	}
	if len(pub.PublishedSlots) != 1 || pub.PublishedSlots[0].ID != rowActionsSlot || pub.PublishedSlots[0].Kind != "inline" {
		t.Errorf("published slots = %+v", pub.PublishedSlots)
	}
	if len(pub.HooksDeclared) != 1 || pub.HooksDeclared[0].ID != refreshHook || pub.HooksDeclared[0].Title == "" {
		t.Errorf("hooks = %+v", pub.HooksDeclared)
	}
	if len(pub.EventsDeclared) != 1 || pub.EventsDeclared[0].Name != selectionEvent || pub.EventsDeclared[0].Title == "" {
		t.Errorf("events = %+v", pub.EventsDeclared)
	}
	// The contributor's subscriptions and on_click action are what the code
	// answers and contributes.
	if len(act.HookRegistrations) != 1 || act.HookRegistrations[0].ID != refreshHook {
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
