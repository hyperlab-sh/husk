// Package secrets authorizes declared reads; values remain in Store crypto only.
package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

const (
	// MaxValue is the largest secret value in bytes (services.md, Limits).
	MaxValue = 64 * 1024
	// MaxDeclared is the most secrets one plugin may declare.
	MaxDeclared = 16
	// MaxPurpose is the longest declared purpose, in runes.
	MaxPurpose = 200
	// DialogTimeout is how long one dialog stays up once shown.
	DialogTimeout = time.Minute
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

// SecretInfo describes one secret name: metadata only, never a value. Set is
// nil when the caller may not learn whether a value exists.
type SecretInfo struct {
	Name     string   `json:"name"`
	Declared []string `json:"declared"`
	Granted  []string `json:"granted"`
	Set      *bool    `json:"set,omitempty"`
	Required bool     `json:"required"`
}

// NamesResult is the secrets.names result: storage is "ok" or "unavailable".
type NamesResult struct {
	Storage string       `json:"storage"`
	Secrets []SecretInfo `json:"secrets"`
}

// Answer is what the user did with a confirm dialog. The zero value is no
// decision: a dismissal, a timeout and a headless host are never a decline.
type Answer int

const (
	// Dismissed means the dialog closed without an answer.
	Dismissed Answer = iota
	// Accepted grants the plugin this secret.
	Accepted
	// Declined is an explicit refusal, remembered for the instance's life.
	Declined
)

// Prompt is the confirm dialog's content: the verified plugin id first, the
// plugin's own display-name claim, then what it asks for. It never carries a
// secret value.
type Prompt struct {
	Plugin     string
	PluginName string
	Name       string
	Purpose    string
	Required   bool
}

// Options injects crypto, loop delivery, dialog, time and value-free audit events.
type Options struct {
	State  c.Tree
	Post   c.Post
	Crypto store.SecretDoc
	// Prompt shows the dialog and returns the user's answer. It runs off the
	// loop, on a goroutine of the service, and must return once ctx is
	// cancelled (the service closed the dialog: a grant, a changed
	// declaration, a drop or the timeout). Nil means no dialog is wired: every
	// ungranted read is answered user_denied without a decline being recorded.
	Prompt    func(ctx context.Context, p Prompt) Answer
	Clock     c.Clock
	Publisher c.Publisher
	// Audit receives the name of every secret revealed to a human.
	Audit   func(name string)
	Context context.Context
	// WaiterJoined observes a read joining a dialog, after queue/show selection.
	// It runs under the dialog lock and must not block or call the Service.
	// Nil disables it; tests use it to synchronize concurrent requests.
	WaiterJoined func(plugin string, waiters int, queued bool)
}

type snapshot struct {
	gen          c.Generation
	declarations map[string]map[string]c.SecretRequest
	names        map[string]string
}

type verdict int

const (
	vContinue verdict = iota // read the value (step 2)
	vDenied                  // user_denied
	vUnavailable
	vDropped  // Pod left live: no answer/event
	vShutdown // host stopped accepting writes or closed
)

type declineKey struct{ plugin, name, digest string }

// dialog is the one prompt a plugin has pending or shown. Every read of the
// same (plugin, name, digest) waits on it.
type dialog struct {
	plugin, name, digest string
	req                  c.SecretRequest
	pluginName           string
	waiters              []chan verdict
	shown                bool
	accepting            bool // accepted; the grant write is queued on the loop
	resolved             bool
	cancel               context.CancelFunc
}

// Service retains grants/declarations and ephemeral prompts, never secret values.
type Service struct {
	opts    Options
	handle  *state.Handle
	current atomic.Pointer[snapshot]
	ctx     context.Context
	cancel  context.CancelFunc

	mu       sync.Mutex
	queue    []*dialog // pending, not yet shown, in request order
	shown    *dialog
	byPlugin map[string]*dialog
	declined map[declineKey]struct{}
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
	s := &Service{opts: opts, handle: h, ctx: ctx, cancel: cancel, byPlugin: map[string]*dialog{}, declined: map[declineKey]struct{}{}}
	s.current.Store(&snapshot{declarations: map[string]map[string]c.SecretRequest{}, names: map[string]string{}})
	return s, err
}

// Close drops every pending prompt and stops their timers.
func (s *Service) Close() {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.byPlugin {
		s.resolveLocked(d, vShutdown)
	}
}

// DropPlugin drops a departed Pod's shown, queued or accepting dialog. Waiters
// return cancelled without an audit event or decline memory. Loop only.
func (s *Service) DropPlugin(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.byPlugin[id]; d != nil {
		s.resolveLocked(d, vDropped)
	}
}

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

// purposeRule names the rule a purpose breaks, or "" when it is acceptable:
// 1 to 200 printable runes, no newline or other control character.
func purposeRule(p string) string {
	n := utf8.RuneCountInString(p)
	if n < 1 || n > MaxPurpose {
		return "purpose must be 1 to 200 characters"
	}
	for _, r := range p {
		if !unicode.IsGraphic(r) {
			return "purpose must be printable, without newline or other control character"
		}
	}
	return ""
}

func (s *Service) grants() []Grant {
	var value struct {
		Grants []Grant `json:"grants"`
	}
	json.Unmarshal(s.opts.State.Snapshot().Slices[state.GrantsNamespace].Value, &value)
	return value.Grants
}

func (s *Service) hasGrant(plugin, name string, req c.SecretRequest) bool {
	digest := Digest(req)
	for _, g := range s.grants() {
		if g.Plugin == plugin && g.Name == name && g.Digest == digest {
			return true
		}
	}
	return false
}

func (s *Service) event(name, plugin, outcome string) {
	if s.opts.Publisher != nil {
		s.opts.Publisher.Publish("secrets.read", map[string]string{"name": name, "plugin_id": plugin, "outcome": outcome})
	}
}

func storageError() error { return c.Failure("unavailable", "storage") }

func deniedError() error { return c.Failure("denied", "user_denied") }

func shutdownError() error { return c.Failure("cancelled", "shutdown") }

// Read implements secrets.get. Only its successful return carries the value.
// A plugin's ungranted read blocks on the confirm dialog; a closed ctx drops
// the wait. A cli caller takes the same audited path as Reveal.
func (s *Service) Read(ctx context.Context, caller Caller, name string) (string, error) {
	// The host and input kinds never handle a secret value, and an invalid
	// caller is refused before it can be attributed an event.
	if err := c.CheckCaller(caller); err != nil {
		return "", err
	}
	if caller.Kind == protocol.CallerHost || caller.Kind == protocol.CallerInput {
		return "", c.Failure("denied", "user_only")
	}
	if caller.Kind == protocol.CallerCLI {
		return s.reveal(name)
	}
	return s.readPlugin(ctx, caller.PluginID, name)
}

// Reveal is the user-only alias of a cli Read: one path, one audit record.
func (s *Service) Reveal(ctx context.Context, caller Caller, name string) (string, error) {
	if err := c.RequireCLI(caller); err != nil {
		return "", err
	}
	return s.reveal(name)
}

// reveal hands a value to a human and audits the name, never the value.
func (s *Service) reveal(name string) (value string, err error) {
	outcome, reported := "unavailable", name
	defer func() { s.event(reported, "", outcome) }()
	if !validName(name) {
		// The event never echoes a name that is not a well-formed secret name.
		outcome, reported = "not_declared", ""
		return "", c.Failure("invalid_params", "invalid_secret_name")
	}
	value, outcome, err = s.load(name)
	if err != nil {
		return "", err
	}
	outcome = "revealed"
	if s.opts.Audit != nil {
		s.opts.Audit(name)
	}
	return value, nil
}

// load reads a stored value (step 2). The outcome is the event outcome of a
// failure; on success the caller picks allowed or revealed.
func (s *Service) load(name string) (string, string, error) {
	if s.opts.Crypto == nil {
		return "", "unavailable", storageError()
	}
	v, exists, err := s.opts.Crypto.Get(name)
	if err != nil {
		return "", "unavailable", storageError()
	}
	if !exists {
		return "", "unset", c.Failure("not_found", "unset")
	}
	// A value past the read cap or not UTF-8 text cannot be returned faithfully.
	if len(v) > MaxValue || !utf8.Valid(v) {
		return "", "unavailable", storageError()
	}
	return string(v), "", nil
}

func (s *Service) readPlugin(ctx context.Context, plugin, name string) (value string, err error) {
	outcome, reported := "unavailable", name
	dropped := false
	defer func() {
		if !dropped {
			s.event(reported, plugin, outcome)
		}
	}()
	if !validName(name) {
		// A requested name is plugin-controlled text: the event never echoes
		// one that is not a well-formed secret name.
		outcome, reported = "not_declared", ""
		return "", c.Failure("not_found", "not_declared")
	}
	if s.ctx.Err() != nil {
		dropped = true // shutdown drops the read like a closed connection
		return "", c.Failure("cancelled", "shutdown")
	}
	req, ok := s.current.Load().declarations[plugin][name]
	if !ok {
		outcome = "not_declared"
		return "", c.Failure("not_found", "not_declared")
	}
	if !s.hasGrant(plugin, name, req) {
		out, werr := s.await(ctx, plugin, name, req)
		if werr != nil {
			outcome = out
			dropped = out == ""
			return "", werr
		}
	}
	value, out, err := s.load(name)
	if err != nil {
		outcome = out
		return "", err
	}
	outcome = "allowed"
	return value, nil
}

// await blocks a plugin's ungranted read on the confirm dialog (Reading, step
// 3 and 4). It returns nil when the read continues at the value read, and
// otherwise the event outcome ("" for a dropped read, which publishes none)
// with the error.
func (s *Service) await(ctx context.Context, plugin, name string, req c.SecretRequest) (string, error) {
	if s.opts.Prompt == nil {
		return "user_denied", deniedError()
	}
	// A read whose connection is already gone never opens a dialog.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	digest := Digest(req)
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return "", err
	}
	// A CLI grant lands before it takes the lock; re-check so a read that
	// raced it does not open a dialog for a granted name.
	if s.hasGrant(plugin, name, req) {
		s.mu.Unlock()
		return "", nil
	}
	if _, declined := s.declined[declineKey{plugin, name, digest}]; declined {
		s.mu.Unlock()
		return "user_denied", deniedError()
	}
	d := s.byPlugin[plugin]
	if d != nil && (d.name != name || d.digest != digest) {
		s.mu.Unlock()
		return "dialog_pending", c.Failure("busy", "dialog_pending")
	}
	if d == nil {
		d = &dialog{plugin: plugin, name: name, digest: digest, req: req, pluginName: s.current.Load().names[plugin]}
		s.byPlugin[plugin] = d
		s.queue = append(s.queue, d)
	}
	w := make(chan verdict, 1)
	d.waiters = append(d.waiters, w)
	s.pumpLocked()
	if s.opts.WaiterJoined != nil {
		s.opts.WaiterJoined(plugin, len(d.waiters), !d.shown && !d.accepting)
	}
	s.mu.Unlock()

	select {
	case v := <-w:
		if s.ctx.Err() != nil {
			return "", shutdownError()
		}
		switch v {
		case vContinue:
			return "", nil
		case vUnavailable:
			return "unavailable", storageError()
		case vDropped:
			return "", c.Failure("cancelled", "plugin_exited")
		case vShutdown:
			return "", shutdownError()
		}
		return "user_denied", deniedError()
	case <-ctx.Done():
		s.leave(d, w)
		return "", ctx.Err()
	case <-s.ctx.Done():
		return "", c.Failure("cancelled", "shutdown")
	}
}

