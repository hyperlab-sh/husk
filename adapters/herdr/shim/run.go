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
	"github.com/hyperlab-sh/husk/adapters/herdr/settings"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type clock struct{}

func (clock) AfterFunc(d time.Duration, f func()) upstream.Timer { return time.AfterFunc(d, f) }

// Options supplies one plugin's runtime inputs and deterministic lifecycle seams.
type Options struct {
	Dir, Binary, Socket, ConfigDir, StateDir, RegistryPath, LogPath, Platform string
	Env                                                                       []string
	Settings                                                                  settings.Config
	Client                                                                    *upstream.Client
	Host                                                                      Host
	Clock                                                                     upstream.Clock
	Now                                                                       func() time.Time
	Spawn                                                                     func(Command) (Process, error)
	// SpawnTerminal starts a pane session; nil uses SpawnTerminal.
	SpawnTerminal func(Command, int, int) (TerminalProcess, error)
	// StartServer is the serialized, detached server-start seam; nil uses flock.
	StartServer func(context.Context) error
	// ServerLaunch replaces only detached exec, leaving the start lock and ping
	// sequence intact. Nil starts the resolved binary in a new session.
	ServerLaunch func(binary string, args, env []string) error
}

type state struct {
	Server    string           `json:"server"`
	Socket    string           `json:"socket"`
	Version   string           `json:"version"`
	Hooks     string           `json:"hooks"`
	Startup   []map[string]any `json:"startup"`
	Surfaces  map[string]any   `json:"surfaces"`
	Commands  int              `json:"commands_running"`
	LastError string           `json:"last_error"`
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
	stopping    bool
	workers     sync.WaitGroup
	children    sync.WaitGroup
	proxy       *proxy.Server
	lastDrop    time.Time
	groupTimers map[Process]upstream.Timer
	panes       map[protocol.SurfaceID]*pane
	paneGate    chan struct{}
	paneCounter uint64
	paneTimers  map[Process]upstream.Timer
}

