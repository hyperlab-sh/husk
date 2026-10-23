package surfaces

import (
	"fmt"
	"strings"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Error is a refusal carrying its wire code. Owner ports and adapters read
// Code and never inspect the message text.
type Error struct {
	Code    protocol.Code
	Message string
	Data    map[string]any
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func errf(code protocol.Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ownName validates that id is "<plugin>.<name>" for the given plugin with
// a name free of the separator and control characters today's wire
// forbids — the same grammar State's ownName enforces. A malformed id is
// invalid_params, an id in another plugin's namespace is denied.
// Contribution ids are namespaced so one plugin can never replace
// another's entry.
func ownName(plugin, id string) error {
	if id == "" {
		return errf(protocol.CodeInvalidParams, "id is required")
	}
	dot := strings.LastIndexByte(id, '.')
	if dot <= 0 || dot == len(id)-1 {
		return errf(protocol.CodeInvalidParams, "id %q must be \"<plugin_id>.<name>\"", id)
	}
	if name := id[dot+1:]; strings.ContainsAny(name, " \t\r\n\x1b;=\\") {
		return errf(protocol.CodeInvalidParams, "id %q: name %q carries a forbidden separator or control character", id, name)
	}
	if owner := id[:dot]; owner != plugin {
		return errf(protocol.CodeDenied, "id %q belongs to plugin %q", id, owner)
	}
	return nil
}
