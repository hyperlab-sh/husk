package huskipc

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

type readResult struct {
	msgs []Envelope
	errs []*LineError
	err  error
}

func readAll(r io.Reader) readResult {
	var res readResult
	res.err = ReadLoopReporting(r,
		func(e Envelope) { res.msgs = append(res.msgs, e) },
		func(e *LineError) { res.errs = append(res.errs, e) })
	return res
}

func types(msgs []Envelope) string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Type)
	}
	return strings.Join(out, ",")
}

func TestReadLoopReportsMalformedJSONAndContinues(t *testing.T) {
	res := readAll(strings.NewReader(`{"type":"toast"` + "\n" + `{"type":"close_modal","close_modal":true}` + "\n"))
	if res.err != nil {
		t.Fatalf("err = %v, want nil at EOF", res.err)
	}
	if types(res.msgs) != "close_modal" {
		t.Fatalf("delivered %q, want the line after the bad one", types(res.msgs))
	}
	if len(res.errs) != 1 {
		t.Fatalf("got %d line errors, want 1", len(res.errs))
	}
	e := res.errs[0]
	if !errors.Is(e, ErrMalformed) || e.MessageType != "" {
		t.Fatalf("line error = %+v (%v), want ErrMalformed with no type", e, e)
	}
	p := e.Payload()
	if p.MessageType != "" || p.Message != "malformed message: unexpected end of JSON input" {
		t.Fatalf("payload = %+v", p)
	}
}

// A well-formed envelope with a mistyped field is malformed too, and the
// error names the message type so the plugin knows which message failed.
func TestReadLoopReportsMistypedFieldWithItsType(t *testing.T) {
	res := readAll(strings.NewReader(`{"type":"slot_update","slot_update":{"lines":"oops"}}` + "\n"))
	if len(res.msgs) != 0 || len(res.errs) != 1 {
		t.Fatalf("msgs=%d errs=%d, want 0/1", len(res.msgs), len(res.errs))
	}
	p := res.errs[0].Payload()
	if p.MessageType != "slot_update" {
		t.Fatalf("message_type = %q, want slot_update", p.MessageType)
	}
	if !strings.HasPrefix(p.Message, "malformed message: json: cannot unmarshal string into Go struct field") || !strings.Contains(p.Message, "lines") {
		t.Fatalf("message = %q", p.Message)
	}
}

func TestReadLoopSkipsOversizedLineAndStaysInSync(t *testing.T) {
	stream := `{"type":"toast","toast":{"title":"before"}}` + "\n" +
		strings.Repeat("x", MaxLineBytes+1) + "\n" +
		`{"type":"toast","toast":{"title":"after"}}` + "\n"
	res := readAll(strings.NewReader(stream))
	if res.err != nil {
		t.Fatalf("err = %v, want nil: an oversized line must not end the stream", res.err)
	}
	if len(res.msgs) != 2 || res.msgs[0].Toast.Title != "before" || res.msgs[1].Toast.Title != "after" {
		t.Fatalf("delivered %+v, want before and after", res.msgs)
	}
	if len(res.errs) != 1 || !errors.Is(res.errs[0], ErrLineTooLong) {
		t.Fatalf("errs = %v, want one ErrLineTooLong", res.errs)
	}
	if p := res.errs[0].Payload(); p.MessageType != "" || p.Message != "line exceeds 4194304 bytes" {
		t.Fatalf("payload = %+v", p)
	}
}

// Exactly MaxLineBytes of content is accepted, with "\n" or "\r\n"; one byte
// more is rejected, with or without a trailing newline at EOF.
func TestReadLoopLineLimitBoundary(t *testing.T) {
	prefix, suffix := `{"type":"toast","toast":{"title":"`, `"}}`
	exact := prefix + strings.Repeat("a", MaxLineBytes-len(prefix)-len(suffix)) + suffix
	over := prefix + strings.Repeat("a", MaxLineBytes-len(prefix)-len(suffix)+1) + suffix
	cases := []struct {
		name     string
		stream   string
		msgs     int
		tooLongs int
	}{
		{"exact LF", exact + "\n", 1, 0},
		{"exact CRLF", exact + "\r\n", 1, 0},
		{"exact at EOF", exact, 1, 0},
		{"over LF", over + "\n", 0, 1},
		{"over at EOF", over, 0, 1},
		{"far over at EOF", over + strings.Repeat("b", 3*MaxLineBytes), 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := readAll(strings.NewReader(tc.stream))
			if res.err != nil || len(res.msgs) != tc.msgs || len(res.errs) != tc.tooLongs {
				t.Fatalf("err=%v msgs=%d errs=%d, want nil/%d/%d", res.err, len(res.msgs), len(res.errs), tc.msgs, tc.tooLongs)
			}
			if tc.msgs == 1 && len(res.msgs[0].Toast.Title) != MaxLineBytes-len(prefix)-len(suffix) {
				t.Fatalf("title length %d", len(res.msgs[0].Toast.Title))
			}
			for _, e := range res.errs {
				if !errors.Is(e, ErrLineTooLong) {
					t.Fatalf("error %v, want ErrLineTooLong", e)
				}
			}
		})
	}
}

func TestReadLoopFramingEdgeCases(t *testing.T) {
	stream := "\n\r\n" + `{"type":"a"}` + "\r\n\n" + `{"type":"b"}`
	res := readAll(iotest.OneByteReader(strings.NewReader(stream)))
	if res.err != nil || len(res.errs) != 0 || types(res.msgs) != "a,b" {
		t.Fatalf("err=%v errs=%v msgs=%q, want a,b and no errors (blank lines skipped, final line without newline delivered)", res.err, res.errs, types(res.msgs))
	}
}

func TestReadLoopReturnsReadError(t *testing.T) {
	boom := errors.New("boom")
	res := readAll(io.MultiReader(strings.NewReader(`{"type":"a"}`+"\n"+`{"type":"partial`), iotest.ErrReader(boom)))
	if !errors.Is(res.err, boom) {
		t.Fatalf("err = %v, want boom", res.err)
	}
	if types(res.msgs) != "a" || len(res.errs) != 0 {
		t.Fatalf("msgs=%q errs=%v: a partial line cut by a read error is neither delivered nor reported", types(res.msgs), res.errs)
	}
}

// ReadLoop keeps its old contract for callers that do not report errors: bad
// lines are skipped and later messages still arrive.
func TestReadLoopWithoutReportingSkipsBadLines(t *testing.T) {
	stream := "nope\n" + strings.Repeat("x", MaxLineBytes+10) + "\n" + `{"type":"a"}` + "\n"
	var got []Envelope
	if err := ReadLoop(strings.NewReader(stream), func(e Envelope) { got = append(got, e) }); err != nil {
		t.Fatalf("err = %v", err)
	}
	if types(got) != "a" {
		t.Fatalf("delivered %q, want a", types(got))
	}
}
