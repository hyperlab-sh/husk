package handlers

import (
	"encoding/json"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func admitReq(method string) protocol.Message {
	id := protocol.NewStringID("7")
	return protocol.Message{Method: method, ID: &id}
}

func idOf(m protocol.Message) string {
	if m.ID == nil {
		return ""
	}
	return m.ID.String()
}

func TestAdmit(t *testing.T) {
	cases := []struct {
		name   string
		method string
		caller contracts.Caller
		code   protocol.Code // empty means admitted
	}{
		{"cli ping", protocol.MethodSessionPing, contracts.Caller{Kind: contracts.CallerCLI}, ""},
		{"plugin ping", protocol.MethodSessionPing, contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "p"}, ""},
		{"plugin state.set", protocol.MethodStateSet, contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "p"}, ""},
		{"cli state.set denied", protocol.MethodStateSet, contracts.Caller{Kind: contracts.CallerCLI}, protocol.CodeDenied},
		{"stem state.set denied", protocol.MethodStateSet, contracts.Caller{Kind: contracts.CallerStem}, protocol.CodeDenied},
		{"cli settings.list", protocol.MethodSettingsList, contracts.Caller{Kind: contracts.CallerCLI}, ""},
		{"plugin settings.list denied", protocol.MethodSettingsList, contracts.Caller{Kind: contracts.CallerPlugin}, protocol.CodeDenied},
		{"unknown method", "nope.nope", contracts.Caller{Kind: contracts.CallerCLI}, protocol.CodeUnknownMethod},
		{"outbound arriving inbound", protocol.MethodEventDeliver, contracts.Caller{Kind: contracts.CallerPlugin}, protocol.CodeUnknownMethod},
		{"unrouted arriving inbound", protocol.MethodProtocolError, contracts.Caller{Kind: contracts.CallerCLI}, protocol.CodeUnknownMethod},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := Admit(admitReq(tc.method), tc.caller)
			if tc.code == "" {
				if reply != nil {
					t.Fatalf("admitted method got refusal %+v", reply.Error)
				}
				return
			}
			if reply == nil {
				t.Fatal("refused method was admitted")
			}
			if reply.Error == nil || reply.Error.Code != tc.code {
				t.Fatalf("error = %+v, want code %s", reply.Error, tc.code)
			}
			if idOf(*reply) != `"7"` {
				t.Fatalf("reply id = %s, want the request id echoed", idOf(*reply))
			}
		})
	}
}

func TestAdmitNamesCallerKind(t *testing.T) {
	reply := Admit(admitReq(protocol.MethodStateSet), contracts.Caller{Kind: contracts.CallerCLI})
	if reply == nil || reply.Error == nil || reply.Error.Code != protocol.CodeDenied {
		t.Fatalf("want denied, got %+v", reply)
	}
	if reply.Error.Message == "" {
		t.Fatal("denied message should name the caller kind")
	}
}

func TestReply(t *testing.T) {
	t.Run("nil result encodes empty object", func(t *testing.T) {
		id := protocol.NewStringID("3")
		m := Reply(protocol.Message{ID: &id}, nil)
		if string(m.Result) != "{}" {
			t.Fatalf("result = %s", m.Result)
		}
		if idOf(m) != `"3"` {
			t.Fatalf("id = %s", idOf(m))
		}
	})
	t.Run("echoes no id when request had none", func(t *testing.T) {
		m := Reply(protocol.Message{Method: protocol.MethodEventEmit}, nil)
		if m.ID != nil {
			t.Fatalf("id = %v", m.ID)
		}
	})
	t.Run("encodes struct", func(t *testing.T) {
		id := protocol.NewStringID("1")
		m := Reply(protocol.Message{ID: &id}, protocol.SessionPingResult{ID: "x"})
		var got map[string]any
		if err := json.Unmarshal(m.Result, &got); err != nil {
			t.Fatal(err)
		}
		if got["id"] != "x" {
			t.Fatalf("result = %s", m.Result)
		}
	})
	t.Run("raw JSON passes through verbatim", func(t *testing.T) {
		id := protocol.NewStringID("1")
		m := Reply(protocol.Message{ID: &id}, json.RawMessage(`{"a":1}`))
		if string(m.Result) != `{"a":1}` {
			t.Fatalf("result = %s", m.Result)
		}
	})
}

func TestErrorReply(t *testing.T) {
	req := func() protocol.Message {
		id := protocol.NewStringID("2")
		return protocol.Message{ID: &id}
	}
	t.Run("coded error keeps code", func(t *testing.T) {
		m := ErrorReply(req(), contracts.NewError(protocol.CodeNotFound, "gone", nil))
		if m.Error == nil || m.Error.Code != protocol.CodeNotFound || m.Error.Message != "gone" {
			t.Fatalf("error = %+v", m.Error)
		}
	})
	t.Run("data error carries data", func(t *testing.T) {
		m := ErrorReply(req(), contracts.NewError(protocol.CodeConflict, "stale", map[string]any{"revision": 9}))
		if string(m.Error.Data) != `{"revision":9}` {
			t.Fatalf("data = %s", m.Error.Data)
		}
	})
	t.Run("plain error becomes internal", func(t *testing.T) {
		m := ErrorReply(req(), errString("boom"))
		if m.Error == nil || m.Error.Code != protocol.CodeInternal || m.Error.Message != "boom" {
			t.Fatalf("error = %+v", m.Error)
		}
	})
	t.Run("coded error with unknown code becomes internal", func(t *testing.T) {
		m := ErrorReply(req(), codedString("x"))
		if m.Error == nil || m.Error.Code != protocol.CodeInternal {
			t.Fatalf("error = %+v", m.Error)
		}
	})
	t.Run("nil error replies empty", func(t *testing.T) {
		m := ErrorReply(req(), nil)
		if m.Error != nil || string(m.Result) != "{}" {
			t.Fatalf("reply = %+v", m)
		}
	})
}

type errString string

func (e errString) Error() string { return string(e) }

type codedString string

func (e codedString) Error() string { return string(e) }
func (e codedString) Code() string  { return string(e) }

func TestInvalidParams(t *testing.T) {
	id := protocol.NewStringID("4")
	m := InvalidParams(protocol.Message{ID: &id}, "params.slice", "is required")
	if m.Error == nil || m.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("error = %+v", m.Error)
	}
	if m.Error.Message != "params.slice: is required" {
		t.Fatalf("message = %s", m.Error.Message)
	}
	var data struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(m.Error.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Field != "params.slice" {
		t.Fatalf("data = %s", m.Error.Data)
	}
}

func TestUnknownMethodReply(t *testing.T) {
	id := protocol.NewStringID("5")
	m := UnknownMethod(protocol.Message{Method: "x.y", ID: &id})
	if m.Error == nil || m.Error.Code != protocol.CodeUnknownMethod {
		t.Fatalf("error = %+v", m.Error)
	}
}
