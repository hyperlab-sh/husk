package shim

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/upstream"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type paneArgs struct {
	Plugin     string                    `json:"plugin_id"`
	Entrypoint string                    `json:"entrypoint"`
	Placement  string                    `json:"placement"`
	Width      protocol.SurfaceDimension `json:"width"`
	Height     protocol.SurfaceDimension `json:"height"`
	Cwd        string                    `json:"cwd"`
	Env        map[string]string         `json:"env"`
	Context    json.RawMessage           `json:"context"`
}

type pane struct {
	id        protocol.SurfaceID
	placement string
	mu        sync.Mutex
	process   TerminalProcess
	size      protocol.TerminalAttachResult
	closed    bool
	changed   chan struct{}
	input     *paneInput
	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
}

func (p *pane) result() map[string]any { return map[string]any{"type": "ok"} }

func wireFailure(err error) *protocol.Error {
	if err == nil {
		return nil
	}
	var wire *protocol.Error
	if errors.As(err, &wire) {
		return wire
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &protocol.Error{Code: protocol.CodeTimeout, Message: "pane request cancelled"}
	}
	return &protocol.Error{Code: protocol.CodeFailed, Message: err.Error()}
}

func (r *runner) openPane(ctx context.Context, entry string, args paneArgs, correlation string) (any, *protocol.Error) {
	// Serialize surface admission, but let cancellation pass the gate.
	select {
	case r.paneGate <- struct{}{}:
	case <-ctx.Done():
		return nil, wireFailure(ctx.Err())
	}
	defer func() { <-r.paneGate }()
	var e *manifest.Entry
	for i := range r.m.Panes {
		candidate := &r.m.Panes[i]
		if strings.ReplaceAll(candidate.ID, ":", "-") == entry && manifest.Supports(candidate.Platforms, r.o.Platform) {
			e = candidate
			break
		}
	}
	if e == nil {
		return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "unknown pane entrypoint"}
	}
	if failure := r.checkServer(ctx); failure != nil {
		return nil, failure
	}
	placement := args.Placement
	if placement == "" {
		placement = e.Placement
	}
	if placement == "" {
		placement = "overlay"
	}
	target := protocol.SurfacePlacementCanvas
	switch placement {
	case "popup":
		target = protocol.SurfacePlacementModal
	case "overlay", "zoomed", "tab", "split":
	default:
		return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "invalid pane placement"}
	}
	width, height := args.Width, args.Height
	if len(width) == 0 && e.Width != nil {
		width, _ = json.Marshal(e.Width)
	}
	if len(height) == 0 && e.Height != nil {
		height, _ = json.Marshal(e.Height)
	}
	r.paneCounter++
	// Normalize the entrypoint prefix to the runtime alphabet and leave room
	// for the full base-36 lifetime counter within the 32-byte name limit.
	prefix := strings.ToLower(strings.ReplaceAll(e.ID, ":", "-"))
	if len(prefix) > 17 {
		prefix = prefix[:17]
	}
	name := prefix + "-" + strconv.FormatUint(r.paneCounter, 36)
	title := []rune(e.Title)
	if len(title) > 80 {
		title = title[:80]
	}
	params := protocol.SurfaceOpenParams{Placement: target, Name: name, Title: string(title), Width: width, Height: height, Frame: &protocol.FrameSpec{Border: true, Label: e.Title, Close: true}}
	raw, err := r.o.Host.Call(ctx, protocol.MethodSurfaceOpen, params)
	if err != nil {
		return nil, wireFailure(err)
	}
	var opened protocol.SurfaceOpenResult
	if err = json.Unmarshal(raw, &opened); err != nil || !opened.Surface.Valid() {
		return nil, &protocol.Error{Code: protocol.CodeFailed, Message: "invalid surface.open reply"}
	}
	pctx, cancel := context.WithCancel(r.ctx)
	p := &pane{id: opened.Surface, placement: placement, changed: make(chan struct{}, 1), input: newPaneInput(), ctx: pctx, cancel: cancel}
	r.mu.Lock()
	if r.stopping || r.ctx.Err() != nil {
		r.mu.Unlock()
		r.closePane(p)
		return nil, wireFailure(context.Canceled)
	}
	r.panes[p.id] = p
	r.mu.Unlock()
	success := false
	defer func() {
		if !success {
			r.closePane(p)
		}
	}()
	var size protocol.TerminalAttachResult
	for {
		raw, err = r.o.Host.Call(ctx, protocol.MethodTerminalAttach, protocol.TerminalAttachParams{Surface: p.id, Capture: protocol.CaptureValueAll.Ptr()})
		if err == nil {
			err = json.Unmarshal(raw, &size)
			break
		}
		var remote *protocol.Error
		if !errors.As(err, &remote) || remote.Code != protocol.CodeUnavailable {
			return nil, wireFailure(err)
		}
		select {
		case <-p.changed:
		case <-p.ctx.Done():
			return nil, wireFailure(p.ctx.Err())
		case <-ctx.Done():
			return nil, wireFailure(ctx.Err())
		}
	}
	if err != nil || size.Width < 1 || size.Height < 1 {
		return nil, &protocol.Error{Code: protocol.CodeFailed, Message: "invalid terminal.attach size"}
	}
	p.mu.Lock()
	p.size = size
	p.mu.Unlock()
	contextCtx, done := r.bounded(ctx, 2*time.Second)
	c, err := BuildContext(contextCtx, r.o.Client, correlation, "api", args.Context)
	done()
	if err != nil {
		return nil, wireFailure(err)
	}
	base := append([]string(nil), r.o.Env...)
	for k, v := range args.Env {
		base = append(base, k+"="+v)
	}
	env := CommandEnvironment(base, r.o.Dir, r.o.Binary, r.proxy.Path(), r.o.ConfigDir, r.o.StateDir, r.m.ID, "", c)
	values := envMap(env)
	delete(values, "HERDR_PANE_ID")
	values["HERDR_PLUGIN_ENTRYPOINT_ID"] = e.ID
	values["TERM"] = "xterm-256color"
	values["COLORTERM"] = "truecolor"
	values["TERM_PROGRAM"] = "husk"
	values["TERM_PROGRAM_VERSION"] = ""
	if pong, err := r.o.Host.Call(ctx, protocol.MethodSessionPing, map[string]any{}); err == nil {
		var v struct {
			Version string `json:"host_version"`
		}
		_ = json.Unmarshal(pong, &v)
		values["TERM_PROGRAM_VERSION"] = v.Version
	}
	env = nil
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	dir := args.Cwd
	if dir == "" {
		dir = r.o.Dir
	}
	p.mu.Lock()
	if p.closed || ctx.Err() != nil {
		p.mu.Unlock()
		return nil, wireFailure(context.Canceled)
	}
	r.mu.Lock()
	if r.stopping || r.commands >= 32 {
		r.mu.Unlock()
		p.mu.Unlock()
		return nil, &protocol.Error{Code: protocol.CodeBusy, Message: "maximum concurrent commands reached", Data: json.RawMessage(`{"reason":"too_many_commands"}`)}
	}
	process, err := r.o.SpawnTerminal(Command{Argv: e.Command, Env: env, Dir: dir}, p.size.Width, p.size.Height)
	if err != nil {
		r.mu.Unlock()
		p.mu.Unlock()
		return nil, wireFailure(err)
	}
	p.process = process
	r.commands++
	r.processes[process] = true
	r.children.Add(1)
	r.mu.Unlock()
	p.mu.Unlock()
	r.change(func(s *state) {
		s.Commands++
		s.Surfaces[string(p.id)] = map[string]any{"entrypoint": e.ID, "placement": target, "pid": process.PID()}
	})
	go r.servePane(p, e.Command)
	success = true
	return p.result(), nil
}

