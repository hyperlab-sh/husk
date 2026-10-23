package services

import (
	"context"
	"errors"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Theme implements contracts.ThemePort over the theme service. Get returns
// the published palette to any authenticated caller — plugins need it to
// colour their own output — and Check dry-rebuilds the palette from fresh
// user-authorized input, reporting contrast failures without applying.
type Theme struct {
	Svc *theme.Service
	// Settings supplies the dry Settings snapshot Theme reads. Required.
	Settings *settings.Service
	// Inputs feeds theme.check its dry rebuild input. Required.
	Inputs InputSource
}

var _ host.ThemePort = Theme{}

// Get implements contracts.ThemePort.
func (p Theme) Get(ctx context.Context, hc host.Caller) (protocol.Palette, error) {
	palette, err := p.Svc.Get(hc)
	return palette, coded(err)
}

// Check implements contracts.ThemePort (user-only: dry input is user-scoped).
// It dry-builds Settings first to get the snapshot Theme would see; a Settings
// that would not publish is ok:false with its problems, and Theme is not
// rebuilt (a reload would skip it too).
func (p Theme) Check(ctx context.Context, hc host.Caller) (host.ThemeCheckResult, error) {
	sc := hc
	if !c.IsUser(sc) {
		return host.ThemeCheckResult{}, coded(c.RequireUser(sc))
	}
	in, err := p.Inputs.Inputs(ctx, sc)
	if err != nil {
		return host.ThemeCheckResult{}, codedInput(err)
	}
	snap, check, err := p.Settings.Preview(ctx, sc, in)
	if err != nil {
		return host.ThemeCheckResult{}, coded(err)
	}
	if !check.OK {
		return validationResult(false, check.Problems), nil
	}
	in.Settings = snap
	report, err := p.Svc.Check(ctx, sc, in)
	if err != nil && !errors.Is(err, c.ErrInvalid) {
		return host.ThemeCheckResult{}, coded(err)
	}
	return validationResult(err == nil, append(check.Problems, report.Problems...)), nil
}
