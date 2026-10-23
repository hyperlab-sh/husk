package services

import (
	"context"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Theme adapts contracts.ThemePort for the theme.* methods. get is open to
// any authenticated caller — plugins need the palette to colour their own
// output; check is a CLI method and is denied to a plugin caller before the
// port is touched.
type Theme struct {
	Port contracts.ThemePort
}

type themeGetResult struct {
	Palette protocol.Palette `json:"palette"`
}

// Serve implements contracts.Handler.
func (h Theme) Serve(ctx context.Context, c contracts.Caller, req protocol.Message, _ contracts.Notifier) protocol.Message {
	if deny := admitCall(req, c); deny != nil {
		return *deny
	}
	switch req.Method {
	case protocol.MethodThemeGet:
		pal, err := h.Port.Get(ctx, c)
		if err != nil {
			return handlers.ErrorReply(req, err)
		}
		return handlers.Reply(req, themeGetResult{Palette: pal})
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
