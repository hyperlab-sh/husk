// Package secrets authorizes declared reads; values remain in Store crypto only.
package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Caller is the verified identity shared with Server adapters.
type Caller = c.Caller

// Grant records consent to one exact declaration digest, never a value.
type Grant struct {
	Plugin string    `json:"plugin"`
	Name   string    `json:"name"`
	Digest string    `json:"digest"`
	At     time.Time `json:"at"`
}

// SecretInfo describes metadata only; Broken indicates unreadable crypto storage.
type SecretInfo struct {
	Name     string   `json:"name"`
	Declared []string `json:"declared"`
	Granted  []string `json:"granted"`
	Set      bool     `json:"set"`
	Broken   bool     `json:"broken,omitempty"`
}

// Prompt contains only the plugin identity, secret name and declared purpose.
type Prompt struct {
	Plugin     string
	PluginName string
	Name       string
	Purpose    string
}

// Options injects crypto, loop delivery, dialog, time and value-free audit events.
type Options struct {
	State     c.Tree
	Post      c.Post
	Crypto    store.SecretDoc
	Prompt    func(Prompt) <-chan bool
	Clock     c.Clock
	Publisher c.Publisher
	Audit     func(string)
	Context   context.Context
}
type snapshot struct {
	gen          c.Generation
	declarations map[string]map[string]c.SecretRequest
	names        map[string]string
}
type pending struct {
	digest string
	denied bool
}

// Service retains grants/declarations and ephemeral prompts, never secret values.
type Service struct {
	opts    Options
	handle  *state.Handle
	current atomic.Pointer[snapshot]
	mu      sync.Mutex
	pending map[string]pending
	ctx     context.Context
	cancel  context.CancelFunc
}

// New registers grants on the loop. Crypto should be Store.Secrets("husk-secrets").
func New(opts Options) (*Service, error) {
	if opts.Clock == nil {
		opts.Clock = c.RealClock{}
	}
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	ctx, cancel := context.WithCancel(opts.Context)
	h, err := opts.State.Register(state.Spec{Namespace: state.GrantsNamespace, Owner: state.OwnerHusk, Scope: state.ScopeUser, Persistent: true, Retention: state.Retention{Policy: state.Manual}, Initial: json.RawMessage(`{"grants":[]}`)})
	if h == nil {
		cancel()
		return nil, err
	}
	s := &Service{opts: opts, handle: h, pending: map[string]pending{}, ctx: ctx, cancel: cancel}
	s.current.Store(&snapshot{declarations: map[string]map[string]c.SecretRequest{}, names: map[string]string{}})
	return s, err
}

// Close cancels pending prompts and stops their timers.
func (s *Service) Close() { s.cancel() }

// Generation is the current declaration generation.
func (s *Service) Generation() c.Generation { return s.current.Load().gen }

// Digest binds name and purpose with unambiguous JSON framing.
func Digest(req c.SecretRequest) string {
	raw, _ := json.Marshal([]string{req.Name, req.Purpose})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func validName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-", r)) {
			return false
		}
	}
	return true
}
func (s *Service) grants() []Grant {
	var value struct {
		Grants []Grant `json:"grants"`
	}
	json.Unmarshal(s.opts.State.Snapshot().Slices[state.GrantsNamespace].Value, &value)
	return value.Grants
}
func (s *Service) hasGrant(plugin, name string, req c.SecretRequest) bool {
	for _, g := range s.grants() {
		if g.Plugin == plugin && g.Name == name && g.Digest == Digest(req) {
			return true
		}
	}
	return false
}

// Grants is a user-only view containing only still-valid consent records.
func (s *Service) Grants(caller Caller) ([]Grant, error) {
	if err := c.RequireUser(caller); err != nil {
		return nil, err
	}
	snap := s.current.Load()
	var out []Grant
	for _, g := range s.grants() {
		if req, ok := snap.declarations[g.Plugin][g.Name]; ok && g.Digest == Digest(req) {
			out = append(out, g)
		}
	}
	return out, nil
}
func (s *Service) event(name, plugin, outcome string) {
	if s.opts.Publisher != nil {
		s.opts.Publisher.Publish("secrets.read", map[string]string{"name": name, "plugin_id": plugin, "outcome": outcome})
	}
}

