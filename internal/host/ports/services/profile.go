package services

import (
	"context"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/services/profile"
)

// Profile implements contracts.ProfilePort over the profile service. List and
// Use are user-only in the service; Use validates the name and posts its
// mutation to the loop, so this wrapper stays synchronous and stateless.
type Profile struct {
	Svc *profile.Service
}

var _ host.ProfilePort = Profile{}

// List implements contracts.ProfilePort.
func (p Profile) List(ctx context.Context, hc host.Caller) ([]host.ProfileInfo, error) {
	infos, err := p.Svc.ListFor(caller(hc))
	if err != nil {
		return nil, coded(err)
	}
	out := make([]host.ProfileInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, host.ProfileInfo{Name: i.Name, Source: i.Source, Active: i.Active})
	}
	return out, nil
}

// Use implements contracts.ProfilePort.
func (p Profile) Use(ctx context.Context, hc host.Caller, name string) error {
	return coded(p.Svc.Use(caller(hc), name))
}
