package services

import (
	"context"
	"errors"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	services "github.com/hyperlab-sh/husk/internal/services"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Settings implements contracts.SettingsPort over the settings service.
//
// The caller view is the service's own (D34/D34a): a plugin sees exactly the
// keys its manifest declared plus public husk.* keys, read-only, and a user
// sees everything. Because Get consults the service's filtered snapshot, a
// textual plugins.a.* prefix is never authorization: for a plugin named "a",
// a key owned by a plugin named "a.b" is not the caller's. When the service
// refuses an out-of-declaration read, this wrapper narrows its
// denied/undeclared_setting refusal to the port contract's not_found, so a
// plugin cannot even learn whether another owner's key exists.
type Settings struct {
	Svc *settings.Service
	// Inputs feeds settings.validate its dry rebuild input. Required.
	Inputs InputSource
}

var _ host.SettingsPort = Settings{}

// Get implements contracts.SettingsPort. The value is the validated typed
// value; the source is the layer it came from.
func (p Settings) Get(ctx context.Context, hc host.Caller, key string) (any, string, error) {
	value, source, err := p.Svc.Get(hc, key)
	if err != nil {
		return nil, "", narrowUndeclared(err)
	}
	return value, source, nil
}

// List implements contracts.SettingsPort: the caller's filtered value map.
func (p Settings) List(ctx context.Context, hc host.Caller) (map[string]any, error) {
	values, err := p.Svc.List(hc)
	return values, coded(err)
}

// Schema implements contracts.SettingsPort. An empty owner lists the whole
// schema the caller may see; otherwise entries are filtered to that owner.
func (p Settings) Schema(ctx context.Context, hc host.Caller, owner string) ([]host.SettingEntry, error) {
	entries, err := p.Svc.SchemaFor(hc, owner)
	if err != nil {
		return nil, coded(err)
	}
	out := make([]host.SettingEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, host.SettingEntry{
			Key:     e.Key,
			Type:    e.Type,
			Default: e.Default,
			Enum:    e.Enum,
			Min:     e.Min,
			Max:     e.Max,
			Doc:     e.Doc,
			Owner:   e.Owner,
			Apply:   e.Apply,
		})
	}
	return out, nil
}

// Validate implements contracts.SettingsPort: it dry-rebuilds settings from
// fresh user-authorized input and reports what would change, without
// applying anything.
func (p Settings) Validate(ctx context.Context, hc host.Caller) (host.ValidationResult, error) {
	in, err := p.dryInput(ctx, hc)
	if err != nil {
		return host.ValidationResult{}, err
	}
	report, err := p.Svc.Validate(ctx, hc, in)
	if err != nil {
		return host.ValidationResult{}, coded(err)
	}
	return validationResult(report), nil
}

// validationResult copies a services report into the host's validation
// shape, cloning the problems so the reply shares no slice with a snapshot.
func validationResult(r c.Report) host.ValidationResult {
	out := host.ValidationResult{Applied: r.Applied}
	if len(r.Problems) > 0 {
		out.Problems = make([]host.Problem, len(r.Problems))
		for i, p := range r.Problems {
			out.Problems[i] = host.Problem{
				Severity: p.Severity,
				Layer:    p.Layer,
				Key:      p.Key,
				Message:  p.Message,
			}
		}
	}
	return out
}

func (p Settings) dryInput(ctx context.Context, hc host.Caller) (services.ReloadInput, error) {
	sc := hc
	if !c.IsUser(sc) {
		return services.ReloadInput{}, coded(c.RequireUser(sc))
	}
	in, err := p.Inputs.Inputs(ctx, sc)
	return in, codedInput(err)
}

// narrowUndeclared maps the service's denied/undeclared_setting refusal to
// not_found with the same reason, per the SettingsPort contract; every other
// error passes through coded unchanged.
func narrowUndeclared(err error) error {
	var serr *c.Error
	if errors.As(err, &serr) && serr != nil && serr.Code == "denied" && serr.Reason == "undeclared_setting" {
		return &codedError{
			code: protocol.CodeNotFound,
			data: map[string]any{"reason": "undeclared_setting"},
			msg:  "setting key not visible to this caller",
		}
	}
	return coded(err)
}
