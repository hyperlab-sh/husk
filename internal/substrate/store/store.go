package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

type realClock struct{}

func (realClock) Now() time.Time                 { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.t.C }
func (t realTimer) Stop() bool          { return t.t.Stop() }

type savedRecord struct {
	env      envelope
	key      Key
	modified time.Time
}
type pendingWrite struct {
	entry        state.Entry
	first, last  time.Time
	epoch        uint64
	failed       bool
	unregistered bool
	registeredAt time.Time
}
type health struct {
	// Diagnostics contains the most recent 50 Store problems.
	Diagnostics []Problem `json:"diagnostics"`
	// Quarantined contains files moved aside during this run.
	Quarantined []string `json:"quarantined"`
	// KeyringFallback reports use of the private on-disk identity.
	KeyringFallback bool `json:"keyring_fallback"`
	// Truncated marks a bounded published summary; full inventories use Check.
	Truncated bool `json:"truncated,omitempty"`
}

// Store owns local persistence and its worker. Construct it with Open, Attach
// it once on the loop, and Close it after owners stop publishing changes.
// Disk-facing methods may block and must never be called on the event loop.
type Store struct {
	opts           Options
	paths          Paths
	source         *LocalSource
	writeSource    Source                             // LocalSource in production; injected by failure tests.
	walkDir        func(string, fs.WalkDirFunc) error // filepath.WalkDir in production.
	clock          Clock
	mu             sync.Mutex
	ioMu           sync.Mutex
	configMu       sync.Mutex
	cryptoMu       sync.Mutex
	declared       map[DocName]map[string]Doc
	declaredRev    uint64 // bumped by Declare and Undeclare, under mu
	layerCache     map[docKey]layerLoad
	resolved       map[docKey]Resolved
	lastGood       map[docKey]Resolved
	loaded         map[string][]savedRecord
	baseline       map[string]json.RawMessage
	inflight       map[string]bool
	blocked        map[string]bool
	policies       map[string]state.Spec
	registered     map[string]time.Time
	dirty          map[string]pendingWrite
	epochs         map[string]uint64
	st             *state.State
	healthHandle   *state.Handle
	health         health
	sealer         Sealer
	wake           chan struct{}
	stop           chan struct{}
	done           chan struct{}
	closing        bool
	closeCtx       context.Context
	closeErr       error
	started        time.Time
	lastClean      time.Time
	lastGeneration int64
	barriers       chan flushRequest
}
type flushRequest struct {
	ctx   context.Context
	done  chan error
	force bool
}