func (r *runner) closePane(p *pane) { r.closePaneFrom(p, false) }

// hostClosed suppresses redundant release notifications for a lifetime the host
// has already removed. All callers run off the stdin notification loop.
func (r *runner) closePaneFrom(p *pane, hostClosed bool) {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		process := p.process
		p.cancel()
		if process != nil {
			_ = process.Close()
		}
		p.mu.Unlock()
		r.mu.Lock()
		delete(r.panes, p.id)
		r.mu.Unlock()
		if process != nil {
			_ = process.Signal(syscall.SIGHUP)
			_ = process.Signal(syscall.SIGTERM)
			// Keep descendants tracked even after the session leader exits. The
			// existing command shutdown path joins these groups at shim EOF.
			r.mu.Lock()
			if r.paneTimers == nil {
				r.paneTimers = map[Process]upstream.Timer{}
			}
			r.paneTimers[process] = r.o.Clock.AfterFunc(2*time.Second, func() {
				r.mu.Lock()
				defer r.mu.Unlock()
				if _, tracked := r.processes[process]; tracked {
					_ = process.Signal(syscall.SIGKILL)
				}
				delete(r.paneTimers, process)
			})
			r.mu.Unlock()
		}
		if !hostClosed {
			// EOF cancels normal requests, but cleanup still gets a bounded chance
			// to release its surface. Signals and the kill timer are already armed.
			ctx, done := r.bounded(context.WithoutCancel(r.ctx), 2*time.Second)
			_ = r.o.Host.Notify(ctx, protocol.MethodSurfaceClose, protocol.SurfaceCloseParams{Surface: p.id})
			done()
		}
		r.change(func(s *state) { delete(s.Surfaces, string(p.id)) })
	})
}

