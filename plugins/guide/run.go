package guide

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Run is the environment-driven plugin main body. Husk supplies stdin/stdout,
// HUSK_SOCKET_PATH and HUSK_PLUGIN_TOKEN; diagnostics go only to stderr.
// It returns when stdin closes, reconnecting a dropped watch until then.
func Run() error {
	path, token := os.Getenv("HUSK_SOCKET_PATH"), os.Getenv("HUSK_PLUGIN_TOKEN")
	if path == "" || token == "" {
		return errors.New("HUSK_SOCKET_PATH and HUSK_PLUGIN_TOKEN must be set by the host")
	}
	return run(context.Background(), config{
		token: token,
		dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
		retry: func(ctx context.Context) error {
			timer := time.NewTimer(100 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}, os.Stdin, os.Stdout, os.Stderr)
}

type config struct {
	token string
	dial  func(context.Context) (io.ReadWriteCloser, error)
	retry func(context.Context) error
}
type input struct {
	host       bool
	generation uint64
	messages   []protocol.Message
	conn       io.ReadWriteCloser
	ended      bool
	recovered  bool
	err        error
}
type pendingKind int

const (
	watchReply pendingKind = iota + 1
	keysReply
	stateReply
)

type pending struct {
	kind    pendingKind
	version uint64
}
type plugin struct {
	cfg                               config
	stdout, socket                    *protocol.Writer
	log                               io.Writer
	layout                            layout
	palette                           protocol.Palette
	keys                              keyListing
	baseline, shown, dirty, keysReady bool
	toggle                            *protocol.Message
	loggedRefusal                     string
	nextID                            int64
	keyVersion                        uint64
	pending                           map[string]pending
}

// pump groups complete messages already buffered by one stream read. A partial
// next line does not delay the complete notifications from the current read.
func pump(ctx context.Context, r io.Reader, host bool, generation uint64, send func(input)) {
	// Keep this size >= protocol.ReadLoop's 64 KiB read buffer: bufio must
	// reuse br rather than wrap it, so Buffered/Peek see the remaining lines.
	br := bufio.NewReaderSize(r, 64*1024)
	var batch []protocol.Message
	flush := func() {
		if len(batch) > 0 {
			send(input{host: host, generation: generation, messages: batch})
			batch = nil
		}
	}
	err := protocol.ReadLoop(br, func(m protocol.Message) {
		batch = append(batch, m)
		rest, _ := br.Peek(br.Buffered())
		if !bytes.ContainsRune(rest, '\n') {
			flush()
		}
	}, func(e *protocol.LineError) {
		// Keep framing errors on the loop so diagnostics never race each other.
		send(input{host: host, generation: generation, err: e})
	})
	flush()
	if ctx.Err() == nil {
		send(input{host: host, generation: generation, ended: true, err: err})
	}
}

func run(parent context.Context, cfg config, stdin io.ReadCloser, stdout, log io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	events := make(chan input)
	var workers sync.WaitGroup
	send := func(i input) {
		select {
		case events <- i:
		case <-ctx.Done():
			if i.conn != nil {
				_ = i.conn.Close()
			}
		}
	}
	start := func(f func()) { workers.Add(1); go func() { defer workers.Done(); f() }() }
	start(func() { pump(ctx, stdin, true, 0, send) })
	var conn io.ReadWriteCloser
	defer func() {
		cancel()
		_ = stdin.Close()
		if conn != nil {
			_ = conn.Close()
		}
		workers.Wait()
	}()
	generation := uint64(0)
	p := &plugin{cfg: cfg, stdout: protocol.NewWriter(stdout), log: log, pending: make(map[string]pending)}
	connect := func() {
		generation++
		gen := generation
		start(func() {
			reported := false
			for ctx.Err() == nil {
				c, err := cfg.dial(ctx)
				if err == nil {
					send(input{generation: gen, conn: c, recovered: reported})
					return
				}
				if !reported {
					send(input{generation: gen, err: fmt.Errorf("connect: %w", err)})
					reported = true
				}
				if cfg.retry(ctx) != nil {
					return
				}
			}
		})
	}
	disconnect := func() {
		if conn != nil {
			_ = conn.Close()
			conn = nil
		}
		p.socket = nil
		p.baseline = false
		p.layout = layout{}
		p.pending = make(map[string]pending)
	}
	connect()
	for {
		select {
		case <-ctx.Done():
			return parent.Err()
		case event := <-events:
			if !event.host && event.generation != generation {
				if event.conn != nil {
					_ = event.conn.Close()
				}
				continue
			}
			if event.conn != nil {
				if event.recovered {
					fmt.Fprintln(log, "guide: connection recovered")
				}
				conn = event.conn
				p.socket = protocol.NewWriter(conn)
				gen := generation
				reader := conn
				start(func() { pump(ctx, reader, false, gen, send) })
				if err := p.call(watchReply, protocol.MethodStateWatch, map[string]any{"slices": []string{"husk.geometry", "husk.surfaces", "husk.focus", "husk.pods"}}); err != nil {
					disconnect()
					connect()
					continue
				}
				if p.toggle != nil || p.shown || !p.keysReady {
					if err := p.requestKeys(); err != nil {
						disconnect()
						connect()
					}
				}
				continue
			}
			if event.ended {
				if event.host {
					return event.err
				}
				fmt.Fprintln(log, "guide: watch disconnected; reconnecting")
				disconnect()
				connect()
				continue
			}
			if event.err != nil {
				fmt.Fprintln(log, "guide:", event.err)
				continue
			}
			for _, m := range event.messages {
				var err error
				if event.host {
					err = p.fromHost(m)
				} else {
					err = p.fromSocket(m)
				}
				if err != nil {
					if event.host && p.socket == nil {
						return err
					}
					fmt.Fprintln(log, "guide: transport:", err)
					disconnect()
					connect()
					break
				}
			}
			if p.socket != nil {
				if err := p.flush(); err != nil {
					fmt.Fprintln(log, "guide: transport:", err)
					disconnect()
					connect()
				}
			}
		}
	}
}

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func (p *plugin) call(kind pendingKind, method string, params any) error {
	p.nextID++
	id, err := protocol.NewIntID(p.nextID)
	if err != nil {
		return err
	}
	p.pending[id.String()] = pending{kind, p.keyVersion}
	return p.socket.Send(protocol.Message{ID: &id, Method: method, Params: raw(params), Token: p.cfg.token})
}
func (p *plugin) update(lines []string) error {
	return p.socket.Send(protocol.Message{Method: protocol.MethodSurfaceUpdate, Params: raw(protocol.SurfaceUpdateParams{Surface: protocol.SurfaceAppOverlay, Lines: lines}), Token: p.cfg.token})
}
func (p *plugin) state() error {
	return p.call(stateReply, protocol.MethodStateSet, map[string]any{"slice": "guide.state", "value": map[string]bool{"shown": p.shown}})
}
func (p *plugin) reply(m protocol.Message, result any, failure *protocol.Error) error {
	reply := protocol.Message{ID: m.ID, Error: failure}
	if failure == nil {
		reply.Result = raw(result)
	}
	return p.stdout.Send(reply)
}
func (p *plugin) requestKeys() error {
	p.keyVersion++
	p.keysReady = false
	return p.call(keysReply, protocol.MethodKeysList, struct{}{})
}
func (p *plugin) fromHost(m protocol.Message) error {
	if m.ID != nil && m.Method != "" {
		if m.Method != protocol.MethodActionInvoke {
			return p.reply(m, nil, &protocol.Error{Code: protocol.CodeUnknownMethod, Message: "unknown method"})
		}
		var a struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(m.Params, &a) != nil {
			return p.reply(m, nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "action.invoke requires an action"})
		}
		if a.Action != toggleAction {
			return p.reply(m, nil, &protocol.Error{Code: protocol.CodeNotFound, Message: "unknown action"})
		}
		if p.toggle != nil {
			return p.reply(m, nil, &protocol.Error{Code: protocol.CodeBusy, Message: "toggle already in progress"})
		}
		if p.socket == nil {
			return p.reply(m, nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "watch reconnecting"})
		}
		p.shown = !p.shown
		if !p.shown {
			p.dirty = false
			if err := p.update([]string{}); err != nil {
				return err
			}
			if err := p.state(); err != nil {
				return err
			}
			return p.reply(m, map[string]bool{"shown": false}, nil)
		}
		p.toggle = &m
		p.dirty = true
		return p.requestKeys()
	}
	switch m.Method {
	case protocol.MethodThemeChanged:
		var theme protocol.ThemeChangedParams
		if err := json.Unmarshal(m.Params, &theme); err != nil {
			fmt.Fprintln(p.log, "guide: theme:", err)
			return nil
		}
		p.palette = theme.Palette
		p.dirty = true
	case protocol.MethodStateChanged, protocol.MethodEventDeliver:
		// The stdin mirror is not a baseline and is never used for drawing.
		// EOF drives reconnect; a mirror received while disconnected cannot make
		// a stale snapshot valid. The connection worker is already retrying.
	}
	return nil
}
func (p *plugin) fromSocket(m protocol.Message) error {
	if m.Method == protocol.MethodStateChanged {
		if !p.baseline {
			return nil
		}
		var v sliceValue
		if err := json.Unmarshal(m.Params, &v); err != nil {
			return nil
		}
		if err := p.layout.apply(v); err != nil {
			fmt.Fprintln(p.log, "guide: state:", err)
			return nil
		}
		p.dirty = true
		if v.Slice == "husk.pods" {
			return p.requestKeys()
		}
		return nil
	}
	if m.ID == nil || m.Method != "" {
		return nil
	}
	request, ok := p.pending[m.ID.String()]
	if !ok {
		return nil
	}
	delete(p.pending, m.ID.String())
	switch request.kind {
	case watchReply:
		if m.Error != nil {
			return fmt.Errorf("state.watch: %s", m.Error.Message)
		}
		var baseline struct {
			Slices []sliceValue `json:"slices"`
		}
		if err := json.Unmarshal(m.Result, &baseline); err != nil {
			return err
		}
		next := layout{}
		for _, v := range baseline.Slices {
			if err := next.apply(v); err != nil {
				return err
			}
		}
		p.layout = next
		p.baseline = true
		p.dirty = true
	case keysReply:
		if request.version != p.keyVersion {
			return nil
		}
		keys := keyListing{}
		if m.Error != nil || json.Unmarshal(m.Result, &keys) != nil {
			keys = keyListing{Failed: true}
			fmt.Fprintln(p.log, "guide: keys.list failed; showing ?")
		}
		p.keys = keys
		p.keysReady = true
		p.dirty = true
	case stateReply:
		if m.Error != nil {
			fmt.Fprintln(p.log, "guide: state.set:", m.Error.Message)
		}
	}
	return nil
}
func (p *plugin) flush() error {
	if !p.baseline || !p.shown || !p.keysReady {
		return nil
	}
	if p.dirty && !p.layout.Geometry.TooSmall && p.layout.Geometry.Terminal.Width > 4 && p.layout.Geometry.Terminal.Height > 2 {
		if err := p.update(p.layout.draw(p.keys, p.palette).lines()); err != nil {
			return err
		}
		p.dirty = false
	}
	if p.toggle == nil {
		return nil
	}
	req := *p.toggle
	p.toggle = nil
	// Claims come from the manifest, not surface.update. Read their resolved
	// occupant from the watched State after attempting the first drawing.
	owner := p.layout.Surfaces.Occupants[protocol.SurfaceAppOverlay].Plugin
	var failure *protocol.Error
	if owner != "guide" {
		message := "app-overlay is occupied by " + owner
		if owner == "" {
			message = "app-overlay is not claimed by guide"
		}
		if message != p.loggedRefusal {
			fmt.Fprintln(p.log, "guide:", message)
			p.loggedRefusal = message
		}
		p.shown = false
		failure = &protocol.Error{Code: protocol.CodeFailed, Message: message}
	} else {
		p.loggedRefusal = ""
	}
	if err := p.state(); err != nil {
		return err
	}
	return p.reply(req, map[string]bool{"shown": p.shown}, failure)
}
