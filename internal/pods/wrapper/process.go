package wrapper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Wrapper owns one launch and all its pipe ends. Its mutex protects only local
// process/channel bookkeeping; host State is updated exclusively from notices.
type Wrapper struct {
	spec                                  manifest.Spec
	opts                                  Options
	ctx                                   context.Context
	cancel                                context.CancelFunc
	token                                 string
	mu                                    sync.Mutex
	info                                  Info
	started, live, finished, shutdown     bool
	killed, groupEnded, terminated        bool
	groupDone                             chan struct{}
	groupExists                           func(int) bool
	signal                                func(int, bool) error
	fault                                 error
	queue                                 *protocol.Writer
	stdin                                 *stdinMux
	stdout                                *os.File
	cmd                                   *exec.Cmd
	done, waited, readDone                chan struct{}
	closedOnce, stopOnce, unavailableOnce sync.Once
	termErr                               error
	reapTimer, killTimer, drainTimer      protocol.Timer
	pollTimer                             protocol.Timer
}

// New prepares a single-use wrapper and a fresh random token, but starts no
// process or goroutine. LogPath is opened only by Start; no Store is imported.
func New(spec manifest.Spec, opts Options) (*Wrapper, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	if opts.Clock == nil {
		opts.Clock = SystemClock{}
	}
	if opts.QueueCapacity == 0 {
		opts.QueueCapacity = QueueCapacity
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = WriteTimeout
	}
	if opts.StopGrace == 0 {
		opts.StopGrace = StopGrace
	}
	if opts.KillWait == 0 {
		opts.KillWait = KillWait
	}
	if opts.EOFReapGrace == 0 {
		opts.EOFReapGrace = EOFReapGrace
	}
	if opts.QueueCapacity < 0 || opts.WriteTimeout < 0 || opts.StopGrace < 0 || opts.KillWait < 0 || opts.EOFReapGrace < 0 {
		return nil, errors.New("wrapper: negative lifecycle limit")
	}
	if opts.LogPath == nil {
		return nil, errors.New("wrapper: LogPath is required")
	}
	if opts.BinPath == "" {
		var err error
		opts.BinPath, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(opts.BinPath) {
		return nil, errors.New("wrapper: binary path must be absolute")
	}
	if opts.BaseEnv == nil {
		opts.BaseEnv = os.Environ()
	} else {
		opts.BaseEnv = slices.Clone(opts.BaseEnv)
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	var token [32]byte
	if _, err := io.ReadFull(opts.Random, token[:]); err != nil {
		return nil, fmt.Errorf("generate plugin token: %w", err)
	}
	ctx, cancel := context.WithCancel(opts.Context)
	return &Wrapper{spec: spec.Clone(), opts: opts, ctx: ctx, cancel: cancel, token: hex.EncodeToString(token[:]), info: Info{PluginID: spec.ID, Status: Starting}, done: make(chan struct{}), waited: make(chan struct{}), readDone: make(chan struct{}), groupDone: make(chan struct{}), groupExists: groupAlive, signal: signalGroup}, nil
}

// PluginID returns the manifest identity.
func (w *Wrapper) PluginID() string { return w.spec.ID }

// Token returns the per-launch credential only for registration with the host.
// It is never included in Info, notices, logs or diagnostics.
func (w *Wrapper) Token() string { return w.token }

// Done closes after the process was waited for and its reader stopped.
func (w *Wrapper) Done() <-chan struct{} { return w.done }

// Info returns a detached lifecycle snapshot.
func (w *Wrapper) Info() Info {
	w.mu.Lock()
	defer w.mu.Unlock()
	v := w.info
	v.StderrTail = slices.Clone(v.StderrTail)
	return v
}

// Start runs on the host loop after preparation. Opening notifications are
// validated and queued before the channel becomes live or any request can send.
func (w *Wrapper) Start(opening []protocol.Message) error {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return ErrAlreadyStarted
	}
	w.started = true
	w.mu.Unlock()
	fail := func(err error) error { w.launchFailed(err); return err }
	for _, m := range opening {
		kind, err := m.Kind()
		if err != nil {
			return fail(err)
		}
		if kind != protocol.KindNotification {
			return fail(errors.New("opening messages must be notifications"))
		}
		if err := m.Validate(); err != nil {
			return fail(err)
		}
	}
	path, err := w.opts.LogPath(w.spec.ID)
	if err != nil {
		return fail(err)
	}
	logFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail(fmt.Errorf("open stderr log: %w", err))
	}
	w.mu.Lock()
	w.info.LogPath = path
	w.mu.Unlock()
	defer logFile.Close()
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		inRead.Close()
		inWrite.Close()
		return fail(err)
	}
	argv := w.spec.Argv()
	cmd := exec.CommandContext(w.ctx, argv[0], argv[1:]...)
	cmd.Dir = w.spec.WorkDir
	cmd.Env = w.environment()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inRead, outWrite, logFile
	// Spec-required real-clock fallback in os/exec: this bounds the leader's
	// cancellation path only, not descendant groups or our injected deadlines.
	cmd.WaitDelay = w.opts.StopGrace
	if err := configureGroup(cmd); err != nil {
		inRead.Close()
		inWrite.Close()
		outRead.Close()
		outWrite.Close()
		return fail(err)
	}
	cmd.Cancel = func() error { return w.terminatePID(cmd.Process.Pid) }
	if err := cmd.Start(); err != nil {
		inRead.Close()
		inWrite.Close()
		outRead.Close()
		outWrite.Close()
		return fail(err)
	}
	inRead.Close()
	outWrite.Close()
	mux := &stdinMux{raw: inWrite, clock: w.opts.Clock, timeout: w.opts.WriteTimeout, closed: make(chan struct{})}
	queue := protocol.NewQueuedWriter(mux, protocol.QueueOptions{Capacity: w.opts.QueueCapacity, WriteTimeout: w.opts.WriteTimeout, Clock: w.opts.Clock, Closer: mux, OnFail: w.fail})
	mux.queue = queue // no enqueue/reader/wait worker can close it before this assignment
	w.mu.Lock()
	w.cmd = cmd
	w.stdin = mux
	w.stdout = outRead
	w.queue = queue
	w.info.PID = cmd.Process.Pid
	w.info.StartedAt = w.opts.Clock.Now()
	w.mu.Unlock()
	var openingErr error
	for _, m := range opening {
		if err := queue.Send(m); err != nil {
			openingErr = err
			break
		}
	}
	w.mu.Lock()
	if openingErr == nil && w.fault == nil && !w.shutdown {
		w.live = true
		w.info.Status = Live
	}
	w.mu.Unlock()
	go w.readStdout(outRead)
	go w.wait(cmd)
	if openingErr != nil {
		go w.fail(openingErr)
	}
	return nil // cmd.Start succeeded; later channel failures arrive as ExitMsg
}

