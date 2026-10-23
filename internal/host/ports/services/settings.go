package services

import (
	"context"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	services "github.com/hyperlab-sh/husk/internal/services"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Settings implements contracts.SettingsPort over the settings service.
//
// The caller view is the service's own: a plugin sees exactly the keys its
// manifest declared plus host and distribution keys marked public, read-only,
// and a user sees everything. A textual plugins.a.* prefix is never
// authorization: for a plugin named "a", a key owned by a plugin named "a.b"
// is not the caller's. A key unknown to the caller is the service's own
// not_found/unknown_setting, so a plugin cannot learn whether another owner's
// key exists.
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
		return nil, "", coded(err)
	}
	return value, source, nil
}

// Set implements contracts.SettingsPort on the handler goroutine.
func (p Settings) Set(ctx context.Context, hc host.Caller, key string, value any) (host.SettingSetResult, error) {
	source, reports, err := p.Svc.Set(ctx, hc, key, value)
	if err != nil {
		return host.SettingSetResult{}, coded(err)
	}
	return host.SettingSetResult{Key: key, Value: c.Clone(value), Source: source, Reports: Reports(reports)}, nil
}

// List implements contracts.SettingsPort: every known and retained key.
func (p Settings) List(ctx context.Context, hc host.Caller) ([]host.SettingValue, error) {
	rows, err := p.Svc.List(hc)
	if err != nil {
		return nil, coded(err)
	}
	out := make([]host.SettingValue, 0, len(rows))
	for _, r := range rows {
		out = append(out, host.SettingValue{Key: r.Key, Value: r.Value, Source: r.Source})
	}
	return out, nil
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
			Public:  e.Public,
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
	check, err := p.Svc.Validate(ctx, hc, in)
	if err != nil {
		return host.ValidationResult{}, coded(err)
	}
	return validationResult(check.OK, check.Problems), nil
}

// validationResult builds the dry-run reply, cloning the problems so the reply
// shares no slice with a snapshot. Problems is never nil: the wire says [].
func validationResult(ok bool, problems []protocol.Problem) host.ValidationResult {
	return host.ValidationResult{OK: ok, Problems: append([]protocol.Problem{}, problems...)}
}

func (p Settings) dryInput(ctx context.Context, hc host.Caller) (services.ReloadInput, error) {
	sc := hc
	if !c.IsUser(sc) {
		return services.ReloadInput{}, coded(c.RequireUser(sc))
	}
	in, err := p.Inputs.Inputs(ctx, sc)
	return in, codedInput(err)
}
