package shim

import (
	"os"
	"syscall"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
)

// serverIdentity is loop-local to attach. An unchanged tuple deliberately cannot
// distinguish a replacement that inherited the same socket file and wire fields.
type serverIdentity struct {
	device, inode uint64
	version       string
	protocol      int
}

func identifyServer(path string, pong upstream.Pong) (serverIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return serverIdentity{}, upstream.Unavailable()
	}
	stat := info.Sys().(*syscall.Stat_t)
	return serverIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino), version: pong.Version, protocol: pong.Protocol}, nil
}

// An acknowledged stream never calls probe: EOF/error is its only loss signal.
// Schedule the next tick before pinging, so a 2 s timeout does not turn the 5 s
// probe cadence into a 7 s interval.
func (r *runner) probe(recorded serverIdentity) {
	schedule := func() (<-chan struct{}, upstream.Timer) {
		due := make(chan struct{}, 1)
		timer := r.o.Clock.AfterFunc(5*time.Second, func() { due <- struct{}{} })
		return due, timer
	}
	due, timer := schedule()
	defer func() { timer.Stop() }()
	failures := 0
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-due:
		}
		due, timer = schedule()
		pong, err := r.o.Client.PingInfo(r.ctx)
		var current serverIdentity
		if err == nil {
			current, err = identifyServer(r.o.Socket, pong)
		}
		if r.ctx.Err() != nil {
			return
		}
		if err != nil {
			failures++
			if upstream.NothingListening(err) || failures >= 3 {
				return
			}
			continue
		}
		failures = 0
		if current != recorded {
			return
		}
	}
}
