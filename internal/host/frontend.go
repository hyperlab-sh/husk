package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	portsservices "github.com/hyperlab-sh/husk/internal/host/ports/services"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	svccontracts "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/dispatcher"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

var _ contracts.Frontend = (*Root)(nil)

// Frontend implements contracts.Host: the root is the frontend's only door
// into the host.
func (r *Root) Frontend() contracts.Frontend { return r }

// Snapshot implements contracts.Frontend.
func (r *Root) Snapshot() *state.Snapshot { return r.st.Snapshot() }

// StartupReports implements contracts.Frontend.
func (r *Root) StartupReports() []contracts.ReloadReport {
	return portsservices.Reports(svccontracts.Clone(r.startupReports))
}

// Palette implements contracts.Frontend.
func (r *Root) Palette() protocol.Palette { return r.rt.Theme.Wire() }

func (r *Root) keys() portsservices.Keys { return portsservices.Keys{Svc: r.rt.Keybinding} }

// Resolve implements contracts.Frontend.
func (r *Root) Resolve(ctx contracts.KeyContext, keys []string) contracts.KeyResolution {
	return r.keys().Resolve(ctx, keys)
}

// Hints implements contracts.Frontend.
func (r *Root) Hints(ctx contracts.KeyContext) []contracts.KeyHint { return r.keys().Hints(ctx) }

// KeysFor implements contracts.Frontend.
func (r *Root) KeysFor(action string) []string { return r.keys().KeysFor(action) }

// InvokeAction implements contracts.Frontend. A key binding arrives as
// CallerInput with Input's own focus snapshot; an action the host starts
// itself arrives as CallerHost with none. Loop only.
func (r *Root) InvokeAction(action string, args map[string]any, kind protocol.CallerKind, focus *protocol.FocusContext, reply func(map[string]any, bool, error)) {
	if reply == nil {
		reply = func(map[string]any, bool, error) {}
	}
	req := dispatcher.ActionRequest{Action: action, Args: args, Caller: dispatcher.Caller{Kind: kind}}
	switch {
	case kind == protocol.CallerInput && focus == nil:
		r.logf("frontend: InvokeAction(%q) as %s without a focus snapshot; not dispatched", action, kind)
		reply(nil, false, contracts.NewError(protocol.CodeInvalidParams, "an input action needs the focus snapshot of its key press", nil))
		return
	case kind == protocol.CallerHost && focus != nil:
		r.logf("frontend: InvokeAction(%q) as %s with a focus snapshot; not dispatched", action, kind)
		reply(nil, false, contracts.NewError(protocol.CodeInvalidParams, "a host action carries no focus snapshot", nil))
		return
	case kind != protocol.CallerInput && kind != protocol.CallerHost:
		reply(nil, false, contracts.NewError(protocol.CodeDenied, fmt.Sprintf("caller kind %q may not use InvokeAction", kind), nil))
		return
	}
	if focus != nil {
		req.Focus = *focus
	}
	req.Reply = func(res dispatcher.ActionReply) {
		switch {
		case res.Err != nil:
			reply(nil, false, contracts.NewError(protocol.Code(res.Err.Code), res.Err.Message, res.Err.Data))
		case res.Declined:
			reply(nil, true, nil)
		default:
			out := res.Result
			if out == nil {
				out = map[string]any{}
			}
			reply(out, false, nil)
		}
	}
	r.disp.Invoke(req)
}

// RegisterHostAction implements contracts.Frontend. Loop only.
func (r *Root) RegisterHostAction(id string, handler contracts.HostActionHandler) error {
	if handler == nil {
		delete(r.hostActions, id)
		r.disp.RegisterHost(id, nil)
		return nil
	}
	if !dispatcher.ValidActionID(id) {
		return contracts.NewError(protocol.CodeInvalidParams, fmt.Sprintf("host action id %q is not a well-formed action id", id), nil)
	}
	decls := r.declaredPlugins()
	if owner, ok := decls.actions[id]; ok {
		return contracts.NewError(protocol.CodeConflict, fmt.Sprintf("action %q belongs to plugin %q; a host action would shadow it", id, owner), nil)
	}
	if !strings.HasPrefix(id, "husk.") && !decls.railIDs[id] {
		return contracts.NewError(protocol.CodeInvalidParams, fmt.Sprintf("host action %q is neither a husk.* action nor a declared rail item id", id), nil)
	}
	if r.hostActions == nil {
		r.hostActions = map[string]bool{}
	}
	r.hostActions[id] = true
	r.disp.RegisterHost(id, func(req dispatcher.ActionRequest) dispatcher.ActionReply {
		res, err := handler(req.Args)
		if errors.Is(err, contracts.ErrDeclined) {
			return dispatcher.ActionReply{Declined: true}
		}
		if err != nil {
			var ce contracts.CodedError
			if errors.As(err, &ce) {
				var data map[string]any
				var de contracts.DataError
				if errors.As(err, &de) {
					data = de.Data()
				}
				return dispatcher.ActionReply{Err: &dispatcher.ActionError{Code: ce.Code(), Message: ce.Error(), Data: data}}
			}
			return dispatcher.ActionReply{Err: &dispatcher.ActionError{Code: string(protocol.CodeInternal), Message: err.Error()}}
		}
		if res == nil {
			res = map[string]any{}
		}
		return dispatcher.ActionReply{Result: res}
	})
	return nil
}

