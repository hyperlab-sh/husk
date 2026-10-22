package handlers

import (
	"fmt"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Admit authorizes req for c against contracts.MethodHomes, the Go form of
// protocol.md's Direction column. It returns nil when the method is homed to
// an inbound port that accepts c's kind. Otherwise it returns the reply to
// send: unknown_method for a method with no inbound handler (including
// Outbound and Unrouted methods arriving inbound), denied for a homed method
// whose caller kinds exclude c.Kind. Adapters call Admit before decoding, so
// refusals never depend on params.
func Admit(req protocol.Message, c contracts.Caller) *protocol.Message {
	home, ok := contracts.MethodHomes()[req.Method]
	if !ok || home.Port == contracts.Outbound || home.Port == contracts.Unrouted {
		return errReply(req, protocol.CodeUnknownMethod, "no inbound handler for "+req.Method)
	}
	for _, kind := range home.Kinds {
		if kind == c.Kind {
			return nil
		}
	}
	return errReply(req, protocol.CodeDenied,
		fmt.Sprintf("%s does not accept a %s caller", req.Method, KindName(c.Kind)))
}

// KindName spells a caller kind for error messages.
func KindName(k contracts.CallerKind) string {
	switch k {
	case contracts.CallerCLI:
		return "CLI"
	case contracts.CallerPlugin:
		return "plugin"
	case contracts.CallerStem:
		return "Stem"
	}
	return "unknown kind"
}

func errReply(req protocol.Message, code protocol.Code, message string) *protocol.Message {
	m := protocol.Message{Error: &protocol.Error{Code: code, Message: message}}
	if req.ID != nil {
		m.ID = req.ID
	}
	return &m
}