func (r *runner) servePane(p *pane, argv []string) {
	defer r.children.Done()
	p.mu.Lock()
	process := p.process
	p.mu.Unlock()
	outputDone := make(chan struct{})
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		for {
			b, ok := p.input.next(p.ctx)
			if !ok {
				return
			}
			if _, err := process.Write(b); err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(outputDone)
		host := r.o.Host
		var closer io.Closer
		if sessions, ok := host.(interface {
			TerminalSession(context.Context) (Host, io.Closer, error)
		}); ok {
			var err error
			host, closer, err = sessions.TerminalSession(p.ctx)
			if err != nil {
				r.closePane(p)
				return
			}
			defer closer.Close()
		}
		_ = process.Pump(p.id, func(params protocol.TerminalWriteParams) error {
			ctx, cancel := r.bounded(p.ctx, 10*time.Second)
			_, failure := host.Call(ctx, protocol.MethodTerminalWrite, params)
			cancel()
			if failure != nil {
				r.closePane(p)
			}
			return failure
		})
	}()
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
	r.change(func(s *state) { s.Commands-- })
	// Let already-written terminal bytes reach ingestion before closing the
	// surface. A descendant holding the slave cannot keep this join forever.
	drained := make(chan struct{})
	timer := r.o.Clock.AfterFunc(2*time.Second, func() { close(drained) })
	select {
	case <-outputDone:
	case <-drained:
	case <-p.ctx.Done():
	}
	timer.Stop()
	r.closePane(p)
	<-outputDone
	<-inputDone
	r.logLine(map[string]any{"command": argv, "status": status, "stderr": stderr, "at": r.o.Now().UTC().Format(time.RFC3339Nano)})
}

func (r *runner) paneNotification(m protocol.Message) {
	switch m.Method {
	case protocol.MethodTerminalInput:
		var p protocol.TerminalInputParams
		if json.Unmarshal(m.Params, &p) != nil {
			return
		}
		b, err := (protocol.TerminalWriteParams{Surface: p.Surface, Data: p.Data}).Decode()
		if err != nil {
			return
		}
		r.mu.Lock()
		pane := r.panes[p.Surface]
		r.mu.Unlock()
		if pane == nil {
			return
		}
		if pane.ctx.Err() == nil && pane.input.append(b, r.o.Now()) {
			r.workers.Add(1)
			go func() {
				defer r.workers.Done()
				r.logLine(map[string]any{"level": "warning", "code": "terminal_input_overflow", "surface": pane.id, "message": "terminal input queue full; newest bytes dropped"})
			}()
		}
	case protocol.MethodSurfaceResized:
		var batch protocol.SurfaceResizedParams
		if json.Unmarshal(m.Params, &batch) != nil {
			return
		}
		for _, size := range batch.Surfaces {
			r.mu.Lock()
			p := r.panes[size.Surface]
			r.mu.Unlock()
			if p == nil {
				continue
			}
			// An initial not-yet-laid-out surface and a final close can both
			// report zero geometry. Confirm the lifetime in State, off the stdin
			// reader, rather than treating temporary invisibility as a close.
			if !size.Visible && size.Width == 0 && size.Height == 0 {
				r.workers.Add(1)
				go func() {
					defer r.workers.Done()
					r.checkPaneLifetime(p)
				}()
				continue
			}
			p.mu.Lock()
			if size.Width > 0 && size.Height > 0 {
				p.size = protocol.TerminalAttachResult{Width: size.Width, Height: size.Height}
				if p.process != nil {
					_ = p.process.Resize(size.Width, size.Height)
				}
			}
			p.mu.Unlock()
			if size.Width > 0 && size.Height > 0 {
				select {
				case p.changed <- struct{}{}:
				default:
				}
			}
		}
	}
}
