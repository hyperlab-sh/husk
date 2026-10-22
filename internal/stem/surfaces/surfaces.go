// Package surfaces is the Surfaces engine: it serves surface.update,
// surface.close, slot.contribute and toast.show, and owns the
// husk.surfaces resolution of the Pod-written declaration slices
// (docs/docs/design/state.md, "Surfaces resolution").
//
// The declaration slices <plugin_id>.occupants and
// <plugin_id>.contributions are written only by the owning Pod, through the
// validated wrappers of the State surfaces package; the engine reads them
// from State snapshots and never writes another owner's slice.
// husk.surfaces is the resolution: one occupant per surface (first claim
// wins, conflicts refused) and the combined per-slot contribution order,
// which the engine records itself in first-observed order across ticks.
// The host renders only from the resolution.
//
// The value types are the State surfaces package's (which freezes the
// contracts entry shapes); the aliases below keep the engine's one-spelling
// rule, and the engine registers the resolution slice through
// RegisterSurfaces.
package surfaces

import (
	"encoding/json"
	"fmt"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
	statessurfaces "github.com/hyperlab-sh/husk/internal/substrate/state/surfaces"
)

// The declaration and resolution value shapes, as defined by the State
// surfaces package: the frozen contracts entries and their slice values.
type (
	// Occupant is one surface a plugin claims for itself.
	Occupant = statessurfaces.Occupant
	// Contribution is one entry a plugin puts in a published slot.
	Contribution = statessurfaces.Contribution
	// Occupants is the value shape of an occupants declaration slice.
	Occupants = statessurfaces.Occupants
	// Contributions is the value shape of a contributions declaration
	// slice.
	Contributions = statessurfaces.Contributions
	// Surfaces is the value shape of husk.surfaces, the resolution slice.
	Surfaces = statessurfaces.Surfaces
)

// splitDeclarationNamespace reports the plugin id and declaration kind of a
// namespace, or ok=false when ns is not a declaration namespace. The core
// owner's own slices are never declarations.
func splitDeclarationNamespace(ns string) (plugin, kind string, ok bool) {
	dot := lastDot(ns)
	if dot <= 0 || dot == len(ns)-1 {
		return "", "", false
	}
	name := ns[dot+1:]
	if name != occupantsSuffix && name != contributionsSuffix {
		return "", "", false
	}
	plugin = ns[:dot]
	if plugin == state.OwnerHusk {
		return "", "", false
	}
	return plugin, name, true
}

const (
	occupantsSuffix     = "occupants"
	contributionsSuffix = "contributions"
)

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// decodeOccupants decodes an occupants declaration value defensively and
// returns the decode error for the caller to log. Entries whose plugin is
// empty or whose surface mismatches their key are dropped: the Pod's
// owner-checked wrappers refuse foreign or stale writes at write time, so
// what survives here is a well-shaped declaration the engine can resolve.
func decodeOccupants(raw json.RawMessage) (Occupants, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v Occupants
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	for surface, occ := range v {
		if occ.Plugin == "" || occ.Surface != surface {
			delete(v, surface)
		}
	}
	return v, nil
}

// decodeContributions decodes a contributions declaration value
// defensively and returns the decode error for the caller to log. Entries
// whose plugin is empty or whose slot mismatches their key are dropped;
// the survivors keep the plugin's declared order.
func decodeContributions(raw json.RawMessage) (Contributions, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v Contributions
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	for slot, list := range v {
		kept := list[:0]
		for _, entry := range list {
			if entry.Plugin == "" || entry.Slot != slot {
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(v, slot)
			continue
		}
		v[slot] = kept
	}
	return v, nil
}