// Open resolves private XDG directories, preloads saved values, and starts the
// persistence worker. The runtime directory comes from the opener
// (Options.RuntimeDir); Store neither binds sockets, creates registration
// entries nor creates that directory.
func Open(opts Options) (*Store, error) {
	if opts.Instance.ID == "" {
		opts.Instance.ID = strconv.Itoa(os.Getpid())
	}
	if !opts.Instance.Named() {
		n, err := strconv.ParseUint(opts.Instance.ID, 10, 64)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("store: invalid instance id %q: want a name or a pid", opts.Instance.ID)
		}
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	if opts.Env.LookupEnv == nil {
		opts.Env.LookupEnv = os.LookupEnv
	}
	if opts.Env.HomeDir == nil {
		opts.Env.HomeDir = os.UserHomeDir
	}
	var err error
	base := func(key, fallback string) (string, error) {
		p, _ := opts.Env.LookupEnv(key)
		if p == "" {
			home, err := opts.Env.HomeDir()
			if err != nil {
				return "", err
			}
			p = filepath.Join(home, fallback)
		}
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("store: %s must be absolute", key)
		}
		p = filepath.Join(p, "husk")
		return p, privateDir(p)
	}
	var paths Paths
	if paths.Config, err = base("XDG_CONFIG_HOME", ".config"); err != nil {
		return nil, err
	}
	if paths.State, err = base("XDG_STATE_HOME", ".local/state"); err != nil {
		return nil, err
	}
	if paths.Data, err = base("XDG_DATA_HOME", ".local/share"); err != nil {
		return nil, err
	}
	if paths.Runtime = opts.RuntimeDir; paths.Runtime != "" {
		if err = privateRuntimeDir(paths.Runtime); err != nil {
			return nil, err
		}
	}
	source, err := NewLocalSource(paths.State)
	if err != nil {
		return nil, err
	}
	docs := map[DocName]Doc{}
	for name, d := range opts.InstanceDocs {
		if !validDoc(name) {
			return nil, ErrInvalidPath
		}
		b, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		doc, err := ParseDoc(b)
		if err != nil {
			return nil, err
		}
		docs[name] = doc
	}
	opts.InstanceDocs = docs
	s := &Store{opts: opts, paths: paths, source: source, writeSource: source, walkDir: filepath.WalkDir, clock: opts.Clock, started: opts.Clock.Now(), declared: map[DocName]map[string]Doc{}, layerCache: map[docKey]layerLoad{}, resolved: map[docKey]Resolved{}, lastGood: map[docKey]Resolved{}, loaded: map[string][]savedRecord{}, baseline: map[string]json.RawMessage{}, inflight: map[string]bool{}, blocked: map[string]bool{}, policies: map[string]state.Spec{}, registered: map[string]time.Time{}, dirty: map[string]pendingWrite{}, epochs: map[string]uint64{}, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), barriers: make(chan flushRequest)}
	if err = s.preload(); err != nil {
		return nil, err
	}
	s.detectFallback()
	if opts.Instance.Named() {
		if err = s.loadMeta(); err != nil {
			return nil, err
		}
		if !opts.Passive {
			if err = s.writeMeta(false); err != nil {
				return nil, err
			}
		}
	}
	// No worker starts until preloading succeeds; startup failure leaks no goroutine.
	go s.run()
	return s, nil
}

// Attach registers Store's own non-persistent diagnostics slice and listener.
// This is a loop-only call. Preload diagnostics are available immediately.
func (s *Store) Attach(st *state.State) error {
	s.mu.Lock()
	if s.st != nil {
		s.mu.Unlock()
		return fmt.Errorf("store: already attached")
	}
	s.mu.Unlock()
	h, err := st.Register(state.Spec{Namespace: state.StoreNamespace, Owner: state.StoreOwner, Scope: state.ScopeInstance})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.st = st
	s.healthHandle = h
	b := s.healthBytes()
	s.mu.Unlock()
	if _, err = h.Set(b); err != nil {
		return err
	}
	st.OnFlush(s.onFlush)
	return nil
}

// Paths returns the resolved bases, never mutable Store internals.
func (s *Store) Paths() Paths { return s.paths }

// Done is closed once the worker has exited, after Close.
func (s *Store) Done() <-chan struct{} { return s.done }

// RuntimeDir returns the verified runtime directory the opener passed, or ""
// when there is none.
func (s *Store) RuntimeDir() string { return s.paths.Runtime }

