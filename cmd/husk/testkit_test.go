package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/surfaces"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

// fakeFE is a contracts.Frontend for tests that assert what the workbench
// sends to a host: it records every outbound call and answers key questions
// from defaultKeys. Snapshot is nil, so the model never rebuilds its catalog
// from State: tests seed the catalog directly (addPlugin).
type fakeFE struct {
	mu        sync.Mutex
	Keys      []keySent
	Mice      []mouseSent
	KeyStates []keyStateSent
	Invoked   []invoked
	Registers []string
	// Views is every surface view the model committed.
	Views []contracts.SurfaceView
	st    *state.State
	// OnInvoke, when set, answers InvokeAction (a host dispatching a call).
	OnInvoke func(action string, args map[string]any)
	// SendErr is returned by every Send*.
	SendErr error
	// Toast is what ActiveToast reports.
	Toast *contracts.Toast
	// Snap, Rails, PubSlots and Frames are what the host would report; Snap
	// nil keeps the model's directly seeded catalog.
	Snap     *state.Snapshot
	Rails    []contracts.RailItem
	PubSlots []contracts.PublishedSlot
	Acts     []contracts.PluginAction
	Frames   map[string]map[protocol.SurfaceID]protocol.FrameSpec
	// HintsFn answers Hints; nil advertises none.
	HintsFn func(contracts.KeyContext) []contracts.KeyHint
	pods    map[string]string
	podErr  map[string]string // last_error per Pod
	occ     map[protocol.SurfaceID]string
	seq     uint64
	// Log is every outbound call in order, for tests that assert on routing.
	Log       []wireMsg
	pluginIDs func() []string
	isHost    func(action string) bool
	defaultKeys
}

// wireMsg is one outbound call as a test reads it. Type is one of "key",
// "key_state", "mouse" or "invoke"; the matching field is set.
type wireMsg struct {
	Plugin        string
	Type          string
	SlotKey       *slotKey
	KeyState      *protocol.KeyStateParams
	SlotMouse     *protocol.SurfaceMouseParams
	CommandInvoke *cmdInvoke
}

type slotKey struct {
	Slot protocol.SurfaceID
	Key  string
	Mods protocol.ModifierState
}

type cmdInvoke struct {
	CommandID string
	Args      map[string]any
}

// pluginWire is one plugin's view of the outbound log.
type pluginWire struct {
	fe *fakeFE
	id string
}

// Reset drops what this plugin has been sent so far.
func (w *pluginWire) Reset() {
	w.fe.mu.Lock()
	defer w.fe.mu.Unlock()
	w.fe.Log = slices.DeleteFunc(w.fe.Log, func(m wireMsg) bool { return m.Plugin == w.id })
}

// Len is the number of messages this plugin has been sent.
func (w *pluginWire) Len() int {
	n := 0
	for _, m := range w.fe.Log {
		if m.Plugin == w.id {
			n++
		}
	}
	return n
}

// envelopes returns the messages sent to one plugin, in order.
func envelopes(t testing.TB, w *pluginWire) []wireMsg {
	t.Helper()
	w.fe.mu.Lock()
	defer w.fe.mu.Unlock()
	var out []wireMsg
	for _, m := range w.fe.Log {
		if m.Plugin == w.id {
			out = append(out, m)
		}
	}
	return out
}

// owner attributes an action id to the live plugin whose id prefixes it.
func (f *fakeFE) owner(action string) string {
	if f.isHost != nil && f.isHost(action) {
		return "" // served by the host itself, never delivered to a plugin
	}
	best := ""
	if f.pluginIDs != nil {
		for _, id := range f.pluginIDs() {
			if (action == id || strings.HasPrefix(action, id+".")) && len(id) > len(best) {
				best = id
			}
		}
	}
	return best
}

type keySent struct {
	Plugin string
	P      protocol.SurfaceKeyParams
}
type mouseSent struct {
	Plugin string
	P      protocol.SurfaceMouseParams
}
type keyStateSent struct {
	Plugin string
	P      protocol.KeyStateParams
}
type invoked struct {
	Action string
	Args   map[string]any
}