// leave drops one waiter whose connection closed. The last waiter takes an
// unanswered prompt with it, shown or queued; nothing is remembered. An
// accepted prompt finishes its grant write regardless.
func (s *Service) leave(d *dialog, w chan verdict) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range d.waiters {
		if x == w {
			d.waiters = append(d.waiters[:i], d.waiters[i+1:]...)
			break
		}
	}
	if len(d.waiters) == 0 && !d.accepting {
		s.resolveLocked(d, vDenied)
	}
}

// pumpLocked shows the next queued prompt when none is shown. Each prompt's
// 60 s clock starts here, when it is shown.
func (s *Service) pumpLocked() {
	if s.shown != nil || len(s.queue) == 0 || s.ctx.Err() != nil {
		return
	}
	d := s.queue[0]
	s.queue = s.queue[1:]
	s.shown, d.shown = d, true
	ctx, cancel := context.WithCancel(s.ctx)
	d.cancel = cancel
	timer := s.opts.Clock.NewTimer(DialogTimeout)
	p := Prompt{Plugin: d.plugin, PluginName: d.pluginName, Name: d.name, Purpose: d.req.Purpose, Required: d.req.Required}
	go s.runDialog(ctx, d, timer, p)
}

func (s *Service) runDialog(ctx context.Context, d *dialog, timer c.Timer, p Prompt) {
	defer timer.Stop()
	answer := make(chan Answer, 1)
	go func() { answer <- s.opts.Prompt(ctx, p) }()
	select {
	case a := <-answer:
		s.answered(d, a)
	case <-timer.C():
		s.answered(d, Dismissed)
	case <-ctx.Done():
		// Resolved elsewhere or the service closed; the dialog sees ctx too.
	}
}

