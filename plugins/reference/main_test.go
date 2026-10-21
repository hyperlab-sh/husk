package main

import (
	"bufio"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
	"github.com/hyperlab-sh/husk/pkg/manifest"
)

// syncBuf is a stderr stand-in that the test can read while the plugin writes.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// harness runs a plugin over pipes and exposes its output as envelopes.
type harness struct {
	t    *testing.T
	in   *io.PipeWriter
	out  chan huskipc.Envelope
	logs *syncBuf
	done chan error
}

func start(t *testing.T, contributor bool) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := &harness{t: t, in: inW, out: make(chan huskipc.Envelope, 64), logs: &syncBuf{}, done: make(chan error, 1)}
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(nil, huskipc.MaxLineBytes)
		for sc.Scan() {
			var e huskipc.Envelope
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				t.Errorf("plugin wrote invalid JSON %q: %v", sc.Text(), err)
				continue
			}
			h.out <- e
		}
		close(h.out)
	}()
	go func() {
		err := run(contributor, inR, outW, h.logs, 5*time.Millisecond)
		outW.Close()
		h.done <- err
	}()
	t.Cleanup(func() { inW.Close() })
	return h
}

func (h *harness) send(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.in, line+"\n"); err != nil {
		h.t.Fatalf("write to plugin: %v", err)
	}
}

func (h *harness) next(typ string) huskipc.Envelope {
	h.t.Helper()
	select {
	case e, ok := <-h.out:
		if !ok {
			h.t.Fatalf("plugin output closed while waiting for %s", typ)
		}
		if e.Type != typ {
			h.t.Fatalf("got %s, want %s", e.Type, typ)
		}
		return e
	case <-time.After(2 * time.Second):
		h.t.Fatalf("timed out waiting for %s", typ)
	}
	return huskipc.Envelope{}
}

// quiet asserts the plugin writes nothing within a short window.
func (h *harness) quiet() {
	h.t.Helper()
	select {
	case e := <-h.out:
		h.t.Fatalf("expected no output, got %s", e.Type)
	case <-time.After(60 * time.Millisecond):
	}
}

