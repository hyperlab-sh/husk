package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func generic(t *testing.T, raw string) string {
	t.Helper()
	var b bytes.Buffer
	if err := Generic(&b, json.RawMessage(raw)); err != nil {
		t.Fatalf("Generic(%s): %v", raw, err)
	}
	return b.String()
}

func TestGenericRendering(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"string is bare", `"hello world"`, "hello world\n"},
		{"integer", `7`, "7\n"},
		{"large number keeps its digits", `12345678901234567890`, "12345678901234567890\n"},
		{"float as written", `1.50`, "1.50\n"},
		{"true", `true`, "true\n"},
		{"false", `false`, "false\n"},
		{"null is an empty line", `null`, "\n"},
		{"empty object is an empty line", `{}`, "\n"},
		{"object: sorted keys aligned", `{"zeta":1,"alpha":"x","mid":[1,2]}`, "alpha  x\nmid    [1,2]\nzeta   1\n"},
		{"object: nested object is compact json", `{"a":{"z":1,"b":null}}`, "a  {\"b\":null,\"z\":1}\n"},
		{"object: explicit null cell is null", `{"a":null}`, "a  null\n"},
		{"object: html is not escaped", `{"a":"<b>","c":{"d":"<&>"}}`, "a  <b>\nc  {\"d\":\"<&>\"}\n"},
		{"array of objects: union of keys, sorted", `[{"b":1,"a":"x"},{"c":true,"a":"yy"}]`, "a   b  c\nx   1\nyy     true\n"},
		{"array of objects: nested cell is compact json", `[{"k":[1,2],"n":"a"}]`, "k      n\n[1,2]  a\n"},
		{"array of scalars: one per line", `["a","b",3]`, "a\nb\n3\n"},
		{"mixed array is one element per line", `[{"a":1},"x",[2]]`, "{\"a\":1}\nx\n[2]\n"},
		{"array of arrays", `[[1],[2,3]]`, "[1]\n[2,3]\n"},
		{"empty array prints nothing", `[]`, ""},
		{"unicode columns align by rune", `{"é":"x","ab":"y"}`, "ab  y\né   x\n"},
		{"lone multi-line string stays bare", `"a\nb"`, "a\nb\n"},
		{"cell: newline and tab are shown escaped", `{"k":"a\nb\tc","z":"y"}`, "k  \"a\\nb\\tc\"\nz  y\n"},
		{"array element with a newline stays on one line", `["a\nb","c"]`, "\"a\\nb\"\nc\n"},
		{"wide glyph key aligns by columns", `{"日本":"x","ab":"y"}`, "ab    y\n日本  x\n"},
		{"emoji and combining marks align", `{"😀":"x","e\u0301":"y"}`, "e\u0301   y\n😀  x\n"},
		{"trailing column is not padded", `[{"a":"x","b":""},{"a":"yyyy","b":"z"}]`, "a     b\nx\nyyyy  z\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := generic(t, tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGenericIsDeterministic(t *testing.T) {
	in := `{"k1":1,"k2":2,"k3":3,"k4":4,"k5":5,"k6":6,"k7":7,"k8":8}`
	first := generic(t, in)
	for i := 0; i < 20; i++ {
		if got := generic(t, in); got != first {
			t.Fatalf("run %d differs: %q vs %q", i, got, first)
		}
	}
}

func TestGenericRejectsInvalidJSONAsProtocolError(t *testing.T) {
	err := Generic(&bytes.Buffer{}, json.RawMessage(`{`))
	var e *Error
	if !errors.As(err, &e) || e.Exit != ExitProtocol {
		t.Fatalf("err = %v", err)
	}
}

