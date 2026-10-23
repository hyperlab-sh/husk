package services

import (
	"context"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/services/profile"
)

// Profile implements contracts.ProfilePort over the profile service. List and
// Use are user-only in the service; Use validates the name, posts its
// mutation to the loop and waits for the reload that mutation caused.
type Profile struct {
	Svc *profile.Service
}

var _ host.ProfilePort = Profile{}

// List implements contracts.ProfilePort.
func (p Profile) List(ctx context.Context, hc host.Caller) ([]host.ProfileInfo, error) {
	infos, err := p.Svc.ListFor(hc)
	if err != nil {
		return nil, coded(err)
	}
	out := make([]host.ProfileInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, host.ProfileInfo{Name: i.Name, Source: i.Source, Active: i.Active})
	}
	return out, nil
}

// Use implements contracts.ProfilePort: the reply carries the reports of the
// reload the change caused; a flight error passes through coded unchanged.
func (p Profile) Use(ctx context.Context, hc host.Caller, name string) (host.ProfileUseResult, error) {
	active, reports, err := p.Svc.Use(ctx, hc, name)
	if err != nil {
		return host.ProfileUseResult{}, coded(err)
	}
	return host.ProfileUseResult{Active: active, Reports: Reports(reports)}, nil
}
