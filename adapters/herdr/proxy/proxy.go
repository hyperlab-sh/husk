// Package proxy provides a private, byte-preserving upstream socket with a closed interception seam.
package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// Request is the portion of a request needed by the interception policy.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Error is the upstream schema's error body, never a Husk envelope.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Options injects the upstream connection and the adapter's interception policy.
type Options struct {
	Dial      func(context.Context) (net.Conn, error)
	Intercept func(context.Context, Request) (result any, failure *Error, handled bool)
	Warn      func(method string)
}

// Server owns its private directory, socket and workers.
type Server struct {
	path, dir string
	listener  net.Listener
	ctx       context.Context
	cancel    context.CancelFunc
	opts      Options
	mu        sync.Mutex
	peers     map[net.Conn]bool
	wg        sync.WaitGroup
	once      sync.Once
}

// Start binds a 0600 socket in a fresh 0700 temporary directory.
func Start(ctx context.Context, opts Options) (*Server, error) {
	dir, err := os.MkdirTemp("", "husk-proxy-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "api.sock")
	l, err := net.Listen("unix", path)
	if err == nil {
		err = os.Chmod(path, 0600)
	}
	if err != nil {
		if l != nil {
			_ = l.Close()
		}
		_ = os.RemoveAll(dir)
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{path: path, dir: dir, listener: l, ctx: ctx, cancel: cancel, opts: opts, peers: map[net.Conn]bool{}}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Path is the socket passed to commands as HERDR_SOCKET_PATH.
func (s *Server) Path() string { return s.path }

// Close stops accepting, cancels interceptors and joins all connections before removing the directory.
func (s *Server) Close() {
	s.once.Do(func() {
		s.cancel()
		_ = s.listener.Close()
		s.mu.Lock()
		for c := range s.peers {
			_ = c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
		_ = os.RemoveAll(s.dir)
	})
}
func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.peers[c] = true
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(c)
	}
}
func management(m string) bool {
	switch m {
	case "server.stop", "server.reload_config", "session.stop", "plugin.link", "plugin.unlink", "plugin.enable", "plugin.disable", "plugin.install":
		return true
	}
	return false
}
func (s *Server) serve(c net.Conn) {
	defer s.wg.Done()
	defer func() { _ = c.Close(); s.mu.Lock(); delete(s.peers, c); s.mu.Unlock() }()
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	up, err := s.opts.Dial(ctx)
	var out sync.Mutex
	var state sync.Mutex
	pending := map[string]bool{}
	alive := err == nil
	answer := func(id string, result any, failure *Error) {
		v := map[string]any{"id": id}
		if failure != nil {
			v["error"] = failure
		} else {
			v["result"] = result
		}
		out.Lock()
		defer out.Unlock()
		_ = json.NewEncoder(c).Encode(v)
	}
	absent := &Error{Code: "unavailable", Message: "herdr server unavailable"}
	stopped := make(chan struct{})
	if up != nil {
		defer up.Close()
		stop := context.AfterFunc(ctx, func() { _ = up.Close() })
		defer stop()
		go func() {
			defer close(stopped)
			// Keep every upstream byte, including spacing, CRLF and streaming events.
			r := bufio.NewReader(up)
			for {
				line, err := r.ReadBytes('\n')
				if len(line) > 0 {
					var reply struct {
						ID string `json:"id"`
					}
					_ = json.Unmarshal(line, &reply)
					state.Lock()
					delete(pending, reply.ID)
					state.Unlock()
					out.Lock()
					_, werr := c.Write(line)
					out.Unlock()
					if werr != nil {
						cancel()
						return
					}
				}
				if err != nil {
					break
				}
			}
			state.Lock()
			alive = false
			ids := pending
			pending = map[string]bool{}
			state.Unlock()
			for id := range ids {
				answer(id, nil, absent)
			}
		}()
	} else {
		close(stopped)
	}
	defer func() {
		cancel()
		if up != nil {
			_ = up.Close()
		}
		<-stopped
	}()
	scan := bufio.NewScanner(c)
	scan.Buffer(make([]byte, 64*1024), 4*1024*1024)
	// Unlike ScanLines this preserves the exact request terminator.
	scan.Split(func(data []byte, eof bool) (int, []byte, error) {
		for i, b := range data {
			if b == '\n' {
				return i + 1, data[:i+1], nil
			}
		}
		if eof && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	for scan.Scan() {
		line := scan.Bytes()
		var req Request
		if json.Unmarshal(line, &req) == nil && s.opts.Intercept != nil {
			result, failure, handled := s.opts.Intercept(ctx, req)
			if handled {
				answer(req.ID, result, failure)
				continue
			}
		}
		if management(req.Method) && s.opts.Warn != nil {
			s.opts.Warn(req.Method)
		}
		state.Lock()
		available := alive
		if available {
			pending[req.ID] = true
		}
		state.Unlock()
		if !available {
			answer(req.ID, nil, absent)
			continue
		}
		if _, err := up.Write(line); err != nil {
			_ = up.Close()
		}
	}
}
