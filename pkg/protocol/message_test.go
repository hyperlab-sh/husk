package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestIDRoundTrip(t *testing.T) {
	for _, raw := range []string{`"id"`, `"a\\b"`, `"\u0061"`, `"<>&"`, `""`, `0`, `-0`, `7`, `-7`, `9007199254740991`, `-9007199254740991`} {
		t.Run(raw, func(t *testing.T) {
			var id ID
			if err := json.Unmarshal([]byte(raw), &id); err != nil {
				t.Fatal(err)
			}
			got, err := id.MarshalJSON()
			if err != nil || string(got) != raw || id.String() != raw {
				t.Fatalf("round trip = %s, %v", got, err)
			}
			var buf bytes.Buffer
			if err := NewWriter(&buf).Send(Message{ID: &id, Result: json.RawMessage(`null`)}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(buf.String(), `"id":`+raw+`,`) {
				t.Fatalf("writer changed id: %s", buf.String())
			}
		})
	}
}

func TestIDRejectsInvalid(t *testing.T) {
	for _, raw := range []string{`null`, `true`, `[]`, `{}`, `1.0`, `1e0`, `-0.0`, `9007199254740992`, `-9007199254740992`, `9223372036854775808`, `+1`, `01`, `"`} {
		t.Run(raw, func(t *testing.T) {
			var id ID
			if err := json.Unmarshal([]byte(raw), &id); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
	if _, err := (ID{}).MarshalJSON(); err == nil {
		t.Fatal("zero ID marshaled")
	}
	for _, n := range []int64{1 << 53, -(1 << 53)} {
		if _, err := NewIntID(n); err == nil {
			t.Fatal("constructed out-of-range id")
		}
	}
	id := NewStringID("hello")
	if id.String() != `"hello"` {
		t.Fatal(id.String())
	}
	if id, err := NewIntID(12); err != nil || id.String() != "12" {
		t.Fatalf("%v %v", id, err)
	}
}

func TestMessageForms(t *testing.T) {
	cases := []struct {
		raw  string
		kind Kind
	}{
		{`{"id":1,"method":"session.ping"}`, KindRequest},
		{`{"method":"theme.changed","params":{}}`, KindNotification},
		{`{"id":"a","result":null}`, KindReply},
		{`{"id":0,"result":{}}`, KindReply},
		{`{"id":1,"error":{"code":"failed","message":"No."}}`, KindReply},
		{`{"id":1,"method":"future.verb","params":{"future":1},"future":42,"jsonrpc":"2.0"}`, KindRequest},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			var m Message
			if err := json.Unmarshal([]byte(tc.raw), &m); err != nil {
				t.Fatal(err)
			}
			kind, err := m.Kind()
			if err != nil || kind != tc.kind {
				t.Fatalf("Kind = %v %v", kind, err)
			}
		})
	}
}

func TestMessageRejectsInvalidForms(t *testing.T) {
	for _, raw := range []string{
		`{}`, `null`, `[]`, `1`, `{"id":1}`, `{"result":{}}`,
		`{"id":null,"method":"session.ping"}`, `{"id":true,"method":"session.ping"}`,
		`{"id":1,"result":{},"error":{"code":"failed","message":"x"}}`,
		`{"id":1,"error":null}`, `{"id":1,"result":{},"error":null}`,
		`{"id":1,"method":"session.ping","result":null}`,
		`{"id":1,"method":"session.ping","error":{"code":"failed","message":"x"}}`,
		`{"method":null}`, `{"id":1,"method":"","result":{}}`,
		`{"id":1,"method":7}`, `{"method":"bad"}`, `{"id":1,"params":{},"result":{}}`,
		`{"id":1,"error":{}}`, `{"id":1,"error":{"code":1,"message":"x"}}`,
		`{"id":1,"error":{"code":"failed","message":null}}`,
		`{"id":1,"error":{"code":"failed","message":"x","data":[]}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var m Message
			if err := json.Unmarshal([]byte(raw), &m); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
}

func TestParamsMustBeObject(t *testing.T) {
	for _, params := range []string{`[]`, `null`, `"x"`, `7`, `true`} {
		var m Message
		err := json.Unmarshal([]byte(`{"id":1,"method":"session.ping","params":`+params+`}`), &m)
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != CodeInvalidParams {
			t.Fatalf("params %s: %v", params, err)
		}
	}
}

func TestUnknownErrorCodeAndFields(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"id":1,"error":{"code":"future","message":"x","data":{"future":1},"future":1}}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Error.Code != CodeInternal {
		t.Fatal(m.Error)
	}
	if string(m.Error.Data) != `{"future":1}` {
		t.Fatal(string(m.Error.Data))
	}
}

func TestErrorCodeDecoding(t *testing.T) {
	for _, code := range Codes() {
		b, _ := json.Marshal(code)
		var got Code
		if err := json.Unmarshal(b, &got); err != nil || got != code {
			t.Fatalf("code %s: %s %v", code, got, err)
		}
	}
	var p ProtocolErrorParams
	if err := json.Unmarshal([]byte(`{"code":"future","message":"x","method":""}`), &p); err != nil || p.Code != CodeInternal {
		t.Fatalf("unknown notification code = %s %v", p.Code, err)
	}
	for _, raw := range []string{`null`, `1`, `[]`} {
		var c Code
		if err := json.Unmarshal([]byte(raw), &c); err == nil {
			t.Fatalf("accepted code %s", raw)
		}
	}
	if MaxProtocolErrorsPerSecond != 10 {
		t.Fatal("protocol.error rate limit changed")
	}
}

func TestValidateProgrammaticMessages(t *testing.T) {
	id := NewStringID("a")
	for _, m := range []Message{
		{ID: &ID{}, Method: MethodSessionPing},
		{ID: &id, Result: json.RawMessage(`{`)},
		{ID: &id, Error: &Error{Code: "future"}},
		{ID: &id, Error: &Error{Code: CodeFailed, Data: json.RawMessage(`[]`)}},
		{ID: &id, Result: json.RawMessage(`null`), Token: "x"},
	} {
		if err := m.Validate(); err == nil {
			t.Fatalf("accepted %+v", m)
		}
	}
}
