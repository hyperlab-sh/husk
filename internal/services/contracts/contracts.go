// Package contracts holds the dependency-free-of-Services contracts shared by
// the coordinator and its five children. It imports neither Stem nor Pods.
package contracts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Generation increases after each successfully applied candidate.
type Generation uint64

// Problem is the one diagnostic type, shared with every layer.
type Problem = protocol.Problem

// Source locates a declaration.
type Source = protocol.Source

// Report describes the snapshot actually serving after a reload step.
type Report struct {
	Service  string     `json:"service"`
	Gen      Generation `json:"gen"`
	Applied  bool       `json:"applied"`
	Problems []Problem  `json:"problems,omitempty"`
}

// SettingsSnapshot is immutable typed configuration. Child settings aliases it.
type SettingsSnapshot interface {
	Generation() Generation
	String(string) string
	Int(string) int64
	Bool(string) bool
	Duration(string) time.Duration
	Strings(string) []string
	Raw(string) (any, bool)
	Source(string) string
}

// ReloadInput is one consistent set of Store inputs for candidate preparation.
type ReloadInput struct {
	Doc          store.Resolved
	Layers       []store.LayerDoc
	Declared     map[store.DocName]map[string]store.Doc
	Settings     SettingsSnapshot
	Profile      *string
	ConfigLayers []store.LayerDoc
}

// Candidate holds an unpublished immutable snapshot. Apply is called at most
// once by the coordinator while its deadline is still open.
type Candidate interface{ Apply() Report }

// Reloadable prepares without publishing; failed or late candidates are dropped.
type Reloadable interface {
	Rebuild(context.Context, ReloadInput) (Candidate, Report, error)
	Generation() Generation
}

// Prepared implements a one-use candidate with a service-owned atomic commit.
type Prepared struct {
	once       sync.Once
	report     Report
	ctx        context.Context
	commit     func() bool
	generation func() Generation
}

// Prepare wraps a conditional atomic snapshot swap; no publication happens here.
func Prepare(ctx context.Context, r Report, commit func() bool, gen func() Generation) Candidate {
	return &Prepared{ctx: ctx, report: r, commit: commit, generation: gen}
}

// Apply publishes once, or reports cancellation/a stale candidate without swapping.
func (p *Prepared) Apply() Report {
	p.once.Do(func() {
		if p.ctx.Err() == nil {
			p.report.Applied = p.commit()
		}
		p.report.Gen = p.generation()
		if !p.report.Applied {
			p.report.Problems = append(p.report.Problems, Problem{Severity: "error", Code: "candidate_superseded", Message: "candidate cancelled or superseded"})
		}
	})
	r := p.report
	r.Problems = append([]Problem(nil), r.Problems...)
	return r
}

// Error is a safe service error mechanically mapped to a protocol error by Server.
type Error struct {
	Code    string
	Reason  string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Failure creates a value-free error with a stable protocol code/reason.
func Failure(code, reason string) error {
	return &Error{Code: code, Reason: reason, Message: code + ": " + reason}
}

// Caller is the verified identity of whoever invokes a Service method; it is
// the one shared type, never decoded from request params.
type Caller = protocol.Caller

// ValidCaller reports whether c is a well-formed identity: a known kind, a
// plugin id exactly when the kind is plugin.
func ValidCaller(c Caller) bool {
	if !c.Kind.Valid() {
		return false
	}
	return (c.Kind == protocol.CallerPlugin) == (c.PluginID != "")
}

// CheckCaller answers denied/invalid_caller for a zero value, an unknown kind,
// a plugin without an id, or any other kind with one.
func CheckCaller(c Caller) error {
	if !ValidCaller(c) {
		return Failure("denied", "invalid_caller")
	}
	return nil
}

// IsUser reports whether c may use user-only operations: a valid cli, host or
// input caller. The host kinds are trusted for reading and reload but never
// handle a secret value (see RequireCLI).
func IsUser(c Caller) bool {
	return ValidCaller(c) && c.Kind != protocol.CallerPlugin
}

// RequireUser protects mutation and user-only exposure methods.
func RequireUser(c Caller) error {
	if err := CheckCaller(c); err != nil {
		return err
	}
	if !IsUser(c) {
		return Failure("denied", "user_only")
	}
	return nil
}

// RequireCLI protects the secret operations only the user's own shell may do:
// the host and input kinds are denied them.
func RequireCLI(c Caller) error {
	if err := CheckCaller(c); err != nil {
		return err
	}
	if c.Kind != protocol.CallerCLI {
		return Failure("denied", "user_only")
	}
	return nil
}

// Tree is the loop capability surface; Snapshot alone is cross-goroutine safe.
type Tree interface {
	Register(state.Spec) (*state.Handle, error)
	Snapshot() *state.Snapshot
	OnFlush(state.Listener)
}

// Post queues loop-only work and must not execute it on the caller goroutine.
type Post func(func()) bool

// Publisher accepts events from any goroutine, posting to the loop when needed.
type Publisher interface{ Publish(name string, data any) }

// PublishFunc adapts an injected callback.
type PublishFunc func(string, any)

func (f PublishFunc) Publish(n string, d any) {
	if f != nil {
		f(n, d)
	}
}

// Clock and Timer are shared with Store to make deadlines deterministic in tests.
type Clock = store.Clock
type Timer = store.Timer

// RealClock is the production clock; tests inject their own timers.
type RealClock struct{}

func (RealClock) Now() time.Time                 { return time.Now() }
func (RealClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.Timer.C }

// Clone returns a deep JSON copy, preserving JSON number precision.
func Clone[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		var zero T
		return zero
	}
	var out T
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&out) != nil {
		var zero T
		return zero
	}
	return out
}

