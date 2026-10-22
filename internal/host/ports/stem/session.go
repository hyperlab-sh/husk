// Package stem implements the host's Stem port wrappers over the real
// modules: the Session identity wrapper and the StatePort wrapper over
// internal/substrate/state. The Calls, Event, Hook and Surface engines are
// separate units; their port wrappers land with them.
//
// Loop rule: only the Bubble Tea loop mutates State. The State wrapper runs
// plugin writes and clears as posted closures and waits on the request
// context; reads and the snapshot run on a copy-on-write Snapshot value.
package stem

import (
	"context"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Session serves session.ping from the running instance's identity. The
// result is the same for every caller kind; nothing here can fail.
type Session struct {
	// InstanceID is the instance's registered id (pid or --instance name).
	InstanceID string
	// PID is the Husk process's pid.
	PID int
	// HostVersion is the distribution's version string, reported as-is.
	HostVersion string
	// StartedAt is when the instance started; uptime is measured from it.
	StartedAt time.Time
	// Clock supplies Now. Nil skips the uptime field.
	Clock contracts.Clock
}

// Ping implements contracts.SessionPort.
func (s *Session) Ping(context.Context, contracts.Caller) (protocol.SessionPingResult, error) {
	var uptime int64
	if s.Clock != nil {
		now := s.Clock.Now()
		if !s.StartedAt.IsZero() && !now.Before(s.StartedAt) {
			uptime = now.Sub(s.StartedAt).Milliseconds()
		}
	}
	return protocol.SessionPingResult{
		ID:              s.InstanceID,
		PID:             s.PID,
		ProtocolVersion: protocol.Version,
		HostVersion:     s.HostVersion,
		UptimeMS:        uptime,
	}, nil
}
