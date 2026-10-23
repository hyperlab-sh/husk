package services

import (
	"context"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Keys implements contracts.KeybindingPort over the keybinding service. The
// service already scopes ListFor to the caller (D34a plugin view: only the
// plugin's own bindings and problems; users see the full merged listing) and
// returns an already-detached clone, so the wrapper is a shape translation,
// not another deep copy: no slice or map is shared with the service.
type Keys struct {
	Svc *keybinding.Service
}

var _ host.KeybindingPort = Keys{}

// List implements contracts.KeybindingPort.
func (p Keys) List(ctx context.Context, hc host.Caller) (host.KeyListing, error) {
	listing, err := p.Svc.ListFor(hc)
	if err != nil {
		return host.KeyListing{}, coded(err)
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
		Frozen:       c.Frozen,
	}
}
