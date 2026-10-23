package protocol

import (
	"bytes"
	"strings"
	"testing"
)

func TestIsLegacyHello(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		id   string
		ok   bool
	}{
		{"v1 hello", `{"type":"hello","hello":{"plugin_id":"alpha","name":"n","protocol_version":"1.0"}}`, "alpha", true},
		{"trailing CR", "{\"type\":\"hello\",\"hello\":{\"plugin_id\":\"alpha\"}}\r", "alpha", true},
		{"no hello object", `{"type":"hello"}`, "", true},
		{"hello object without plugin_id", `{"type":"hello","hello":{}}`, "", true},
		{"mistyped hello object", `{"type":"hello","hello":"x"}`, "", true},
		{"with a token", `{"type":"hello","token":"t","hello":{"plugin_id":"a"}}`, "a", true},
		{"with an id", `{"type":"hello","id":1}`, "", false},
		{"with a method", `{"type":"hello","method":"x.y"}`, "", false},
		{"v2 request", `{"id":1,"method":"session.ping"}`, "", false},
		{"v2 notification", `{"method":"theme.changed","params":{}}`, "", false},
		{"other v1 type", `{"type":"ready"}`, "", false},
		{"type not a string", `{"type":5}`, "", false},
		{"no type", `{"hello":{"plugin_id":"a"}}`, "", false},
		{"not an object", `[1]`, "", false},
		{"not JSON", `hello`, "", false},
		{"empty", ``, "", false},
		{"invalid UTF-8", "{\"type\":\"hello\",\"hello\":{\"plugin_id\":\"\xff\"}}", "", false},
		{"oversize", `{"type":"hello","pad":"` + strings.Repeat("a", MaxLineBytes) + `"}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := IsLegacyHello([]byte(tc.line))
			if id != tc.id || ok != tc.ok {
				t.Fatalf("IsLegacyHello = (%q, %v), want (%q, %v)", id, ok, tc.id, tc.ok)
			}
		})
	}
}

func TestLegacyRejectionLine(t *testing.T) {
	want := `{"type":"protocol_error","protocol_error":{"message_type":"hello","message":"unsupported protocol major 1: host speaks ` + Version + `"}}` + "\n"
	if got := string(LegacyRejectionLine()); got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
	if LegacyRejectionMessage() != "unsupported protocol major 1: host speaks "+Version {
		t.Fatal(LegacyRejectionMessage())
	}
}

func TestReadLoopFirstObservesOnlyTheFirstNonblankLine(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     string
		stopAt    string // the observer asks to stop when it sees this line
		observed  []string
		delivered int
		errors    int
	}{
		{"observer declines", "\n{\"id\":1,\"method\":\"a.b\"}\n{\"id\":2,\"method\":\"a.b\"}\n", "", []string{`{"id":1,"method":"a.b"}`}, 2, 0},
		{"observer stops", "{\"type\":\"hello\"}\n{\"id\":2,\"method\":\"a.b\"}\n", `{"type":"hello"}`, []string{`{"type":"hello"}`}, 0, 0},
		{"bad first line is still reported", "nope\n{\"id\":2,\"method\":\"a.b\"}\n", "", []string{"nope"}, 1, 1},
		{"crlf is trimmed", "{\"id\":1,\"method\":\"a.b\"}\r\n", "", []string{`{"id":1,"method":"a.b"}`}, 1, 0},
		{"unterminated final line", `{"id":1,"method":"a.b"}`, "", []string{`{"id":1,"method":"a.b"}`}, 1, 0},
		{"oversize first line is never observed", strings.Repeat("a", MaxLineBytes+1) + "\n{\"type\":\"hello\"}\n", "", nil, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observed []string
			var delivered, errs int
			err := ReadLoopFirst(bytes.NewReader([]byte(tc.input)), func(line []byte) bool {
				observed = append(observed, string(line))
				return string(line) == tc.stopAt
			}, func(Message) { delivered++ }, func(*LineError) { errs++ })
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(observed, "|") != strings.Join(tc.observed, "|") || delivered != tc.delivered || errs != tc.errors {
				t.Fatalf("observed %q delivered %d errors %d; want %q %d %d", observed, delivered, errs, tc.observed, tc.delivered, tc.errors)
			}
		})
	}
}

func TestSendLine(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	if err := w.SendLine([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.SendLine([]byte("{\"b\":2}\n")); err != nil {
		t.Fatal(err)
	}
	if b.String() != "{\"a\":1}\n{\"b\":2}\n" {
		t.Fatalf("%q", b.String())
	}
	if err := w.SendLine(bytes.Repeat([]byte("a"), MaxLineBytes+1)); err != ErrLineTooLong {
		t.Fatalf("oversize err = %v", err)
	}
}
