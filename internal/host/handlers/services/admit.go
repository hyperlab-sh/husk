package services

import (
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// admitCall layers the Services denied rows on the Mux's own Admit. The
// method-home check (unknown method, outbound arriving inbound, caller kind)
// comes first; then a plugin-kind caller with no plugin id is denied (D36/
// D51): it owns no settings, cannot be rate-limited under a meaningful key,
// and must not reach any port.
func admitCall(req protocol.Message, c contracts.Caller) *protocol.Message {
	if deny := handlers.Admit(req, c); deny != nil {
		return deny
	}
	if c.Kind == contracts.CallerPlugin && c.PluginID == "" {
		denied := handlers.Denied(req, req.Method+" does not accept a plugin caller with no id")
		return &denied
	}
	return nil
}