func welcome(w, hgt int) string {
	return `{"type":"welcome","welcome":{"protocol_version":"1.1","slot_sizes":{"main-stage":{"width":` + itoa(w) + `,"height":` + itoa(hgt) + `}}}}`
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestPublisherHelloDeclaresEveryExtensionPoint(t *testing.T) {
	h := start(t, false)
	hello := h.next("hello").Hello
	if hello.ProtocolVersion != huskipc.ProtocolVersion {
		t.Errorf("protocol_version = %q, want %q", hello.ProtocolVersion, huskipc.ProtocolVersion)
	}
	if hello.PluginID != publisherID || len(hello.Slots) != 1 || hello.Slots[0] != huskipc.SlotMainStage {
		t.Errorf("identity = %q slots %v", hello.PluginID, hello.Slots)
	}
	want := huskipc.FrameSpec{Border: true, Label: "Reference plugin", Close: true}
	if got := hello.Frames[huskipc.SlotMainStage]; got != want {
		t.Errorf("frame = %+v, want %+v", got, want)
	}
	if len(hello.PublishedSlots) != 1 || hello.PublishedSlots[0].ID != rowActionsSlot || hello.PublishedSlots[0].Kind != "inline" {
		t.Errorf("published slots = %+v", hello.PublishedSlots)
	}
	if len(hello.Hooks) != 1 || hello.Hooks[0].ID != refreshHook || hello.Hooks[0].Title == "" {
		t.Errorf("hooks = %+v", hello.Hooks)
	}
	if len(hello.Events) != 1 || hello.Events[0].ID != selectionEvent || hello.Events[0].Title == "" {
		t.Errorf("events = %+v", hello.Events)
	}
	// Every declared ID must pass the host's namespace rule: the final dot
	// separates the plugin ID from one dot-free name.
	for _, id := range []string{rowActionsSlot, refreshHook, selectionEvent} {
		i := strings.LastIndexByte(id, '.')
		if id[:i] != publisherID || strings.Contains(id[i+1:], ".") || id[i+1:] == "" {
			t.Errorf("id %q is not in the %s namespace", id, publisherID)
		}
	}
}

func TestRenderUsesSlotSizeAndAnchors(t *testing.T) {
	h := start(t, false)
	h.next("hello")
	h.send(welcome(50, 20))
	u := h.next("slot_update").SlotUpdate
	if u.Slot != huskipc.SlotMainStage || u.PluginID != publisherID {
		t.Fatalf("slot_update target = %s/%s", u.PluginID, u.Slot)
	}
	if got := u.Lines[1]; got != strings.Repeat("─", 50) {
		t.Errorf("separator = %d cells, want 50", len([]rune(got)))
	}
	for i, row := range rows {
		line := u.Lines[2+i]
		anchor := "\x1b]husk;slot=" + rowActionsSlot + ";key=" + row + "\x1b\\"
		if !strings.HasSuffix(line, anchor) {
			t.Errorf("row %d = %q, want anchor suffix %q", i, line, anchor)
		}
	}
	if !strings.HasPrefix(u.Lines[2], "> alpha") || !strings.HasPrefix(u.Lines[3], "  bravo") {
		t.Errorf("selection marker wrong: %q %q", u.Lines[2], u.Lines[3])
	}
	for i, l := range u.Lines {
		if i >= 2 && i < 2+len(rows) {
			continue
		}
		if n := len([]rune(l)); n > 50 {
			t.Errorf("line %d is %d cells, over the 50 cell surface", i, n)
		}
	}

	h.send(`{"type":"slot_resize","slot_resize":{"slot_sizes":{"main-stage":{"width":30,"height":10}}}}`)
	u = h.next("slot_update").SlotUpdate
	if got := u.Lines[1]; got != strings.Repeat("─", 30) {
		t.Errorf("after resize separator = %d cells, want 30", len([]rune(got)))
	}
	if !strings.Contains(strings.Join(u.Lines, "\n"), "size 30x10") {
		t.Errorf("status line does not report the new size: %q", u.Lines)
	}

	// Same size again, and any modifier traffic, must not re-render.
	h.send(`{"type":"slot_resize","slot_resize":{"slot_sizes":{"main-stage":{"width":30,"height":10}}}}`)
	h.send(`{"type":"key_state","key_state":{"phase":"down","key":"alt","held":true,"modifiers":{"alt":true}}}`)
	h.quiet()
}

func TestKeysEmitEventAndCallHook(t *testing.T) {
	h := start(t, false)
	h.next("hello")
	h.send(welcome(60, 20))
	h.next("slot_update")

	h.send(`{"type":"slot_key","slot_key":{"slot":"main-stage","key":"j"}}`)
	ev := h.next("event_emit").EventEmit
	if ev.Topic != selectionEvent || ev.Data["row"] != "bravo" {
		t.Errorf("event = %+v", ev)
	}
	if u := h.next("slot_update").SlotUpdate; !strings.HasPrefix(u.Lines[3], "> bravo") {
		t.Errorf("selected row not moved: %q", u.Lines[3])
	}
	h.send(`{"type":"slot_key","slot_key":{"slot":"main-stage","key":"k"}}`)
	h.send(`{"type":"slot_key","slot_key":{"slot":"main-stage","key":"k"}}`)
	h.next("event_emit")
	h.next("slot_update")
	if ev := h.next("event_emit").EventEmit; ev.Data["row"] != "charlie" {
		t.Errorf("selection must wrap to the last row, got %v", ev.Data["row"])
	}
	h.next("slot_update")

	h.send(`{"type":"slot_key","slot_key":{"slot":"main-stage","key":"r"}}`)
	c1 := h.next("hook_call").HookCall
	h.send(`{"type":"slot_key","slot_key":{"slot":"main-stage","key":"r"}}`)
	c2 := h.next("hook_call").HookCall
	if c1.ID != refreshHook || c1.RequestID != publisherID+".refresh-1" || c2.RequestID != publisherID+".refresh-2" {
		t.Errorf("request ids = %q, %q (hook %q)", c1.RequestID, c2.RequestID, c1.ID)
	}

	h.send(`{"type":"hook_result","hook_result":{"id":"` + refreshHook + `","request_id":"` + c1.RequestID + `","plugin_id":"` + contributorID + `"}}`)
	got := strings.Join(h.next("slot_update").SlotUpdate.Lines, "\n")
	if !strings.Contains(got, "replies: 1, last "+contributorID) {
		t.Errorf("hook result not shown: %q", got)
	}
	h.send(`{"type":"hook_result","hook_result":{"id":"` + refreshHook + `","request_id":"x","plugin_id":"` + contributorID + `","error":"timeout"}}`)
	got = strings.Join(h.next("slot_update").SlotUpdate.Lines, "\n")
	if !strings.Contains(got, "replies: 2, last timeout") {
		t.Errorf("hook timeout not shown: %q", got)
	}

	// Unbound keys do nothing.
	h.send(`{"type":"slot_key","slot_key":{"slot":"main-stage","key":"x"}}`)
	h.quiet()
}

func TestProtocolErrorIsLoggedAndNeverAnswered(t *testing.T) {
	h := start(t, false)
	h.next("hello")
	h.send(`{"type":"protocol_error","protocol_error":{"message_type":"slot_update","message":"nope"}}`)
	h.quiet()
	if !strings.Contains(h.logs.String(), `host rejected "slot_update": nope`) {
		t.Errorf("stderr = %q", h.logs.String())
	}
}

func TestEOFEndsTheRun(t *testing.T) {
	h := start(t, false)
	h.next("hello")
	h.in.Close()
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("run returned %v at EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return at EOF")
	}
}

