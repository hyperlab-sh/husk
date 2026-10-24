package fakeherdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Request records both parsed fields and the exact original line (including
// its newline) so proxy tests can assert byte-for-byte forwarding.
type Request struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
	Raw    []byte         `json:"-"`
}

// Options configures immutable fixtures. Gates delays each named method until
// its channel closes; PingGate is a shorthand when Gates has no ping entry.
// OnRequest runs outside locks and can signal deterministic test barriers.
// Passthrough explicitly allows non-schema methods only; schema-known methods
// always validate. Fixture maps are copied by Start. Omitted records are
// generated from schema.
type Options struct {
	Version              string
	Plugins              []map[string]any
	Pane, Workspace, Tab map[string]any
	PingGate             <-chan struct{}
	Gates                map[string]<-chan struct{}
	Passthrough          []string
	OnRequest            func(Request)
	// Reply may modify a generated result or supply a remote refusal. It runs
	// outside locks; tests use synchronized state to model live server changes.
	Reply func(Request, map[string]any) *ReplyError
	// DisconnectFirst closes the first N validated requests for each named
	// method before replying. Errors injects explicit remote error replies.
	DisconnectFirst map[string]int
	Errors          map[string]ReplyError
}

// ReplyError is an injected upstream error body.
type ReplyError struct {
	Code, Message string
}

// Server owns a private test socket, its connections, and all serving workers.
type Server struct {
	listener      net.Listener
	ctx           context.Context
	cancel        context.CancelFunc
	opts          Options
	catalog       catalog
	requestSchema *jsonschema.Schema
	knownMethods  map[string]bool
	passthrough   map[string]bool
	disconnect    map[string]int
	mu            sync.Mutex
	peers         map[*peer]bool
	records       []Request
	wg            sync.WaitGroup
	once          sync.Once
}

type peer struct {
	conn  net.Conn
	mu    sync.Mutex
	names map[string]bool // protected by Server.mu
	id    string
}

func (p *peer) write(v any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return json.NewEncoder(p.conn).Encode(v)
}

