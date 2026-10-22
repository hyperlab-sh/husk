package protocol

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestMessageIgnoresMixedCaseMembers(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"ID":1,"Method":"session.ping"}`, ""},
		{`{"id":1,"Result":{}}`, ""},
		{`{"id":1,"Method":"session.ping"}`, ""},
		{`{"method":"toast.show","Params":[1]}`, `{"method":"toast.show"}`},
		{`{"ID":1,"method":"session.ping"}`, `{"method":"session.ping"}`},
		{`{"id":1,"result":{},"Result":null,"Error":[]}`, `{"id":1,"result":{}}`},
		{`{"method":"toast.show","ID":null,"Method":5,"Params":[],"Result":{},"Error":null,"Token":1}`, `{"method":"toast.show"}`},
		{`{"id":1,"method":"session.ping","token":"real","Token":false}`, `{"id":1,"method":"session.ping","token":"real"}`},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var m Message
			err := json.Unmarshal([]byte(tc.raw), &m)
			if tc.want == "" {
				var failure *Error
				if !errors.As(err, &failure) || failure.Code != CodeInvalidMessage {
					t.Fatalf("expected invalid_message, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(m)
			if err != nil || string(b) != tc.want {
				t.Fatalf("message=%s err=%v want %s", b, err, tc.want)
			}
		})
	}
}

func TestErrorIgnoresMixedCaseMembers(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"Code":"failed","Message":"x"}`, ""},
		{`{"code":"failed","Message":"x"}`, ""},
		{`{"Code":"failed","message":"x"}`, ""},
		{`{"code":"failed","message":"x","Data":[]}`, `{"code":"failed","message":"x"}`},
		{`{"code":"failed","Code":null,"message":"x","Message":17,"data":{"future":1},"Data":[]}`, `{"code":"failed","message":"x","data":{"future":1}}`},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var failure Error
			err := json.Unmarshal([]byte(tc.raw), &failure)
			if tc.want == "" {
				if err == nil {
					t.Fatal("accepted missing exact-case error fields")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(failure)
			if err != nil || string(b) != tc.want {
				t.Fatalf("error=%s err=%v want %s", b, err, tc.want)
			}
		})
	}
}

func TestInvalidNonReplyGetsProtocolError(t *testing.T) {
	for _, raw := range []string{`{"id":1}`, `{"id":1,"method":5}`} {
		_, failures := readMessages(t, []byte(raw))
		if len(failures) != 1 {
			t.Fatal(failures)
		}
		m, ok := failures[0].Response()
		if !ok || m.Method != MethodProtocolError || m.ID != nil {
			t.Fatalf("Response = %+v, %v", m, ok)
		}
		var p ProtocolErrorParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Fatal(err)
		}
		if p.Code != CodeInvalidMessage {
			t.Fatal(p.Code)
		}
	}
}

func TestLineErrorResponseUsesWireSafeMessage(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"method":"toast.show","params":[]}`, "params must be an object"},
		{`{"id":1,"method":5}`, "message contains a field with an invalid JSON type"},
		{`not JSON`, "line is not valid UTF-8 JSON"},
	} {
		_, failures := readMessages(t, []byte(tc.raw))
		if len(failures) != 1 {
			t.Fatal(failures)
		}
		m, ok := failures[0].Response()
		if !ok {
			t.Fatal("missing error response")
		}
		var p ProtocolErrorParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Fatal(err)
		}
		if p.Message != tc.want {
			t.Fatalf("message = %q want %q", p.Message, tc.want)
		}
	}
}

func TestResetLineReleasesLargeBuffers(t *testing.T) {
	for _, capacity := range []int{0, 1024, readBufferBytes, readBufferBytes + 1, MaxLineBytes} {
		line := resetLine(make([]byte, capacity))
		if len(line) != 0 {
			t.Fatal("line not reset")
		}
		if capacity > readBufferBytes {
			if line != nil {
				t.Fatalf("retained %d-byte backing array", capacity)
			}
		} else if cap(line) != capacity {
			t.Fatalf("discarded ordinary %d-byte buffer", capacity)
		}
	}
}
