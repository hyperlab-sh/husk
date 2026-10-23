package services

import (
	"context"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Theme adapts contracts.ThemePort for the theme.* methods. get is open to
// any authenticated caller — plugins need the palette to colour their own
// output; check is a CLI method and is denied to a plugin caller before the
// port is touched.
type Theme struct {
	Port    contracts.ThemePort
	Limiter contracts.Limiter
}

type themeGetResult struct {
	Palette protocol.Palette `json:"palette"`
	// ANSI maps palette roles (never name) to indices 0..15; absent for hex themes.
	ANSI protocol.ANSIRoles `json:"ansi,omitempty"`
}

// Serve implements contracts.Handler.
func (h Theme) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := admitCall(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodThemeGet:
		// One budget with the other plugin-callable Services methods; the
		// call counts whatever the caller's params.
		if deny := admitRateLimited(req, c, h.Limiter); deny != nil {
			return *deny
		}
		pal, indices, err := h.Port.Get(ctx, c)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, themeGetResult{Palette: pal, ANSI: indices})
	case protocol.MethodThemeList:
		if deny := admitRateLimited(req, c, h.Limiter); deny != nil {
			return *deny
		}
		rows, err := h.Port.List(ctx, c)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		if rows == nil {
			rows = []contracts.ThemeEntry{}
		}
		return handlers.Reply(req, struct {
			Themes []contracts.ThemeEntry `json:"themes"`
		}{rows})
	case protocol.MethodThemeCheck:
		res, err := h.Port.Check(ctx, c)
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
