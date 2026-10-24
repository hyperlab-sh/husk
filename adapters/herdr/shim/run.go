package shim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/proxy"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func correlationID(id *protocol.ID) string {
	var s string
	if json.Unmarshal([]byte(id.String()), &s) == nil {
		return s
	}
	return id.String()
}

type clock struct{}

func (clock) AfterFunc(d time.Duration, f func()) upstream.Timer { return time.AfterFunc(d, f) }

// Options supplies one plugin's runtime inputs and deterministic lifecycle seams.
type Options struct {
	Dir, Binary, Socket, ConfigDir, StateDir, RegistryPath, LogPath, Platform string
	Env                                                                       []string
	Client                                                                    *upstream.Client
	Host                                                                      Host
	Clock                                                                     upstream.Clock
	Now                                                                       func() time.Time
	Spawn                                                                     func(Command) (Process, error)
	// SpawnTerminal replaces the shared PTY helper for deterministic tests.
	SpawnTerminal func(Command, int, int) (TerminalProcess, error)
}
type state struct {
	Server    string         `json:"server"`
	Socket    string         `json:"socket"`
	Version   string         `json:"version"`
	Surfaces  map[string]any `json:"surfaces"`
	Commands  int            `json:"commands_running"`
	LastError string         `json:"last_error"`
}
type runner struct {
	o           Options
	m           manifest.Manifest
	id          string
	ctx         context.Context
	cancel      context.CancelFunc
	log         io.Writer
	logMu       sync.Mutex
	mu          sync.Mutex
	publish     sync.Mutex
	state       state
	processes   map[Process]bool
	commands    int
	stopping    bool
	workers     sync.WaitGroup
	children    sync.WaitGroup
	proxy       *proxy.Server
	groupTimers map[Process]upstream.Timer
	panes       map[protocol.SurfaceID]*pane
	paneGate    chan struct{}
	paneCounter uint64
	paneTimers  map[Process]upstream.Timer
}

