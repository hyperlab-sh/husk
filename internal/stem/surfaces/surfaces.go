// Package surfaces is the Surfaces engine: it serves surface.update,
// surface.close, slot.contribute and toast.show, and owns the
// husk.surfaces resolution of the Pod-written declaration slices
// (docs/docs/design/state.md, "Surfaces resolution").
//
// The declaration slices <plugin_id>.occupants and
// <plugin_id>.contributions are written only by the owning Pod, through its
// validated wrappers; the engine reads them from State snapshots and never
// writes another owner's slice. husk.surfaces is the resolution: one
// occupant per surface (first claim wins, conflicts refused) and the
// combined per-slot contribution order, which the engine records itself in
// first-observed order across ticks (D49). The host renders only from the
// resolution.
//
// The value types here mirror the slice shapes defined by the State
// surfaces package (contracts.Occupant and contracts.Contribution are the
// frozen entry shapes); the engine registers the resolution slice through
// state.Register until the shared registration helper lands.
package surfaces

import (
	"encoding/json"
	"fmt"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Occupant is one surface a plugin claims for itself, the frozen contracts
// shape.
type Occupant = contracts.Occupant

// Contribution is one entry a plugin puts in a published slot, the frozen
// contracts shape.
type Contribution = contracts.Contribution

// Occupants is the value shape of an occupants declaration slice: the owning
// plugin's claims, one Occupant per claimed surface, keyed by surface.
type Occupants map[protocol.SurfaceID]Occupant

// Contributions is the value shape of a contributions declaration slice:
// the owning plugin's entries grouped by target slot, the plugin's own
// order within each slot.
type Contributions map[string][]Contribution

// Surfaces is the value shape of husk.surfaces, the resolution slice. Only
// the engine writes it; every other module and every plugin reads
// snapshots. Visible, Placement and Overlays are host view state carried in
// the same slice; the engine does not write them.
type Surfaces struct {
	// Occupants maps each occupied surface to its one occupant: the first
	// claim wins and conflicting later claims are refused.
	Occupants map[protocol.SurfaceID]Occupant `json:"occupants"`
	// Contributions maps each published slot to its entries in the
	// engine-recorded order: first-observed order across ticks, entries of
	// a removed owner dropped.
	Contributions map[string][]Contribution `json:"contributions"`
	// Visible lists the surfaces the layout currently shows.
	Visible []protocol.SurfaceID `json:"visible"`
	// Placement is the active placement.
	Placement Placement `json:"placement"`
	// Overlays are the active host overlays.
	Overlays Overlays `json:"overlays"`
}

// Placement is the active placement.
type Placement struct {
	RailItem string `json:"rail_item"`
	Kind     string `json:"kind"`
}

// Overlays are the active host overlays.
type Overlays struct {
	Modal        ModalOverlay `json:"modal"`
	Palette      bool         `json:"palette"`
	SlotExplorer bool         `json:"slot_explorer"`
}

// ModalOverlay is the modal overlay: which plugin's rail item is open.
type ModalOverlay struct {
	Plugin   string `json:"plugin"`
	RailItem string `json:"rail_item"`
}

// OccupantsNamespace returns the occupants declaration namespace of pluginID.
func OccupantsNamespace(pluginID string) string { return pluginID + ".occupants" }

// ContributionsNamespace returns the contributions declaration namespace of
// pluginID.
func ContributionsNamespace(pluginID string) string { return pluginID + ".contributions" }

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

// decodeOccupants decodes an occupants declaration value defensively: a
// malformed slice logs and resolves as absent. Entries naming a plugin
// other than the namespace owner, or a surface other than their key, are
// dropped: the Pod's wrappers refuse them at write time, so seeing one here
// means a stale or foreign value.
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
// defensively: entries naming a plugin other than the namespace owner or a
// slot other than their key are dropped, and the remaining entries keep the
// plugin's declared order.
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