// Read implements secrets.get. Only its successful return carries secret bytes.
func (s *Service) Read(caller Caller, name string) (value []byte, err error) {
	// The host and input kinds never handle a secret value, and an invalid
	// caller is refused before it can be attributed an event.
	if err := c.CheckCaller(caller); err != nil {
		return nil, err
	}
	if caller.Kind == protocol.CallerHost || caller.Kind == protocol.CallerInput {
		return nil, c.Failure("denied", "user_only")
	}
	outcome, reported := "unavailable", name
	defer func() { s.event(reported, caller.PluginID, outcome) }()
	if !validName(name) {
		// A requested name is plugin-controlled text: the event never echoes
		// one that is not a well-formed secret name.
		outcome, reported = "not_declared", ""
		return nil, c.Failure("not_found", "not_declared")
	}
	if s.ctx.Err() != nil {
		return nil, c.SafeError("unavailable")
	}
	if !c.IsUser(caller) {
		req, ok := s.current.Load().declarations[caller.PluginID][name]
		if !ok {
			outcome = "not_declared"
			return nil, c.Failure("not_found", "not_declared")
		}
		if !s.hasGrant(caller.PluginID, name, req) {
			if s.request(caller.PluginID, name, req) {
				outcome = "user_denied"
				return nil, c.Failure("denied", "user_denied")
			}
			outcome = "needs_grant"
			return nil, c.Failure("denied", "needs_grant")
		}
	}
	if s.opts.Crypto == nil {
		return nil, c.SafeError("unavailable")
	}
	v, exists, readErr := s.opts.Crypto.Get(name)
	if readErr != nil {
		return nil, c.SafeError("unavailable")
	}
	if !exists {
		outcome = "not_found"
		return nil, c.Failure("not_found", "secret_unset")
	}
	if len(v) > 64*1024 {
		return nil, c.SafeError("unavailable")
	}
	outcome = "allowed"
	return append([]byte(nil), v...), nil
}
func (s *Service) request(plugin, name string, req c.SecretRequest) bool {
	key := plugin + "\x00" + name
	digest := Digest(req)
	s.mu.Lock()
	if p, exists := s.pending[key]; exists && p.digest == digest {
		if p.denied {
			delete(s.pending, key)
			s.mu.Unlock()
			return true
		}
		s.mu.Unlock()
		return false
	}
	s.pending[key] = pending{digest: digest}
	s.mu.Unlock()
	timer := s.opts.Clock.NewTimer(time.Minute)
	go func() {
		defer timer.Stop()
		accepted := false
		var answers <-chan bool
		if s.opts.Prompt != nil {
			answers = s.opts.Prompt(Prompt{plugin, s.current.Load().names[plugin], name, req.Purpose})
		}
		select {
		case accepted = <-answers:
		case <-timer.C():
		case <-s.ctx.Done():
			return
		}
		if accepted && s.ctx.Err() == nil {
			if s.opts.Post != nil && s.opts.Post(func() {
				current, ok := s.current.Load().declarations[plugin][name]
				if s.ctx.Err() == nil && ok && Digest(current) == digest {
					s.writeGrant(plugin, name, digest)
				}
				// Keep the prompt pending until its loop callback actually ran;
				// retries while the post is queued must not open another dialog.
				s.mu.Lock()
				if s.pending[key].digest == digest {
					delete(s.pending, key)
				}
				s.mu.Unlock()
			}) {
				return
			}
		}
		s.mu.Lock()
		if s.pending[key].digest == digest {
			s.pending[key] = pending{digest: digest, denied: true}
		}
		s.mu.Unlock()
	}()
	return false
}
func (s *Service) writeGrant(plugin, name, digest string) {
	gs := s.grants()
	next := []Grant{}
	for _, g := range gs {
		if g.Plugin != plugin || g.Name != name {
			next = append(next, g)
		}
	}
	next = append(next, Grant{plugin, name, digest, s.opts.Clock.Now().UTC()})
	raw, _ := json.Marshal(map[string]any{"grants": next})
	s.handle.Set(raw)
}

// Grant accepts user consent and posts the only State write path.
func (s *Service) Grant(caller Caller, plugin, name string) error {
	if err := c.RequireCLI(caller); err != nil {
		return err
	}
	req, ok := s.current.Load().declarations[plugin][name]
	if !ok {
		return c.Failure("not_found", "not_declared")
	}
	digest := Digest(req)
	if s.opts.Post == nil || !s.opts.Post(func() {
		if current, ok := s.current.Load().declarations[plugin][name]; ok && Digest(current) == digest {
			s.writeGrant(plugin, name, digest)
		}
	}) {
		return c.SafeError("unavailable")
	}
	return nil
}

