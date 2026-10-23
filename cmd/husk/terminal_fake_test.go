package main

import (
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func (f *fakeFE) TerminalView(id protocol.SurfaceID) contracts.TerminalDrawer {
	if f.TerminalDrawers == nil {
		return nil
	}
	return f.TerminalDrawers[id]
}
func (f *fakeFE) SendTerminalKey(id protocol.SurfaceID, key uv.KeyPressEvent) {
	f.TerminalKeys = append(f.TerminalKeys, key)
	if term, ok := f.TerminalDrawers[id].(interface{ Key(uv.KeyPressEvent) }); ok {
		term.Key(key)
	}
}
func (f *fakeFE) SendTerminalMouse(id protocol.SurfaceID, event uv.MouseEvent) {
	f.TerminalMice = append(f.TerminalMice, event)
	if term, ok := f.TerminalDrawers[id].(interface{ Mouse(uv.MouseEvent) }); ok {
		term.Mouse(event)
	}
}
func (f *fakeFE) SendTerminalPaste(id protocol.SurfaceID, text string) {
	f.TerminalPastes = append(f.TerminalPastes, text)
	if term, ok := f.TerminalDrawers[id].(interface{ Paste(string) }); ok {
		term.Paste(text)
	}
}
