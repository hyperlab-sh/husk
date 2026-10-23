package services

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Secrets adapts contracts.SecretsPort for the secrets.* methods. get is
// open to plugins under the shared per-plugin rate limit; names is open to
// plugins under the same budget; set, grant, revoke and reveal are CLI methods and are
// denied to a plugin caller before the port is touched. Values ride only in
// the get result and the reveal result, exactly as services.md's examples
// pin them.
type Secrets struct {
	Port contracts.SecretsPort
	// Limiter is the one Services budget (every plugin-callable method), keyed
	// by plugin id (services.md, Limits). Register requires it; only a hand-built
	// adapter struct can leave it nil, and the guard only defends that.
	Limiter contracts.Limiter
}

type secretsNameParams struct {
	Name string `json:"name"`
}

// secretsValueParams keeps value raw: encoding/json turns invalid UTF-8 and
// lone surrogate escapes into U+FFFD without an error, so the handler checks
// the wire text before decoding it.
type secretsValueParams struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
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
		return handlers.Reply(req, secretsValueResult{Name: p.Name, Value: value})
	case protocol.MethodSecretsNames:
		if deny := admitRateLimited(req, c, h.Limiter); deny != nil {
			return *deny
		}
		names, err := h.Port.Names(ctx, c)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		if names.Secrets == nil {
			names.Secrets = []contracts.SecretInfo{}
		}
		return handlers.Reply(req, names)
	case protocol.MethodSecretsSet:
		var p secretsValueParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.name", p.Name); bad != nil {
			return *bad
		}
		value, bad := decodeSecretValue(req, p.Value)
		if bad != nil {
			return *bad
		}
		if err := h.Port.Set(ctx, c, p.Name, value); err != nil {
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
		return handlers.Reply(req, secretsValueResult{Name: p.Name, Value: value})
	}
	return handlers.UnknownMethod(req)
}

// decodeSecretValue decodes params.value, which must be a JSON string of
// valid UTF-8 text. The empty string is a value; an absent field is not.
func decodeSecretValue(req protocol.Message, raw json.RawMessage) (string, *protocol.Message) {
	if len(raw) == 0 || string(raw) == "null" {
		bad := handlers.InvalidParams(req, "params.value", "is required")
		return "", &bad
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		bad := handlers.InvalidParams(req, "params.value", "must be a string")
		return "", &bad
	}
	if !validJSONText(raw) {
		data, _ := json.Marshal(map[string]string{"field": "params.value", "reason": "not_utf8"})
		return "", &protocol.Message{ID: req.ID, Error: &protocol.Error{
			Code: protocol.CodeInvalidParams, Message: "params.value: not valid UTF-8", Data: data,
		}}
	}
	return value, nil
}

// validJSONText reports whether a JSON string literal holds valid UTF-8 text:
// no invalid byte sequence and no unpaired surrogate escape.
func validJSONText(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	hex := func(b []byte) (rune, bool) {
		var r rune
		if len(b) < 4 {
			return 0, false
		}
		for _, c := range b[:4] {
			switch {
			case c >= '0' && c <= '9':
				r = r<<4 | rune(c-'0')
			case c >= 'a' && c <= 'f':
				r = r<<4 | rune(c-'a'+10)
			case c >= 'A' && c <= 'F':
				r = r<<4 | rune(c-'A'+10)
			default:
				return 0, false
			}
		}
		return r, true
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) || raw[i] != 'u' {
			continue
		}
		r, ok := hex(raw[i+1:])
		if !ok {
			return false
		}
		i += 4
		switch {
		case r >= 0xDC00 && r <= 0xDFFF:
			return false
		case r >= 0xD800 && r <= 0xDBFF:
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, ok := hex(raw[i+3:])
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}