func TestTablePadsColumnsWithAtLeastTwoSpaces(t *testing.T) {
	var b bytes.Buffer
	if err := WriteTable(&b, []string{"ID", "PID"}, [][]string{{"notes", "5120"}, {"4242", "4242"}}); err != nil {
		t.Fatal(err)
	}
	if want := "ID     PID\nnotes  5120\n4242   4242\n"; b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

func opts(f Format, tty bool) (Options, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return Options{Format: f, Stdout: &out, Stderr: &errb, StdoutIsTTY: tty}, &out, &errb
}

func TestWriteJSONIsCompactPipedAndIndentedOnATerminal(t *testing.T) {
	o, out, _ := opts(JSON, false)
	if err := o.WriteJSON([]byte(`{ "a": [1, 2] }`)); err != nil || out.String() != "{\"a\":[1,2]}\n" {
		t.Fatalf("piped: %q %v", out.String(), err)
	}
	o, out, _ = opts(JSON, true)
	if err := o.WriteJSON([]byte(`{"a":[1,2]}`)); err != nil || out.String() != "{\n  \"a\": [\n    1,\n    2\n  ]\n}\n" {
		t.Fatalf("tty: %q %v", out.String(), err)
	}
}

func TestReportJSONIsOneObjectPerLine(t *testing.T) {
	o, out, errb := opts(JSON, false)
	o.Warn(WarningUnresponsive, "instance \"x\" did not answer within 500ms; skipped")
	code := o.Report(Ambiguous("2 instances are running; choose one with --instance <id>", []Candidate{
		{ID: "notes", PID: 5120, StartedAt: time.Date(2026, 10, 22, 6, 2, 11, 0, time.UTC)},
		{ID: "4242", PID: 4242, StartedAt: time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC)},
	}))
	if code != ExitAmbiguous || out.Len() != 0 {
		t.Fatalf("code %d stdout %q", code, out.String())
	}
	want := `{"warning":{"code":"unresponsive","message":"instance \"x\" did not answer within 500ms; skipped"}}` + "\n" +
		`{"error":{"code":"ambiguous","data":{"candidates":[{"id":"4242","pid":4242,"started_at":"2026-10-22T06:00:00Z"},{"id":"notes","pid":5120,"started_at":"2026-10-22T06:02:11Z"}]},"message":"2 instances are running; choose one with --instance <id>"}}` + "\n"
	if errb.String() != want {
		t.Fatalf("stderr =\n%s\nwant\n%s", errb.String(), want)
	}
}

func TestReportTableAppendsCandidates(t *testing.T) {
	o, _, errb := opts(Table, false)
	o.Report(Ambiguous("2 instances are running; choose one with --instance <id>", []Candidate{
		{ID: "notes", StartedAt: time.Date(2026, 10, 22, 6, 2, 11, 0, time.UTC)},
		{ID: "4242", StartedAt: time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC)},
	}))
	want := "husk: 2 instances are running; choose one with --instance <id>\n" +
		"  4242   started 2026-10-22T06:00:00Z\n" +
		"  notes  started 2026-10-22T06:02:11Z\n"
	if errb.String() != want {
		t.Fatalf("stderr =\n%s\nwant\n%s", errb.String(), want)
	}
}

func TestReportMapsEveryCodeToItsExit(t *testing.T) {
	for _, tc := range []struct {
		err  error
		exit int
		code string
	}{
		{Usage("x"), ExitUsage, "usage"},
		{NoInstance(nil, "x"), ExitNoInstance, "no_instance"},
		{Ambiguous("x", nil), ExitAmbiguous, "ambiguous"},
		{Transport(nil, "x"), ExitTransport, "transport"},
		{Protocol(nil, "x"), ExitProtocol, "protocol"},
		{Environment(nil, "x"), ExitEnvironment, "environment"},
		{Store(nil, "x"), ExitRemote, "store"},
		{errors.New("plain"), ExitTransport, "transport"},
	} {
		o, _, errb := opts(JSON, false)
		if got := o.Report(tc.err); got != tc.exit || !strings.Contains(errb.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%v: exit %d stderr %q, want %d %s", tc.err, got, errb.String(), tc.exit, tc.code)
		}
	}
	if ExitEnvironment != 7 || ExitProtocol != 6 || ExitTransport != 5 {
		t.Error("exit code values drifted from the spec")
	}
}

func TestFromReplyForwardsCodeAndDataAndMapsTheReaderCodes(t *testing.T) {
	for _, code := range protocol.Codes() {
		want := ExitRemote
		switch code {
		case protocol.CodeParseError, protocol.CodeInvalidMessage, protocol.CodeLineTooLong, protocol.CodeVersionMismatch:
			want = ExitProtocol
		}
		e := FromReply(&protocol.Error{Code: code, Message: "m", Data: json.RawMessage(`{"k":1}`)})
		if e.Exit != want || e.Code != string(code) || e.Message != "m" || string(e.Data) != `{"k":1}` {
			t.Errorf("%s: %+v, want exit %d", code, e, want)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"json", "table"} {
		if f, err := ParseFormat(s); err != nil || string(f) != s {
			t.Errorf("ParseFormat(%q) = %q, %v", s, f, err)
		}
	}
	for _, s := range []string{"", "xml", "JSON", "json "} {
		var e *Error
		if _, err := ParseFormat(s); !errors.As(err, &e) || e.Exit != ExitUsage {
			t.Errorf("ParseFormat(%q) = %v, want usage error", s, err)
		}
	}
}

func TestTextQuotesControlCharactersOnly(t *testing.T) {
	for in, want := range map[string]string{
		"plain":         "plain",
		"":              "",
		"a b: c":        "a b: c",
		"l1\nl2":        `"l1\nl2"`,
		"\x1b[31mred":   `"\u001b[31mred"`,
		"tab\there":     `"tab\there"`,
		"<html> & café": "<html> & café",
	} {
		if got := Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
}
