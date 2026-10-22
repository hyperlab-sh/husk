package services

import (
	"encoding/json"
	"errors"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// decodeParams unmarshals req.Params into dst. Absent params leave dst at its
// zero value, which per-method required-field checks then reject. A decode
// failure returns invalid_params naming the offending field —
// "params.<field>" for a typed field, "params" when the whole object has the
// wrong shape.
func decodeParams(req protocol.Message, dst any) *protocol.Message {
	if len(req.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Params, dst); err != nil {
		field := "params"
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			field = "params." + te.Field
		}
		bad := handlers.InvalidParams(req, field, err.Error())
		return &bad
	}
	return nil
}

// requireString rejects an empty required string field.
func requireString(req protocol.Message, field, value string) *protocol.Message {
	if value == "" {
		bad := handlers.InvalidParams(req, field, "is required")
		return &bad
	}
	return nil
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
// the refusal reply when the budget is exhausted. CLI and Stem callers and a
// nil limiter (the composition root opted out) always pass; the key is the
// verified plugin id, never a params value.
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
