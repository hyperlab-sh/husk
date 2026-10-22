package services

import (
	"context"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/services/keybinding"
)

// Keys implements contracts.KeybindingPort over the keybinding service. The
// service already scopes ListFor to the caller (D34a plugin view: only the
// plugin's own bindings and problems; users see the full merged listing), so
// the wrapper is a deep structural copy into the wire shape.
type Keys struct {
	Svc *keybinding.Service
}

var _ host.KeybindingPort = Keys{}

// List implements contracts.KeybindingPort.
func (p Keys) List(ctx context.Context, hc host.Caller) (host.KeyListing, error) {
	listing := p.Svc.ListFor(caller(hc))
	out := host.KeyListing{
		Entries:  make([]host.KeyEntry, 0, len(listing.Entries)),
		Problems: make([]host.KeyProblem, 0, len(listing.Problems)),
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
			Source:     keySource(e.Source),
			State:      e.State,
			ShadowedBy: nil,
		}
		if e.ShadowedBy != nil {
			shadow := keySource(*e.ShadowedBy)
			entry.ShadowedBy = &shadow
		}
		out.Entries = append(out.Entries, entry)
	}
	for _, p := range listing.Problems {
		out.Problems = append(out.Problems, host.KeyProblem{
			Code:    p.Code,
			Layer:   p.Layer,
			Key:     p.Key,
			Message: p.Message,
			Source:  keySource(p.Source),
		})
	}
	return out, nil
}

func keySource(s keybinding.Source) host.KeySource {
	return host.KeySource{
		Layer:    string(s.Layer),
		File:     s.File,
		Line:     s.Line,
		PluginID: s.Plugin,
		Order:    s.Order,
	}
}
