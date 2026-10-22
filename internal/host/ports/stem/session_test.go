package stem

import (
	"context"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

func TestSessionPing(t *testing.T) {
	start := time.Date(2025, 7, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		s    Session
		want protocol.SessionPingResult
	}{
		{
			name: "identity and uptime",
			s:    Session{InstanceID: "husk-4242", PID: 4242, HostVersion: "0.9.1", StartedAt: start, Clock: fakeClock{start.Add(1500 * time.Millisecond)}},
			want: protocol.SessionPingResult{ID: "husk-4242", PID: 4242, ProtocolVersion: protocol.Version, HostVersion: "0.9.1", UptimeMS: 1500},
		},
		{
			name: "nil clock skips uptime",
			s:    Session{InstanceID: "husk-1", PID: 1, HostVersion: "dev", StartedAt: start},
			want: protocol.SessionPingResult{ID: "husk-1", PID: 1, ProtocolVersion: protocol.Version, HostVersion: "dev"},
		},
		{
			name: "clock before start keeps uptime at zero",
			s:    Session{InstanceID: "husk-1", PID: 1, StartedAt: start, Clock: fakeClock{start.Add(-time.Second)}},
			want: protocol.SessionPingResult{ID: "husk-1", PID: 1, ProtocolVersion: protocol.Version},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.s.Ping(context.Background(), contracts.Caller{Kind: contracts.CallerCLI})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("ping = %+v, want %+v", got, tc.want)
			}
		})
	}
}