// Revoke posts removal of one grant; a plugin can never revoke consent.
func (s *Service) Revoke(caller Caller, plugin, name string) error {
	if err := c.RequireCLI(caller); err != nil {
		return err
	}
	if s.opts.Post == nil || !s.opts.Post(func() {
		var next []Grant
		for _, g := range s.grants() {
			if g.Plugin != plugin || g.Name != name {
				next = append(next, g)
			}
		}
		raw, _ := json.Marshal(map[string]any{"grants": next})
		s.handle.Set(raw)
	}) {
		return c.SafeError("unavailable")
	}
	return nil
}

// Set accepts values from a user-only Server stdin adapter, never from argv.
func (s *Service) Set(caller Caller, name string, value []byte) error {
	if err := c.RequireCLI(caller); err != nil {
		return err
	}
	if !validName(name) {
		return c.Failure("invalid_params", "invalid_secret_name")
	}
	if len(value) > 64*1024 {
		return c.Failure("invalid_params", "secret_limit")
	}
	if s.opts.Crypto == nil || s.opts.Crypto.Put(name, append([]byte(nil), value...)) != nil {
		return c.SafeError("unavailable")
	}
	return nil
}

// Reveal is user-only and audits the name, never the value.
func (s *Service) Reveal(caller Caller, name string) ([]byte, error) {
	if err := c.RequireCLI(caller); err != nil {
		return nil, err
	}
	v, err := s.Read(caller, name)
	if err == nil && s.opts.Audit != nil {
		s.opts.Audit(name)
	}
	return v, err
}

// Names exposes metadata for declared names (plus stored names to the user).
func (s *Service) Names(caller Caller) ([]SecretInfo, error) {
	if err := c.CheckCaller(caller); err != nil {
		return nil, err
	}
	snap := s.current.Load()
	names := map[string]bool{}
	for plugin, decls := range snap.declarations {
		if c.IsUser(caller) || plugin == caller.PluginID {
			for name := range decls {
				names[name] = true
			}
		}
	}
	set := map[string]bool{}
	broken := s.opts.Crypto == nil
	if !broken {
		keys, err := s.opts.Crypto.Keys()
		broken = err != nil
		for _, name := range keys {
			set[name] = true
			if c.IsUser(caller) {
				names[name] = true
			}
		}
	}
	out := []SecretInfo{}
	for name := range names {
		info := SecretInfo{Name: name, Set: set[name], Broken: broken}
		for plugin, decls := range snap.declarations {
			if req, ok := decls[name]; ok && (c.IsUser(caller) || caller.PluginID == plugin) {
				info.Declared = append(info.Declared, plugin)
				if s.hasGrant(plugin, name, req) {
					info.Granted = append(info.Granted, plugin)
				}
			}
		}
		sort.Strings(info.Declared)
		sort.Strings(info.Granted)
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if broken {
		return out, c.SafeError("unavailable")
	}
	return out, nil
}

// Rebuild prepares declarations only; no crypto values enter a snapshot/report.
func (s *Service) Rebuild(ctx context.Context, in c.ReloadInput) (c.Candidate, c.Report, error) {
	old := s.current.Load()
	r := c.Report{Service: "secrets", Gen: old.gen}
	catalog, ps := c.Catalogs(in)
	r.Problems = append(r.Problems, ps...)
	next := &snapshot{gen: old.gen + 1, declarations: map[string]map[string]c.SecretRequest{}, names: map[string]string{}}
	for plugin, cat := range catalog {
		next.names[plugin] = cat.Name
		next.declarations[plugin] = map[string]c.SecretRequest{}
		for _, req := range cat.SecretRequests {
			if !validName(req.Name) {
				c.Issue(&r.Problems, "error", "invalid_secret_name", "plugin:"+plugin, "secret_requests", "invalid secret name")
				continue
			}
			if _, duplicate := next.declarations[plugin][req.Name]; duplicate {
				c.Issue(&r.Problems, "error", "duplicate_secret_declaration", "plugin:"+plugin, req.Name, "duplicate secret declaration")
				continue
			}
			next.declarations[plugin][req.Name] = req
		}
	}
	if err := c.Invalid(r.Problems); err != nil {
		return nil, r, err
	}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	return c.Prepare(ctx, r, func() bool { return s.current.CompareAndSwap(old, next) }, s.Generation), r, nil
}
