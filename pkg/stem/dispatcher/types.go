// Package dispatcher routes an action to its single owner and returns the
// owner's reply to whoever asked. It decides who receives a request and whether
// the sender may send it; waiting on a plugin belongs to pkg/stem/calls.
//
// A Dispatcher is confined to the host loop: Invoke, RegisterHost and Complete
// are called from Update and never block. It reads the action catalog, plugin
// liveness and focus through small injected views and never writes any of them.
// It imports neither Store, Pods, Services nor the Server.
package dispatcher

import (
	"strings"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// MaxArgsBytes bounds the encoded size of ActionRequest.Args.
const MaxArgsBytes = 1 << 20

// maxActionIDLen bounds a catalog id: a 64-byte plugin id, a dot and a name.
const maxActionIDLen = 256

// HostOwner is the owner of every host action.
const HostOwner = "husk"

// Requester is the name this module registers with Calls.
const Requester = "dispatcher"

// Error codes carried by ActionError. They are the wire codes of the protocol
// registry that this module can produce itself; an owner's own error code is
// passed through verbatim.
const (
	CodeInvalidParams = "invalid_params"
	CodeNotFound      = "not_found"
	CodeDenied        = "denied"
	CodeUnavailable   = "unavailable"
	CodeBusy          = "busy"
	CodeInternal      = "internal"
)

// Caller says who started a request; it is the one shared identity type.
type Caller = protocol.Caller

// CallerKind is the kind of a Caller.
type CallerKind = protocol.CallerKind

// The caller kinds, as the owner receives them in params.caller.kind.
const (
	CallerInput  = protocol.CallerInput
	CallerPlugin = protocol.CallerPlugin
	CallerCLI    = protocol.CallerCLI
	CallerHost   = protocol.CallerHost
)

// FocusContext is the focus at dispatch time; it is the one shared focus type.
type FocusContext = protocol.FocusContext

// ActionRequest is one request to run an action.
type ActionRequest struct {
	// Action is the catalog id, "<plugin_id>.<name>" or "husk.<name>".
	Action string
	// Args is a JSON object and may be nil.
	Args   map[string]any
	Caller Caller
	// Focus is used as given only for CallerInput; every other caller gets
	// State's focus read at dispatch time.
	Focus FocusContext
	// Reply is invoked exactly once, on the loop goroutine. A nil Reply
	// discards the answer.
	Reply func(ActionReply)
}

// ActionReply is the outcome of one request.
type ActionReply struct {
	// Result is the owner's result object; nil on error.
	Result map[string]any
	// Err is nil on success.
	Err *ActionError
	// Declined is set only by a host handler that refuses a key press: the
	// action had nothing to do and the key belongs to the focused occupant. It
	// is host-only and never serialized: an in-process caller receives it
	// unchanged, and the wire port answers it with an empty success. A plugin
	// action cannot decline, so replies converted from an owner never set it.
	Declined bool
}

// ActionError is a failed request.
type ActionError struct {
	// Code is a wire error code.
	Code    string
	Message string
	// Data is the wire error's structured data member, such as
	// {"reason":"plugin_exited"}; nil when there is none.
	Data map[string]any
}

// Error implements error.
func (e *ActionError) Error() string { return e.Code + ": " + e.Message }

// ActionDef is one catalog entry.
type ActionDef struct {
	ID string
	// Owner is a plugin id, or HostOwner.
	Owner string
	// Public means the manifest marks the action invocable by other plugins.
	Public bool
}

// Catalog is the read-only action catalog. It is implemented outside Stem
// over the declared "plugins" document and the host action list.
type Catalog interface {
	// Lookup returns the entry for a catalog id.
	Lookup(action string) (ActionDef, bool)
	// List returns every entry in any order.
	List() []ActionDef
}

// Liveness is the read-only view of plugin liveness.
type Liveness interface {
	// Live reports that the plugin's husk.pods status is "live".
	Live(pluginID string) bool
}

// FocusSource reads the focus State holds right now.
type FocusSource interface {
	Focus() FocusContext
}

// ValidActionID reports whether id is a well-formed catalog id.
func ValidActionID(id string) bool { return validActionID(id) }

// validActionID reports whether id is a well-formed catalog id: at least two
// non-empty dot-separated segments and no whitespace or separator characters.
func validActionID(id string) bool {
	if id == "" || len(id) > maxActionIDLen {
		return false
	}
	if strings.ContainsAny(id, " \t\r\n\x1b\x00;=\\") {
		return false
	}
	parts := strings.Split(id, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}

// Permitted reports whether caller may invoke def. The default is deny.
func Permitted(caller Caller, def ActionDef) bool {
	switch caller.Kind {
	case CallerInput, CallerCLI, CallerHost:
		return true
	case CallerPlugin:
		if caller.PluginID == "" {
			return false
		}
		return caller.PluginID == def.Owner || def.Public
	}
	return false
}
