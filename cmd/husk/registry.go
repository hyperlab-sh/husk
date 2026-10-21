package main

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
	"github.com/hyperlab-sh/husk/pkg/manifest"
)

// The registry is confined to Bubble Tea's update loop. Transport goroutines
// enqueue messages rather than mutating it or the workbench maps.
type publishedSlot struct {
	Owner         string
	Spec          huskipc.SlotDeclarePayload
	Contributions []huskipc.SlotContribution
	// FromManifest marks a slot registered from the owner's husk-plugin.json
	// before it connected. Its hello may repeat the declaration.
	FromManifest bool
}

const (
	// hookTimeout bounds how long a subscriber may leave a hook call
	// unanswered before Husk answers on its behalf with hookTimeoutError.
	hookTimeout = 10 * time.Second
	// maxPendingHookCalls caps unanswered calls across all plugins.
	maxPendingHookCalls = 1024
	// maxPendingPerSubscriber caps unanswered calls waiting on one
	// subscriber, so a quiet subscriber cannot exhaust the global cap.
	maxPendingPerSubscriber = 64
	// hookTimeoutError is the hook_result.error sent for an expired call.
	hookTimeoutError = "timeout"
)

// clock is a value type (not a func) so TestViewIsPure can deep-compare models.
type clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type namedPublication struct {
	Owner       string
	Title       string
	Description string
	Subscribers []string // registration order, with duplicate subscriptions ignored
	// FromManifest marks a hook or event registered from the owner's
	// husk-plugin.json before it connected.
	FromManifest bool
}

type pendingHook struct {
	HookID  string
	Caller  string
	Waiting []string
	Expires time.Time
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
	// Clock and HookTimeout are fields so tests control time without sleeping.
	Clock       clock
	HookTimeout time.Duration
}

