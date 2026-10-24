package surfaces

import (
	"slices"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// SurfaceOpen serves surface.open. Runtime occupancy is engine-owned and never
// enters the Pod declaration slices. A reply observes its own State write.
func (e *Engine) SurfaceOpen(plugin string, p protocol.SurfaceOpenParams) (protocol.SurfaceOpenResult, error) {
	fail := func(code protocol.Code, message string) (protocol.SurfaceOpenResult, error) {
		return protocol.SurfaceOpenResult{}, errf(code, "%s", message)
	}
	if plugin == "" || !e.live.Live(plugin) {
		return fail(protocol.CodeDenied, "surface.open requires a live plugin")
	}
	if err := p.Validate(); err != nil {
		return fail(protocol.CodeInvalidParams, err.Error())
	}
	id := protocol.SurfaceID(plugin + "." + p.Name)
	if !id.Valid() {
		return fail(protocol.CodeInvalidParams, "invalid plugin surface namespace")
	}
	count := 0
	for _, r := range e.runtime {
		if r.Surface == id {
			return fail(protocol.CodeConflict, "surface name is already open")
		}
		if r.Plugin == plugin {
			count++
		}
	}
	if count >= 8 {
		return protocol.SurfaceOpenResult{}, &Error{Code: protocol.CodeBusy, Message: "runtime surface limit reached", Data: map[string]any{"reason": "too_many_surfaces"}}
	}
	if p.Placement == protocol.SurfacePlacementModal {
		overlays := e.slice.Get().Overlays
		busy := overlays.Modal.Plugin != "" || overlays.SettingsCenter != ""
		for _, r := range e.runtime {
			busy = busy || r.Placement == protocol.SurfacePlacementModal
		}
		if busy {
			return protocol.SurfaceOpenResult{}, &Error{Code: protocol.CodeBusy, Message: "a modal is open", Data: map[string]any{"reason": "modal_open"}}
		}
	}
	r := contracts.RuntimeSurface{Surface: id, Plugin: plugin, Placement: p.Placement, Title: p.Title, Width: slices.Clone(p.Width), Height: slices.Clone(p.Height)}
	if p.Frame != nil {
		frame := *p.Frame
		r.Frame = &frame
	}
	e.absorbForeign()
	if err := e.slice.Update(func(v *Surfaces) {
		if v.Runtime == nil {
			v.Runtime = map[protocol.SurfaceID]contracts.RuntimeSurface{}
		}
		if v.Occupants == nil {
			v.Occupants = Occupants{}
		}
		v.Runtime[id] = r
		v.Occupants[id] = Occupant{Surface: id, Plugin: plugin}
	}); err != nil {
		return protocol.SurfaceOpenResult{}, err
	}
	e.runtime = append(e.runtime, r)
	e.markRev()
	return protocol.SurfaceOpenResult{Surface: id}, nil
}

// SurfaceFocus serves surface.focus without changing a surface's lifetime.
// Moving the declaration to the end promotes a canvas in the frontend stack.
func (e *Engine) SurfaceFocus(plugin string, p protocol.SurfaceFocusParams) error {
	if plugin == "" || !e.live.Live(plugin) {
		return errf(protocol.CodeDenied, "surface.focus requires a live plugin")
	}
	if !p.Surface.Valid() {
		return errf(protocol.CodeInvalidParams, "invalid surface")
	}
	for i, r := range e.runtime {
		if r.Surface != p.Surface {
			continue
		}
		if r.Plugin != plugin {
			return errf(protocol.CodeDenied, "surface belongs to another plugin")
		}
		if r.Placement == protocol.SurfacePlacementModal {
			o := e.slice.Get().Overlays
			if o.SettingsCenter != "" || o.Modal.Plugin != "" && o.Modal.Surface != p.Surface {
				return &Error{Code: protocol.CodeBusy, Message: "a modal is open", Data: map[string]any{"reason": "modal_open"}}
			}
		}
		r.FocusRevision++
		e.runtime = append(slices.Delete(e.runtime, i, i+1), r)
		return nil
	}
	if occ := e.slice.Get().Occupants[p.Surface]; occ.Plugin != "" && occ.Plugin != plugin {
		return errf(protocol.CodeDenied, "surface belongs to another plugin")
	}
	return errf(protocol.CodeNotFound, "runtime surface not found")
}

// RuntimeSurfaces returns detached runtime declarations in opening order.
// Geometry, visibility and focus remain the frontend's responsibility. Loop-only.
func (e *Engine) RuntimeSurfaces() []contracts.RuntimeSurface {
	out := slices.Clone(e.runtime)
	for i := range out {
		out[i].Width = slices.Clone(out[i].Width)
		out[i].Height = slices.Clone(out[i].Height)
		if out[i].Frame != nil {
			f := *out[i].Frame
			out[i].Frame = &f
		}
	}
	return out
}

// notifyRuntimeClosed sends the final visibility fact before dropping occupancy.
// This is best-effort even for a Pod that just left live; a dead channel may
// refuse it, but no later resize is sent for the removed runtime surface.
func (e *Engine) notifyRuntimeClosed(r contracts.RuntimeSurface) {
	msg, err := notification(protocol.MethodSurfaceResized, protocol.SurfaceResizedParams{Surfaces: []protocol.SurfaceSize{{Surface: r.Surface, Visible: false}}})
	if err != nil {
		return
	}
	for w := range e.pluginWatchers[r.Plugin] {
		if err := w.n.Notify(msg); err != nil {
			e.log.Printf("surfaces: runtime close notification: %v", err)
		}
	}
}
