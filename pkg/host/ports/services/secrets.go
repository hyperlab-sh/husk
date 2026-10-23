package services

import (
	"context"

	host "github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/services/secrets"
)

// Secrets implements contracts.SecretsPort over the secrets service. Values
// live only in Store crypto; Get returns the plaintext solely to the caller the
// service authorized (a plugin with an exact declaration digest and grant, or
// a user), and Set/Grant/Revoke/Reveal are user-only in the service. The
// wrapper only converts identities and error codes; refusals keep their
// data.reason (not_declared, unset, user_denied, dialog_pending, storage).
type Secrets struct {
	Svc *secrets.Service
}

var _ host.SecretsPort = Secrets{}

// Get implements contracts.SecretsPort. A plugin's ungranted read blocks on
// the confirm dialog; ctx is the request's, so a closed connection drops it.
func (p Secrets) Get(ctx context.Context, hc host.Caller, name string) (string, error) {
	value, err := p.Svc.Read(ctx, hc, name)
	return value, coded(err)
}

// Names implements contracts.SecretsPort: metadata only, never values.
func (p Secrets) Names(ctx context.Context, hc host.Caller) (host.SecretNames, error) {
	res, err := p.Svc.Names(hc)
	if err != nil {
		return host.SecretNames{}, coded(err)
	}
	out := host.SecretNames{Storage: res.Storage, Secrets: make([]host.SecretInfo, 0, len(res.Secrets))}
	for _, i := range res.Secrets {
		out.Secrets = append(out.Secrets, host.SecretInfo{
			Name:     i.Name,
			Declared: i.Declared,
			Granted:  i.Granted,
			Set:      i.Set,
			Required: i.Required,
		})
	}
	return out, nil
}

// Set implements contracts.SecretsPort (user-only in the service).
func (p Secrets) Set(ctx context.Context, hc host.Caller, name, value string) error {
	return coded(p.Svc.Set(hc, name, value))
}

// Grant implements contracts.SecretsPort (user-only in the service).
func (p Secrets) Grant(ctx context.Context, hc host.Caller, plugin, name string) error {
	return coded(p.Svc.Grant(ctx, hc, plugin, name))
}

// Revoke implements contracts.SecretsPort (user-only in the service).
func (p Secrets) Revoke(ctx context.Context, hc host.Caller, plugin, name string) error {
	return coded(p.Svc.Revoke(ctx, hc, plugin, name))
}

// Reveal implements contracts.SecretsPort (user-only in the service). The
// service audits the reveal; this wrapper never logs the value.
func (p Secrets) Reveal(ctx context.Context, hc host.Caller, name string) (string, error) {
	value, err := p.Svc.Reveal(ctx, hc, name)
	return value, coded(err)
}
