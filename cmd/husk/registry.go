package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
)

// The registry is confined to Bubble Tea's update loop. Transport goroutines
// enqueue messages rather than mutating it or the workbench maps.
type publishedSlot struct {
	Owner         string
	Spec          huskipc.SlotDeclarePayload
	Contributions []huskipc.SlotContribution
}

type namedPublication struct {
	Owner       string
	Subscribers []string // registration order, with duplicate subscriptions ignored
}

type pendingHook struct {
	HookID  string
	Caller  string
	Waiting []string
}

type registryDelivery struct {
	PluginID string
	Envelope huskipc.Envelope
}

type publicationRegistry struct {
	Slots     map[string]*publishedSlot
	Hooks     map[string]*namedPublication
	Events    map[string]*namedPublication
	Occupants map[huskipc.SlotID]string // legacy surface IDs remain unchanged on the wire
	Pending   map[string]*pendingHook
}

func newPublicationRegistry() *publicationRegistry {
	r := &publicationRegistry{
		Slots: make(map[string]*publishedSlot), Hooks: make(map[string]*namedPublication),
		Events: make(map[string]*namedPublication), Occupants: make(map[huskipc.SlotID]string),
		Pending: make(map[string]*pendingHook),
	}
	for _, s := range huskipc.AllSlots {
		_ = r.declareSlot("husk", huskipc.SlotDeclarePayload{ID: "husk." + string(s.ID), Title: s.Title, Description: s.Description, Kind: "region"})
	}
	for _, s := range []huskipc.SlotID{huskipc.SlotModalSurface, huskipc.SlotFullCanvas, huskipc.SlotWorkspaceStage} {
		_ = r.declareSlot("husk", huskipc.SlotDeclarePayload{ID: "husk." + string(s), Title: string(s), Kind: "region"})
	}
	return r
}

