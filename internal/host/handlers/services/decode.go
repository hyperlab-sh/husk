package services

import (
	"encoding/json"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// decodeParams unmarshals req.Params into dst via the shared
// handlers.DecodeParams. Absent params leave dst at its zero value, which
// per-method required-field checks then reject.
func decodeParams(req protocol.Message, dst any) *protocol.Message {
	return handlers.DecodeParams(req, dst)
}

// requireString rejects an empty required string field via the shared
// handlers.RequireString.
func requireString(req protocol.Message, field, value string) *protocol.Message {
	return handlers.RequireString(req, field, value)
}

// rateLimited is the shared refusal reply for an exhausted per-plugin budget:
// busy with data.reason rate_limited (services.md, Limits).
func rateLimited(req protocol.Message) protocol.Message {
	m := protocol.Message{Error: &protocol.Error{
		Code:    protocol.CodeBusy,
		Message: "rate limit exceeded",
	}}
	if data, err := json.Marshal(map[string]string{"reason": "rate_limited"}); err == nil {
		m.Error.Data = data
	}
	if req.ID != nil {
		m.ID = req.ID
	}
	return m
}

// admitRateLimited applies limiter to one plugin caller's request and returns
// the refusal reply when the budget is exhausted. CLI and Stem callers always
// pass; the key is the verified plugin id, never a params value. Register
// guarantees a non-nil limiter; the nil check only defends a hand-built
// adapter struct.
func admitRateLimited(req protocol.Message, c contracts.Caller, limiter contracts.Limiter) *protocol.Message {
	if c.Kind != contracts.CallerPlugin || limiter == nil {
		return nil
	}
	if !limiter.Allow(c.PluginID) {
		bad := rateLimited(req)
		return &bad
	}
	return nil
}
