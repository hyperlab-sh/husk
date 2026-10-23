// Package wrapper supervises one plugin process and its stdio protocol channel.
// Workers post notices; they never write host logs or application State.
package wrapper

import (
	"context"
	"errors"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Liveness is the plugin process lifecycle exposed to the owning loop.
type Liveness string

const (
	// Starting precedes the launch gate and process start.
	Starting Liveness = "starting"
	// Live means the process started and opening notifications are queued.
	Live Liveness = "live"
	// Exited is a clean exit or any exit during instance shutdown.
	Exited Liveness = "exited"
	// Failed is a launch, process or channel failure.
	Failed Liveness = "failed"
)

const (
	// QueueCapacity is the default maximum number of queued stdin lines.
	QueueCapacity = 1024
	// WriteTimeout bounds a queued or legacy control write.
	WriteTimeout = 5 * time.Second
	// StopGrace is the default SIGTERM-to-SIGKILL grace.
	StopGrace = 2 * time.Second
	// KillWait is the bounded wait after killing survivors.
	KillWait = time.Second
	// EOFReapGrace allows an exiting child to be reaped after stdout closes.
	EOFReapGrace = 100 * time.Millisecond
)

var (
	// ErrNotLive rejects channel use before launch or after exit.
	ErrNotLive = errors.New("wrapper: plugin not live")
	// ErrAlreadyStarted rejects reusing a wrapper for another launch.
	ErrAlreadyStarted = errors.New("wrapper: process already started")
	// ErrStdoutClosed means stdout closed but the process survived reap grace.
	ErrStdoutClosed = errors.New("wrapper: stdout closed while process is running")
)

// Channel is the non-blocking stdin endpoint exposed to Stem modules.
type Channel interface {
	PluginID() string
	Send(protocol.Message) error
}

// ReplySink must be goroutine-safe. Both methods only enqueue loop input;
// they never resolve Calls or mutate loop-owned state on the worker goroutine.
type ReplySink interface {
	Reply(pluginID string, reply protocol.Message)
	Closed(pluginID string, reason error)
}

// Info is a detached lifecycle snapshot; it intentionally never contains tokens.
type Info struct {
	PluginID   string
	Status     Liveness
	PID        int
	StartedAt  time.Time
	LogPath    string
	LastError  string
	ExitStatus string
	StderrTail []string
	Shutdown   bool
}

// ExitMsg posts the completed launch outcome to the loop.
type ExitMsg struct {
	Info Info
	Err  error
}

// LaunchFailedMsg reports an attempt that could not start a child.
type LaunchFailedMsg struct {
	PluginID string
	LogPath  string
	Err      error
}

// ReapedMsg revokes authentication immediately after Wait, before the final
// stdout drain. It does not cancel Calls; ClosedMsg follows drained replies.
type ReapedMsg struct{ PluginID string }

// ChannelClosedMsg revokes authentication immediately on pipe closure. Calls
// cleanup waits for ClosedMsg with the final exit or reap-grace failure reason.
type ChannelClosedMsg struct{ PluginID string }

// ClosedMsg tells the loop to fail pending calls before applying exit liveness.
type ClosedMsg struct {
	PluginID string
	Reason   error
}

// LogMsg is a host log notice, not a write to the plugin stderr log.
type LogMsg struct {
	PluginID string
	Level    string
	Message  string
}

// LivenessMsg is applied by the parent Host only on the loop.
type LivenessMsg struct{ Info Info }

// Options supplies instance context and process seams. Post must be safe from
// workers and must return during shutdown; it may block for loop backpressure.
// LogPath runs on Start's caller and returns a fresh path owned by Store's rule.
// All owned timers use Clock; os/exec's WaitDelay is an additional stdlib safety
// fallback. BaseEnv defaults to os.Environ, with all HUSK_* entries removed.
type Options struct {
	Context       context.Context
	Post          func(tea.Msg)
	Replies       ReplySink
	Clock         protocol.Clock
	LogPath       func(pluginID string) (string, error)
	BaseEnv       []string
	SocketPath    string
	InstanceID    string
	BinPath       string
	QueueCapacity int
	WriteTimeout  time.Duration
	StopGrace     time.Duration
	KillWait      time.Duration
	EOFReapGrace  time.Duration
	Random        io.Reader
}

// Process is the lifecycle surface used by Pods and fake-wrapper tests. All
// methods are concurrency-safe; Start is called once on the host loop. Terminate
// and Kill signal the process group, not just the immediate process.
type Process interface {
	Channel
	Start(opening []protocol.Message) error
	Info() Info
	Token() string
	MarkShutdown()
	// FailVersionMismatch fails the plugin as a major-1 speaker and stops it.
	FailVersionMismatch()
	CloseStdin()
	Terminate() error
	Kill() error
	Done() <-chan struct{}
}

// SystemClock is the production clock for queued writes and process deadlines.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// AfterFunc schedules a stoppable watchdog.
func (SystemClock) AfterFunc(d time.Duration, fn func()) protocol.Timer { return time.AfterFunc(d, fn) }