// hostActionDefs lists the registered host actions for the Dispatcher's
// catalog. Loop-only, like every catalog read.
func (r *Root) hostActionDefs() []dispatcher.ActionDef {
	defs := make([]dispatcher.ActionDef, 0, len(r.hostActions))
	for id := range r.hostActions {
		defs = append(defs, dispatcher.ActionDef{ID: id})
	}
	return defs
}

// SendKey implements contracts.Frontend. Loop only.
func (r *Root) SendKey(plugin string, params protocol.SurfaceKeyParams) error {
	if err := params.Validate(); err != nil {
		return invalidParams(err)
	}
	return r.deliver(plugin, protocol.MethodInputKey, params)
}

// SendMouse implements contracts.Frontend. Loop only.
func (r *Root) SendMouse(plugin string, params protocol.SurfaceMouseParams) error {
	if !params.Surface.Valid() {
		return contracts.NewError(protocol.CodeInvalidParams, fmt.Sprintf("mouse delivery requires a valid surface, got %q", params.Surface), nil)
	}
	return r.deliver(plugin, protocol.MethodInputMouse, params)
}

// SendKeyState implements contracts.Frontend. Loop only.
func (r *Root) SendKeyState(plugin string, params protocol.KeyStateParams) error {
	if err := params.Validate(); err != nil {
		return invalidParams(err)
	}
	return r.deliver(plugin, protocol.MethodInputKeyState, params)
}

func invalidParams(err error) error {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return contracts.NewError(perr.Code, perr.Message, nil)
	}
	return contracts.NewError(protocol.CodeInvalidParams, err.Error(), nil)
}

// deliver sends one notification to plugin's Pod: not_found for an id the
// host never added, unavailable for a Pod that is not live.
func (r *Root) deliver(plugin, method string, params any) error {
	pod, ok := r.pods.Pod(plugin)
	if !ok {
		return contracts.NewError(protocol.CodeNotFound, fmt.Sprintf("no plugin %q", plugin), nil)
	}
	if err := pod.Notify(method, params); err != nil {
		if errors.Is(err, wrapper.ErrNotLive) {
			return contracts.NewError(protocol.CodeUnavailable, fmt.Sprintf("plugin %q is not live", plugin), nil)
		}
		return contracts.NewError(protocol.CodeFailed, err.Error(), nil)
	}
	return nil
}