// Send marshals and enqueues without waiting for pipe I/O.
func (w *Wrapper) Send(m protocol.Message) error {
	w.mu.Lock()
	q, live, finished := w.queue, w.live, w.finished
	w.mu.Unlock()
	if finished || q == nil {
		return ErrNotLive
	}
	if err := q.Err(); err != nil {
		return err
	}
	if !live {
		return ErrNotLive
	}
	return q.Send(m)
}

// MarkShutdown makes any subsequent exit normal and refuses new sends.
func (w *Wrapper) MarkShutdown() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shutdown = true
	w.live = false
	for _, timer := range []protocol.Timer{w.reapTimer, w.killTimer} {
		if timer != nil {
			timer.Stop()
		}
	}
}

// CloseStdin flushes queued lines and then closes stdin; it never waits.
func (w *Wrapper) CloseStdin() {
	w.mu.Lock()
	q := w.queue
	w.mu.Unlock()
	if q != nil {
		_ = q.Close()
	}
}

// Terminate sends SIGTERM to the process group exactly once.
func (w *Wrapper) Terminate() error {
	w.mu.Lock()
	pid, finished := w.info.PID, w.finished
	w.mu.Unlock()
	if finished {
		return os.ErrProcessDone
	}
	if pid == 0 {
		return nil
	}
	return w.terminatePID(pid)
}
func (w *Wrapper) terminatePID(pid int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished || w.groupEnded {
		return os.ErrProcessDone
	}
	if !w.groupExists(pid) {
		w.endGroupLocked()
		return os.ErrProcessDone
	}
	if !w.terminated {
		w.terminated = true
		w.termErr = w.signal(pid, false)
		if errors.Is(w.termErr, os.ErrProcessDone) {
			w.endGroupLocked()
		}
	}
	return w.termErr
}

// Kill sends SIGKILL once and preserves stdout until EOF or the kill wait.
func (w *Wrapper) Kill() error { return w.kill(false) }

