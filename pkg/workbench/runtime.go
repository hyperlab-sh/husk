package workbench

import (
	"reflect"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// runtimeCanvas returns the shown top of the instance-wide opening-order stack.
func (m *workbenchModel) runtimeCanvas() protocol.SurfaceID {
	if m.runtimeHidden {
		return ""
	}
	return topCanvas(m.runtime)
}

func topCanvas(rs []contracts.RuntimeSurface) protocol.SurfaceID {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].Placement == protocol.SurfacePlacementCanvas {
			return rs[i].Surface
		}
	}
	return ""
}

func (m *workbenchModel) runtimeSurface(id protocol.SurfaceID) (contracts.RuntimeSurface, bool) {
	for _, r := range m.runtime {
		if r.Surface == id {
			return r, true
		}
	}
	return contracts.RuntimeSurface{}, false
}

// syncRuntime runs during Update, never View. The underlying rail placement is
// left untouched while a runtime canvas is shown, so it can be restored intact.
func (m *workbenchModel) syncRuntime() bool {
	if m.fe == nil {
		return false
	}
	next := m.fe.RuntimeSurfaces()
	if reflect.DeepEqual(next, m.runtime) {
		return false
	}
	oldTop := topCanvas(m.runtime)
	newTop := topCanvas(next)
	opened := false
	for _, r := range next {
		if _, had := m.runtimeSurface(r.Surface); !had {
			if r.Placement == protocol.SurfacePlacementCanvas {
				opened = true
			}
			if r.Placement == protocol.SurfacePlacementModal {
				m.closeSettings()
				m.modal = modalState{PluginID: r.Plugin, Surface: r.Surface}
			}
		}
	}
	if m.runtimeCanvas() == "" && newTop != "" {
		m.runtimeFocus = m.focusedSlot
	}
	wasShown := !m.runtimeHidden
	m.runtime = next
	if opened {
		m.runtimeHidden = false
	}
	if newTop == "" {
		m.runtimeHidden = false
		if oldTop != "" && wasShown {
			m.setFocus(m.runtimeFocus)
		}
	} else if opened || wasShown && newTop != oldTop {
		m.setFocus(newTop)
	}
	if !m.modal.Surface.Layout() && m.modal.Surface != "" {
		if _, exists := m.runtimeSurface(m.modal.Surface); !exists {
			m.clearPending()
			m.modal = modalState{}
		}
	}
	m.recomputeLayout()
	return true
}

// hideRuntime reports whether it hid a shown canvas stack.
func (m *workbenchModel) hideRuntime() bool {
	if m.runtimeCanvas() == "" {
		return false
	}
	m.runtimeHidden = true
	m.setFocus(m.runtimeFocus)
	return true
}

func (m *workbenchModel) restoreRuntime() {
	if !m.runtimeHidden || topCanvas(m.runtime) == "" {
		return
	}
	m.runtimeFocus = m.focusedSlot
	m.runtimeHidden = false
	m.setFocus(topCanvas(m.runtime))
	m.recomputeLayout()
}

func (m *workbenchModel) closeRuntime(id protocol.SurfaceID) {
	if m.fe == nil {
		return
	}
	if err := m.fe.CloseSurface(id); err != nil {
		m.logf("closing runtime surface %s: %v", id, err)
		return
	}
	m.syncRuntime()
}

// modalSurface is the actual id for geometry, content and every input path.
func (m *workbenchModel) modalSurface() protocol.SurfaceID {
	if m.modal.Surface != "" {
		return m.modal.Surface
	}
	return protocol.SurfaceModal
}

func (m *workbenchModel) runtimeModalRect(id protocol.SurfaceID) slotRect {
	r, _ := m.runtimeSurface(id)
	w := r.Width.Resolve(m.width, min(92, m.width-10), 20, m.width-10)
	h := r.Height.Resolve(m.height, min(24, m.height-6), 6, m.height-6)
	if m.width < 30 || m.height < 12 {
		w, h = m.width, m.height
	}
	return m.frameRect(id, max(0, (m.width-w)/2), max(0, (m.height-h)/2), w, h)
}
