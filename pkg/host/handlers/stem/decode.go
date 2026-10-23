// Package stem hosts the Server handler adapters for the Stem-owned method
// families: session, action, event, hook, surface/slot/toast and state. Each
// adapter is a contracts.Handler that admits the caller against
// contracts.MethodHomes, decodes the protocol.Message params, calls its port
// and encodes the reply; ports never see wire envelopes. The port
// implementations themselves live elsewhere — the Session and State wrappers
// in pkg/host/ports/stem, the rest in their own units.
package stem

import (
	"bytes"
	"encoding/json"

	"github.com/hyperlab-sh/husk/pkg/host/handlers"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// rawObject validates a raw JSON object field and returns it. A missing or
// null field returns nil (absent) unless required; a present field that is
// not a JSON object is invalid_params, so ports receive only object values
// and can marshal them straight into slices.
func rawObject(req protocol.Message, field string, raw json.RawMessage, required bool) (json.RawMessage, *protocol.Message) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		if required {
			bad := handlers.InvalidParams(req, field, "is required")
			return nil, &bad
		}
		return nil, nil
	}
	if trimmed[0] != '{' {
		bad := handlers.InvalidParams(req, field, "must be a JSON object")
		return nil, &bad
	}
	return trimmed, nil
}

// replyRaw encodes a raw JSON result, mapping absent to the empty object.
func replyRaw(req protocol.Message, raw json.RawMessage) protocol.Message {
	if len(bytes.TrimSpace(raw)) == 0 {
		return handlers.Reply(req, nil)
	}
	return handlers.Reply(req, raw)
}