// Run serves Husk requests until stdin EOF or a terminal attach failure. Attach
// and action work never hold up the stdin reader, so EOF always begins shutdown.
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
	r := &runner{panes: map[protocol.SurfaceID]*pane{}, paneGate: make(chan struct{}, 1), o: o, m: m, id: id, ctx: ctx, cancel: cancel, log: log, processes: map[Process]bool{}, state: state{Server: "absent", Socket: o.Socket, Startup: []map[string]any{}, Surfaces: map[string]any{}}}
	p, err := proxy.Start(ctx, proxy.Options{Dial: o.Client.Dial, Clock: o.Clock, Intercept: r.intercept, Warn: func(m string) { r.logLine(map[string]any{"level": "warning", "plugin": r.m.ID, "method": m}) }})
	if err != nil {
		return err
	}
	r.proxy = p
	terminal := make(chan error, 1)
	r.workers.Add(1)
	go func() { defer r.workers.Done(); terminal <- r.attach() }()
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
		case err = <-terminal:
			break loop
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
	r.mu.Unlock()
	r.mu.Lock()
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
	if err != nil {
		r.change(func(s *state) { s.LastError = err.Error() })
		r.logLine(map[string]any{"level": "error", "message": err.Error()})
	}
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
	t := r.o.Clock.AfterFunc(d, cancel)
	return ctx, func() { t.Stop(); cancel() }
}
func (r *runner) wait(d time.Duration) bool {
	ready := make(chan struct{})
	t := r.o.Clock.AfterFunc(d, func() { close(ready) })
	defer t.Stop()
	select {
	case <-ready:
		return true
	case <-r.ctx.Done():
		return false
	}
}
func (r *runner) attach() error {
	var backoff upstream.Backoff
	var startupServer *serverIdentity
	for {
		var identity serverIdentity
		observe := func(pong upstream.Pong) error {
			var err error
			identity, err = identifyServer(r.o.Socket, pong)
			return err
		}
		var session *upstream.Session
		var err error
		if r.o.Settings.Server == "start" {
			session, err = r.o.Client.Attach(r.ctx, r.m.MinVersion, observe)
			if retryable(err) && r.ctx.Err() == nil {
				start := r.o.StartServer
				if start == nil {
					start = func(ctx context.Context) error { return startServer(ctx, r.o) }
				}
				if err = start(r.ctx); err != nil {
					return fmt.Errorf("herdr server did not start: %w", err)
				}
				session, err = r.o.Client.Attach(r.ctx, r.m.MinVersion, observe)
			}
		} else {
			session, err = r.o.Client.Attach(r.ctx, r.m.MinVersion, observe)
		}
		if err != nil {
			var named *upstream.Error
			if errors.As(err, &named) && named.Code == "herdr_too_old" {
				r.change(func(s *state) { s.Server = "too_old" })
			}
			if r.ctx.Err() != nil {
				return nil
			}
			if retryable(err) {
				r.change(func(s *state) { s.Server = "absent" })
				if !r.wait(backoff.Next()) {
					return nil
				}
				continue
			}
			return err
		}
		enabled := false
		var plugins []struct {
			ID      string `json:"plugin_id"`
			Enabled bool   `json:"enabled"`
		}
		if err = json.Unmarshal(session.Plugins, &plugins); err != nil {
			return fmt.Errorf("herdr_protocol: plugin.list: %w", err)
		}
		for _, p := range plugins {
			if p.ID == r.m.ID && p.Enabled {
				enabled = true
			}
		}
		owner := r.o.Settings.HookOwner(r.m.ID, enabled)
		var names []string
		if owner == "shim" {
			for _, e := range r.m.Events {
				if manifest.Supports(e.Platforms, r.o.Platform) {
					names = append(names, e.On)
				}
			}
		}
		allowed, refused := upstream.FilterHookSubscriptions(names)
		for _, f := range refused {
			r.logLine(map[string]any{"code": f.Err.Code, "message": f.Err.Message})
		}
		var stream *upstream.Stream
		if len(allowed) > 0 {
			stream, err = r.o.Client.Subscribe(r.ctx, allowed)
			if err != nil {
				if r.ctx.Err() != nil {
					return nil
				}
				if retryable(err) && !upstream.IsRemote(err) {
					r.change(func(s *state) { s.Server = "lost" })
					if !r.wait(backoff.Next()) {
						return nil
					}
					continue
				}
				var remote *upstream.Error
				if errors.As(err, &remote) && remote.Code == "herdr_protocol" && !upstream.IsRemote(err) {
					return err
				}
				// An explicit subscription refusal suspends event hooks only.
				r.logLine(map[string]any{"message": err.Error()})
			}
		}
		backoff = upstream.Backoff{}
		r.change(func(s *state) { s.Server = "attached"; s.Version = session.Version; s.Hooks = owner; s.LastError = "" })
		if owner == "server" {
			startupServer = nil
		}
		if owner == "shim" && (startupServer == nil || *startupServer != identity) {
			completed := identity
			startupServer = &completed
			for _, e := range r.m.Startup {
				if manifest.Supports(e.Platforms, r.o.Platform) {
					if _, err := r.launch(e, "", "startup", nil, "plugin.startup", "startup", nil); err != nil {
						r.logLine(map[string]any{"event": "startup", "error": err.Error()})
					}
				}
			}
		}
		if stream != nil {
			for {
				event, e := stream.Next()
				if e != nil {
					if r.ctx.Err() != nil {
						return nil
					}
					r.logLine(map[string]any{"message": e.Error()})
					break
				}
				for _, hook := range r.m.Events {
					if manifest.Supports(hook.Platforms, r.o.Platform) && (hook.On == event.Event || strings.Replace(hook.On, ".", "_", 1) == event.Event) {
						raw, _ := json.Marshal(event)
						if _, err := r.launch(hook, "", hook.On, raw, hook.On, "api", nil); err != nil {
							var refusal *protocol.Error
							if !errors.As(err, &refusal) || refusal.Code != protocol.CodeBusy {
								r.logLine(map[string]any{"event": hook.On, "error": err.Error()})
							}
						}
					}
				}
			}
		} else {
			r.probe(identity)
			if r.ctx.Err() != nil {
				return nil
			}
		}
		r.change(func(s *state) { s.Server = "lost" })
		if !r.wait(backoff.Next()) {
			return nil
		}
	}
}
func retryable(err error) bool {
	if err == nil {
		return false
	}
	var e *upstream.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || !errors.As(err, &e) || e.Code == "unavailable"
}