// answered applies what the user did with the shown dialog d.
func (s *Service) answered(d *dialog, a Answer) {
	s.mu.Lock()
	if d.resolved || s.shown != d {
		s.mu.Unlock()
		return
	}
	if s.ctx.Err() != nil {
		s.resolveLocked(d, vShutdown)
		s.mu.Unlock()
		return
	}
	switch a {
	case Declined:
		s.declined[declineKey{d.plugin, d.name, d.digest}] = struct{}{}
		s.resolveLocked(d, vDenied)
		s.mu.Unlock()
		return
	case Accepted:
		// The dialog is done; the prompt stays pending until the grant write
		// ran, so reads arriving meanwhile wait on it instead of opening another.
		d.accepting = true
		s.shown, d.shown = nil, false
		d.cancel()
		s.pumpLocked()
		s.mu.Unlock()
		if err := s.postWrite(func() { s.acceptWrite(d) }); err != nil {
			v := vUnavailable
			if failure, ok := err.(*c.Error); ok && failure.Code == "cancelled" {
				v = vShutdown
			}
			s.mu.Lock()
			s.resolveLocked(d, v)
			s.mu.Unlock()
		}
		return
	}
	s.resolveLocked(d, vDenied)
	s.mu.Unlock()
}

// acceptWrite runs on the loop: the accepted grant is written only if the
// declaration still has the digest the user saw.
func (s *Service) acceptWrite(d *dialog) {
	s.mu.Lock()
	done := d.resolved
	s.mu.Unlock()
	if done {
		return
	}
	v := vDenied
	if s.ctx.Err() != nil {
		v = vShutdown
	} else if cur, ok := s.current.Load().declarations[d.plugin][d.name]; ok && Digest(cur) == d.digest {
		v = vContinue
		if err := s.writeGrant(d.plugin, d.name, d.digest); err != nil {
			v = vUnavailable
		}
	}
	s.mu.Lock()
	s.resolveLocked(d, v)
	s.mu.Unlock()
}

