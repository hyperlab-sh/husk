package protocol

// CallerKind says how a request reached the module that handles it.
type CallerKind string

const (
	// CallerCLI is a socket request with no token: the user's own shell.
	CallerCLI CallerKind = "cli"
	// CallerPlugin is a socket request with a valid per-launch token.
	CallerPlugin CallerKind = "plugin"
	// CallerInput is a key binding resolved by Input, however it reaches the
	// Dispatcher (directly, or relayed through Frontend.InvokeAction).
	CallerInput CallerKind = "input"
	// CallerHost is an action the host starts itself: the palette, a rail
	// item, Stem internals.
	CallerHost CallerKind = "host"
)

// Valid reports whether k is one of the four defined kinds. The zero value is
// not valid.
func (k CallerKind) Valid() bool {
	switch k {
	case CallerCLI, CallerPlugin, CallerInput, CallerHost:
		return true
	}
	return false
}

// Caller is the verified identity of whoever sent a request. The accepting
// module derives it from the channel, never from params.
type Caller struct {
	// Kind is "cli", "plugin", "input" or "host".
	Kind CallerKind `json:"kind"`
	// PluginID is set only for "plugin": the exact plugin id.
	PluginID string `json:"plugin_id,omitempty"`
	// ConnID is the Server's per-connection number; 0 for in-process callers.
	// It is never serialized.
	ConnID uint64 `json:"-"`
}

// FocusContext is the focus at dispatch time: State's focus slice, or, for an
// `input` caller, the snapshot Input read on the key press.
type FocusContext struct {
	// Surface is the focused surface id, "" when nothing has focus.
	Surface string `json:"surface"`
	// Owner is the plugin id occupying Surface, "" when unoccupied or host.
	Owner string `json:"owner"`
}
