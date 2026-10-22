// Package surfaces defines the State slices of the Surfaces area: the
// per-plugin occupancy and contribution declarations and the husk.surfaces
// resolution the host renders from (docs/docs/design/state.md, "Level 2:
// Migration from today").
//
// The declaration slices <plugin_id>.occupants and <plugin_id>.contributions
// are written only by the owning Pod — never by the plugin, whose requests
// the Server refuses on those namespaces — one slice per plugin, registered
// at launch next to <plugin_id>.state. Their value types are aliases of the
// frozen internal/host/contracts shapes. A Pod teardown unregisters them, so
// a crashed or removed plugin's declarations disappear through the normal
// owner-cleanup path.
//
// husk.surfaces is the resolution: one occupant per surface (first claim
// wins, conflicts refused), the combined per-slot contribution order the
// engine records — first observation across ticks; within one tick, by
// namespace then by position in the declaring plugin's entries — entries of
// a removed owner's slots dropped, plus visibility, the active placement and
// overlays. Only the Surfaces engine writes it, and the host renders only
// from it; the engine itself is the stem-surfaces unit's to build — this
// package defines the slice and its value shape.
package surfaces

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

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

// Occupants is the value shape of an occupants declaration slice: the
// owning plugin's claims, one Occupant per claimed surface, keyed by
// surface. A surface key with its own Occupant.Surface repeats by design,
// keeping the frozen contracts shape as the entry type.
type Occupants map[protocol.SurfaceID]Occupant

// Contributions is the value shape of a contributions declaration slice:
// the owning plugin's entries grouped by target slot, in the order the Pod
// received them. The value carries no sequence for the combined order —
// the Surfaces engine records the cross-plugin order (state.md); a slice's
// entry position is only the engine's within-tick tiebreaker. A repeated
// contribution id replaces the earlier entry of its slot in place; ids are
// unique per slot, so a foreign plugin's id cannot collide inside one
// slice.
type Contributions map[string][]Contribution

// SurfacesNamespace is the namespace of the resolution slice the host
// renders from.
const SurfacesNamespace = "husk.surfaces"

// Surfaces is the value shape of husk.surfaces, the Stem-owned resolution
// slice. Only the Surfaces engine writes it; every other module and every
// plugin reads snapshots.
type Surfaces struct {
	// Occupants maps each occupied surface to its one occupant: the first
	// claim wins and conflicting later claims are refused.
	Occupants map[protocol.SurfaceID]Occupant `json:"occupants"`
	// Contributions maps each published slot to its combined entries in
	// the order the engine records: first observation across ticks;
	// within one tick, by namespace then by the declaring plugin's entry
	// position. A removed owner's slot entries are dropped.
	Contributions map[string][]Contribution `json:"contributions"`
	// Visible lists the surfaces the layout currently shows.
	Visible []protocol.SurfaceID `json:"visible"`
	// Placement is the active placement.
	Placement Placement `json:"placement"`
	// Overlays are the active host overlays.
	Overlays Overlays `json:"overlays"`
}

// Placement is the active placement, the frozen contracts shape.
type Placement = contracts.Placement

// Overlays are the active host overlays, the frozen contracts shape.
type Overlays = contracts.Overlays

// ModalOverlay is the modal overlay: which plugin's rail item is open, the
// frozen contracts shape.
type ModalOverlay = contracts.ModalOverlay

// OccupantsNamespace returns the occupants declaration namespace of pluginID:
// "<plugin_id>.occupants". The plugin id is the namespace owner, so the Pod
// that registers it is its sole writer.
func OccupantsNamespace(pluginID string) string {
	return pluginID + ".occupants"
}

// ContributionsNamespace returns the contributions declaration namespace of
// pluginID: "<plugin_id>.contributions".
func ContributionsNamespace(pluginID string) string {
	return pluginID + ".contributions"
}

