package shim

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestPaneReviewTerminalAcknowledgementCorrelation(t *testing.T) {
	for _, mode := range []string{"unrelated_lines", "protocol_error"} {
		t.Run(mode, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			h := &terminalHost{conn: client, decoder: json.NewDecoder(client)}
			done := make(chan error, 1)
			go func() {
				var req protocol.Message
				if err := json.NewDecoder(server).Decode(&req); err != nil {
					done <- err
					return
				}
				w := protocol.NewWriter(server)
				if mode == "protocol_error" {
					done <- w.Send(protocol.Message{Method: protocol.MethodProtocolError, Params: json.RawMessage(`{"code":"denied","message":"output refused"}`)})
					return
				}
				if err := w.Send(protocol.Message{Method: protocol.MethodStateChanged, Params: json.RawMessage(`{}`)}); err != nil {
					done <- err
					return
				}
				wrong := protocol.NewStringID("not-our-request")
				if err := w.Send(protocol.Message{ID: &wrong, Result: json.RawMessage(`{"wrong":true}`)}); err != nil {
					done <- err
					return
				}
				done <- w.Send(protocol.Message{ID: req.ID, Result: json.RawMessage(`{"ingested":true}`)})
			}()
			result, err := h.Call(context.Background(), protocol.MethodTerminalWrite, map[string]any{"surface": "demo.pane", "data": "eA=="})
			if mode == "protocol_error" {
				var failure *protocol.Error
				if !errors.As(err, &failure) || failure.Code != protocol.CodeDenied {
					t.Fatalf("protocol.error accepted as acknowledgement: %s %v", result, err)
				}
			} else if err != nil || string(result) != `{"ingested":true}` {
				t.Fatalf("uncorrelated acknowledgement: %s %v", result, err)
			}
			if err := take(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}
}
