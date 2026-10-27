package shim

import (
	"encoding/json"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func (r *runner) checkPaneLifetime(p *pane) {
	ctx, done := r.bounded(r.ctx, 2*time.Second)
	defer done()
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			last = ctx.Err()
			break
		}
		raw, err := r.o.Host.Call(ctx, protocol.MethodStateGet, map[string]any{"slice": "husk.surfaces"})
		if err != nil {
			last = err
			continue
		}
		var snapshot struct {
			Value struct {
				Runtime map[protocol.SurfaceID]json.RawMessage `json:"runtime"`
			} `json:"value"`
		}
		if err = json.Unmarshal(raw, &snapshot); err != nil {
			last = err
			continue
		}
		if snapshot.Value.Runtime[p.id] == nil {
			r.closePaneFrom(p, true)
		}
		return
	}
	r.logLine(map[string]any{"level": "warning", "code": "runtime_state_check_failed", "surface": p.id, "message": last.Error()})
}