// RegisterOccupants registers pluginID's occupants declaration slice:
// instance scope, not persistent, initial {}. The Pod keeps the returned
// wrapper private and applies its plugin's claim requests through it; there
// is no way to write the slice without owner-parity validation. Registration
// fails with state.ErrInvalidSpec for the reserved core owner "husk" and
// otherwise defers to state.Register's spec validation.
func RegisterOccupants(s *state.State, pluginID string) (*OccupantsSlice, error) {
	if pluginID == state.OwnerHusk {
		return nil, fmt.Errorf("%w: occupants: %q is the reserved core owner", state.ErrInvalidSpec, pluginID)
	}
	slice, err := state.Register(s, state.Spec{
		Namespace: OccupantsNamespace(pluginID),
		Owner:     pluginID,
		Scope:     state.ScopeInstance,
	}, Occupants{})
	if err != nil {
		return nil, err
	}
	return &OccupantsSlice{slice: slice, pluginID: pluginID}, nil
}

// RegisterContributions registers pluginID's contributions declaration
// slice: instance scope, not persistent, initial {}. The same ownership
// rules as RegisterOccupants apply.
func RegisterContributions(s *state.State, pluginID string) (*ContributionsSlice, error) {
	if pluginID == state.OwnerHusk {
		return nil, fmt.Errorf("%w: contributions: %q is the reserved core owner", state.ErrInvalidSpec, pluginID)
	}
	slice, err := state.Register(s, state.Spec{
		Namespace: ContributionsNamespace(pluginID),
		Owner:     pluginID,
		Scope:     state.ScopeInstance,
	}, Contributions{})
	if err != nil {
		return nil, err
	}
	return &ContributionsSlice{slice: slice, pluginID: pluginID}, nil
}

// RegisterSurfaces registers the husk.surfaces resolution slice: owner husk,
// instance scope, not persistent, initial {}. Only the Surfaces engine
// (stem-surfaces) holds the returned slice and writes it; this registration
// is part of the engine's setup, and no plugin or Pod ever receives it.
func RegisterSurfaces(s *state.State) (*state.Slice[Surfaces], error) {
	return state.Register(s, state.Spec{
		Namespace: SurfacesNamespace,
		Owner:     state.OwnerHusk,
		Scope:     state.ScopeInstance,
	}, Surfaces{})
}

// OccupantsSlice is the Pod's write handle to its occupants declaration
// slice. Every write runs Validate first, so nothing outside the owning
// plugin's authority can commit; reads are snapshot reads. No raw
// *state.Slice escapes this package.
type OccupantsSlice struct {
	slice    *state.Slice[Occupants]
	pluginID string
}

// Namespace returns the slice's namespace, also after unregistration. The
// Pod's request check compares a plugin's requested namespace against it,
// never the owner string: a plugin has no namespace it may write here.
func (s *OccupantsSlice) Namespace() string {
	return s.slice.Namespace()
}

// Get returns an independently decoded current value. Callers treat it as
// read-only.
func (s *OccupantsSlice) Get() Occupants {
	return s.slice.Get()
}

// Set validates occupants against the owning plugin and commits them. An
// invalid value is refused with no effect on value, revision or snapshot.
func (s *OccupantsSlice) Set(occupants Occupants) error {
	if err := occupants.Validate(s.pluginID); err != nil {
		return err
	}
	return s.slice.Set(occupants)
}

// Unregister removes the slice, keeping the last revision as a tombstone.
// Repeated calls are harmless. This is the owner-cleanup path: the Pod tears
// its plugin's declarations down with it.
func (s *OccupantsSlice) Unregister() {
	s.slice.Unregister()
}

// ContributionsSlice is the Pod's write handle to its contributions
// declaration slice, with the same validated-write rules as OccupantsSlice.
type ContributionsSlice struct {
	slice    *state.Slice[Contributions]
	pluginID string
}

// Namespace returns the slice's namespace, also after unregistration; the
// Pod's request check compares against it, never the owner string.
func (s *ContributionsSlice) Namespace() string {
	return s.slice.Namespace()
}

// Get returns an independently decoded current value. Callers treat it as
// read-only.
func (s *ContributionsSlice) Get() Contributions {
	return s.slice.Get()
}

// Set validates contributions against the owning plugin and commits them.
// An invalid value is refused with no effect on value, revision or snapshot.
func (s *ContributionsSlice) Set(contributions Contributions) error {
	if err := contributions.Validate(s.pluginID); err != nil {
		return err
	}
	return s.slice.Set(contributions)
}