func (w *Wrapper) kill(escalation bool) error {
	w.mu.Lock()
	if w.finished || w.groupEnded || (escalation && w.shutdown) {
		w.mu.Unlock()
		return os.ErrProcessDone
	}
	pid := w.info.PID
	if pid == 0 {
		w.mu.Unlock()
		return nil
	}
	if !w.groupExists(pid) {
		w.endGroupLocked()
		w.mu.Unlock()
		return os.ErrProcessDone
	}
	err := w.signal(pid, true)
	w.killed = true
	// Any outcome is terminal for group supervision, including EPERM: never
	// strand finishGroup waiting for a signal that cannot be delivered.
	if err != nil && !errors.Is(err, os.ErrProcessDone) && w.fault == nil {
		w.fault = fmt.Errorf("kill process group: %w", err)
	}
	w.endGroupLocked()
	w.armDrainLocked()
	w.mu.Unlock()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		go w.log("error", fmt.Sprintf("plugin %s: kill process group: %v", w.spec.ID, err))
	}
	return err
}

func (w *Wrapper) environment() []string {
	env := make([]string, 0, len(w.opts.BaseEnv)+6)
	for _, v := range w.opts.BaseEnv {
		if !strings.HasPrefix(v, "HUSK_") {
			env = append(env, v)
		}
	}
	return append(env, "HUSK_SOCKET_PATH="+w.opts.SocketPath, "HUSK_INSTANCE="+w.opts.InstanceID, "HUSK_BIN_PATH="+w.opts.BinPath, "HUSK_PLUGIN_ID="+w.spec.ID, "HUSK_PLUGIN_TOKEN="+w.token, "HUSK_PROTOCOL_VERSION="+protocol.Version)
}

func (w *Wrapper) launchFailed(err error) {
	w.cancel()
	w.mu.Lock()
	w.finished = true
	w.info.Status = Failed
	w.info.LastError = err.Error()
	path := w.info.LogPath
	w.mu.Unlock()
	// Start runs on the loop. Neither ReplySink nor Post may run there, even
	// when no executable was started; both can block on Program.Send.
	go func() {
		w.closed(err)
		w.post(LaunchFailedMsg{PluginID: w.spec.ID, LogPath: path, Err: err})
		close(w.waited)
		close(w.readDone)
		close(w.done)
	}()
}

func (w *Wrapper) closed(err error) {
	w.closedOnce.Do(func() {
		if w.opts.Replies != nil {
			w.opts.Replies.Closed(w.spec.ID, err)
		}
	})
}
func (w *Wrapper) post(msg any) {
	if w.opts.Post != nil {
		w.opts.Post(msg)
	}
}
func (w *Wrapper) log(level, message string) {
	w.post(LogMsg{PluginID: w.spec.ID, Level: level, Message: message})
}

func (w *Wrapper) recordFailure(err error) {
	w.mu.Lock()
	if !w.shutdown && w.fault == nil {
		w.fault = err
	}
	w.live = false
	w.mu.Unlock()
	w.closed(err)
}

func (w *Wrapper) fail(err error) {
	select {
	case <-w.waited:
		return
	default:
	}
	if errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || brokenPipe(err) {
		w.awaitReap(err)
		return
	}
	w.fatal(err)
}

func (w *Wrapper) fatal(err error) {
	select {
	case <-w.waited:
		return
	default:
	}
	w.recordFailure(fmt.Errorf("stopped reading: %w", err))
	w.stop()
}

// Pipe closure revokes authentication immediately, but Closed waits for the
// final exit reason and buffered replies, or for the injected reap grace.
func (w *Wrapper) awaitReap(err error) {
	w.mu.Lock()
	w.live = false
	select {
	case <-w.waited:
	default:
		if !w.shutdown && !w.finished && w.reapTimer == nil {
			w.reapTimer = w.opts.Clock.AfterFunc(w.opts.EOFReapGrace, func() { w.fatal(err) })
		}
	}
	w.mu.Unlock()
	w.unavailableOnce.Do(func() { w.post(ChannelClosedMsg{PluginID: w.spec.ID}) })
}

func (w *Wrapper) stop() {
	w.stopOnce.Do(func() {
		w.mu.Lock()
		q := w.queue
		w.mu.Unlock()
		if q != nil {
			q.Abort()
		}
		_ = w.Terminate()
		w.cancel()
		w.mu.Lock()
		w.armGroupLocked()
		w.mu.Unlock()
	})
}