func (r *runner) request(m protocol.Message) (any, *protocol.Error) {
	if m.Method != protocol.MethodActionInvoke {
		return nil, &protocol.Error{Code: protocol.CodeUnknownMethod, Message: "unknown method"}
	}
	var p struct {
		Action string   `json:"action"`
		Args   paneArgs `json:"args"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "invalid action params"}
	}
	local := strings.TrimPrefix(p.Action, r.id+".")
	if strings.HasPrefix(local, "pane-") {
		ctx, done := r.bounded(r.ctx, 10*time.Second)
		defer done()
		return r.openPane(ctx, strings.TrimPrefix(local, "pane-"), p.Args, correlationID(m.ID))
	}
	r.mu.Lock()
	attached := r.state.Server == "attached"
	r.mu.Unlock()
	if !attached {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "herdr server unavailable", Data: json.RawMessage(`{"reason":"herdr_absent"}`)}
	}
	for _, e := range r.m.Actions {
		if strings.ReplaceAll(e.ID, ":", "-") == local && manifest.Supports(e.Platforms, r.o.Platform) {
			result, err := r.launch(e, e.ID, "", nil, correlationID(m.ID), "api", p.Args.Context)
			if err != nil {
				var wire *protocol.Error
				if errors.As(err, &wire) {
					return nil, wire
				}
				return nil, &protocol.Error{Code: protocol.CodeFailed, Message: err.Error()}
			}
			return result, nil
		}
	}
	return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "unknown action"}
}
func (r *runner) launch(e manifest.Entry, action, event string, eventJSON json.RawMessage, correlation, source string, provided json.RawMessage) (map[string]any, error) {
	ctx, done := r.bounded(r.ctx, 2*time.Second)
	defer done()
	c, err := BuildContext(ctx, r.o.Client, correlation, source, provided)
	if err != nil {
		var remote *upstream.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &remote) && remote.Code == "unavailable" {
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: err.Error(), Data: json.RawMessage(`{"reason":"herdr_absent"}`)}
		}
		return nil, err
	}
	env := CommandEnvironment(r.o.Env, r.o.Dir, r.o.Binary, r.proxy.Path(), r.o.ConfigDir, r.o.StateDir, r.m.ID, action, event, eventJSON, c)
	r.mu.Lock()
	if r.stopping || r.ctx.Err() != nil {
		r.mu.Unlock()
		return nil, context.Canceled
	}
	running := 0
	for _, leader := range r.processes {
		if leader {
			running++
		}
	}
	if running >= 32 {
		now := r.o.Now()
		log := r.lastDrop.IsZero() || now.Sub(r.lastDrop) >= time.Second
		if log {
			r.lastDrop = now
		}
		r.mu.Unlock()
		if log {
			r.logLine(map[string]any{"code": "command_limit", "message": "maximum concurrent commands reached"})
		}
		return nil, &protocol.Error{Code: protocol.CodeBusy, Message: "maximum concurrent commands reached"}
	}
	process, err := r.o.Spawn(Command{Argv: e.Command, Env: env, Dir: r.o.Dir})
	if err != nil {
		r.mu.Unlock()
		r.logLine(map[string]any{"command": e.Command, "error": err.Error()})
		return nil, err
	}
	r.processes[process] = true
	r.children.Add(1)
	r.mu.Unlock()
	at := r.o.Now()
	logRecord := map[string]any{"log_id": fmt.Sprintf("command-%d", process.PID()), "plugin_id": r.m.ID, "command": e.Command, "status": "running", "started_unix_ms": at.UnixMilli()}
	if action != "" {
		logRecord["action_id"] = action
	}
	if event != "" {
		logRecord["event"] = event
	}
	r.change(func(s *state) { s.Commands++ })
	go func() {
		defer r.children.Done()
		status, stderr := process.Wait()
		r.mu.Lock()
		if groupAlive(process) {
			r.processes[process] = false
			r.observeGroup(process)
		} else {
			delete(r.processes, process)
		}
		r.mu.Unlock()
		r.logLine(map[string]any{"command": e.Command, "status": status, "stderr": stderr, "at": r.o.Now().UTC().Format(time.RFC3339Nano)})
		r.change(func(s *state) {
			s.Commands--
			if event == "startup" {
				s.Startup = append(s.Startup, map[string]any{"command": e.Command, "status": status, "at": r.o.Now().UTC().Format(time.RFC3339Nano)})
				if len(s.Startup) > 32 {
					s.Startup = s.Startup[len(s.Startup)-32:]
				}
			}
		})
		if action != "" && status != 0 {
			_ = r.o.Host.Notify(r.ctx, protocol.MethodToastShow, map[string]any{"message": fmt.Sprintf("%s: %s exited %d", r.m.Name, e.Title, status), "level": "warn"})
		}
	}()
	return map[string]any{"started": true, "pid": process.PID(), "log": r.o.LogPath, "context": c, "command_log": logRecord, "action_info": map[string]any{"plugin_id": r.m.ID, "action_id": action, "title": e.Title, "command": e.Command}}, nil
}
func (r *runner) stopCommands() {
	r.mu.Lock()
	ps := make([]Process, 0, len(r.processes))
	for p := range r.processes {
		ps = append(ps, p)
	}
	r.mu.Unlock()
	if len(ps) == 0 {
		r.children.Wait()
		return
	}
	r.mu.Lock()
	for _, p := range ps {
		if _, tracked := r.processes[p]; tracked {
			_ = p.Signal(syscall.SIGTERM)
		}
	}
	r.mu.Unlock()
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
	// Leaders can exit while descendants keep their process group alive. Such
	// groups still get the full TERM/KILL policy, even after leader reaping.
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

// observeGroup is called with mu held. Empty observation fences later signals.
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