// resolveLocked ends a prompt: it closes the dialog if shown, leaves the queue
// if queued, answers every waiter and shows the next prompt.
func (s *Service) resolveLocked(d *dialog, v verdict) {
	if d.resolved {
		return
	}
	d.resolved = true
	if s.byPlugin[d.plugin] == d {
		delete(s.byPlugin, d.plugin)
	}
	if s.shown == d {
		s.shown = nil
	}
	for i, q := range s.queue {
		if q == d {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			break
		}
	}
	if d.cancel != nil {
		d.cancel()
	}
	for _, w := range d.waiters {
		w <- v
	}
	d.waiters = nil
	s.pumpLocked()
}

// reconcile applies a newly published declaration set to the prompts and the
// decline memory: a prompt whose declaration changed digest or went away is
// closed with user_denied and remembers nothing; a decline for a superseded
// digest is forgotten.
func (s *Service) reconcile(next *snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := make([]*dialog, 0, len(s.byPlugin))
	for _, d := range s.byPlugin {
		pending = append(pending, d)
	}
	for _, d := range pending {
		if cur, ok := next.declarations[d.plugin][d.name]; !ok || Digest(cur) != d.digest {
			s.resolveLocked(d, vDenied)
		}
	}
	for k := range s.declined {
		if cur, ok := next.declarations[k.plugin][k.name]; ok && Digest(cur) != k.digest {
			delete(s.declined, k)
		}
	}
}

