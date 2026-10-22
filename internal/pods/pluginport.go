package pods

import (
	"context"
	"slices"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

var _ contracts.PluginPort = (*Host)(nil)

// List serves plugin.list: every Pod's spec summary plus liveness, in stable
// id order. Loop-only.
func (h *Host) List(_ context.Context, _ contracts.Caller) ([]contracts.PluginInfo, error) {
	ids := make([]string, 0, len(h.pods))
	for id := range h.pods {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]contracts.PluginInfo, 0, len(ids))
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
	return out, nil
}

// Log serves plugin.log: the stderr log path recorded for a plugin id, or a
// not_found CodedError. Loop-only.
func (h *Host) Log(_ context.Context, _ contracts.Caller, pluginID string) (string, error) {
	p, ok := h.pods[pluginID]
	if !ok {
		return "", contracts.NewError(protocol.CodeNotFound, "unknown plugin id "+pluginID, nil)
	}
	return p.Info().LogPath, nil
}
