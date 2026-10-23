package wrapper

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type streamSink struct {
	replies []protocol.Message
	closed  int
}

func (s *streamSink) Reply(_ string, m protocol.Message) { s.replies = append(s.replies, m) }
func (s *streamSink) Closed(string, error)               { s.closed++ }

func readStream(t *testing.T, r io.Reader, clock *testclock.Clock) ([]protocol.Message, []protocol.Message, []LogMsg) {
	t.Helper()
	sink := &streamSink{}
	var logs []LogMsg
	w, err := New(manifest.Spec{ID: "fake", Protocol: 2, Command: []string{"plugin"}}, Options{Clock: clock, LogPath: func(string) (string, error) { return "unused", nil }, Replies: sink, Post: func(msg tea.Msg) {
		if m, ok := msg.(LogMsg); ok {
			logs = append(logs, m)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer w.cancel()
	close(w.waited) // no child: EOF cannot arm a process reap timer
	var out bytes.Buffer
	w.queue = protocol.NewQueuedWriter(&out, protocol.QueueOptions{Clock: clock})
	go w.readStdout(r)
	testclock.Receive(t, w.readDone)
	_ = w.queue.Close()
	testclock.Receive(t, w.queue.Done())
	var notices []protocol.Message
	if err := protocol.ReadLoop(&out, func(m protocol.Message) { notices = append(notices, m) }, func(e *protocol.LineError) { t.Error(e) }); err != nil {
		t.Fatal(err)
	}
	return notices, sink.replies, logs
}

func TestProtocolErrorNotificationsAreNeverAnswered(t *testing.T) {
	input := `{"method":"protocol.error","params":{"code":"failed","message":"x","method":""}}` + "\n" + `{"method":"protocol.error","params":[]}` + "\n"
	notices, _, _ := readStream(t, strings.NewReader(input), testclock.New())
	if len(notices) != 0 {
		t.Fatal("protocol.error was answered")
	}
}

func TestReaderRecoversBadUTF8OversizeAndMalformedReply(t *testing.T) {
	input := "not JSON\n" + string([]byte{0xff}) + "\n" + strings.Repeat("x", protocol.MaxLineBytes+5) + "\n" + `{"id":7,"result":{},"error":{"code":"failed","message":"x"}}` + "\n" + `{"id":8,"result":null}` + "\n"
	notices, replies, _ := readStream(t, strings.NewReader(input), testclock.New())
	if len(notices) != 3 || len(replies) != 2 || replies[0].ID.String() != "7" || replies[0].Result != nil || string(replies[1].Result) != "null" {
		t.Fatal("reader did not recover/salvage")
	}
	var p protocol.ProtocolErrorParams
	if err := json.Unmarshal(notices[2].Params, &p); err != nil {
		t.Fatal(err)
	}
	var limit struct {
		Limit int `json:"limit"`
	}
	if err := json.Unmarshal(p.Data, &limit); err != nil || p.Code != protocol.CodeLineTooLong || limit.Limit != protocol.MaxLineBytes {
		t.Fatal(p, err)
	}
}

type pacedReader struct {
	clock *testclock.Clock
	line  int
}

func (r *pacedReader) Read(b []byte) (int, error) {
	if r.line == 13 {
		return 0, io.EOF
	}
	if r.line == 12 {
		r.clock.Advance(time.Second)
	}
	r.line++
	return copy(b, "bad\n"), nil
}
func TestProtocolErrorRateLimitAndSuppressedReport(t *testing.T) {
	clock := testclock.New()
	notices, _, logs := readStream(t, &pacedReader{clock: clock}, clock)
	if len(notices) != 11 {
		t.Fatalf("notifications=%d want 10 then 1", len(notices))
	}
	found := false
	for _, m := range logs {
		if strings.Contains(m.Message, "2 rejections suppressed") {
			found = true
		}
	}
	if !found {
		t.Fatal("suppressed diagnostics were not summarized")
	}
}

func TestSuppressedRejectionsAreReportedAtEOF(t *testing.T) {
	notices, _, logs := readStream(t, strings.NewReader(strings.Repeat("bad\n", 12)), testclock.New())
	if len(notices) != protocol.MaxProtocolErrorsPerSecond {
		t.Fatal("rejection notification budget changed", len(notices))
	}
	summaries := 0
	for _, log := range logs {
		if log.Message == "husk: protocol error from fake: 2 rejections suppressed" {
			summaries++
		}
	}
	if summaries != 1 {
		t.Fatal("EOF did not flush exactly one suppression summary", summaries)
	}
}

func TestNonReplyLogMatchesContractAndSharesRejectionBudget(t *testing.T) {
	line := `{"method":"surface.update","params":{}}` + "\n"
	notices, _, logs := readStream(t, strings.NewReader(strings.Repeat(line, 12)), testclock.New())
	count := 0
	for _, log := range logs {
		if log.Message == "plugin fake sent a non-reply on stdout" {
			count++
		}
	}
	if count != protocol.MaxProtocolErrorsPerSecond || len(notices) != count || len(logs) != count+1 {
		t.Fatal("non-reply log spelling or rejection budget changed", count, len(notices), len(logs))
	}
}

func TestFirstLineLegacyProbeIsBoundedAndExact(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		legacy bool
	}{
		{`{"type":"hello"}` + "\n", true}, {"\n \n" + `{"hello":{},"type":"hello"}`, true},
		{`{"type":"hello","method":"sample.notice"}` + "\n", false},
		{`{"id":1,"result":{}}` + "\n" + `{"type":"hello"}` + "\n", false},
		{strings.Repeat("x", protocol.MaxLineBytes+20), false},
		{`{"type":"hello","pad":"` + string([]byte{0xff}) + `"}` + "\n", false},
		{`{"type":"hello","pad":"` + strings.Repeat("x", protocol.MaxLineBytes-len(`{"type":"hello","pad":""}`)+1) + `"}` + "\n", false},
	} {
		p := &firstLineReader{r: strings.NewReader(tc.raw)}
		if _, err := io.Copy(io.Discard, p); err != nil {
			t.Fatal(err)
		}
		if p.legacy != tc.legacy || len(p.line) != 0 {
			t.Fatal("legacy probe mismatch")
		}
	}
}

func TestWrapperDefaultsAndTokenConstruction(t *testing.T) {
	w, err := New(manifest.Spec{ID: "fake", Protocol: 2, Command: []string{"plugin"}}, Options{LogPath: func(string) (string, error) { return "unused", nil }, Random: bytes.NewReader(bytes.Repeat([]byte{7}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	defer w.cancel()
	if len(w.Token()) != 64 || w.opts.QueueCapacity != 1024 || w.opts.WriteTimeout != 5*time.Second || w.opts.StopGrace != 2*time.Second || w.opts.KillWait != time.Second || w.opts.EOFReapGrace != 100*time.Millisecond {
		t.Fatal("wrapper defaults changed")
	}
	for _, o := range []Options{{LogPath: func(string) (string, error) { return "unused", nil }, QueueCapacity: -1}, {LogPath: func(string) (string, error) { return "unused", nil }, Random: bytes.NewReader(nil)}} {
		if _, err := New(manifest.Spec{ID: "fake", Protocol: 2, Command: []string{"plugin"}}, o); err == nil {
			t.Fatal("invalid constructor accepted")
		}
	}
}
