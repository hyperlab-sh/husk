// Package dispatcher routes an action to its single owner and returns the
// owner's reply to whoever asked. It decides who receives a request and whether
// the sender may send it; waiting on a plugin belongs to internal/stem/calls.
//
// A Dispatcher is confined to the host loop: Invoke, RegisterHost and Complete
// are called from Update and never block. It reads the action catalog, plugin
// liveness and focus through small injected views and never writes any of them.
// It imports neither Store, Pods, Services nor the Server.
package dispatcher

import "strings"

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

// CallerKind says who started a request.
type CallerKind uint8

const (
	// CallerInput is the user pressing a key or choosing a palette entry.
	CallerInput CallerKind = iota + 1
	// CallerPlugin is an authenticated plugin.
	CallerPlugin
	// CallerCLI is a shell outside any plugin: the user typing.
	CallerCLI
	// CallerHost is a host module acting on its own.
	CallerHost
)

// String returns the wire spelling of the kind, as the owner receives it in
// params.caller.kind.
func (k CallerKind) String() string {
	switch k {
	case CallerInput:
		return "input"
	case CallerPlugin:
		return "plugin"
	case CallerCLI:
		return "cli"
	case CallerHost:
		return "host"
	}
	return "unknown"
}

// Caller says who started a request. It is set by the module that accepts the
// request, never read from params.
type Caller struct {
	Kind CallerKind
	// PluginID is set only when Kind is CallerPlugin.
	PluginID string
}

// FocusContext is the focus slice of State at dispatch time.
type FocusContext struct {
	// Surface is the focused surface id, "" when nothing has focus.
	Surface string `json:"surface"`
	// Owner is the plugin id occupying Surface, "" when unoccupied or host.
	Owner string `json:"owner"`
}

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