// OpenPluginLog exclusively creates a private log, trying at most 100 names.
func (s *Store) OpenPluginLog(pluginID string) (*os.File, error) {
	if !validID(pluginID) {
		return nil, ErrInvalidPath
	}
	for i := 0; i < 100; i++ {
		name := pluginID
		if i > 0 {
			name += fmt.Sprintf("-%d", i)
		}
		p, err := pathInside(s.paths.State, []string{"logs", s.opts.Instance.ID, name + ".log"}, true)
		if err != nil {
			return nil, err
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, fmt.Errorf("store: log name attempts exhausted")
}
func (s *Store) loadMeta() error {
	data, entry, err := s.source.Get(context.Background(), Key{Scope: state.ScopeInstance, Instance: s.opts.Instance.ID, Path: []string{"meta.json"}})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var previous struct {
		LastClean time.Time `json:"last_clean_exit"`
	}
	err = json.Unmarshal(data, &previous)
	if err == nil && (len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{') {
		err = fmt.Errorf("metadata must be a JSON object")
	}
	if err != nil {
		if s.opts.Passive {
			return nil // left in place: a passive Open resets nothing
		}
		return s.quarantineFile(entry, "instance metadata", "metadata_reset", err)
	}
	s.lastClean = previous.LastClean
	return nil
}
func (s *Store) writeMeta(clean bool) error {
	if clean {
		s.lastClean = s.clock.Now()
	}
	var lastClean any
	if !s.lastClean.IsZero() {
		lastClean = s.lastClean
	}
	b, err := json.Marshal(map[string]any{"last_start": s.startedAt(), "last_clean_exit": lastClean})
	if err != nil {
		return err
	}
	return s.source.Put(context.Background(), Key{Scope: state.ScopeInstance, Instance: s.opts.Instance.ID, Path: []string{"meta.json"}}, b)
}
func (s *Store) startedAt() time.Time { return s.started }
func (s *Store) diagnostic(p Problem, quarantined string) {
	s.mu.Lock()
	s.health.Diagnostics = append(s.health.Diagnostics, p)
	if len(s.health.Diagnostics) > 50 {
		s.health.Diagnostics = append([]Problem(nil), s.health.Diagnostics[len(s.health.Diagnostics)-50:]...)
	}
	if quarantined != "" {
		s.health.Quarantined = append(s.health.Quarantined, quarantined)
	}
	s.mu.Unlock()
	s.postHealth()
}
func (s *Store) postHealth() {
	s.mu.Lock()
	post, h := s.opts.Post, s.healthHandle
	s.mu.Unlock()
	if post == nil || h == nil {
		return
	}
	post(func() {
		s.mu.Lock()
		b := s.healthBytes()
		s.mu.Unlock()
		_, _ = h.Set(b)
	})
}

// healthBytes is called under mu. Recoverable errors must never make a core
// slice write fail. Keep full internal data, but bound the published summary.
func (s *Store) healthBytes() []byte {
	// health contains only concrete JSON-compatible fields; encoding cannot fail.
	encode := func(v health) []byte { b, _ := json.Marshal(v); return b }
	value := s.health
	if b := encode(value); len(b) <= state.CoreMaxBytes {
		return b
	}
	value.Truncated = true
	if len(value.Quarantined) > 0 {
		cut := sort.Search(len(value.Quarantined)+1, func(i int) bool {
			v := value
			v.Quarantined = v.Quarantined[i:]
			return len(encode(v)) <= state.CoreMaxBytes
		})
		if cut <= len(value.Quarantined) {
			value.Quarantined = value.Quarantined[cut:]
			return encode(value)
		}
		value.Quarantined = nil
	}
	if len(value.Diagnostics) > 1 {
		cut := sort.Search(len(value.Diagnostics), func(i int) bool {
			v := value
			v.Diagnostics = v.Diagnostics[i:]
			return len(encode(v)) <= state.CoreMaxBytes
		})
		if cut < len(value.Diagnostics) {
			value.Diagnostics = value.Diagnostics[cut:]
			return encode(value)
		}
		value.Diagnostics = value.Diagnostics[len(value.Diagnostics)-1:]
	}
	if len(value.Diagnostics) == 1 {
		p := value.Diagnostics[0]
		clip := func(s string, n int) string {
			if len(s) > n {
				return s[:n] + "…"
			}
			return s
		}
		p.Code = clip(p.Code, 64)
		p.Message = clip(p.Message, 4096)
		p.File = clip(p.File, 1024)
		p.Path = clip(p.Path, 256)
		p.Layer = Layer(clip(string(p.Layer), 32))
		p.LowerLayer = Layer(clip(string(p.LowerLayer), 32))
		value.Diagnostics = []Problem{p}
	}
	return encode(value)
}

func (s *Store) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Close flushes persistent pending changes (including removed owners' values)
// and stops the worker, bounded by ctx.
// It is idempotent. A timed-out caller can call again to await worker completion.
func (s *Store) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.closing {
		s.closing = true
		s.closeCtx = ctx
		close(s.stop)
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeErr
	}
}
func (s *Store) flushNow(ctx context.Context) error { // deterministic worker barrier used by package tests
	req := flushRequest{ctx: ctx, done: make(chan error, 1), force: true}
	select {
	case <-s.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case s.barriers <- req:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-req.done:
		return err
	}
}
