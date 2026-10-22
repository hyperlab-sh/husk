package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func readMessages(t *testing.T, input []byte) ([]Message, []*LineError) {
	t.Helper()
	var messages []Message
	var failures []*LineError
	if err := ReadLoop(bytes.NewReader(input), func(m Message) { messages = append(messages, m) }, func(e *LineError) { failures = append(failures, e) }); err != nil {
		t.Fatal(err)
	}
	return messages, failures
}

func TestReadLoopFramingAndUnknownFields(t *testing.T) {
	input := "\n\r\n \t\r\n" + `{"id":"a","method":"session.ping","params":{"future":1},"future":true}` + "\r\n" + `{"id":"a","result":null}`
	messages, failures := readMessages(t, []byte(input))
	if len(messages) != 2 || len(failures) != 0 {
		t.Fatalf("messages=%d errors=%v", len(messages), failures)
	}
	if messages[0].ID.String() != `"a"` || string(messages[1].Result) != "null" {
		t.Fatal(messages)
	}
}

func TestReadLoopRecoversFromBadLines(t *testing.T) {
	cases := []struct {
		line       string
		code       Code
		id, method string
	}{
		{"not JSON", CodeParseError, "", ""},
		{string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}), CodeParseError, "", ""},
		{`[]`, CodeInvalidMessage, "", ""},
		{`{"id":7,"method":"session.ping","result":{}}`, CodeInvalidMessage, "7", MethodSessionPing},
		{`{"id":1,"method":"session.ping","params":[]}`, CodeInvalidParams, "1", MethodSessionPing},
		{`{"id":null,"method":"session.ping"}`, CodeInvalidMessage, "", MethodSessionPing},
		{`{"id":7,"method":"session.ping","token":17}`, CodeInvalidMessage, "7", MethodSessionPing},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			messages, failures := readMessages(t, []byte(tc.line+"\n"+`{"id":2,"result":{}}`+"\n"))
			if len(messages) != 1 || len(failures) != 1 {
				t.Fatalf("messages=%d errors=%v", len(messages), failures)
			}
			e := failures[0]
			id := ""
			if e.ID != nil {
				id = e.ID.String()
			}
			if e.Code != tc.code || id != tc.id || e.Method != tc.method {
				t.Fatalf("got %+v, id=%q", e, id)
			}
		})
	}
}

func TestReadLoopLineLimit(t *testing.T) {
	base := `{"method":"session.ping"}`
	for _, terminator := range []string{"\n", "\r\n", ""} {
		for _, extra := range []int{0, 1, 100000} {
			name := strings.ReplaceAll(terminator, "\n", "LF")
			t.Run(name+strings.Repeat("+", min(extra, 2)), func(t *testing.T) {
				line := base + strings.Repeat(" ", MaxLineBytes-len(base)+extra)
				input := line + terminator
				wantMessages := 1
				if extra != 0 {
					wantMessages = 0
				}
				if terminator != "" {
					input += base + "\n"
					wantMessages++
				}
				messages, failures := readMessages(t, []byte(input))
				if len(messages) != wantMessages {
					t.Fatalf("messages=%d want %d", len(messages), wantMessages)
				}
				if extra == 0 && len(failures) != 0 {
					t.Fatal(failures)
				}
				if extra > 0 && (len(failures) != 1 || failures[0].Code != CodeLineTooLong || !errors.Is(failures[0], ErrLineTooLong)) {
					t.Fatal(failures)
				}
			})
		}
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadLoopTransportErrorAndNilCallbacks(t *testing.T) {
	want := errors.New("transport failed")
	if err := ReadLoop(failingReader{want}, nil, nil); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if err := ReadLoop(strings.NewReader("bad\n{}\n"+`{"method":"session.ping"}`), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := ReadLoop(strings.NewReader(""), nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLineErrorResponse(t *testing.T) {
	id, _ := NewIntID(7)
	for _, tc := range []struct {
		e       LineError
		respond bool
		kind    Kind
	}{
		{LineError{Code: CodeInvalidMessage, ID: &id, Method: MethodSessionPing, Err: io.ErrUnexpectedEOF}, true, KindReply},
		{LineError{Code: CodeParseError, Err: io.ErrUnexpectedEOF}, true, KindNotification},
		{LineError{Code: CodeLineTooLong, Err: ErrLineTooLong}, true, KindNotification},
		{LineError{Code: CodeInvalidMessage, ID: &id, Err: io.ErrUnexpectedEOF}, false, 0},
		{LineError{Code: CodeInvalidParams, Method: MethodProtocolError, Err: io.ErrUnexpectedEOF}, false, 0},
	} {
		m, ok := tc.e.Response()
		if ok != tc.respond {
			t.Fatalf("Response = %v", ok)
		}
		if !ok {
			continue
		}
		if kind, err := m.Kind(); kind != tc.kind || err != nil {
			t.Fatalf("Kind = %v %v", kind, err)
		}
		if err := m.Validate(); err != nil {
			t.Fatal(err)
		}
		if tc.e.Code == CodeLineTooLong {
			var p ProtocolErrorParams
			if err := json.Unmarshal(m.Params, &p); err != nil {
				t.Fatal(err)
			}
			if string(p.Data) != `{"limit":4194304}` {
				t.Fatal(string(p.Data))
			}
		}
	}
}

func TestReaderNeverAnswersMalformedReplyOrProtocolError(t *testing.T) {
	for _, raw := range []string{`{"id":1,"result":{},"error":{}}`, `{"id":null,"result":{}}`, `{"method":"protocol.error","params":[]}`} {
		_, failures := readMessages(t, []byte(raw))
		if len(failures) != 1 {
			t.Fatal(failures)
		}
		if _, ok := failures[0].Response(); ok {
			t.Fatalf("would answer %s", raw)
		}
	}
}
