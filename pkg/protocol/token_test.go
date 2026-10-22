package protocol

import (
	"encoding/json"
	"testing"
)

func TestTokenPresenceDistinguishesEmptyFromAbsentAndResets(t *testing.T) {
	var m Message
	for _, tc := range []struct {
		raw     string
		present bool
	}{
		{`{"id":1,"method":"session.ping","token":"secret"}`, true},
		{`{"id":1,"method":"session.ping","token":""}`, true},
		{`{"id":1,"method":"session.ping"}`, false},
		{`{"id":1,"method":"session.ping","Token":"ignored"}`, false},
	} {
		if err := json.Unmarshal([]byte(tc.raw), &m); err != nil {
			t.Fatal(err)
		}
		if m.HasToken() != tc.present {
			t.Fatalf("presence=%v want %v", m.HasToken(), tc.present)
		}
	}
	if !(Message{Token: "value"}).HasToken() || (Message{}).HasToken() {
		t.Fatal("programmatic presence")
	}
	if err := json.Unmarshal([]byte(`{"id":1,"result":{},"token":""}`), &m); err == nil {
		t.Fatal("reply accepted credentials")
	}
	wire, err := json.Marshal(Message{Method: MethodSessionPing})
	if err != nil || string(wire) != `{"method":"session.ping"}` {
		t.Fatal("wire changed", string(wire), err)
	}
}
