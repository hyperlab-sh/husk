// Package profile owns the active profile slice and an immutable profile list.
package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync/atomic"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// StoreReader lists names without exposing filesystem mechanics.
type StoreReader interface{ Profiles() []string }

// ProfileInfo describes one available profile.
type ProfileInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Active bool   `json:"active"`
}

// Waiter is a registration on one reload flight.
type Waiter interface {
	// Wait blocks until the flight ends or ctx does. Cancelling ctx detaches
	// this waiter only.
	Wait(ctx context.Context) ([]c.Report, error)
}

// Reloader is the coordinator seam profile.use needs.
type Reloader interface {
	// Settled reports whether no flight is pending and the last selection is name.
	Settled(name string) bool
	// AwaitProfile runs on the loop right after the slice write: it registers
	// a waiter on the flight that write causes, without blocking.
	AwaitProfile(name string) Waiter
	// LastReports are the reports of the most recent completed flight.
	LastReports() []c.Report
}

// Options is constructed on the loop; State registration happens in New only.
// Flag is the --profile start flag: a selection input of the startup flight,
// never written to State by itself.
type Options struct {
	Store  StoreReader
	State  c.Tree
	Post   c.Post
	Flag   *string
	Source func(string) string
}

// Service owns only its profile handle and a replaceable read snapshot.
type Service struct {
	opts     Options
	handle   *state.Handle
	current  atomic.Pointer[snapshot]
	reloader Reloader
}
type snapshot struct {
	gen    c.Generation
	active string
	list   []ProfileInfo
}

// New registers husk.profile on the loop, retaining any usable hydration warning.
func New(opts Options) (*Service, error) {
	s := &Service{opts: opts}
	h, err := opts.State.Register(state.Spec{Namespace: state.ProfileNamespace, Owner: state.OwnerHusk, Scope: state.ScopeInstance, Persistent: true, Retention: state.Retention{Policy: state.KeepLatest}})
	if h == nil {
		return nil, err
	}
	s.handle = h
	active, _ := s.stateActive()
	s.current.Store(&snapshot{active: active})
	return s, err
}

// Bind connects the coordinator; call it once, before the first profile.use.
func (s *Service) Bind(r Reloader) { s.reloader = r }

// stateActive reads the slice: its name and whether it is present. An absent
// slice has no active key; {"active":""} is present and means no profile.
func (s *Service) stateActive() (string, bool) {
	var v struct {
		Active *string `json:"active"`
	}
	json.Unmarshal(s.opts.State.Snapshot().Slices[state.ProfileNamespace].Value, &v)
	if v.Active == nil {
		return "", false
	}
	return *v.Active, true
}

// Active is the last applied profile selection.
func (s *Service) Active() string { return s.current.Load().active }

// Generation is the currently applied generation.
func (s *Service) Generation() c.Generation { return s.current.Load().gen }

// List returns a detached sorted list; ListFor supplies CLI authorization.
func (s *Service) List() []ProfileInfo { return append([]ProfileInfo(nil), s.current.Load().list...) }

// ListFor implements profile.list, a user-only method.
func (s *Service) ListFor(caller c.Caller) ([]ProfileInfo, error) {
	if err := c.RequireUser(caller); err != nil {
		return nil, err
	}
	return s.List(), nil
}

