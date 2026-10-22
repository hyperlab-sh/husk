package services

import (
	"context"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Secrets adapts contracts.SecretsPort for the secrets.* methods. get is
// open to plugins under the shared per-plugin rate limit; names is open to
// plugins uncapped; set, grant, revoke and reveal are CLI methods and are
// denied to a plugin caller before the port is touched. Values ride only in
// the get result and the reveal result, exactly as services.md's examples
// pin them.
type Secrets struct {
	Port contracts.SecretsPort
	// Limiter is the shared settings.get/secrets.get budget, keyed by plugin
	// id (services.md, Limits). Register requires it; only a hand-built
	// adapter struct can leave it nil, and the guard only defends that.
	Limiter contracts.Limiter
}

type secretsNameParams struct {
	Name string `json:"name"`
}

type secretsValueParams struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type secretsPluginParams struct {
	Plugin string `json:"plugin"`
	Name   string `json:"name"`
}

type secretsValueResult struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Serve implements contracts.Handler.
func (h Secrets) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := admitCall(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodSecretsGet:
		// The cap counts requests, not valid requests (services.md, Limits):
		// check before decoding so malformed params still consume budget.
		if deny := admitRateLimited(req, c, h.Limiter); deny != nil {
			return *deny
		}
		var p secretsNameParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.name", p.Name); bad != nil {
			return *bad
		}
		value, err := h.Port.Get(ctx, c, p.Name)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, secretsValueResult{Name: p.Name, Value: string(value)})
	case protocol.MethodSecretsNames:
		infos, err := h.Port.Names(ctx, c)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		if infos == nil {
			infos = []contracts.SecretInfo{}
		}
		return handlers.Reply(req, infos)
	case protocol.MethodSecretsSet:
		var p secretsValueParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.name", p.Name); bad != nil {
			return *bad
		}
		if err := h.Port.Set(ctx, c, p.Name, []byte(p.Value)); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	case protocol.MethodSecretsGrant:
		var p secretsPluginParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.plugin", p.Plugin); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.name", p.Name); bad != nil {
			return *bad
		}
		if err := h.Port.Grant(ctx, c, p.Plugin, p.Name); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	case protocol.MethodSecretsRevoke:
		var p secretsPluginParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.plugin", p.Plugin); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.name", p.Name); bad != nil {
			return *bad
		}
		if err := h.Port.Revoke(ctx, c, p.Plugin, p.Name); err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, nil)
	case protocol.MethodSecretsReveal:
		var p secretsNameParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.name", p.Name); bad != nil {
			return *bad
		}
		value, err := h.Port.Reveal(ctx, c, p.Name)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, secretsValueResult{Name: p.Name, Value: string(value)})
	}
	return handlers.UnknownMethod(req)
}