// Decode converts Store's JSON trees without numeric float conversion.
func Decode(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(out)
}

// Invalid returns every accumulated problem rather than the first.
func Invalid(ps []Problem) error {
	for _, p := range ps {
		if p.Severity == "error" {
			return errors.New("service validation failed")
		}
	}
	return nil
}

// Issue appends a value-free diagnostic for a document key.
func Issue(ps *[]Problem, severity, code, layer, key, message string) {
	*ps = append(*ps, Problem{Severity: severity, Code: code, Layer: layer, Key: key, Message: message})
}

// Catalog is the normalized per-plugin document published by Pod conversion.
type Catalog struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Actions          []ActionDecl    `json:"actions"`
	Surfaces         []SurfaceDecl   `json:"surfaces"`
	SecretRequests   []SecretRequest `json:"secret_requests"`
	SettingsDeclared []SettingDecl   `json:"settings_declared"`
}

// ActionDecl identifies a plugin action (remaining catalog fields are opaque).
type ActionDecl struct {
	ID string `json:"id"`
}

// SurfaceDecl names an owned surface.
type SurfaceDecl struct {
	ID string `json:"id"`
}

// SecretRequest is the declaration whose name and purpose authorize a grant.
type SecretRequest struct {
	Name     string `json:"name"`
	Purpose  string `json:"purpose"`
	Required bool   `json:"required"`
}

// SettingDecl is the normalized settings schema shared with manifest conversion.
type SettingDecl struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Default any      `json:"default"`
	Enum    []string `json:"enum,omitempty"`
	Min     *int64   `json:"min,omitempty"`
	Max     *int64   `json:"max,omitempty"`
	Doc     string   `json:"doc,omitempty"`
	Owner   string   `json:"owner,omitempty"`
	Apply   string   `json:"apply,omitempty"`
}

// Catalogs decodes declared plugin objects and collects malformed catalog problems.
func Catalogs(in ReloadInput) (map[string]Catalog, []Problem) {
	out := map[string]Catalog{}
	var ps []Problem
	for id, doc := range in.Declared[store.DeclaredPlugins] {
		var c Catalog
		if err := Decode(doc, &c); err != nil || c.ID != id {
			Issue(&ps, "warning", "invalid_plugin_catalog", "plugin:"+id, "", "invalid plugin catalog")
			continue
		}
		out[id] = c
	}
	return out, ps
}

// SafeError reports an operation failure without copying an underlying value.
func SafeError(code string) error { return Failure(code, "service_unavailable") }

// Dotted retrieves a leaf without interpreting its meaning.
func Dotted(doc store.Doc, key string) (any, bool) {
	var value any = doc
	for _, part := range strings.Split(key, ".") {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return value, true
}

// ContextError converts cancelled preparation to an unapplied report.
func ContextError(ctx context.Context, r Report) (Candidate, Report, error) {
	return nil, r, fmt.Errorf("service preparation cancelled: %w", ctx.Err())
}