func newPublicationRegistry() *publicationRegistry {
	r := &publicationRegistry{
		Slots: make(map[string]*publishedSlot), Hooks: make(map[string]*namedPublication),
		Events: make(map[string]*namedPublication), Occupants: make(map[huskipc.SlotID]string),
		Pending: make(map[string]*pendingHook), Clock: realClock{}, HookTimeout: hookTimeout,
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

// validateSlot checks a slot declaration. holds reports whether plugin holds a
// host surface: the live occupancy for a runtime declaration, the manifest's
// slots for a declaration made before the plugin connects.
func (r *publicationRegistry) validateSlot(plugin string, s huskipc.SlotDeclarePayload, holds func(huskipc.SlotID) bool) error {
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
		if !holds(s.Rect.Surface) {
			return fmt.Errorf("region surface %q is not held by %s", s.Rect.Surface, plugin)
		}
	}
	return nil
}

func (r *publicationRegistry) declareSlot(plugin string, s huskipc.SlotDeclarePayload) error {
	holds := func(surface huskipc.SlotID) bool { return r.Occupants[surface] == plugin }
	if err := r.validateSlot(plugin, s, holds); err != nil {
		return err
	}
	r.insertSlot(plugin, s)
	return nil
}

// insertSlot stores an already validated declaration, copying its rect.
func (r *publicationRegistry) insertSlot(plugin string, s huskipc.SlotDeclarePayload) {
	if s.Rect != nil {
		rect := *s.Rect
		s.Rect = &rect
	}
	r.Slots[s.ID] = &publishedSlot{Owner: plugin, Spec: s}
}

// declareFromManifest registers the declarations of a husk-plugin.json under
// the plugin's id, before the plugin connects, with the validation a runtime
// declaration gets. Region surfaces are checked against the manifest's slots,
// since the plugin has not claimed them yet. It is atomic: on error nothing
// from this manifest stays registered.
func (r *publicationRegistry) declareFromManifest(p *manifest.Plugin) (err error) {
	var slotIDs, hookIDs, eventIDs []string
	defer func() {
		if err == nil {
			return
		}
		for _, id := range slotIDs {
			delete(r.Slots, id)
		}
		for _, id := range hookIDs {
			delete(r.Hooks, id)
		}
		for _, id := range eventIDs {
			delete(r.Events, id)
		}
	}()
	declared := make(map[huskipc.SlotID]bool, len(p.Slots))
	for _, s := range p.Slots {
		declared[huskipc.SlotID(s)] = true
	}
	holds := func(surface huskipc.SlotID) bool {
		return declared[surface] && r.Slots["husk."+string(surface)] != nil
	}
	for _, s := range p.PublishedSlots {
		if err = r.validateSlot(p.ID, s, holds); err != nil {
			return fmt.Errorf("published_slots: %w", err)
		}
		r.insertSlot(p.ID, s)
		slotIDs = append(slotIDs, s.ID)
		r.Slots[s.ID].FromManifest = true
	}
	for _, h := range p.Hooks {
		if err = declareNamedInfo(r.Hooks, p.ID, h.ID, h.Title, h.Description); err != nil {
			return fmt.Errorf("hooks: %w", err)
		}
		hookIDs = append(hookIDs, h.ID)
		r.Hooks[h.ID].FromManifest = true
	}
	for _, e := range p.Events {
		if err = declareNamedInfo(r.Events, p.ID, e.ID, e.Title, e.Description); err != nil {
			return fmt.Errorf("events: %w", err)
		}
		eventIDs = append(eventIDs, e.ID)
		r.Events[e.ID].FromManifest = true
	}
	return nil
}

// declareSlotFromHello declares a slot from hello. A slot the plugin's
// manifest already registered is accepted when the hello repeats it: fields the
// hello leaves empty defer to the manifest, fields it sets must match. Any other
// existing slot is an error, as for slot_declare.
func (r *publicationRegistry) declareSlotFromHello(plugin string, s huskipc.SlotDeclarePayload) error {
	existing := r.Slots[s.ID]
	if existing == nil || !existing.FromManifest || existing.Owner != plugin {
		return r.declareSlot(plugin, s)
	}
	m := existing.Spec
	switch {
	case s.Title != "" && s.Title != m.Title,
		s.Description != "" && s.Description != m.Description,
		s.Kind != "" && s.Kind != m.Kind,
		s.Rect != nil && (m.Rect == nil || *s.Rect != *m.Rect):
		return fmt.Errorf("slot %q conflicts with its manifest declaration", s.ID)
	}
	return nil
}

// declareNamedFromHello is declareSlotFromHello for hooks and events.
func declareNamedFromHello(publications map[string]*namedPublication, plugin, id, title, description string) error {
	existing := publications[id]
	if existing == nil || !existing.FromManifest || existing.Owner != plugin {
		return declareNamedInfo(publications, plugin, id, title, description)
	}
	if (title != "" && title != existing.Title) || (description != "" && description != existing.Description) {
		return fmt.Errorf("id %q conflicts with its manifest declaration", id)
	}
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
	return declareNamedInfo(publications, plugin, id, "", "")
}

// declareNamedInfo keeps the declared Title and Description so palettes and
// docs can introspect hooks and events.
func declareNamedInfo(publications map[string]*namedPublication, plugin, id, title, description string) error {
	if err := ownID(plugin, id); err != nil {
		return err
	}
	if publications[id] != nil {
		return fmt.Errorf("id %q is already declared", id)
	}
	publications[id] = &namedPublication{Owner: plugin, Title: title, Description: description}
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
	if len(r.Pending) >= maxPendingHookCalls {
		return nil, fmt.Errorf("too many pending hook calls")
	}
	for _, target := range p.Subscribers {
		if r.pendingFor(target) >= maxPendingPerSubscriber {
			return nil, fmt.Errorf("too many pending hook calls for subscriber %q", target)
		}
	}
	var out []registryDelivery
	for _, target := range p.Subscribers {
		out = append(out, registryDelivery{target, huskipc.Envelope{Type: "hook_call", HookCall: &call}})
	}
	if len(out) != 0 {
		r.Pending[call.RequestID] = &pendingHook{HookID: call.ID, Caller: plugin, Waiting: slices.Clone(p.Subscribers), Expires: r.Clock.Now().Add(r.HookTimeout)}
	}
	return out, nil
}

// pendingFor counts unanswered hook calls still waiting on plugin.
func (r *publicationRegistry) pendingFor(plugin string) int {
	n := 0
	for _, p := range r.Pending {
		for _, w := range p.Waiting {
			if w == plugin {
				n++
			}
		}
	}
	return n
}

// expireHooks answers every call past its deadline with a timeout result for
// each subscriber still waiting, then forgets the call. A late real result is
// then refused as an unknown request. Sorted IDs keep output deterministic.
func (r *publicationRegistry) expireHooks(now time.Time) []registryDelivery {
	ids := make([]string, 0, len(r.Pending))
	for id, p := range r.Pending {
		if !now.Before(p.Expires) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var out []registryDelivery
	for _, id := range ids {
		p := r.Pending[id]
		delete(r.Pending, id)
		for _, target := range p.Waiting {
			out = append(out, registryDelivery{p.Caller, huskipc.Envelope{Type: "hook_result", HookResult: &huskipc.HookResultPayload{ID: p.HookID, RequestID: id, PluginID: target, Error: hookTimeoutError}}})
		}
	}
	return out
}

// PublicationInfo describes one declared hook or event for introspection.
type PublicationInfo struct {
	ID, Owner, Title, Description string
}

// describe lists declarations sorted by ID.
func describe(publications map[string]*namedPublication) []PublicationInfo {
	out := make([]PublicationInfo, 0, len(publications))
	for id, p := range publications {
		out = append(out, PublicationInfo{id, p.Owner, p.Title, p.Description})
	}
	slices.SortFunc(out, func(a, b PublicationInfo) int { return cmp.Compare(a.ID, b.ID) })
	return out
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

// ownedSlotDeliveries returns a slot_contributions snapshot for every slot the
// plugin owns, sorted by slot id, so a plugin that connects after others
// contributed (to manifest declarations, say) receives what it missed.
func (r *publicationRegistry) ownedSlotDeliveries(plugin string) []registryDelivery {
	ids := make([]string, 0, len(r.Slots))
	for id, s := range r.Slots {
		if s.Owner == plugin {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	out := make([]registryDelivery, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.slotDelivery(id))
	}
	return out
}

// rollbackHello undoes a failed hello for a plugin that was not connected:
// surface claims and every declaration not registered from its manifest. The
// manifest declarations, with the contributions and subscriptions other
// plugins already made to them, stay.
func (r *publicationRegistry) rollbackHello(plugin string) {
	for slot, owner := range r.Occupants {
		if owner == plugin {
			delete(r.Occupants, slot)
		}
	}
	for id, s := range r.Slots {
		if s.Owner == plugin && !s.FromManifest {
			delete(r.Slots, id)
		}
	}
	for _, publications := range []map[string]*namedPublication{r.Hooks, r.Events} {
		for id, p := range publications {
			if p.Owner == plugin && !p.FromManifest {
				delete(publications, id)
			}
		}
	}
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
	for _, id := range slices.Sorted(maps.Keys(r.Slots)) {
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
	for _, id := range slices.Sorted(maps.Keys(r.Pending)) {
		p := r.Pending[id]
		if p.Caller == plugin {
			delete(r.Pending, id)
			continue
		}
		waiting := slices.Clone(p.Waiting)
		for _, target := range waiting {
			if target == plugin || r.Hooks[p.HookID] == nil {
				deliveries, _ := r.hookResult(target, huskipc.HookResultPayload{ID: p.HookID, RequestID: id, Error: "plugin disconnected"})
				out = append(out, deliveries...)
			}
		}
	}
	return out
}
