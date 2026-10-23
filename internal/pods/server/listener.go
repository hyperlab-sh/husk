package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// SocketServer is the concrete, concurrency-safe instance listener.
type SocketServer struct {
	mu                            sync.Mutex
	cfg                           Config
	handlers                      map[string]Handler
	listener                      *net.UnixListener
	conns                         map[*connection]struct{}
	attempted, closed, registered bool
	ident                         instance.Identity
	closeErr                      error
	nextID                        uint64
	wg                            sync.WaitGroup
	diagnosticPosting             atomic.Bool
	peer                          func(*net.UnixConn) (uint32, error)
	probe                         func(string) error
	lockWait                      time.Duration
}

// New constructs an inert server. Start validates configuration before binding.
func New(cfg Config) *SocketServer {
	if cfg.InstanceID == "" {
		cfg.InstanceID = cfg.Entry.ID
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = strconv.Itoa(os.Getpid())
	}
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	return &SocketServer{cfg: cfg, handlers: make(map[string]Handler), conns: make(map[*connection]struct{}), peer: peerUID, probe: instance.Probe}
}

// Handle registers prefix (without a trailing dot) once, before Start.
// Duplicate, reserved, malformed and late registrations panic.
func (s *SocketServer) Handle(prefix string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix = strings.TrimSuffix(prefix, ".")
	if s.attempted || s.closed || prefix == "session" || h == nil {
		panic("server: invalid or late handler registration")
	}
	if protocol.ValidateMethod(prefix+".call") != nil {
		panic("server: invalid handler prefix")
	}
	if _, exists := s.handlers[prefix]; exists {
		panic("server: duplicate handler prefix")
	}
	s.handlers[prefix] = h
}

// SocketPath returns this instance's deterministic socket path.
func (s *SocketServer) SocketPath() string {
	return instance.SocketPath(s.cfg.RuntimeDir, s.cfg.InstanceID)
}

// Start claims the socket, registers only after listen, then accepts clients.
// A registration failure is fatal and removes only the socket just claimed.
func (s *SocketServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempted || s.closed {
		return errors.New("server: already started or closed")
	}
	s.attempted = true
	if err := instance.ValidID(s.cfg.InstanceID); err != nil {
		return err
	}
	if !filepath.IsAbs(s.cfg.RuntimeDir) {
		return errors.New("server: runtime directory must be absolute")
	}
	if err := instance.CheckRuntimeDir(s.cfg.RuntimeDir); err != nil {
		return err
	}
	path := s.SocketPath()
	if err := instance.CheckSocketPath(path); err != nil {
		return err
	}
	e := s.cfg.Entry
	if e.ID != "" && e.ID != s.cfg.InstanceID {
		return errors.New("server: registration id differs from instance id")
	}
	if e.Socket != "" && e.Socket != path {
		return errors.New("server: registration socket differs from instance socket")
	}
	e.ID, e.Socket = s.cfg.InstanceID, path
	if e.PID == 0 {
		e.PID = os.Getpid()
	}
	if e.StartedAt.IsZero() {
		e.StartedAt = s.cfg.Clock.Now()
	}
	e.ProtocolVersion = protocol.Version
	ln, err := bind(path)
	if inUse(err) {
		if err = s.reclaim(path); err != nil {
			return err
		}
		ln, err = bind(path)
		if inUse(err) {
			return fmt.Errorf("instance %q is already running", e.ID)
		}
	}
	if err != nil {
		return err
	}
	// Explicit unlink while the listener still holds its claim avoids Close
	// automatically deleting a replacement socket that bound after our unlink.
	ln.SetUnlinkOnClose(false)
	rollback := func(err error) error { _ = os.Remove(path); _ = ln.Close(); return err }
	// The identity is taken from the socket this bind created, right after
	// the bind, never from one that recovery unlinked.
	ident, err := instance.SocketIdentity(path)
	if err != nil {
		return rollback(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return rollback(err)
	}
	if err := instance.Register(s.cfg.RuntimeDir, e); err != nil {
		return rollback(err)
	}
	s.cfg.Entry = e
	s.listener = ln
	s.ident = ident
	s.registered = true
	s.wg.Add(1)
	go s.accept()
	return nil
}

func (s *SocketServer) accept() {
	defer s.wg.Done()
	for {
		raw, err := s.listener.AcceptUnix()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				s.post(0, "warn", fmt.Sprintf("accept failed: %v; plugins and the CLI can no longer call in", err))
			}
			return
		}
		s.mu.Lock()
		s.nextID++
		id := s.nextID
		closed := s.closed
		full := len(s.conns) >= MaxConnections
		s.mu.Unlock()
		if closed || full {
			raw.Close()
			if closed {
				return
			}
			s.post(id, "warn", "rejected IPC connection: connection limit reached")
			continue
		}
		uid, err := s.peer(raw)
		if err != nil || uid != uint32(os.Getuid()) {
			raw.Close()
			s.post(id, "warn", "rejected IPC connection: unreadable or foreign peer uid")
			continue
		}
		c := newConnection(s, raw, id)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			c.close()
			return
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() { defer s.wg.Done(); c.run(); s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }()
	}
}
func (s *SocketServer) post(id uint64, level, message string) {
	if s.cfg.Post != nil {
		s.cfg.Post(Notice{ConnID: id, Level: level, Message: message})
	}
}

func bind(path string) (*net.UnixListener, error) {
	return net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
}

func inUse(err error) bool {
	return err != nil && (errors.Is(err, os.ErrExist) || errors.Is(err, syscall.EADDRINUSE))
}

// reclaim runs claim step 3 after EADDRINUSE: probe the holder; a reply or a
// timeout fails the start and removes nothing, while a refused or missing
// socket goes through instance.RecoverStale, whose re-probe under the
// directory lock decides again before anything is removed.
func (s *SocketServer) reclaim(path string) error {
	id := s.cfg.InstanceID
	if err := s.holderError(id, instance.ClassOf(s.probe(path))); err != nil {
		return err
	}
	classes, err := instance.RecoverStaleWith(s.cfg.RuntimeDir, []*instance.Entry{{ID: id}},
		&instance.Deps{Ping: s.probe, LockWait: s.lockWait})
	if errors.Is(err, instance.ErrLocked) {
		return fmt.Errorf("runtime dir %s is locked", s.cfg.RuntimeDir)
	}
	if err != nil {
		return err
	}
	return s.holderError(id, classes[0])
}

// holderError is the start failure for a name whose holder is not stale.
func (s *SocketServer) holderError(id string, class instance.Class) error {
	switch class {
	case instance.VerdictLive:
		return fmt.Errorf("instance %q is already running", id)
	case instance.VerdictUnresponsive:
		return fmt.Errorf("instance %q did not answer; it may be busy", id)
	}
	return nil
}

// Close refuses new clients, unregisters while the claim is still held, closes
// every stream and cancels its handlers. It never waits on handler cooperation.
func (s *SocketServer) Close() error {
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		s.wg.Wait()
		return err
	}
	s.closed = true
	if s.registered {
		s.closeErr = instance.Unregister(s.cfg.RuntimeDir, s.cfg.InstanceID, s.ident)
	}
	ln := s.listener
	var conns []*connection
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		c.close()
	}
	s.wg.Wait()
	return s.closeErr
}

var _ Server = (*SocketServer)(nil)