func (f *fakeFE) Snapshot() *state.Snapshot { return f.Snap }

func (f *fakeFE) Hints(ctx contracts.KeyContext) []contracts.KeyHint {
	if f.HintsFn == nil {
		return nil
	}
	return f.HintsFn(ctx)
}
func (f *fakeFE) Palette() protocol.Palette {
	return protocol.Palette(theme.Default())
}
func (f *fakeFE) InvokeAction(action string, args map[string]any, reply func(map[string]any, error)) {
	f.mu.Lock()
	f.Invoked = append(f.Invoked, invoked{action, args})
	f.Log = append(f.Log, wireMsg{Plugin: f.owner(action), Type: "invoke", CommandInvoke: &cmdInvoke{CommandID: action, Args: args}})
	on := f.OnInvoke
	f.mu.Unlock()
	if on != nil {
		on(action, args)
	}
	if reply != nil {
		reply(nil, nil)
	}
}
func (f *fakeFE) RegisterHostAction(id string, _ contracts.HostActionHandler) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Registers = append(f.Registers, id)
	return nil
}
func (f *fakeFE) SendKey(plugin string, p protocol.SurfaceKeyParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Keys = append(f.Keys, keySent{plugin, p})
	f.Log = append(f.Log, wireMsg{Plugin: plugin, Type: "key", SlotKey: &slotKey{Slot: p.Surface, Key: p.Key, Mods: p.Modifiers}})
	return f.SendErr
}
func (f *fakeFE) SendMouse(plugin string, p protocol.SurfaceMouseParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Mice = append(f.Mice, mouseSent{plugin, p})
	f.Log = append(f.Log, wireMsg{Plugin: plugin, Type: "mouse", SlotMouse: &p})
	return f.SendErr
}
func (f *fakeFE) SendKeyState(plugin string, p protocol.KeyStateParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.KeyStates = append(f.KeyStates, keyStateSent{plugin, p})
	f.Log = append(f.Log, wireMsg{Plugin: plugin, Type: "key_state", KeyState: &p})
	return f.SendErr
}
func (f *fakeFE) RailItems() []contracts.RailItem           { return f.Rails }
func (f *fakeFE) PublishedSlots() []contracts.PublishedSlot { return f.PubSlots }
func (f *fakeFE) Actions() []contracts.PluginAction         { return f.Acts }
func (f *fakeFE) SurfaceFrames() map[string]map[protocol.SurfaceID]protocol.FrameSpec {
	return f.Frames
}
func (f *fakeFE) ActiveToast() (contracts.Toast, bool) {
	if f.Toast == nil {
		return contracts.Toast{}, false
	}
	return *f.Toast, true
}

// OwnSlices registers the three frontend slices on a real State, so tests read
// back what the model wrote.
func (f *fakeFE) OwnSlices() (contracts.FrontendSlices, error) {
	f.st = state.New(state.Options{})
	reg := func(ns string, persistent bool) *state.Handle {
		spec := state.Spec{Namespace: ns, Owner: state.OwnerHusk, Scope: state.ScopeInstance}
		if persistent {
			spec.Persistent = true
			spec.Retention = state.Retention{Policy: state.KeepLatest}
		}
		h, err := f.st.Register(spec)
		if err != nil {
			panic(err)
		}
		return h
	}
	return contracts.FrontendSlices{
		Layout:   reg(state.LayoutNamespace, true),
		Geometry: reg(state.GeometryNamespace, false),
		Focus:    reg(state.FocusNamespace, false),
	}, nil
}

func (f *fakeFE) UpdateSurfaceView(fn func(*contracts.SurfaceView)) error {
	var v contracts.SurfaceView
	fn(&v)
	f.Views = append(f.Views, v)
	return nil
}

// revision is the State revision of a frontend slice, 0 when never written.
func (f *fakeFE) revision(ns string) uint64 {
	f.st.Flush()
	return f.st.Snapshot().Slices[ns].Revision
}

