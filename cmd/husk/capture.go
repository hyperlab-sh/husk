package main

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type captureTimeoutMsg struct{ gen uint64 }

func (m *WorkbenchModel) releaseCapture() {
	if m.keyContext().Capture == protocol.CaptureNone {
		return
	}
	m.captureReleased = true
	m.captureGen++
	msg := captureTimeoutMsg{gen: m.captureGen}
	if m.after != nil {
		m.cmds = append(m.cmds, m.after(sequenceTimeout, msg))
	} else {
		m.cmds = append(m.cmds, tea.Tick(sequenceTimeout, func(_ time.Time) tea.Msg { return msg }))
	}
}
