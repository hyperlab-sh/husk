package surfaces

import (
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/terminals"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type attachment struct {
	plugin string
	term   *terminals.Terminal
}

// TerminalAttach allocates an emulator only for the current live occupant.
// send is a nonblocking, worker-safe offer to that occupant's outbound queue.
func (e *Engine) TerminalAttach(plugin string, p protocol.TerminalAttachParams, size protocol.TerminalAttachResult, send func([]byte)) (protocol.TerminalAttachResult, error) {
	if err := p.Validate(); err != nil {
		return size, errf(protocol.CodeInvalidParams, "%v", err)
	}
	if plugin == "" || e.slice.Get().Occupants[p.Surface].Plugin != plugin || !e.live.Live(plugin) {
		return size, errf(protocol.CodeDenied, "terminal.attach requires the live occupant")
	}
	if _, ok := e.terminals[p.Surface]; ok {
		return size, errf(protocol.CodeConflict, "surface already has a terminal")
	}
	if size.Width < 1 || size.Height < 1 {
		return size, errf(protocol.CodeUnavailable, "surface geometry is not available")
	}
	history := 2000
	if p.Scrollback != nil {
		history = *p.Scrollback
	}
	if e.terminals == nil {
		e.terminals = map[protocol.SurfaceID]attachment{}
	}
	term := terminals.New(size.Width, size.Height, history, send)
	e.terminals[p.Surface] = attachment{plugin, term}
	term.Focus(e.terminalFocus[p.Surface])
	e.terminalDirty = true
	capture := protocol.CaptureAll
	if p.Capture != nil {
		capture = p.Capture.Mode()
	}
	e.absorbForeign()
	if err := e.slice.Update(func(v *Surfaces) {
		if v.Capture == nil {
			v.Capture = map[protocol.SurfaceID]protocol.CaptureMode{}
		}
		if v.Terminals == nil {
			v.Terminals = map[protocol.SurfaceID]contracts.TerminalMetadata{}
		}
		if capture == protocol.CaptureNone {
			delete(v.Capture, p.Surface)
		} else {
			v.Capture[p.Surface] = capture
		}
		v.Terminals[p.Surface] = contracts.TerminalMetadata{}
	}); err != nil {
		term.Close()
		delete(e.terminals, p.Surface)
		return size, err
	}
	e.markRev()
	return size, nil
}

// Terminal returns a loop-only emulator view, or nil when unattached.
func (e *Engine) Terminal(surface protocol.SurfaceID) *terminals.Terminal {
	a, ok := e.terminals[surface]
	if !ok {
		return nil
	}
	return a.term
}

// TerminalWrite validates the chunk before checking the attachment. Refused
// notifications are logged and dropped by the caller, never painted as lines.
func (e *Engine) TerminalWrite(plugin string, p protocol.TerminalWriteParams) error {
	b, err := p.Decode()
	if err != nil {
		return errf(protocol.CodeInvalidParams, "%v", err)
	}
	a, ok := e.terminals[p.Surface]
	if !ok || a.plugin != plugin || e.slice.Get().Occupants[p.Surface].Plugin != plugin || !e.live.Live(plugin) {
		e.log.Printf("terminals: dropped write to unattached or foreign surface %s", p.Surface)
		return nil
	}
	before := a.term.Metadata()
	a.term.Write(b)
	if a.term.Metadata() != before {
		e.terminalDirty = true
	}
	return nil
}

// TerminalDetach discards an attachment without releasing its surface claim.
func (e *Engine) TerminalDetach(plugin string, p protocol.TerminalDetachParams) error {
	if !p.Surface.Valid() {
		return errf(protocol.CodeInvalidParams, "invalid surface")
	}
	if plugin == "" || e.slice.Get().Occupants[p.Surface].Plugin != plugin {
		return errf(protocol.CodeDenied, "terminal.detach requires the occupant")
	}
	e.detachTerminal(p.Surface)
	return nil
}

func (e *Engine) detachTerminal(surface protocol.SurfaceID) {
	a, ok := e.terminals[surface]
	if !ok {
		return
	}
	a.term.Close()
	delete(e.terminals, surface)
	e.absorbForeign()
	if err := e.slice.Update(func(v *Surfaces) { delete(v.Terminals, surface); delete(v.Capture, surface) }); err != nil {
		e.log.Printf("terminals: detach: %v", err)
	}
	e.markRev()
}

func (e *Engine) flushTerminals() {
	if !e.terminalDirty {
		return
	}
	e.terminalDirty = false
	if len(e.terminals) == 0 {
		return
	}
	current := map[protocol.SurfaceID]contracts.TerminalMetadata{}
	for id, a := range e.terminals {
		m := a.term.Metadata()
		current[id] = contracts.TerminalMetadata{Title: m.Title, AltScreen: m.AltScreen, Mouse: m.Mouse, Scrollback: m.Scrollback}
	}
	e.absorbForeign()
	if err := e.slice.Update(func(v *Surfaces) { v.Terminals = current }); err != nil {
		e.log.Printf("terminals: metadata: %v", err)
	}
	e.markRev()
}

// CloseTerminals joins every input drain during host shutdown.
func (e *Engine) CloseTerminals() {
	for id := range e.terminals {
		e.detachTerminal(id)
	}
}
