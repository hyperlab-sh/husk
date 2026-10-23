package stem

import (
	"context"
	"encoding/json"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// State adapts contracts.StatePort for the state.* methods: reads and the
// snapshot are loop-free, writes and clears are posted to the loop by the
// port, and watch registers the connection's notifier for state.changed
// notifications. Field requirements live here; slice ownership and caps live
// in the port.
type State struct {
	Port contracts.StatePort
}

type getParams struct {
	Slice string `json:"slice"`
	Path  string `json:"path"`
}

type setParams struct {
	Slice string          `json:"slice"`
	Value json.RawMessage `json:"value"`
}

type patchParams struct {
	Slice      string          `json:"slice"`
	Patch      json.RawMessage `json:"patch"`
	IfRevision *uint64         `json:"if_revision"`
}

type snapshotParams struct {
	Values *bool `json:"values"`
}

type stateWatchParams struct {
	Slices []string `json:"slices"`
}

type clearParams struct {
	Namespace string `json:"namespace"`
	Force     bool   `json:"force"`
}

type revisionResult struct {
	Slice    string `json:"slice"`
	Revision uint64 `json:"revision"`
}

// Serve implements contracts.Handler.
func (h State) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, n contracts.Notifier) protocol.Message {
	if deny := handlers.Admit(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodStateGet:
		var p getParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.slice", p.Slice); bad != nil {
			return *bad
		}
		res, err := h.Port.Get(ctx, c, p.Slice, p.Path)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, res)
	case protocol.MethodStateSet:
		var p setParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.slice", p.Slice); bad != nil {
			return *bad
		}
		value, bad := rawObject(req, "params.value", p.Value, true)
		if bad != nil {
			return *bad
		}
		rev, err := h.Port.Set(ctx, c, p.Slice, value)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, revisionResult{Slice: p.Slice, Revision: rev})
	case protocol.MethodStatePatch:
		var p patchParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.slice", p.Slice); bad != nil {
			return *bad
		}
		patch, bad := rawObject(req, "params.patch", p.Patch, true)
		if bad != nil {
			return *bad
		}
		var ifRevision uint64
		if p.IfRevision != nil {
			ifRevision = *p.IfRevision
		}
		rev, err := h.Port.Patch(ctx, c, p.Slice, patch, ifRevision)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, revisionResult{Slice: p.Slice, Revision: rev})
	case protocol.MethodStateSnapshot:
		var p snapshotParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		values := p.Values != nil && *p.Values
		res, err := h.Port.Snapshot(ctx, c, values)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, res)
	case protocol.MethodStateWatch:
		var p stateWatchParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		res, err := h.Port.Watch(ctx, c, p.Slices, n)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, res)
	case protocol.MethodStateClear:
		var p clearParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.namespace", p.Namespace); bad != nil {
			return *bad
		}
		if err := h.Port.Clear(ctx, c, p.Namespace, p.Force); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	}
	return handlers.UnknownMethod(req)
}
