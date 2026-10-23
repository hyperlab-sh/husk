package services

import (
	"context"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Settings adapts contracts.SettingsPort for the settings.* methods. get is
// open to plugins under the shared per-plugin rate limit; list, schema and
// validate are CLI methods and are denied to a plugin caller before the port
// is touched.
type Settings struct {
	Port contracts.SettingsPort
	// Limiter is the one Services budget (every plugin-callable method), keyed
	// by plugin id (services.md, Limits). Register requires it; only a hand-built
	// adapter struct can leave it nil, and the guard only defends that.
	Limiter contracts.Limiter
}

type settingsGetParams struct {
	Key string `json:"key"`
}

type settingsSchemaParams struct {
	Owner string `json:"owner"`
}

// settingsEntries is the {"entries":[...]} shape of settings.list and
// settings.schema.
type settingsEntries[T any] struct {
	Entries []T `json:"entries"`
}

type settingsGetResult struct {
	Key    string `json:"key"`
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// Serve implements contracts.Handler.
func (h Settings) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := admitCall(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodSettingsGet:
		// The cap counts requests, not valid requests (services.md, Limits):
		// check before decoding so malformed params still consume budget.
		if deny := admitRateLimited(req, c, h.Limiter); deny != nil {
			return *deny
		}
		var p settingsGetParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		if bad := handlers.RequireString(req, "params.key", p.Key); bad != nil {
			return *bad
		}
		value, source, err := h.Port.Get(ctx, c, p.Key)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, settingsGetResult{Key: p.Key, Value: value, Source: source})
	case protocol.MethodSettingsList:
		rows, err := h.Port.List(ctx, c)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		if rows == nil {
			rows = []contracts.SettingValue{}
		}
		return handlers.Reply(req, settingsEntries[contracts.SettingValue]{Entries: rows})
	case protocol.MethodSettingsSchema:
		var p settingsSchemaParams
		if bad := handlers.DecodeParams(req, &p); bad != nil {
			return *bad
		}
		entries, err := h.Port.Schema(ctx, c, p.Owner)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		if entries == nil {
			entries = []contracts.SettingEntry{}
		}
		return handlers.Reply(req, settingsEntries[contracts.SettingEntry]{Entries: entries})
	case protocol.MethodSettingsValidate:
		res, err := h.Port.Validate(ctx, c)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		if res.Problems == nil {
			res.Problems = []protocol.Problem{}
		}
		return handlers.Reply(req, res)
	}
	return handlers.UnknownMethod(req)
}