// withFE attaches a recording frontend to a hermetic model and routes its
// host-action dispatch to the model's own handlers, as the real Dispatcher
// would after registerActions.
func withFE(m *WorkbenchModel) *fakeFE {
	if fe, ok := m.fe.(*fakeFE); ok {
		return fe
	}
	fe := &fakeFE{pluginIDs: func() []string {
		var ids []string
		for id := range m.plugins {
			ids = append(ids, id)
		}
		return ids
	}}
	fe.isHost = func(action string) bool {
		_, ok := m.railItemByID(action)
		return ok || m.actions[action] != nil
	}
	m.fe, m.keys = fe, fe
	fe.OnInvoke = func(action string, args map[string]any) {
		if h := m.actions[action]; h != nil {
			_, _ = h(args)
		} else if ri, ok := m.railItemByID(action); ok {
			m.activateRailItem(ri)
		}
	}
	return fe
}

// addPlugin makes id a live plugin occupying the given surfaces with the given
// content, the state a refresh from a host produces, and lays out.
func addPlugin(m *WorkbenchModel, id string, surfs map[protocol.SurfaceID][]string) {
	m.plugins[id] = true
	for s, lines := range surfs {
		m.catalog.Occupants[s] = id
		m.content(id, protocol.SurfaceUpdateParams{Surface: s, Title: string(s), Lines: lines})
	}
	m.recomputeLayout()
}

// addSlot declares a published slot of owner and returns it.
func addSlot(m *WorkbenchModel, id, owner, kind string, rect *contracts.PublishedRect) *publishedSlot {
	s := &publishedSlot{ID: id, Owner: owner, Kind: kind, Rect: rect}
	m.catalog.Slots[id] = s
	return s
}

// contribute appends a contribution to a declared slot.
func contribute(m *WorkbenchModel, slot string, c contribution) {
	m.catalog.Slots[slot].Contributions = append(m.catalog.Slots[slot].Contributions, c)
}

// setContent publishes content for a surface the way the content sink does.
func setContent(m *WorkbenchModel, plugin string, p protocol.SurfaceUpdateParams) {
	m.content(plugin, p)
}

func (f *fakeFE) lastKey() (keySent, bool) {
	if len(f.Keys) == 0 {
		return keySent{}, false
	}
	return f.Keys[len(f.Keys)-1], true
}

func (f *fakeFE) keysTo(plugin string) []string {
	var out []string
	for _, k := range f.Keys {
		if k.Plugin == plugin {
			out = append(out, k.P.Key)
		}
	}
	return slices.Clone(out)
}

// hello is what a test declares about a plugin: the surfaces it occupies and
// the published slots it owns.
type hello struct {
	PluginID       string
	Slots          []protocol.SurfaceID
	PublishedSlots []slotDecl
	Frames         map[protocol.SurfaceID]protocol.FrameSpec
	RailItems      []contracts.RailItem
	Binds          []bind
}

// bind stands in for a key a plugin's keymap document binds: Focus empty is
// global, otherwise it scopes the binding to that surface.
type bind struct{ Key, Action, Focus string }

type slotDecl struct {
	ID   string
	Kind string
	Rect *contracts.PublishedRect
}

// addPublicationTestPlugin makes the plugin live with a recording frontend and
// returns its outbound view.
func addPublicationTestPlugin(m *WorkbenchModel, h hello) (string, *pluginWire) {
	fe := withFE(m)
	m.plugins[h.PluginID] = true
	for _, s := range h.Slots {
		m.catalog.Occupants[s] = h.PluginID
	}
	if h.Frames != nil {
		m.catalog.Frames[h.PluginID] = h.Frames
	}
	for _, ri := range h.RailItems {
		ri.Plugin = h.PluginID
		m.railItems = append(m.railItems, ri)
		fe.Rails = append(fe.Rails, ri)
	}
	for _, b := range h.Binds {
		fe.Extra = append(fe.Extra, defaultBinding{key: b.Key, action: b.Action, focus: b.Focus})
	}
	for _, d := range h.PublishedSlots {
		addSlot(m, d.ID, h.PluginID, d.Kind, d.Rect)
	}
	m.recomputeLayout()
	return h.PluginID, &pluginWire{fe: fe, id: h.PluginID}
}

