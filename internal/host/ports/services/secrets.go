package services

import (
	"context"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
)

// Secrets implements contracts.SecretsPort over the secrets service. Values
// live only in Store crypto; Read returns a plaintext copy solely to the
// caller the service authorized (a plugin with an exact declaration digest
// and grant, or a user), and Set/Grant/Revoke/Reveal are user-only in the
// service. The wrapper only converts identities and error codes; refusals
// keep their data.reason (not_declared, needs_grant, user_denied).
type Secrets struct {
	Svc *secrets.Service
}

var _ host.SecretsPort = Secrets{}

// Get implements contracts.SecretsPort.
func (p Secrets) Get(ctx context.Context, hc host.Caller, name string) ([]byte, error) {
	value, err := p.Svc.Read(caller(hc), name)
	return value, coded(err)
}

// Names implements contracts.SecretsPort: metadata only, never values.
func (p Secrets) Names(ctx context.Context, hc host.Caller) ([]host.SecretInfo, error) {
	infos, err := p.Svc.Names(caller(hc))
	if err != nil {
		return nil, coded(err)
	}
	out := make([]host.SecretInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, host.SecretInfo{
			Name:     i.Name,
			Declared: i.Declared,
			Granted:  i.Granted,
			Set:      i.Set,
			Broken:   i.Broken,
		})
	}
	return out, nil
}

// Set implements contracts.SecretsPort (user-only in the service).
func (p Secrets) Set(ctx context.Context, hc host.Caller, name string, value []byte) error {
	return coded(p.Svc.Set(caller(hc), name, value))
}

// Grant implements contracts.SecretsPort (user-only in the service).
func (p Secrets) Grant(ctx context.Context, hc host.Caller, plugin, name string) error {
	return coded(p.Svc.Grant(caller(hc), plugin, name))
}

// Revoke implements contracts.SecretsPort (user-only in the service).
func (p Secrets) Revoke(ctx context.Context, hc host.Caller, plugin, name string) error {
	return coded(p.Svc.Revoke(caller(hc), plugin, name))
}

// Reveal implements contracts.SecretsPort (user-only in the service). The
// service audits the reveal; this wrapper never logs the value.
func (p Secrets) Reveal(ctx context.Context, hc host.Caller, name string) ([]byte, error) {
	value, err := p.Svc.Reveal(caller(hc), name)
	return value, coded(err)
}