func validPluginID(id string) bool {
	if id == "" || strings.HasPrefix(id, ".") || strings.HasSuffix(id, ".") || strings.Contains(id, "..") {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func ownID(plugin, id string) error {
	// The final dot separates the plugin ID from a single resource-name
	// component. Dotted plugin IDs remain valid without overlapping parents.
	separator := strings.LastIndexByte(id, '.')
	if !validPluginID(plugin) || separator < 0 || id[:separator] != plugin || separator == len(id)-1 || strings.ContainsAny(id, " \t\r\n\x1b;=\\") {
		return fmt.Errorf("id %q must use %s.<name> with a non-empty dot-free name", id, plugin)
	}
	return nil
}

func (r *publicationRegistry) validateSlot(plugin string, s huskipc.SlotDeclarePayload) error {
	if err := ownID(plugin, s.ID); err != nil {
		return err
	}
	if _, exists := r.Slots[s.ID]; exists {
		return fmt.Errorf("slot %q is already declared", s.ID)
	}
	if s.Kind != "inline" && s.Kind != "region" {
		return fmt.Errorf("slot kind must be inline or region")
	}
	if s.Kind == "inline" && s.Rect != nil {
		return fmt.Errorf("inline slots use anchors, not rect")
	}
	if s.Kind == "region" && plugin != "husk" {
		if s.Rect == nil || s.Rect.X < 0 || s.Rect.Y < 0 || s.Rect.Width <= 0 || s.Rect.Height <= 0 {
			return fmt.Errorf("region requires a positive rect with a surface")
		}
		if r.Occupants[s.Rect.Surface] != plugin {
			return fmt.Errorf("region surface %q is not held by %s", s.Rect.Surface, plugin)
		}
	}
	return nil
}

func (r *publicationRegistry) declareSlot(plugin string, s huskipc.SlotDeclarePayload) error {
	if err := r.validateSlot(plugin, s); err != nil {
		return err
	}
	if s.Rect != nil {
		rect := *s.Rect
		s.Rect = &rect
	}
	r.Slots[s.ID] = &publishedSlot{Owner: plugin, Spec: s}
	return nil
}

// claimSurface uses the same published slot registry as plugin declarations.
// Host layout IDs are kept as compatibility aliases for husk.<id>.
func (r *publicationRegistry) claimSurface(plugin string, slot huskipc.SlotID) error {
	s := r.Slots["husk."+string(slot)]
	if s == nil || s.Owner != "husk" {
		return fmt.Errorf("unknown host surface %q", slot)
	}
	if owner := r.Occupants[slot]; owner != "" && owner != plugin {
		return fmt.Errorf("surface %q is occupied by %s", slot, owner)
	}
	r.Occupants[slot] = plugin
	return nil
}

func declareNamed(publications map[string]*namedPublication, plugin, id string) error {
	if err := ownID(plugin, id); err != nil {
		return err
	}
	if publications[id] != nil {
		return fmt.Errorf("id %q is already declared", id)
	}
	publications[id] = &namedPublication{Owner: plugin}
	return nil
}

func subscribe(publications map[string]*namedPublication, plugin, id string) error {
	p := publications[id]
	if p == nil {
		return fmt.Errorf("unknown publication %q", id)
	}
	for _, subscriber := range p.Subscribers {
		if subscriber == plugin {
			return nil
		}
	}
	p.Subscribers = append(p.Subscribers, plugin)
	return nil
}

func (r *publicationRegistry) slotDelivery(id string) registryDelivery {
	s := r.Slots[id]
	contributions := append([]huskipc.SlotContribution{}, s.Contributions...)
	return registryDelivery{s.Owner, huskipc.Envelope{Type: "slot_contributions", SlotContributions: &huskipc.SlotContributionsPayload{SlotID: id, Contributions: contributions}}}
}

func (r *publicationRegistry) contribute(plugin string, c huskipc.SlotContributePayload) ([]registryDelivery, error) {
	if err := ownID(plugin, c.ID); err != nil {
		return nil, err
	}
	if c.OnClick != "" {
		if err := ownID(plugin, c.OnClick); err != nil {
			return nil, err
		}
	}
	s := r.Slots[c.SlotID]
	if s == nil {
		return nil, fmt.Errorf("unknown slot %q", c.SlotID)
	}
	if s.Owner == plugin {
		return nil, fmt.Errorf("contributions must target another publisher")
	}
	entry := huskipc.SlotContribution{SlotContributePayload: c, PluginID: plugin}
	for i, old := range s.Contributions {
		if old.ID == c.ID {
			if old.PluginID != plugin {
				return nil, fmt.Errorf("contribution %q belongs to another authenticated plugin", c.ID)
			}
			s.Contributions[i] = entry
			return []registryDelivery{r.slotDelivery(c.SlotID)}, nil
		}
	}
	s.Contributions = append(s.Contributions, entry)
	return []registryDelivery{r.slotDelivery(c.SlotID)}, nil
}

func (r *publicationRegistry) callHook(plugin string, call huskipc.HookCallPayload) ([]registryDelivery, error) {
	p := r.Hooks[call.ID]
	if p == nil {
		return nil, fmt.Errorf("unknown hook %q", call.ID)
	}
	if err := ownID(plugin, call.RequestID); err != nil {
		return nil, err
	}
	if r.Pending[call.RequestID] != nil {
		return nil, fmt.Errorf("request_id is already pending")
	}
	if len(r.Pending) >= 1024 {
		return nil, fmt.Errorf("too many pending hook calls")
	}
	var out []registryDelivery
	for _, target := range p.Subscribers {
		out = append(out, registryDelivery{target, huskipc.Envelope{Type: "hook_call", HookCall: &call}})
	}
	if len(out) != 0 {
		r.Pending[call.RequestID] = &pendingHook{HookID: call.ID, Caller: plugin, Waiting: append([]string(nil), p.Subscribers...)}
	}
	return out, nil
}

func (r *publicationRegistry) hookResult(plugin string, result huskipc.HookResultPayload) ([]registryDelivery, error) {
	p := r.Pending[result.RequestID]
	if p == nil || p.HookID != result.ID {
		return nil, fmt.Errorf("unknown hook request")
	}
	for i, target := range p.Waiting {
		if target == plugin {
			p.Waiting = append(p.Waiting[:i], p.Waiting[i+1:]...)
			if len(p.Waiting) == 0 {
				delete(r.Pending, result.RequestID)
			}
			result.PluginID = plugin
			return []registryDelivery{{p.Caller, huskipc.Envelope{Type: "hook_result", HookResult: &result}}}, nil
		}
	}
	return nil, fmt.Errorf("plugin is not awaiting a result for this request")
}

func (r *publicationRegistry) emitEvent(plugin string, event huskipc.EventPayload) ([]registryDelivery, error) {
	p := r.Events[event.Topic]
	if p == nil || p.Owner != plugin {
		return nil, fmt.Errorf("event topic must be declared by its emitter")
	}
	event.PluginID = plugin
	var out []registryDelivery
	for _, target := range p.Subscribers {
		out = append(out, registryDelivery{target, huskipc.Envelope{Type: "event_emit", EventEmit: &event}})
	}
	return out, nil
}

// removePlugin drops declarations, occupancy, subscriptions and contributions.
// Sorted slot/request IDs keep notifications deterministic across map iteration.
func (r *publicationRegistry) removePlugin(plugin string) []registryDelivery {
	var out []registryDelivery
	for slot, owner := range r.Occupants {
		if owner == plugin {
			delete(r.Occupants, slot)
		}
	}
	ids := make([]string, 0, len(r.Slots))
	for id := range r.Slots {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := r.Slots[id]
		if s.Owner == plugin {
			delete(r.Slots, id)
			continue
		}
		kept := make([]huskipc.SlotContribution, 0, len(s.Contributions))
		for _, c := range s.Contributions {
			if c.PluginID != plugin {
				kept = append(kept, c)
			}
		}
		if len(kept) != len(s.Contributions) {
			s.Contributions = kept
			out = append(out, r.slotDelivery(id))
		}
	}
	for _, publications := range []map[string]*namedPublication{r.Hooks, r.Events} {
		for id, p := range publications {
			if p.Owner == plugin {
				delete(publications, id)
				continue
			}
			kept := make([]string, 0, len(p.Subscribers))
			for _, subscriber := range p.Subscribers {
				if subscriber != plugin {
					kept = append(kept, subscriber)
				}
			}
			p.Subscribers = kept
		}
	}
	ids = ids[:0]
	for id := range r.Pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := r.Pending[id]
		if p.Caller == plugin {
			delete(r.Pending, id)
			continue
		}
		waiting := append([]string(nil), p.Waiting...)
		for _, target := range waiting {
			if target == plugin || r.Hooks[p.HookID] == nil {
				deliveries, _ := r.hookResult(target, huskipc.HookResultPayload{ID: p.HookID, RequestID: id, Error: "plugin disconnected"})
				out = append(out, deliveries...)
			}
		}
	}
	return out
}