// Start binds path in a caller-owned private temporary directory. An absent
// server is simulated by delaying Start, not by touching any live socket.
func Start(path string, opts Options) (*Server, error) {
	catalog := loadCatalog()
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://adapter.test/schema.json", catalog.root); err != nil {
		return nil, err
	}
	requestSchema, err := compiler.Compile("https://adapter.test/schema.json#/schemas/request")
	if err != nil {
		return nil, fmt.Errorf("compile request schema: %w", err)
	}
	knownMethods := map[string]bool{}
	for _, branch := range catalog.ref("#/schemas/request")["oneOf"].([]any) {
		properties := branch.(map[string]any)["properties"].(map[string]any)
		knownMethods[properties["method"].(map[string]any)["const"].(string)] = true
	}
	passthrough := map[string]bool{}
	for _, method := range opts.Passthrough {
		passthrough[method] = true
	}
	disconnect := map[string]int{}
	for method, count := range opts.DisconnectFirst {
		disconnect[method] = count
	}
	errors := map[string]ReplyError{}
	for method, err := range opts.Errors {
		errors[method] = err
	}
	opts.Errors = errors
	gates := map[string]<-chan struct{}{}
	for method, gate := range opts.Gates {
		gates[method] = gate
	}
	opts.Gates = gates
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	// Detach all maps, including nested records, from the caller.
	raw, err := json.Marshal(struct {
		Plugins              []map[string]any
		Pane, Workspace, Tab map[string]any
	}{opts.Plugins, opts.Pane, opts.Workspace, opts.Tab})
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	var fixtures struct {
		Plugins              []map[string]any
		Pane, Workspace, Tab map[string]any
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		_ = listener.Close()
		return nil, err
	}
	opts.Plugins, opts.Pane, opts.Workspace, opts.Tab = fixtures.Plugins, fixtures.Pane, fixtures.Workspace, fixtures.Tab
	if opts.Version == "" {
		opts.Version = "0.9.5"
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{listener: listener, ctx: ctx, cancel: cancel, opts: opts, catalog: catalog, requestSchema: requestSchema, knownMethods: knownMethods, passthrough: passthrough, disconnect: disconnect, peers: map[*peer]bool{}}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Close stops accepting, closes even stalled pings/subscriptions, and joins all
// serving goroutines. It is safe to call repeatedly or concurrently.
func (s *Server) Close() {
	s.once.Do(func() {
		s.cancel()
		_ = s.listener.Close()
		s.mu.Lock()
		for p := range s.peers {
			_ = p.conn.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		p := &peer{conn: conn}
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.peers[p] = true
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(p)
	}
}

func (s *Server) serve(p *peer) {
	defer s.wg.Done()
	defer func() { _ = p.conn.Close(); s.mu.Lock(); delete(s.peers, p); s.mu.Unlock() }()
	r := bufio.NewReader(p.conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var document any
		if json.Unmarshal(line, &document) != nil {
			return
		}
		var req Request
		_ = json.Unmarshal(line, &req) // schema validation below handles mistyped fields.
		req.Raw = append([]byte(nil), line...)
		s.mu.Lock()
		s.records = append(s.records, cloneRequest(req))
		s.mu.Unlock()
		if s.opts.OnRequest != nil {
			s.opts.OnRequest(cloneRequest(req))
		}
		if s.knownMethods[req.Method] || !s.passthrough[req.Method] {
			if err := s.requestSchema.Validate(document); err != nil {
				if err = p.write(map[string]any{"id": req.ID, "error": map[string]string{"code": "invalid_params", "message": err.Error()}}); err != nil {
					return
				}
				continue
			}
		}
		s.mu.Lock()
		drop := s.disconnect[req.Method] > 0
		if drop {
			s.disconnect[req.Method]--
		}
		s.mu.Unlock()
		if drop {
			return
		}
		if failure, ok := s.opts.Errors[req.Method]; ok {
			if err := p.write(map[string]any{"id": req.ID, "error": map[string]string{"code": failure.Code, "message": failure.Message}}); err != nil {
				return
			}
			continue
		}
		gate := s.opts.Gates[req.Method]
		if gate == nil && req.Method == "ping" {
			gate = s.opts.PingGate
		}
		if gate != nil {
			select {
			case <-gate:
			case <-s.ctx.Done():
				return
			}
		}
		result := s.respond(req)
		if s.opts.Reply != nil {
			if failure := s.opts.Reply(cloneRequest(req), result); failure != nil {
				if err := p.write(map[string]any{"id": req.ID, "error": map[string]string{"code": failure.Code, "message": failure.Message}}); err != nil {
					return
				}
				continue
			}
		}
		if req.Method == "events.subscribe" {
			// Serialize registration and ack with pushes, so Subscribe returning is a
			// reliable barrier: no injected event can overtake its acknowledgement.
			p.mu.Lock()
			names := map[string]bool{}
			if entries, ok := req.Params["subscriptions"].([]any); ok {
				for _, entry := range entries {
					if v, ok := entry.(map[string]any); ok {
						name, _ := v["type"].(string)
						names[name] = true
					}
				}
			}
			s.mu.Lock()
			p.names = names
			p.id = req.ID
			s.mu.Unlock()
			err = json.NewEncoder(p.conn).Encode(map[string]any{"id": req.ID, "result": result})
			p.mu.Unlock()
		} else {
			err = p.write(map[string]any{"id": req.ID, "result": result})
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) record(name string, fixture map[string]any) map[string]any {
	if fixture != nil {
		return fixture
	}
	result := s.catalog.sample(s.catalog.ref("#/schemas/success_response/$defs/" + name)).(map[string]any)
	switch name {
	case "PaneInfo":
		result["pane_id"] = "pane-1"
		result["terminal_id"] = "terminal-1"
		result["workspace_id"] = "workspace-1"
		result["tab_id"] = "tab-1"
		result["focused"] = true
	case "WorkspaceInfo":
		result["workspace_id"] = "workspace-1"
		result["active_tab_id"] = "tab-1"
	case "TabInfo":
		result["tab_id"] = "tab-1"
		result["workspace_id"] = "workspace-1"
	}
	return result
}

func (s *Server) respond(req Request) map[string]any {
	switch req.Method {
	case "ping":
		r := s.catalog.result("pong")
		r["version"] = s.opts.Version
		r["protocol"] = s.catalog.root["protocol"]
		return r
	case "plugin.list":
		r := s.catalog.result("plugin_list")
		if s.opts.Plugins != nil {
			r["plugins"] = s.opts.Plugins
		}
		return r
	case "pane.current":
		r := s.catalog.result("pane_current")
		r["pane"] = s.record("PaneInfo", s.opts.Pane)
		return r
	case "workspace.get":
		r := s.catalog.result("workspace_info")
		r["workspace"] = s.record("WorkspaceInfo", s.opts.Workspace)
		return r
	case "tab.get":
		r := s.catalog.result("tab_info")
		r["tab"] = s.record("TabInfo", s.opts.Tab)
		return r
	case "events.subscribe":
		return s.catalog.result("subscription_started")
	case "notification.show":
		r := s.catalog.result("notification_show")
		r["shown"] = true
		r["reason"] = "shown"
		return r
	case "plugin.pane.open":
		if req.Params["placement"] == "popup" {
			return s.catalog.result("ok")
		}
		r := s.catalog.result("plugin_pane_opened")
		r["plugin_pane"] = map[string]any{"plugin_id": req.Params["plugin_id"], "entrypoint": req.Params["entrypoint"], "pane": s.record("PaneInfo", s.opts.Pane)}
		return r
	default:
		return s.catalog.result("ok") // popup.close and recorded passthrough
	}
}

func cloneRequest(req Request) Request {
	req.Raw = append([]byte(nil), req.Raw...)
	if req.Params != nil {
		raw, _ := json.Marshal(req.Params)
		req.Params = nil
		_ = json.Unmarshal(raw, &req.Params)
	}
	return req
}

// Requests returns detached snapshots of all requests in arrival order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Request, len(s.records))
	for i, r := range s.records {
		result[i] = cloneRequest(r)
	}
	return result
}

func (s *Server) subscribers() []*peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	var peers []*peer
	for p := range s.peers {
		if p.names != nil {
			peers = append(peers, p)
		}
	}
	return peers
}

// Emit synchronously injects an event into matching subscriptions. Lifecycle
// events use underscore envelope names and dotted subscription names; filtered
// subscription events already carry dotted names. The payload stays opaque.
// Callers must keep clients reading: injection is synchronous, and Close can
// interrupt a blocked write. Every matching peer is attempted; errors are joined.
func (s *Server) Emit(event string, data any) error {
	var failures []error
	for _, p := range s.subscribers() {
		s.mu.Lock()
		matches := p.names[event] || p.names[strings.Replace(event, "_", ".", 1)]
		s.mu.Unlock()
		if matches {
			if err := p.write(map[string]any{"event": event, "data": data}); err != nil {
				failures = append(failures, fmt.Errorf("emit: %w", err))
			}
		}
	}
	return errors.Join(failures...)
}

// DropSubscriptions simulates upstream EOF, without stopping the listener.
func (s *Server) DropSubscriptions() {
	for _, p := range s.subscribers() {
		_ = p.conn.Close()
	}
}

// FailSubscriptions injects schema-defined error replies when reply is true.
// When false, it injects a defensive id-less error-body fixture, which is not
// defined by the upstream schema. Both forms terminate a client's stream.
// As with Emit, callers must keep clients reading or interrupt writes with Close.
func (s *Server) FailSubscriptions(code, message string, reply bool) error {
	for _, p := range s.subscribers() {
		r := map[string]any{"error": map[string]string{"code": code, "message": message}}
		if reply {
			s.mu.Lock()
			r["id"] = p.id
			s.mu.Unlock()
		}
		if err := p.write(r); err != nil {
			return err
		}
	}
	return nil
}
