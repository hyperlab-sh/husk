package services

import (
	"context"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Theme implements contracts.ThemePort over the theme service. Get returns
// the published palette to any authenticated caller — plugins need it to
// colour their own output — and Check dry-rebuilds the palette from fresh
// user-authorized input, reporting contrast failures without applying.
type Theme struct {
	Svc *theme.Service
	// Inputs feeds theme.check its dry rebuild input. Required.
	Inputs InputSource
}

var _ host.ThemePort = Theme{}

// Get implements contracts.ThemePort.
func (p Theme) Get(ctx context.Context, hc host.Caller) (protocol.Palette, error) {
	return p.Svc.Get(caller(hc)), nil
}

// Check implements contracts.ThemePort (user-only: dry input is user-scoped).
func (p Theme) Check(ctx context.Context, hc host.Caller) (host.ThemeCheckResult, error) {
	sc := caller(hc)
	if !sc.User() {
		return host.ThemeCheckResult{}, coded(c.RequireUser(sc))
	}
	in, err := p.Inputs.Inputs(ctx, sc)
	if err != nil {
		return host.ThemeCheckResult{}, coded(err)
	}
	report, err := p.Svc.Check(ctx, sc, in)
	if err != nil {
		return host.ThemeCheckResult{}, coded(err)
	}
	return validationResult(report), nil
}
