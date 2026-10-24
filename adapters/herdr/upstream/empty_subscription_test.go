package upstream_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

func TestSubscribeEmptyHookListDoesNotDial(t *testing.T) {
	allowed, refused := upstream.FilterHookSubscriptions([]string{"pane.agent_status_changed", "pane.output_matched", "pane.scroll_changed"})
	if len(allowed) != 0 || len(refused) != 3 {
		t.Fatalf("all-refused filter result: allowed=%v, refused=%v", allowed, refused)
	}
	for _, tt := range []struct {
		name  string
		names []string
	}{
		{"nil", nil},
		{"empty", []string{}},
		{"every hook refused", allowed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dials := 0
			c := upstream.New(upstream.Options{Dial: func(context.Context, string, string) (net.Conn, error) {
				dials++
				return nil, errors.New("must not dial an empty subscription")
			}})
			stream, err := c.Subscribe(t.Context(), tt.names)
			if stream != nil {
				stream.Close()
				t.Fatal("empty hook list returned a stream")
			}
			e := named(t, err, "invalid_subscription")
			if dials != 0 || !strings.Contains(e.Message, "events.subscribe") {
				t.Fatalf("dials=%d, error=%v", dials, e)
			}
		})
	}
}
