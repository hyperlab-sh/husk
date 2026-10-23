package services

import (
	"context"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	services "github.com/hyperlab-sh/husk/internal/services"
)

// Config implements contracts.ConfigPort over the Services coordinator.
// ReloadFor is the user-only entry: it refuses non-user callers and runs the
// coalesced reload (reason "cli") through the coordinator's loop seam,
// returning one report per service. The wrapper holds no state of its own.
type Config struct {
	Co *services.Coordinator
}

var _ host.ConfigPort = Config{}

// Reload implements contracts.ConfigPort.
func (p Config) Reload(ctx context.Context, hc host.Caller) ([]host.ReloadReport, error) {
	reports, err := p.Co.ReloadFor(ctx, hc)
	if err != nil {
		return nil, coded(err)
	}
	return Reports(reports), nil
}

// Reports converts the Coordinator's reports to the wire-facing type shared by
// config.reload and plugin.remove.
func Reports(reports []services.Report) []host.ReloadReport {
	out := make([]host.ReloadReport, 0, len(reports))
	for _, r := range reports {
		rep := host.ReloadReport{
			Service: r.Service,
			Gen:     uint64(r.Gen),
			Applied: r.Applied,
		}
		rep.Problems = append(rep.Problems, r.Problems...)
		out = append(out, rep)
	}
	return out
}