func TestContributorFillsSlotAndAnswersHook(t *testing.T) {
	h := start(t, true)
	hello := h.next("hello").Hello
	if hello.PluginID != contributorID || len(hello.Slots) != 0 {
		t.Errorf("contributor hello = %+v", hello)
	}
	h.send(welcome(50, 20))
	c := h.next("slot_contribute").SlotContribute
	if c.SlotID != rowActionsSlot || c.ID != inspectContrib || c.OnClick != inspectCommand || !strings.Contains(c.Content, "[inspect]") {
		t.Errorf("contribution = %+v", c)
	}
	if got := h.next("hook_subscribe").HookSubscribe.ID; got != refreshHook {
		t.Errorf("hook subscription = %q", got)
	}
	if got := h.next("event_subscribe").EventSubscribe.ID; got != selectionEvent {
		t.Errorf("event subscription = %q", got)
	}

	h.send(`{"type":"hook_call","hook_call":{"id":"` + refreshHook + `","request_id":"go.reference.refresh-7"}}`)
	r := h.next("hook_result").HookResult
	if r.ID != refreshHook || r.RequestID != "go.reference.refresh-7" || r.Data["ready"] != true || r.Error != "" {
		t.Errorf("hook result = %+v", r)
	}

	h.send(`{"type":"command_invoke","command_invoke":{"command_id":"` + inspectCommand + `","args":{"row_key":"bravo"}}}`)
	if toast := h.next("toast").Toast; toast.PluginID != contributorID || toast.Message != "row bravo" {
		t.Errorf("toast = %+v", toast)
	}
	h.send(`{"type":"command_invoke","command_invoke":{"command_id":"someone.else"}}`)
	h.quiet()
}

func TestContributorRetriesWhenPublisherIsNotConnectedYet(t *testing.T) {
	h := start(t, true)
	h.next("hello")
	h.send(welcome(50, 20))
	h.next("slot_contribute")
	h.next("hook_subscribe")
	h.next("event_subscribe")

	// The host answers with protocol_error while the publisher is missing;
	// the contributor must try again, and must not reply to the error.
	h.send(`{"type":"protocol_error","protocol_error":{"message_type":"slot_contribute","message":"unknown slot \"` + rowActionsSlot + `\""}}`)
	h.next("slot_contribute")
	h.next("hook_subscribe")
	h.next("event_subscribe")
	h.quiet()
}

func TestContributorRetriesAreBounded(t *testing.T) {
	h := start(t, true)
	h.next("hello")
	for i := 0; i < maxRetries+10; i++ {
		h.send(`{"type":"protocol_error","protocol_error":{"message_type":"slot_contribute","message":"unknown slot"}}`)
		// Drain whatever the retry produced so the pipe never blocks.
		for drained := false; !drained; {
			select {
			case <-h.out:
			case <-time.After(15 * time.Millisecond):
				drained = true
			}
		}
	}
	h.quiet()
	if n := strings.Count(h.logs.String(), "host rejected"); n != maxRetries+10 {
		t.Errorf("logged %d rejections, want %d", n, maxRetries+10)
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

func TestManifestsMatchHelloAndShareOneBinary(t *testing.T) {
	pub, err := manifest.Load(".")
	if err != nil {
		t.Fatal(err)
	}
	act, err := manifest.Load("../reference-actions")
	if err != nil {
		t.Fatal(err)
	}
	if pub.ID != publisherID || act.ID != contributorID {
		t.Errorf("manifest ids = %q, %q; want %q, %q", pub.ID, act.ID, publisherID, contributorID)
	}
	if pub.Transport != manifest.TransportStdio || act.Transport != manifest.TransportStdio {
		t.Errorf("transports = %q, %q; both must be stdio", pub.Transport, act.Transport)
	}
	if len(pub.Slots) != 1 || pub.Slots[0] != string(huskipc.SlotMainStage) {
		t.Errorf("publisher slots = %v", pub.Slots)
	}
	pc, ac := pub.ResolvedCommand(), act.ResolvedCommand()
	if filepath.Clean(pc[0]) != filepath.Clean(ac[0]) {
		t.Errorf("both plugins must run the same binary: %q vs %q", pc[0], ac[0])
	}
	if len(pc) != 1 || len(ac) != 2 || ac[1] != "--contributor" {
		t.Errorf("argv = %q, %q", pc, ac)
	}
}
