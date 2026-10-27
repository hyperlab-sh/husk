package services

import (
	"context"

	host "github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/services/keybinding"
)

// Keys implements contracts.KeybindingPort over the keybinding service. The
// service scopes ListFor to the caller: plugins see their own entries and
// problems plus effective host and declared rail-item bindings; users see the
// full merged listing. Runtime availability is joined here, never in the
// resolver. No slice or map in the result is shared with the service.
type Keys struct {
	Svc *keybinding.Service
	// View captures current action liveness and live plugins' declared surfaces.
	// Nil is an empty catalog; pure Resolve, Hints and KeysFor do not use it.
	View func(context.Context) (KeyView, error)
}

// KeyView is the runtime catalog for one listing. Actions maps known action
// ids to availability (host actions are true). Surfaces contains declarations
// only for live plugins, keyed by plugin id and then surface id.
type KeyView struct {
	Actions  map[string]bool
	Surfaces map[string]map[string]bool
}

var _ host.KeybindingPort = Keys{}

// List implements contracts.KeybindingPort.
func (p Keys) List(ctx context.Context, hc host.Caller) (host.KeyListing, error) {
	listing, err := p.Svc.ListFor(hc)
	if err != nil {
		return host.KeyListing{}, coded(err)
	}
	var view KeyView
	if p.View != nil {
		view, err = p.View(ctx)
		if err != nil {
			return host.KeyListing{}, coded(err)
		}
	}
	out := host.KeyListing{
		Entries:  make([]host.KeyEntry, 0, len(listing.Entries)),
		Problems: make([]protocol.Problem, 0, len(listing.Problems)),
	}
	for _, e := range listing.Entries {
		entry := host.KeyEntry{
			Keys:       e.Keys,
			Action:     e.Action,
			Args:       e.Args,
			Hint:       e.Hint,
			Focus:      e.Focus,
			Plugin:     e.Plugin,
			Disable:    e.Disable,
			Source:     e.Source,
			State:      e.State,
			ShadowedBy: nil,
		}
		if entry.State == "effective" {
			live, known := view.Actions[entry.Action]
			switch {
			case !known:
				entry.State = "unknown_action"
			case !live || entry.Plugin != "" && !view.Surfaces[entry.Plugin][entry.Focus]:
				entry.State = "unavailable"
			}
		}
		if e.ShadowedBy != nil {
			shadow := *e.ShadowedBy
			entry.ShadowedBy = &shadow
		}
		out.Entries = append(out.Entries, entry)
	}
	out.Problems = append(out.Problems, listing.Problems...)
	return out, nil
}

// Resolve is the frontend's in-process key lookup (contracts.Frontend). It
// is pure, lock-free and callable from any goroutine; the binding is a
// detached copy in the keys.list entry shape.
func (p Keys) Resolve(ctx host.KeyContext, keys []string) host.KeyResolution {
	r := p.Svc.Resolve(keyContext(ctx), keys)
	out := host.KeyResolution{}
	switch r.Outcome {
	case keybinding.Prefix:
		out.Outcome = host.KeyPrefix
	case keybinding.Match:
		out.Outcome = host.KeyMatch
	}
	if r.Binding != nil {
		b := r.Binding
		out.Binding = &host.KeyEntry{
			Keys:    b.Keys,
			Action:  b.Action,
			Args:    b.Args,
			Hint:    b.Hint,
			Focus:   b.Focus,
			Plugin:  b.Plugin,
			Disable: b.Disable,
			Source:  b.Source,
			State:   "effective",
		}
	}
	return out
}

// Hints returns the effective footer hints for ctx (at most four).
func (p Keys) Hints(ctx host.KeyContext) []host.KeyHint {
	hints := p.Svc.Hints(keyContext(ctx))
	out := make([]host.KeyHint, 0, len(hints))
	for _, h := range hints {
		out = append(out, host.KeyHint{Keys: h.Keys, Action: h.Action, Text: h.Text})
	}
	return out
}

// KeysFor returns the effective spellings of action, strongest first.
func (p Keys) KeysFor(action string) []string { return p.Svc.KeysFor(action) }

func keyContext(c host.KeyContext) keybinding.KeyContext {
	return keybinding.KeyContext{
		FocusSurface: c.FocusSurface,
		FocusOwner:   c.FocusOwner,
		ModalOpen:    c.ModalOpen,
		ModalAction:  c.ModalAction,
		Capture:      c.Capture,
		Frozen:       c.Frozen,
	}
}