func (s *Service) writeGrant(plugin, name, digest string) error {
	next := []Grant{}
	for _, g := range s.grants() {
		if g.Plugin != plugin || g.Name != name {
			next = append(next, g)
		}
	}
	next = append(next, Grant{plugin, name, digest, s.opts.Clock.Now().UTC()})
	raw, _ := json.Marshal(map[string]any{"grants": next})
	_, err := s.handle.Set(raw)
	return err
}

// postWrite distinguishes a missing storage seam from a loop that has stopped
// accepting work. The callback must also check shutdown before mutating State.
func (s *Service) postWrite(f func()) error {
	if s.ctx.Err() != nil {
		return shutdownError()
	}
	if s.opts.Post == nil {
		return storageError()
	}
	if !s.opts.Post(f) {
		return shutdownError()
	}
	return nil
}

// postAndWait queues f on the loop and returns once it ran, so a write is
// visible when the call returns. ctx abandons the wait, not the write; shutdown
// abandons the write too, even if an already-queued callback is later drained.
func (s *Service) postAndWait(ctx context.Context, f func()) error {
	done := make(chan error, 1)
	if err := s.postWrite(func() {
		if s.ctx.Err() != nil {
			done <- shutdownError()
			return
		}
		f()
		done <- nil
	}); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return shutdownError()
	}
}

// Grant records the user's consent for the plugin's current declaration and
// returns once the write executed. It closes a pending dialog for the pair,
// whose read then continues, and clears any remembered decline.
func (s *Service) Grant(ctx context.Context, caller Caller, plugin, name string) error {
	if err := c.RequireCLI(caller); err != nil {
		return err
	}
	req, ok := s.current.Load().declarations[plugin][name]
	if !ok {
		return c.Failure("not_found", "not_declared")
	}
	digest := Digest(req)
	granted := false
	var writeErr error
	err := s.postAndWait(ctx, func() {
		cur, ok := s.current.Load().declarations[plugin][name]
		if !ok || Digest(cur) != digest {
			return
		}
		writeErr = s.writeGrant(plugin, name, digest)
		if writeErr != nil {
			return
		}
		granted = true
		s.mu.Lock()
		defer s.mu.Unlock()
		for k := range s.declined {
			if k.plugin == plugin && k.name == name {
				delete(s.declined, k)
			}
		}
		if d := s.byPlugin[plugin]; d != nil && d.name == name && d.digest == digest {
			s.resolveLocked(d, vContinue)
		}
	})
	if err != nil {
		return err
	}
	if writeErr != nil {
		return storageError()
	}
	if !granted {
		// The declaration changed or went away before the write ran.
		return c.Failure("not_found", "not_declared")
	}
	return nil
}

// Revoke removes one grant and returns once the write executed. Revoking a
// grant that does not exist succeeds; a plugin can never revoke consent.
func (s *Service) Revoke(ctx context.Context, caller Caller, plugin, name string) error {
	if err := c.RequireCLI(caller); err != nil {
		return err
	}
	var writeErr error
	err := s.postAndWait(ctx, func() {
		next := []Grant{}
		for _, g := range s.grants() {
			if g.Plugin != plugin || g.Name != name {
				next = append(next, g)
			}
		}
		raw, _ := json.Marshal(map[string]any{"grants": next})
		_, writeErr = s.handle.Set(raw)
	})
	if err != nil {
		return err
	}
	if writeErr != nil {
		return storageError()
	}
	return nil
}