// declaredPlugin is what the frontend reads from one declared plugins
// document.
type declaredPlugin struct {
	Actions []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"actions"`
	RailItems      []contracts.RailItem `json:"rail_items"`
	PublishedSlots []struct {
		ID   string                   `json:"id"`
		Kind string                   `json:"kind"`
		Rect *contracts.PublishedRect `json:"rect"`
	} `json:"published_slots"`
	Surfaces []struct {
		ID    protocol.SurfaceID  `json:"id"`
		Frame *protocol.FrameSpec `json:"frame"`
	} `json:"surfaces"`
}

// declaredCatalog is everything the frontend reads from the declared plugins
// documents.
type declaredCatalog struct {
	index  declaredIndex
	rail   []contracts.RailItem
	slots  []contracts.PublishedSlot
	frames map[string]map[protocol.SurfaceID]protocol.FrameSpec
	acts   []contracts.PluginAction
}

// declaredIndex indexes the declared plugins documents.
type declaredIndex struct {
	railIDs map[string]bool
	actions map[string]string // action id -> declaring plugin
}

// declaredPlugins decodes every declared plugins document, in plugin id
// order. A document that does not decode is skipped and reported once.
func (r *Root) declaredPlugins() declaredIndex {
	idx, _ := r.readDeclared()
	return idx
}

func (r *Root) readDeclared() (declaredIndex, []contracts.RailItem) {
	c := r.readCatalog()
	return c.index, c.rail
}

// readCatalog returns the decoded catalog, rebuilt only when the Store's
// declared-documents revision moved. Callers must not mutate the result. Any
// goroutine.
func (r *Root) readCatalog() declaredCatalog {
	rev := r.store.DeclaredRevision()
	r.catMu.Lock()
	defer r.catMu.Unlock()
	if r.catValid && r.catRev == rev {
		return r.cat
	}
	r.cat, r.catRev, r.catValid = r.buildCatalog(), rev, true
	return r.cat
}

func (r *Root) buildCatalog() declaredCatalog {
	docs := r.store.Declared(store.DeclaredPlugins)
	ids := make([]string, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	idx := declaredIndex{railIDs: map[string]bool{}, actions: map[string]string{}}
	items := []contracts.RailItem{}
	slots := []contracts.PublishedSlot{}
	frames := map[string]map[protocol.SurfaceID]protocol.FrameSpec{}
	acts := []contracts.PluginAction{}
	for _, id := range ids {
		raw, err := json.Marshal(docs[id])
		var d declaredPlugin
		if err == nil {
			err = json.Unmarshal(raw, &d)
		}
		if err != nil {
			r.warnDeclaredOnce(id, err)
			continue
		}
		for _, a := range d.Actions {
			idx.actions[a.ID] = id
			acts = append(acts, contracts.PluginAction{Plugin: id, ID: a.ID, Title: a.Title, Description: a.Description})
		}
		for _, item := range d.RailItems {
			item.Plugin = id
			idx.railIDs[item.ID] = true
			items = append(items, item)
		}
		for _, sl := range d.PublishedSlots {
			slots = append(slots, contracts.PublishedSlot{Plugin: id, ID: sl.ID, Kind: sl.Kind, Rect: sl.Rect})
		}
		for _, sf := range d.Surfaces {
			if sf.Frame == nil {
				continue
			}
			if frames[id] == nil {
				frames[id] = map[protocol.SurfaceID]protocol.FrameSpec{}
			}
			frames[id][sf.ID] = *sf.Frame
		}
	}
	return declaredCatalog{index: idx, rail: items, slots: slots, frames: frames, acts: acts}
}

// warnDeclaredOnce reports an undecodable plugins document the first time it
// is seen for plugin id. Any goroutine.
func (r *Root) warnDeclaredOnce(id string, err error) {
	r.declWarnMu.Lock()
	defer r.declWarnMu.Unlock()
	if r.declWarned == nil {
		r.declWarned = map[string]bool{}
	}
	if r.declWarned[id] {
		return
	}
	r.declWarned[id] = true
	r.log(pods.LogMsg{PluginID: id, Level: "warn", Message: fmt.Sprintf("declared plugins document skipped: %v", err)})
}

// RailItems implements contracts.Frontend.
func (r *Root) RailItems() []contracts.RailItem {
	_, items := r.readDeclared()
	return items
}

// PublishedSlots implements contracts.Frontend.
func (r *Root) PublishedSlots() []contracts.PublishedSlot {
	return r.readCatalog().slots
}

// SurfaceFrames implements contracts.Frontend.
func (r *Root) SurfaceFrames() map[string]map[protocol.SurfaceID]protocol.FrameSpec {
	return r.readCatalog().frames
}

// Actions implements contracts.Frontend.
func (r *Root) Actions() []contracts.PluginAction {
	return r.readCatalog().acts
}

// refuse records a plugin the host refused before a Pod existed. The caller
// logs it; Refusals hands it to the frontend.
func (r *Root) refuse(plugin, reason string) {
	r.refusalsMu.Lock()
	r.refusals = append(r.refusals, contracts.PluginRefusal{Plugin: plugin, Reason: reason})
	r.refusalsMu.Unlock()
}

// Refusals implements contracts.Frontend.
func (r *Root) Refusals() []contracts.PluginRefusal {
	r.refusalsMu.Lock()
	defer r.refusalsMu.Unlock()
	return slices.Clone(r.refusals)
}

// ActiveToast implements contracts.Frontend.
func (r *Root) ActiveToast() (contracts.Toast, bool) {
	t, ok := r.engine.ActiveToast()
	if !ok {
		return contracts.Toast{}, false
	}
	return contracts.Toast{Plugin: t.Plugin, Level: t.Level, Title: t.Title, Message: t.Message}, true
}

// Tick implements Looped: the end-of-Update flushes, for a frontend write
// that was not caused by a host message.
func (r *Root) Tick() { r.tick() }

// OwnSlices implements contracts.Frontend.
func (r *Root) OwnSlices() (contracts.FrontendSlices, error) {
	if r.frontendSlices != nil {
		return contracts.FrontendSlices{}, errors.New("host: frontend slices already registered")
	}
	reg := func(namespace string, persistent bool) (*state.Handle, error) {
		spec := state.Spec{Namespace: namespace, Owner: state.OwnerHusk, Scope: state.ScopeInstance}
		if persistent {
			spec.Persistent = true
			spec.Retention = state.Retention{Policy: state.KeepLatest}
		}
		return r.st.Register(spec)
	}
	var s contracts.FrontendSlices
	var err error
	if s.Layout, err = reg(state.LayoutNamespace, true); err != nil {
		return contracts.FrontendSlices{}, err
	}
	if s.Geometry, err = reg(state.GeometryNamespace, false); err != nil {
		s.Layout.Unregister()
		return contracts.FrontendSlices{}, err
	}
	if s.Focus, err = reg(state.FocusNamespace, false); err != nil {
		s.Layout.Unregister()
		s.Geometry.Unregister()
		return contracts.FrontendSlices{}, err
	}
	r.frontendSlices = &s
	return s, nil
}

// UpdateSurfaceView implements contracts.Frontend.
func (r *Root) UpdateSurfaceView(fn func(*contracts.SurfaceView)) error {
	return r.engine.UpdateView(fn)
}
