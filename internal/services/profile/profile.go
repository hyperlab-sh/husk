// Package profile owns the active profile slice and an immutable profile list.
package profile

import (
	"context"
	"encoding/json"
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

// Options is constructed on the loop; State registration happens in New only.
type Options struct {
	Store   StoreReader
	State   c.Tree
	Post    c.Post
	Flag    *string
	Default string
	Source  func(string) string
}

// Service owns only its profile handle and a replaceable read snapshot.
type Service struct {
	opts    Options
	handle  *state.Handle
	current atomic.Pointer[snapshot]
}
type snapshot struct {
	gen    c.Generation
	active string
	list   []ProfileInfo
}

// New registers husk.profile on the loop, retaining any usable hydration warning.
func New(opts Options) (*Service, error) {
	s := &Service{opts: opts}
	raw, _ := json.Marshal(map[string]string{"active": opts.Default})
	h, err := opts.State.Register(state.Spec{Namespace: state.ProfileNamespace, Owner: state.OwnerHusk, Scope: state.ScopeInstance, Persistent: true, Retention: state.Retention{Policy: state.KeepLatest}, Initial: raw})
	if h == nil {
		return nil, err
	}
	s.handle = h
	if opts.Flag != nil {
		b, _ := json.Marshal(map[string]string{"active": *opts.Flag})
		if _, setErr := h.Set(b); setErr != nil {
			return nil, setErr
		}
	}
	s.current.Store(&snapshot{active: s.stateActive()})
	return s, err
}
func (s *Service) stateActive() string {
	entry := s.opts.State.Snapshot().Slices[state.ProfileNamespace]
	var v struct {
		Active string `json:"active"`
	}
	json.Unmarshal(entry.Value, &v)
	return v.Active
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

// Use validates a name and posts the State mutation; reload follows its listener.
func (s *Service) Use(caller c.Caller, name string) error {
	if err := c.RequireUser(caller); err != nil {
		return err
	}
	known := name == ""
	for _, p := range s.current.Load().list {
		if p.Name == name {
			known = true
		}
	}
	if !known {
		return c.Failure("not_found", "unknown_profile")
	}
	if s.opts.Post == nil || !s.opts.Post(func() { b, _ := json.Marshal(map[string]string{"active": name}); s.handle.Set(b) }) {
		return c.Failure("unavailable", "loop_closed")
	}
	return nil
}

// ChooseProfile computes the startup/current selection from State and the base
// user document, before the coordinator resolves the selected profile layer.
func (s *Service) ChooseProfile(base store.Resolved) string {
	active := s.stateActive()
	if s.Generation() == 0 && s.opts.Flag == nil && !s.opts.State.Snapshot().Slices[state.ProfileNamespace].Hydrated && active == "" && base.Origins["profile.default"] == store.LayerUser {
		v, _ := c.Dotted(base.Doc, "profile.default")
		active, _ = v.(string)
	}
	return active
}

// Rebuild revalidates selection and prepares a whole list without mutating State.
func (s *Service) Rebuild(ctx context.Context, in c.ReloadInput) (c.Candidate, c.Report, error) {
	old := s.current.Load()
	r := c.Report{Service: "profile", Gen: old.gen}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	active := s.stateActive()
	if old.gen == 0 && s.opts.Flag == nil && !s.opts.State.Snapshot().Slices[state.ProfileNamespace].Hydrated && active == "" && in.Doc.Origins["profile.default"] == store.LayerUser {
		v, _ := c.Dotted(in.Doc.Doc, "profile.default")
		active, _ = v.(string)
	}
	original := s.stateActive()
	requested := active
	if in.Profile != nil {
		active = *in.Profile
	}
	names := s.opts.Store.Profiles()
	sources := map[string]store.Layer{}
	if inventory, ok := s.opts.Store.(interface{ ProfileSources() map[string]store.Layer }); ok {
		sources = inventory.ProfileSources()
	}
	sort.Strings(names)
	next := &snapshot{gen: old.gen + 1, active: active}
	found := active == ""
	requestedFound := requested == ""
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
		requestedFound = requestedFound || name == requested
	}
	if !found || !requestedFound {
		next.active = ""
		c.Issue(&r.Problems, "warning", "user", "profile.default", "unknown or deleted active profile; using no profile")
	}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	return c.Prepare(ctx, r, func() bool {
		if !s.current.CompareAndSwap(old, next) {
			return false
		}
		if next.active != s.stateActive() && s.opts.Post != nil {
			s.opts.Post(func() {
				if s.stateActive() == original || s.stateActive() == "" {
					b, _ := json.Marshal(map[string]string{"active": next.active})
					s.handle.Set(b)
				}
			})
		}
		return true
	}, s.Generation), r, nil
}