// Run serves Husk requests until stdin EOF. Upstream work is per request and
// never holds the stdin reader, so EOF always begins shutdown.
func Run(ctx context.Context, o Options, in io.Reader, out, log io.Writer) error {
	if closer, ok := o.Host.(io.Closer); ok {
		defer closer.Close()
	}
	if o.Clock == nil {
		o.Clock = clock{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Spawn == nil {
		o.Spawn = Spawn
	}
	if o.SpawnTerminal == nil {
		o.SpawnTerminal = SpawnTerminal
	}
	if o.Platform == "" {
		o.Platform = runtime.GOOS
	}
	if o.Client == nil {
		o.Client = upstream.New(upstream.Options{Socket: o.Socket, Clock: o.Clock})
	}
	data, err := os.ReadFile(filepath.Join(o.Dir, "herdr-plugin.toml"))
	if err != nil {
		return err
	}
	m, err := manifest.Parse(data)
	if err != nil {
		return err
	}
	id, err := manifest.MapID(m.ID)
	if err != nil {
		return err
	}
	if o.Binary == "" {
		return fmt.Errorf("herdr binary not found")
	}
	if err = ensureDirs(o.ConfigDir, o.StateDir); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := &runner{o: o, m: m, id: id, ctx: ctx, cancel: cancel, log: log, processes: map[Process]bool{}, panes: map[protocol.SurfaceID]*pane{}, paneGate: make(chan struct{}, 1), state: state{Server: "unchecked", Socket: o.Socket, Surfaces: map[string]any{}}}
	p, err := proxy.Start(ctx, proxy.Options{Dial: o.Client.Dial, Clock: o.Clock, Intercept: r.intercept, Warn: func(method string) { r.logLine(map[string]any{"level": "warning", "plugin": m.ID, "method": method}) }})
	if err != nil {
		return err
	}
	r.proxy = p
	r.change(func(*state) {})
	messages := make(chan protocol.Message)
	readDone := make(chan error, 1)
	writer := protocol.NewWriter(out)
	go func() {
		readDone <- protocol.ReadLoop(in, func(m protocol.Message) {
			select {
			case messages <- m:
			case <-ctx.Done():
			}
		}, func(e *protocol.LineError) { r.logLine(map[string]any{"code": e.Code, "message": e.Error()}) })
	}()
loop:
	for {
		select {
		case <-ctx.Done():
			err = nil
			break loop
		case err = <-readDone:
			if errors.Is(err, io.EOF) {
				err = nil
			}
			break loop
		case m := <-messages:
			if m.ID == nil {
				r.paneNotification(m)
				continue
			}
			if m.Method == "" {
				continue
			}
			r.workers.Add(1)
			go func() {
				defer r.workers.Done()
				result, failure := r.request(m)
				reply := protocol.Message{ID: m.ID, Error: failure}
				if failure == nil {
					reply.Result, _ = json.Marshal(result)
				}
				_ = writer.Send(reply)
			}()
		}
	}
	cancel()
	if closer, ok := in.(io.Closer); ok {
		_ = closer.Close()
	}
	p.Close()
	r.mu.Lock()
	r.stopping = true
	for _, t := range r.groupTimers {
		t.Stop()
	}
	panes := make([]*pane, 0, len(r.panes))
	for _, p := range r.panes {
		panes = append(panes, p)
	}
	r.mu.Unlock()
	for _, p := range panes {
		r.closePane(p)
	}
	r.workers.Wait()
	r.stopCommands()
	r.mu.Lock()
	for _, t := range r.paneTimers {
		t.Stop()
	}
	r.mu.Unlock()
	return err
}
func (r *runner) logLine(v any) {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	_ = json.NewEncoder(r.log).Encode(v)
}
func (r *runner) change(f func(*state)) {
	r.publish.Lock()
	defer r.publish.Unlock()
	r.mu.Lock()
	f(&r.state)
	raw, _ := json.Marshal(r.state)
	r.mu.Unlock()
	ctx, done := r.bounded(r.ctx, 2*time.Second)
	defer done()
	_, _ = r.o.Host.Call(ctx, protocol.MethodStateSet, map[string]any{"slice": r.id + ".state", "value": json.RawMessage(raw)})
}
func (r *runner) bounded(parent context.Context, d time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	timer := r.o.Clock.AfterFunc(d, cancel)
	return ctx, func() { timer.Stop(); cancel() }
}
func (r *runner) checkServer(ctx context.Context) *protocol.Error {
	version, err := r.o.Client.Check(ctx, r.m.MinVersion)
	if err != nil {
		r.change(func(s *state) { s.Server = "absent"; s.LastError = err.Error() })
		var named *upstream.Error
		if errors.As(err, &named) && named.Code != "unavailable" {
			return &protocol.Error{Code: protocol.CodeFailed, Message: err.Error()}
		}
		return &protocol.Error{Code: protocol.CodeUnavailable, Message: upstream.Unavailable().Message, Data: json.RawMessage(`{"reason":"herdr_absent"}`)}
	}
	r.change(func(s *state) { s.Server = "available"; s.Version = version; s.LastError = "" })
	return nil
}
func (r *runner) request(m protocol.Message) (any, *protocol.Error) {
	if m.Method != protocol.MethodActionInvoke {
		return nil, &protocol.Error{Code: protocol.CodeUnknownMethod, Message: "unknown method"}
	}
	var p struct {
		Action string          `json:"action"`
		Args   json.RawMessage `json:"args"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "invalid action params"}
	}
	local := strings.TrimPrefix(p.Action, r.id+".")
	if strings.HasPrefix(local, "pane-") {
		var args paneArgs
		if len(p.Args) > 0 && json.Unmarshal(p.Args, &args) != nil {
			return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "invalid pane args"}
		}
		ctx, done := r.bounded(r.ctx, 10*time.Second)
		defer done()
		return r.openPane(ctx, strings.TrimPrefix(local, "pane-"), args, correlationID(m.ID))
	}
	var args struct {
		Context json.RawMessage `json:"context"`
	}
	if len(p.Args) > 0 && json.Unmarshal(p.Args, &args) != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "invalid action args"}
	}
	for _, e := range r.m.Actions {
		if strings.ReplaceAll(e.ID, ":", "-") == local && manifest.Supports(e.Platforms, r.o.Platform) {
			result, err := r.launch(e, e.ID, correlationID(m.ID), args.Context)
			if err != nil {
				return nil, wireFailure(err)
			}
			return result, nil
		}
	}
	return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "unknown action"}
}
func (r *runner) launch(e manifest.Entry, action, correlation string, provided json.RawMessage) (map[string]any, error) {
	ctx, done := r.bounded(r.ctx, 2*time.Second)
	defer done()
	if failure := r.checkServer(ctx); failure != nil {
		return nil, failure
	}
	c, err := BuildContext(ctx, r.o.Client, correlation, "api", provided)
	if err != nil {
		var remote *upstream.Error
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &remote) && remote.Code == "unavailable" {
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error(), Data: json.RawMessage(`{"reason":"herdr_absent"}`)}
		}
		return nil, err
	}
	env := CommandEnvironment(r.o.Env, r.o.Dir, r.o.Binary, r.proxy.Path(), r.o.ConfigDir, r.o.StateDir, r.m.ID, action, c)
	r.mu.Lock()
	if r.stopping || r.ctx.Err() != nil {
		r.mu.Unlock()
		return nil, context.Canceled
	}
	if r.commands >= 32 {
		r.mu.Unlock()
		return nil, &protocol.Error{Code: protocol.CodeBusy, Message: "maximum concurrent commands reached"}
	}
	process, err := r.o.Spawn(Command{Argv: e.Command, Env: env, Dir: r.o.Dir})
	if err != nil {
		r.mu.Unlock()
		r.logLine(map[string]any{"command": e.Command, "error": err.Error()})
		return nil, err
	}
	r.commands++
	r.processes[process] = true
	r.children.Add(1)
	r.mu.Unlock()
	record := map[string]any{"log_id": fmt.Sprintf("command-%d", process.PID()), "plugin_id": r.m.ID, "command": e.Command, "status": "running", "started_unix_ms": r.o.Now().UnixMilli(), "action_id": action}
	r.change(func(s *state) { s.Commands++ })
	go func() {
		defer r.children.Done()
		status, stderr := process.Wait()
		r.mu.Lock()
		r.commands--
		if groupAlive(process) {
			r.processes[process] = false
			r.observeGroup(process)
		} else {
			delete(r.processes, process)
		}
		r.mu.Unlock()
		r.logLine(map[string]any{"command": e.Command, "status": status, "stderr": stderr, "at": r.o.Now().UTC().Format(time.RFC3339Nano)})
		r.change(func(s *state) { s.Commands-- })
		if status != 0 {
			_ = r.o.Host.Notify(r.ctx, protocol.MethodToastShow, map[string]any{"message": fmt.Sprintf("%s: %s exited %d", r.m.Name, e.Title, status), "level": "warn"})
		}
	}()
	return map[string]any{"started": true, "pid": process.PID(), "log": r.o.LogPath, "context": c, "command_log": record, "action_info": map[string]any{"plugin_id": r.m.ID, "action_id": action, "title": e.Title, "command": e.Command}}, nil
}
func (r *runner) stopCommands() {
	r.mu.Lock()
	ps := make([]Process, 0, len(r.processes))
	for p := range r.processes {
		ps = append(ps, p)
	}
	for _, p := range ps {
		if _, stopping := r.paneTimers[p]; !stopping {
			_ = p.Signal(syscall.SIGTERM)
		}
	}
	r.mu.Unlock()
	if len(ps) == 0 {
		r.children.Wait()
		return
	}
	done := make(chan struct{})
	go func() { r.children.Wait(); close(done) }()
	killed := make(chan struct{})
	timer := r.o.Clock.AfterFunc(2*time.Second, func() {
		r.mu.Lock()
		for p := range r.processes {
			_ = p.Signal(syscall.SIGKILL)
		}
		r.mu.Unlock()
		close(killed)
	})
	defer timer.Stop()
	select {
	case <-killed:
		<-done
		return
	case <-done:
	}
	for {
		r.mu.Lock()
		alive := false
		for p := range r.processes {
			if groupAlive(p) {
				alive = true
			} else {
				delete(r.processes, p)
			}
		}
		r.mu.Unlock()
		if !alive {
			return
		}
		check := make(chan struct{})
		poll := r.o.Clock.AfterFunc(50*time.Millisecond, func() { close(check) })
		select {
		case <-killed:
			poll.Stop()
			return
		case <-check:
			poll.Stop()
		}
	}
}
func groupAlive(p Process) bool {
	if g, ok := p.(interface{ GroupAlive() bool }); ok {
		return g.GroupAlive()
	}
	return false
}

// observeGroup runs with mu held. Empty observation fences later signals.
func (r *runner) observeGroup(p Process) {
	if r.stopping {
		return
	}
	if r.groupTimers == nil {
		r.groupTimers = map[Process]upstream.Timer{}
	}
	r.groupTimers[p] = r.o.Clock.AfterFunc(50*time.Millisecond, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !groupAlive(p) {
			delete(r.processes, p)
			delete(r.groupTimers, p)
			return
		}
		r.observeGroup(p)
	})
}