func (w *Wrapper) closePipes() {
	w.mu.Lock()
	in, out := w.stdin, w.stdout
	w.mu.Unlock()
	if in != nil {
		_ = in.raw.Close()
	}
	if out != nil {
		_ = out.Close()
	}
}

const groupPollInterval = 50 * time.Millisecond

// endGroupLocked is the signal fence, independent of reader/final-notice
// completion. Once empty or terminally signalled, this group is never reused.
func (w *Wrapper) endGroupLocked() {
	if w.groupEnded {
		return
	}
	w.groupEnded = true
	close(w.groupDone)
	for _, timer := range []protocol.Timer{w.killTimer, w.pollTimer} {
		if timer != nil {
			timer.Stop()
		}
	}
}

func (w *Wrapper) armDrainLocked() {
	if w.drainTimer == nil && !w.finished {
		w.drainTimer = w.opts.Clock.AfterFunc(w.opts.KillWait, w.closePipes)
	}
}

func (w *Wrapper) armGroupLocked() {
	if w.finished || w.groupEnded {
		return
	}
	if !w.groupExists(w.info.PID) {
		w.endGroupLocked()
		return
	}
	if w.killTimer == nil && !w.shutdown {
		w.killTimer = w.opts.Clock.AfterFunc(w.opts.StopGrace, func() {
			if err := w.kill(true); !errors.Is(err, os.ErrProcessDone) {
				w.log("warn", fmt.Sprintf("plugin %s did not exit within %s of SIGTERM; killing it", w.spec.ID, w.opts.StopGrace))
			}
		})
	}
	if w.pollTimer == nil {
		w.pollTimer = w.opts.Clock.AfterFunc(groupPollInterval, w.pollGroup)
	}
}

func (w *Wrapper) pollGroup() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pollTimer = nil
	w.armGroupLocked()
}

// A reaped leader may leave children without inherited stdio. Poll on the
// injected clock so a promptly exiting child cancels escalation and completes
// without spending the full stop grace or producing a false survivor warning.
func (w *Wrapper) finishGroup() {
	_ = w.Terminate()
	w.mu.Lock()
	w.armGroupLocked()
	w.mu.Unlock()
	<-w.groupDone
}

func (w *Wrapper) wait(cmd *exec.Cmd) {
	err := cmd.Wait()
	close(w.waited)
	w.mu.Lock()
	if w.reapTimer != nil {
		w.reapTimer.Stop()
	}
	w.mu.Unlock()
	w.post(ReapedMsg{PluginID: w.spec.ID})
	w.finishGroup()
	// The group is finished, but buffered replies still belong to this launch.
	// Bound a pipe held by an escaped descendant without signalling again.
	select {
	case <-w.readDone:
	default:
		w.mu.Lock()
		w.armDrainLocked()
		w.mu.Unlock()
	}
	<-w.readDone
	// Buffered final replies precede once-only Calls cancellation. A pipe EOF
	// inside reap grace is not itself the reason for a clean or crashed exit.
	w.mu.Lock()
	reason := w.fault
	shutdown := w.shutdown
	w.mu.Unlock()
	if reason == nil || shutdown {
		reason = errors.New("exited")
		if err != nil && !shutdown {
			reason = fmt.Errorf("crashed (%s): %w", cmd.ProcessState.String(), err)
		}
	}
	w.closed(reason)
	w.mu.Lock()
	q := w.queue
	w.mu.Unlock()
	if q != nil {
		q.Abort()
		<-q.Done()
	}
	w.closePipes()
	w.mu.Lock()
	w.live = false
	w.finished = true
	w.info.Shutdown = w.shutdown
	w.info.ExitStatus = cmd.ProcessState.String()
	w.info.Status = Exited
	cause := w.fault
	if cause == nil {
		cause = err
	}
	if !w.shutdown && cause != nil {
		w.info.Status = Failed
		w.info.LastError = cause.Error()
	}
	if w.killTimer != nil {
		w.killTimer.Stop()
	}
	if w.drainTimer != nil {
		w.drainTimer.Stop()
	}
	path := w.info.LogPath
	w.mu.Unlock()
	tail := tailLines(path, 3)
	w.mu.Lock()
	w.info.StderrTail = tail
	info := w.info
	info.StderrTail = slices.Clone(w.info.StderrTail)
	w.mu.Unlock()
	w.post(ExitMsg{Info: info, Err: cause})
	close(w.done)
}

var _ Process = (*Wrapper)(nil)