// newPublicationRegistry is an empty catalog; the render tests build slots on
// it directly.
func newPublicationRegistry() *surfaceCatalog { return newSurfaceCatalog() }

// mustDeclare declares an inline slot owned by owner.
func mustDeclare(t testing.TB, r *surfaceCatalog, owner, id string) {
	t.Helper()
	if err := r.declareSlot(owner, slotDecl{ID: id, Kind: "inline"}); err != nil {
		t.Fatal(err)
	}
}

// declareSlot declares a published slot (inline or region) of owner.
func (r *surfaceCatalog) declareSlot(owner string, d slotDecl) error {
	if d.Kind != "inline" && d.Kind != "region" {
		return fmt.Errorf("slot %s: kind %q", d.ID, d.Kind)
	}
	r.Slots[d.ID] = &publishedSlot{ID: d.ID, Owner: owner, Kind: d.Kind, Rect: d.Rect}
	return nil
}

// mustContribute appends a contribution from plugin to a declared slot.
func mustContribute(t testing.TB, r *surfaceCatalog, plugin, slot, id, content string) {
	t.Helper()
	s := r.Slots[slot]
	if s == nil {
		t.Fatalf("slot %s not declared", slot)
	}
	s.Contributions = append(s.Contributions, contribution{PluginID: plugin, ID: id, Content: content, OnClick: "b.click"})
}

// snapshot builds the State a host would hold: Pod statuses by plugin and the
// Surfaces resolution slice.
func snapshot(seq uint64, pods map[string]string, res surfaces.Surfaces, lastErr ...map[string]string) *state.Snapshot {
	pd := struct {
		Plugins map[string]map[string]string `json:"plugins"`
	}{Plugins: map[string]map[string]string{}}
	for id, st := range pods {
		pd.Plugins[id] = map[string]string{"status": st}
		for _, errs := range lastErr {
			if e := errs[id]; e != "" {
				pd.Plugins[id]["last_error"] = e
			}
		}
	}
	pj, _ := json.Marshal(pd)
	rj, _ := json.Marshal(res)
	return &state.Snapshot{Seq: seq, Slices: map[string]state.Entry{
		state.PodsNamespace:     {Value: pj},
		state.SurfacesNamespace: {Value: rj},
	}}
}

// claimSurface records plugin as the occupant of surface.
func (r *surfaceCatalog) claimSurface(plugin string, surface protocol.SurfaceID) error {
	r.Occupants[surface] = plugin
	return nil
}

// publish rebuilds Snap from the fake host's Pods and occupancy.
func (f *fakeFE) publish() {
	res := surfaces.Surfaces{Occupants: map[protocol.SurfaceID]contracts.Occupant{}}
	for s, p := range f.occ {
		res.Occupants[s] = contracts.Occupant{Surface: s, Plugin: p}
	}
	f.seq++
	f.Snap = snapshot(f.seq, f.pods, res, f.podErr)
}

// hostPlugin brings a plugin live through the same path a real host does:
// State changes, the model refreshes from it and lays out.
func hostPlugin(m *WorkbenchModel, id string, slots ...protocol.SurfaceID) {
	fe := withFE(m)
	if fe.pods == nil {
		fe.pods, fe.occ = map[string]string{}, map[protocol.SurfaceID]string{}
	}
	fe.pods[id] = "live"
	for _, s := range slots {
		fe.occ[s] = id
	}
	fe.publish()
	m.refresh()
}

// hostLeave makes a plugin exit: its Pod ends and its surfaces free up.
func hostLeave(m *WorkbenchModel, id string) {
	fe := withFE(m)
	fe.pods[id] = "exited"
	for s, p := range fe.occ {
		if p == id {
			delete(fe.occ, s)
		}
	}
	fe.publish()
	m.refresh()
}

// bind adds a key for an action, as a plugin's keymap document would.
func (f *fakeFE) bind(key, action string) {
	f.Extra = append(f.Extra, defaultBinding{key: key, action: action})
}
