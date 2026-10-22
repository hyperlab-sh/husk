// Package surfaces defines the Pod-owned State slices for surface occupancy
// and published-slot contributions: the State side of the registry split in
// docs/docs/design/state.md, "Level 2: Migration from today".
//
// Each Pod registers one occupants slice and one contributions slice for its
// plugin, at launch and before the plugin starts, the same way it registers
// <plugin_id>.state. The Pod holds the write capability; the host and every
// reader else read the snapshots. A Pod teardown unregisters its slices, so
// a crashed or removed plugin's occupancy and contributions disappear through
// the normal owner-cleanup path.
//
// The value shapes are the ones state.md gives for husk.surfaces occupancy
// and published-slot contributions; if a later unit needs different fields it
// changes internal/host/contracts, not this package.
package surfaces

import (
	"fmt"
	"strings"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

// Occupant is the plugin id occupying one surface. The occupants slice value
// is a JSON object mapping a surface name to its Occupant, exactly the
// "occupants" shape in state.md:
//
//	{"sidebar-list":"example.files","main-stage":"example.editor"}
type Occupant string

// Occupants is the value shape of an occupants slice: the owning plugin's
// claims, surface name by occupant. A pod only ever records its own claims,
// so every value names the slice's owner (Validate).
type Occupants map[string]Occupant

// Contribution is one entry in a published slot's contribution list, the
// shape state.md gives:
//
//	{"id":"example.git.branch","plugin":"example.git","label":"main"}
type Contribution struct {
	// ID is the contribution's id, "<plugin_id>.<name>" like today's
	// contribute payloads.
	ID string `json:"id"`
	// Plugin is the contributing plugin; within one slice always the owner.
	Plugin string `json:"plugin"`
	// Label is the short text the host surface shows.
	Label string `json:"label"`
}

// Contributions is the value shape of a contributions slice: the owning
// plugin's contributions, grouped by the id of the published slot they
// target. Each list keeps arrival order; Upsert replaces in place.
type Contributions map[string][]Contribution

// OccupantsNamespace returns the occupants slice namespace of pluginID:
// "<plugin_id>.occupants". The plugin id is the namespace owner, so the Pod
// that registers it is its sole writer.
func OccupantsNamespace(pluginID string) string {
	return pluginID + ".occupants"
}

// ContributionsNamespace returns the contributions slice namespace of
// pluginID: "<plugin_id>.contributions".
func ContributionsNamespace(pluginID string) string {
	return pluginID + ".contributions"
}

// RegisterOccupants registers pluginID's occupants slice: instance scope, not
// persistent, initial {}. The caller (the Pod) keeps the returned slice
// private and applies its plugin's claim requests through it. Registration
// fails with state.ErrInvalidSpec for the reserved core owner "husk" and
// otherwise defers to state.Register's spec validation.
func RegisterOccupants(s *state.State, pluginID string) (*state.Slice[Occupants], error) {
	if pluginID == state.OwnerHusk {
		return nil, fmt.Errorf("%w: occupants: %q is the reserved core owner", state.ErrInvalidSpec, pluginID)
	}
	return state.Register(s, state.Spec{
		Namespace: OccupantsNamespace(pluginID),
		Owner:     pluginID,
		Scope:     state.ScopeInstance,
	}, Occupants{})
}

// RegisterContributions registers pluginID's contributions slice: instance
// scope, not persistent, initial {}. The same ownership rules as
// RegisterOccupants apply.
func RegisterContributions(s *state.State, pluginID string) (*state.Slice[Contributions], error) {
	if pluginID == state.OwnerHusk {
		return nil, fmt.Errorf("%w: contributions: %q is the reserved core owner", state.ErrInvalidSpec, pluginID)
	}
	return state.Register(s, state.Spec{
		Namespace: ContributionsNamespace(pluginID),
		Owner:     pluginID,
		Scope:     state.ScopeInstance,
	}, Contributions{})
}

// Upsert replaces c's earlier entry with the same ID in place, or appends c
// at the end of publication's list, and reports whether the value changed.
// Replacement in place is today's contribute behaviour: a repeated
// contribution id from the same plugin updates its entry without moving it,
// so arrival order is stable. Upsert mutates cs; use it inside
// Slice[Contributions].Update, which commits only changed encodings.
func (cs Contributions) Upsert(publication string, c Contribution) bool {
	list := cs[publication]
	for i, old := range list {
		if old.ID == c.ID {
			if old == c {
				return false
			}
			list[i] = c
			return true
		}
	}
	cs[publication] = append(list, c)
	return true
}

// Validate checks the slice's owner-parity invariants: every recorded
// occupant is pluginID itself, since a pod records only its own claims. An
// occupant naming another plugin would mean a pod wrote past its authority.
func (o Occupants) Validate(pluginID string) error {
	for surface, occ := range o {
		if occ != Occupant(pluginID) {
			return fmt.Errorf("surfaces: occupant of %q is %q, not the owner %q", surface, occ, pluginID)
		}
	}
	return nil
}

// Validate checks the slice's owner-parity invariants: every contribution's
// Plugin is pluginID, and its ID is that plugin's own "<plugin_id>.<name>"
// with a non-empty dot-free name — the same rule today's contribute enforces
// with ownID. A foreign plugin or id would mean a pod wrote past its
// authority.
func (cs Contributions) Validate(pluginID string) error {
	for publication, list := range cs {
		for _, c := range list {
			if c.Plugin != pluginID {
				return fmt.Errorf("surfaces: contribution %q in %q names plugin %q, not the owner %q", c.ID, publication, c.Plugin, pluginID)
			}
			if !ownName(pluginID, c.ID) {
				return fmt.Errorf("surfaces: contribution id %q must use %s.<name> with a non-empty dot-free name", c.ID, pluginID)
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
