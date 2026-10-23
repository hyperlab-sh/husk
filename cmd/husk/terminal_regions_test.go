package main

import (
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestTerminalNonRegionSurfacesDraw(t *testing.T) {
	for _, id := range []protocol.SurfaceID{protocol.SurfaceActivityRail, protocol.SurfaceAppOverlay} {
		t.Run(string(id), func(t *testing.T) {
			m, fe, term, _ := terminalModel(t, false)
			m.catalog.Occupants[id] = "stage"
			fe.TerminalDrawers = map[protocol.SurfaceID]contracts.TerminalDrawer{id: testDrawer{term}}
			term.Write([]byte("PTY"))
			if !strings.Contains(m.View().Content, "PTY") {
				t.Fatalf("terminal on %s did not draw", id)
			}
		})
	}
}