// Upsert validates c against the owning plugin and replaces its earlier
// entry with the same ID in c's slot in place, or appends it to that slot's
// list, today's contribute behaviour for one plugin's own lists. A
// contribution whose Slot does not name the target slot is refused, as is
// anything Validate refuses; an identical entry commits nothing. Mutations
// run on the loop goroutine, so read-modify-write is race-free.
func (s *ContributionsSlice) Upsert(slot string, c Contribution) error {
	if c.Slot != slot {
		return fmt.Errorf("surfaces: contribution slot %q does not match target slot %q", c.Slot, slot)
	}
	v := s.Get()
	if v == nil {
		v = Contributions{}
	}
	if !v.Upsert(slot, c) {
		return nil
	}
	return s.Set(v)
}

// Unregister removes the slice, keeping the last revision as a tombstone.
// Repeated calls are harmless.
func (s *ContributionsSlice) Unregister() {
	s.slice.Unregister()
}

// Upsert replaces c's earlier entry with the same ID in c's slot in place,
// or appends c to that slot's list, and reports whether the value changed.
// Replacement in place keeps the entry's position stable, mirroring today's
// contribute for one plugin's own list; ids stay scoped to their slot, as in
// the registry.
func (cs *Contributions) Upsert(slot string, c Contribution) bool {
	for i, old := range (*cs)[slot] {
		if old.ID == c.ID {
			if old == c {
				return false
			}
			(*cs)[slot][i] = c
			return true
		}
	}
	(*cs)[slot] = append((*cs)[slot], c)
	return true
}

// Validate checks the slice's owner-parity invariants: every declaration
// names the owning plugin and a defined host layout surface. Anything else
// would mean the Pod wrote past its authority or accepted an unknown
// surface, which today's claimSurface refuses. Surfaces are visited in
// sorted order, so the same invalid value refuses with the same message.
func (o Occupants) Validate(pluginID string) error {
	for _, surface := range slices.Sorted(maps.Keys(o)) {
		occ := o[surface]
		if occ.Plugin != pluginID {
			return fmt.Errorf("surfaces: occupant of %q is %q, not the owner %q", surface, occ.Plugin, pluginID)
		}
		if occ.Surface != surface {
			return fmt.Errorf("surfaces: occupant stored under %q names surface %q", surface, occ.Surface)
		}
		if !occ.Surface.Valid() {
			return fmt.Errorf("surfaces: occupant names %q, not a defined host layout surface", occ.Surface)
		}
	}
	return nil
}

// Validate checks the slice's owner-parity invariants: every entry names the
// owning plugin, its ID and OnClick are that plugin's own
// "<plugin_id>.<name>" with a non-empty dot-free name (the rule today's
// contribute enforces with ownID), no entry targets an empty slot, and a
// stored entry names the key it is stored under: an occupant under surface
// key K has Surface == K, a contribution under slot key K has Slot == K, so
// the maps cannot misfile an entry. The remaining registry checks — the
// slot must exist and must not be the plugin's own — need the declaration
// catalog and stay Pod-side (see state.md, "Pod responsibilities").
func (cs Contributions) Validate(pluginID string) error {
	for _, slot := range slices.Sorted(maps.Keys(cs)) {
		for _, c := range cs[slot] {
			if c.Plugin != pluginID {
				return fmt.Errorf("surfaces: contribution %q names plugin %q, not the owner %q", c.ID, c.Plugin, pluginID)
			}
			if c.Slot == "" {
				return errors.New("surfaces: contribution names no slot")
			}
			if c.Slot != slot {
				return fmt.Errorf("surfaces: contribution %q stored under %q names slot %q", c.ID, slot, c.Slot)
			}
			if !ownName(pluginID, c.ID) {
				return fmt.Errorf("surfaces: contribution id %q must use %s.<name> with a non-empty dot-free name", c.ID, pluginID)
			}
			if c.OnClick != "" && !ownName(pluginID, c.OnClick) {
				return fmt.Errorf("surfaces: contribution on_click %q must be %s.<name>, the plugin's own action", c.OnClick, pluginID)
			}
		}
	}
	return nil
}

// ownName reports whether id is pluginID.<name> with a non-empty dot-free
// name free of the control and separator characters today's wire forbids,
// mirroring cmd/husk ownID without importing it.
func ownName(pluginID, id string) bool {
	sep := strings.LastIndexByte(id, '.')
	if sep <= 0 || id[:sep] != pluginID {
		return false
	}
	return id[sep+1:] != "" && !strings.ContainsAny(id, " \t\r\n\x1b;=\\")
}
