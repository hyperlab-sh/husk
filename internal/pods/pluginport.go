package pods

import (
	"context"
	"slices"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

var _ contracts.PluginPort = (*Host)(nil)

// List serves plugin.list: every Pod's spec summary plus liveness, in stable
// id order. The Pod maps are loop-owned, so the read runs on the loop through
// the Do seam; without a running loop the answer is unavailable, never a
// racy snapshot (D57). plugin.list is the user's own view of the running
// plugins — only the user's CLI may ask (a plugin or Stem caller over the
// port is not the user, mirroring state.clear).
func (h *Host) List(ctx context.Context, c contracts.Caller) ([]contracts.PluginInfo, error) {
	if c.Kind != contracts.CallerCLI {
		return nil, contracts.NewError(protocol.CodeDenied, "plugin.list is the user's own view of running plugins; only the user's CLI may do it", nil)
	}
	var out []contracts.PluginInfo
	if err := h.loop.Do(ctx, func() {
		ids := make([]string, 0, len(h.pods))
		for id := range h.pods {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		out = make([]contracts.PluginInfo, 0, len(ids))
		for _, id := range ids {
			p := h.pods[id]
			spec, info := p.Spec(), p.Info()
			out = append(out, contracts.PluginInfo{
				ID:          spec.ID,
				Name:        spec.Name,
				Version:     spec.Version,
				Description: spec.Description,
				Status:      contracts.PodStatus(info.Status),
				PID:         info.PID,
				StartedAt:   info.StartedAt,
				LastError:   info.LastError,
				Log:         info.LogPath,
			})
		}
	}); err != nil {
		return nil, contracts.NewError(protocol.CodeUnavailable, "the host loop no longer runs", nil)
	}
	return out, nil
}

// Log serves plugin.log: the stderr log path recorded for a plugin id, or a
// not_found CodedError. Like List it is CLI-only and reads the loop-owned Pod
// map through the Do seam (D57).
func (h *Host) Log(ctx context.Context, c contracts.Caller, pluginID string) (string, error) {
	if c.Kind != contracts.CallerCLI {
		return "", contracts.NewError(protocol.CodeDenied, "plugin.log reads a plugin's host-side stderr log; only the user's CLI may do it", nil)
	}
	var path string
	var found bool
	if err := h.loop.Do(ctx, func() {
		p, ok := h.pods[pluginID]
		if !ok {
			return
		}
		found, path = true, p.Info().LogPath
	}); err != nil {
		return "", contracts.NewError(protocol.CodeUnavailable, "the host loop no longer runs", nil)
	}
	if !found {
		return "", contracts.NewError(protocol.CodeNotFound, "unknown plugin id "+pluginID, nil)
	}
	return path, nil
}