// Use implements profile.use. One posted loop callback always records the
// choice in State (an identical write changes nothing), then replies at once
// with LastReports only when the coordinator is settled on the name and it is
// the active profile. Otherwise it registers on the flight that applies the
// write, joining an uncaptured flight or its queued follow-up after capture.
// The reply carries that flight's reports. Correlation is the registration,
// never a comparison of names or generations.
func (s *Service) Use(ctx context.Context, caller c.Caller, name string) (string, []c.Report, error) {
	if err := c.RequireUser(caller); err != nil {
		return "", nil, err
	}
	snap := s.current.Load()
	known := name == ""
	for _, p := range snap.list {
		known = known || p.Name == name
	}
	if !known {
		return "", nil, c.Failure("not_found", "unknown_profile")
	}
	if s.reloader == nil {
		return "", nil, c.Failure("unavailable", "no_reloader")
	}
	type registered struct {
		w   Waiter
		err error
	}
	reg := make(chan registered, 1)
	if s.opts.Post == nil || !s.opts.Post(func() {
		b, _ := json.Marshal(map[string]string{"active": name})
		if _, err := s.handle.Set(b); err != nil {
			reg <- registered{err: err}
			return
		}
		if s.reloader.Settled(name) && s.Active() == name {
			reg <- registered{}
			return
		}
		reg <- registered{w: s.reloader.AwaitProfile(name)}
	}) {
		return "", nil, c.Failure("unavailable", "loop_closed")
	}
	select {
	case r := <-reg:
		if r.err != nil {
			return "", nil, c.Failure("unavailable", "state_write")
		}
		if r.w == nil {
			return name, s.reloader.LastReports(), nil
		}
		reports, err := r.w.Wait(ctx)
		if err != nil {
			return "", reports, err
		}
		// The flight may have selected something else: the profile directory can
		// vanish between validation and step 2.
		return s.Active(), reports, nil
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
}

// Select is step 2's profile choice, before the one Store.Resolve. layers are
// the unmerged config layers read without a profile; only the user layer's
// profile.default counts. First match wins: the --profile flag (startup flight
// only), a present husk.profile slice (its value, "" included: an explicit no
// profile ends selection), the user-layer default while the slice is absent,
// none. A name Store does not list is not activated: unknown_profile and "".
// write reports a selection the slice must record: a --profile choice, or a
// present slice naming a vanished profile. A default selection writes nothing.
func (s *Service) Select(startup bool, layers []store.LayerDoc, names []string) (name string, problems []c.Problem, write bool) {
	layer, key := "", ""
	fromFlag, fromSlice := false, false
	slice, present := s.stateActive()
	switch {
	case startup && s.opts.Flag != nil:
		name, fromFlag = *s.opts.Flag, true
	case present:
		name, fromSlice = slice, true
	default:
		for _, l := range layers {
			if v, ok := c.Dotted(l.Doc, "profile.default"); ok && l.Layer == store.LayerUser {
				name, _ = v.(string)
				layer, key = "user", "profile.default"
			}
		}
	}
	if name == "" {
		return "", nil, false
	}
	for _, n := range names {
		if n == name {
			return name, nil, fromFlag
		}
	}
	return "", []c.Problem{{Severity: "warning", Code: "unknown_profile", Layer: layer, Key: key, Message: fmt.Sprintf("profile %s does not exist; using no profile", name)}}, fromFlag || fromSlice
}

// Rebuild prepares the profile list for the selection the coordinator made. It
// writes no State; Apply posts the slice write-back when the selection differs
// from the slice.
func (s *Service) Rebuild(ctx context.Context, in c.ReloadInput) (c.Candidate, c.Report, error) {
	old := s.current.Load()
	r := c.Report{Service: "profile", Gen: old.gen, Problems: append([]c.Problem(nil), in.ProfileProblems...)}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	original, wasPresent := s.stateActive()
	active := original
	if in.Profile != nil {
		active = *in.Profile
	}
	for _, l := range in.ConfigLayers {
		if _, ok := c.Dotted(l.Doc, "profile.default"); ok && l.Layer != store.LayerUser {
			r.Problems = append(r.Problems, c.Problem{Severity: "warning", Code: "profile_default_ignored", Layer: string(l.Layer), Key: "profile.default", Message: "profile.default is honoured from the user layer only", Source: &c.Source{Layer: string(l.Layer), File: l.File}})
		}
	}
	names := s.opts.Store.Profiles()
	sources := map[string]store.Layer{}
	if inventory, ok := s.opts.Store.(interface{ ProfileSources() map[string]store.Layer }); ok {
		sources = inventory.ProfileSources()
	}
	sort.Strings(names)
	next := &snapshot{gen: old.gen + 1, active: active}
	found := active == ""
	for _, name := range names {
		source := "user"
		if layer, ok := sources[name]; ok {
			source = string(layer)
		}
		if s.opts.Source != nil {
			source = s.opts.Source(name)
		}
		next.list = append(next.list, ProfileInfo{name, source, name == active})
		found = found || name == active
	}
	if !found {
		next.active = ""
		for i := range next.list {
			next.list[i].Active = false
		}
		r.Problems = append(r.Problems, c.Problem{Severity: "warning", Code: "unknown_profile", Message: fmt.Sprintf("profile %s does not exist; using no profile", active)})
	}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	return c.Prepare(ctx, r, func() bool {
		if !s.current.CompareAndSwap(old, next) {
			return false
		}
		if cur, present := s.stateActive(); in.ProfileWrite && (!present || cur != next.active) && s.opts.Post != nil {
			s.opts.Post(func() {
				if cur, present := s.stateActive(); cur == original && present == wasPresent {
					b, _ := json.Marshal(map[string]string{"active": next.active})
					s.handle.Set(b)
				}
			})
		}
		return true
	}, s.Generation), r, nil
}