// Set stores a value from a user-only Server adapter, never from argv.
func (s *Service) Set(caller Caller, name string, value string) error {
	if err := c.RequireCLI(caller); err != nil {
		return err
	}
	if !validName(name) {
		return c.Failure("invalid_params", "invalid_secret_name")
	}
	if len(value) > MaxValue {
		return c.Failure("invalid_params", "secret_limit")
	}
	if !utf8.ValidString(value) {
		return c.Failure("invalid_params", "not_utf8")
	}
	if s.opts.Crypto == nil || s.opts.Crypto.Put(name, []byte(value)) != nil {
		return storageError()
	}
	return nil
}

// Names serves secrets.names: every name to a user (declared or stored), a
// plugin's own declared names to a plugin, with set shown only for granted ones.
func (s *Service) Names(caller Caller) (NamesResult, error) {
	if err := c.CheckCaller(caller); err != nil {
		return NamesResult{}, err
	}
	user := c.IsUser(caller)
	snap := s.current.Load()
	set := map[string]bool{}
	storage := "ok"
	if s.opts.Crypto == nil {
		storage = "unavailable"
	} else if keys, err := s.opts.Crypto.Keys(); err != nil {
		storage = "unavailable"
	} else {
		for _, k := range keys {
			set[k] = true
		}
	}
	granted := map[declineKey]struct{}{}
	for _, g := range s.grants() {
		granted[declineKey{g.Plugin, g.Name, g.Digest}] = struct{}{}
	}
	infos := map[string]*SecretInfo{}
	info := func(name string) *SecretInfo {
		if i := infos[name]; i != nil {
			return i
		}
		i := &SecretInfo{Name: name, Declared: []string{}, Granted: []string{}}
		infos[name] = i
		return i
	}
	for plugin, decls := range snap.declarations {
		if !user && plugin != caller.PluginID {
			continue
		}
		for name, req := range decls {
			i := info(name)
			i.Declared = append(i.Declared, plugin)
			i.Required = i.Required || req.Required
			if _, ok := granted[declineKey{plugin, name, Digest(req)}]; ok {
				i.Granted = append(i.Granted, plugin)
			}
		}
	}
	if user {
		for name := range set {
			info(name)
		}
	}
	out := make([]SecretInfo, 0, len(infos))
	for name, i := range infos {
		sort.Strings(i.Declared)
		sort.Strings(i.Granted)
		if user || len(i.Granted) > 0 {
			v := set[name]
			i.Set = &v
		}
		out = append(out, *i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return NamesResult{Storage: storage, Secrets: out}, nil
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
		decls := map[string]c.SecretRequest{}
		next.declarations[plugin] = decls
		seen := map[string]bool{} // well-formed names met so far, dropped or not
		warn := func(order int, code, key, message string) {
			r.Problems = append(r.Problems, c.Problem{
				Severity: "warning", Code: code, Layer: "plugin:" + plugin, Key: key, Message: message,
				Source: &c.Source{Layer: "plugin:" + plugin, Plugin: plugin, Order: order},
			})
		}
		for i, req := range cat.SecretRequests {
			switch {
			case i >= MaxDeclared:
				warn(i, "too_many_secrets", "", "a plugin may declare at most 16 secrets; this declaration is dropped")
			case !validName(req.Name):
				warn(i, "invalid_secret_name", "secret_requests", "invalid secret name")
			case seen[req.Name]:
				warn(i, "duplicate_secret", req.Name, "duplicate secret declaration")
			default:
				seen[req.Name] = true
				if rule := purposeRule(req.Purpose); rule != "" {
					warn(i, "invalid_purpose", req.Name, rule)
					continue
				}
				decls[req.Name] = req
			}
		}
	}
	if err := c.Invalid(r.Problems); err != nil {
		return nil, r, err
	}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	commit := func() bool {
		if !s.current.CompareAndSwap(old, next) {
			return false
		}
		s.reconcile(next)
		return true
	}
	changed := !reflect.DeepEqual(old.declarations, next.declarations) || !reflect.DeepEqual(old.names, next.names)
	return c.Prepare(ctx, r, changed, commit, s.Generation), r, nil
}
