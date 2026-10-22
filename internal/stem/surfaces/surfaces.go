// Package surfaces is the Stem engine that owns the four surfaces methods —
// surface.update, surface.close, slot.contribute and toast.show — and the
// resolution slice husk.surfaces.
//
// Per design/state.md the inputs are Pod-written declaration slices: each
// plugin's Pod registers <plugin_id>.occupants (the host surfaces the plugin
// claims) and <plugin_id>.contributions (its per-slot contributions). This
// engine resolves those declarations into the Stem-owned slice husk.surfaces:
// occupancy per surface, first claim wins and conflicting claims are refused;
// per-slot contributions combined across plugins in arrival order. A removed
// owner's entries are dropped from the resolution while its declarations stay,
// and registration is re-resolved when the plugin returns. The host renders
// only from husk.surfaces.
//
// Like internal/stem/calls, the engine is loop-only: it runs no goroutines,
// takes no locks and never blocks. Its clock, liveness view and published-slot
// catalog are injected; it imports no Pods or Services code.
package surfaces

import (
	"encoding/json"
	"strings"
)

// Surfaces is the value shape of the husk.surfaces slice: the surface name
// maps to its occupant plugin, and each published slot maps to the ordered
// contributions resolved for it. The host renders only from this slice.
type Surfaces struct {
	Occupants     map[string]string  `json:"occupants"`
	Contributions map[string][]Entry `json:"contributions"`
}

// Entry is one resolved contribution: its contributor and the local id it
// declared. Content labels stay in the contributing plugin's declaration.
type Entry struct {
	Plugin string `json:"plugin"`
	ID     string `json:"id"`
}

// Contribution is one declared entry of a <plugin_id>.contributions slice.
// It mirrors the declaration shape written by Pods: the local id names the
// entry, plugin is the contributor, and label is the contributed content.
type Contribution struct {
	ID     string `json:"id"`
	Plugin string `json:"plugin"`
	Label  string `json:"label"`
}

// Declaration slice name suffixes. The namespace is "<plugin_id>.occupants"
// or "<plugin_id>.contributions"; the Pod writes the slice, this engine reads.
const (
	occupantsSuffix     = "occupants"
	contributionsSuffix = "contributions"
)

// splitDeclarationNamespace splits a declaration slice namespace into its
// plugin id and declaration kind. ok is false for every other namespace,
// including husk.surfaces itself and core slices under "husk".
func splitDeclarationNamespace(namespace string) (plugin, kind string, ok bool) {
	dot := strings.LastIndex(namespace, ".")
	if dot <= 0 || dot == len(namespace)-1 {
		return "", "", false
	}
	plugin, suffix := namespace[:dot], namespace[dot+1:]
	if plugin == "husk" {
		return "", "", false
	}
	switch suffix {
	case occupantsSuffix, contributionsSuffix:
		return plugin, suffix, true
	}
	return "", "", false
}

// decodeOccupants decodes an occupants declaration value: surface name to
// claiming plugin. Values are decoded defensively; a claim that names a
// plugin other than the slice owner is ignored by the resolver.
func decodeOccupants(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 {
		return map[string]string{}, nil
	}
	var claims map[string]string
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	if claims == nil {
		claims = map[string]string{}
	}
	return claims, nil
}

// decodeContributions decodes a contributions declaration value: slot id to
// the plugin's ordered entries. List order is the Pod's write order.
func decodeContributions(raw json.RawMessage) (map[string][]Contribution, error) {
	if len(raw) == 0 {
		return map[string][]Contribution{}, nil
	}
	var slots map[string][]Contribution
	if err := json.Unmarshal(raw, &slots); err != nil {
		return nil, err
	}
	if slots == nil {
		slots = map[string][]Contribution{}
	}
	return slots, nil
}
