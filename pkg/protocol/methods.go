package protocol

// Method names defined by protocol 2.0. Payload ownership is documented below.
const (
	// MethodSessionPing reports instance identity and versions.
	MethodSessionPing = "session.ping"
	// MethodProtocolError rejects an uncorrelated line without expecting a reply.
	MethodProtocolError = "protocol.error"
	// MethodActionInvoke invokes an action; see design/message-kinds.md.
	MethodActionInvoke = "action.invoke"
	// MethodActionList lists actions; see design/message-kinds.md.
	MethodActionList = "action.list"
	// MethodEventEmit accepts an event; see design/message-kinds.md.
	MethodEventEmit = "event.emit"
	// MethodEventDeliver delivers an event; see design/message-kinds.md.
	MethodEventDeliver = "event.deliver"
	// MethodEventWatch watches events; see design/message-kinds.md.
	MethodEventWatch = "event.watch"
	// MethodHookCall calls hook subscribers; see design/hooks.md.
	MethodHookCall = "hook.call"
	// MethodHookList lists declared hooks and their subscribers (minor 2.1);
	// see design/hooks.md.
	MethodHookList = "hook.list"
	// MethodStateGet reads a slice; see design/state.md.
	MethodStateGet = "state.get"
	// MethodStateSet replaces a slice; see design/state.md.
	MethodStateSet = "state.set"
	// MethodStatePatch patches a slice; see design/state.md.
	MethodStatePatch = "state.patch"
	// MethodStateSnapshot takes a snapshot; see design/state.md.
	MethodStateSnapshot = "state.snapshot"
	// MethodStateWatch watches slices; see design/state.md.
	MethodStateWatch = "state.watch"
	// MethodStateClear clears a slice; see design/state.md.
	MethodStateClear = "state.clear"
	// MethodStateChanged reports a changed slice; see design/state.md.
	MethodStateChanged = "state.changed"
	// MethodSurfaceUpdate renders a surface; see design/protocol.md.
	MethodSurfaceUpdate = "surface.update"
	// MethodSurfaceClose closes a surface; see design/protocol.md.
	MethodSurfaceClose = "surface.close"
	// MethodSurfaceResized reports geometry; see design/protocol.md.
	MethodSurfaceResized = "surface.resized"
	// MethodInputKey delivers a key; see design/keybindings.md.
	MethodInputKey = "input.key"
	// MethodInputMouse delivers a mouse event; see design/protocol.md.
	MethodInputMouse = "input.mouse"
	// MethodInputKeyState delivers modifier state; see design/keybindings.md.
	MethodInputKeyState = "input.key_state"
	// MethodThemeChanged delivers a palette; see design/services.md.
	MethodThemeChanged = "theme.changed"
	// MethodThemeGet reads the palette; see design/services.md.
	MethodThemeGet = "theme.get"
	// MethodThemeCheck checks contrast; see design/services.md.
	MethodThemeCheck = "theme.check"
	// MethodSlotContribute fills an extension point; see design/message-kinds.md.
	MethodSlotContribute = "slot.contribute"
	// MethodSlotContributions reports contributions; see design/message-kinds.md.
	MethodSlotContributions = "slot.contributions"
	// MethodToastShow displays a toast; see design/protocol.md.
	MethodToastShow = "toast.show"
	// MethodSettingsGet reads settings; see design/services.md.
	MethodSettingsGet = "settings.get"
	// MethodSettingsList lists settings; see design/services.md.
	MethodSettingsList = "settings.list"
	// MethodSettingsSchema reads schemas; see design/services.md.
	MethodSettingsSchema = "settings.schema"
	// MethodSettingsValidate validates settings; see design/services.md.
	MethodSettingsValidate = "settings.validate"
	// MethodSecretsGet reads a secret; see design/services.md.
	MethodSecretsGet = "secrets.get"
	// MethodSecretsNames lists secret names; see design/services.md.
	MethodSecretsNames = "secrets.names"
	// MethodSecretsSet sets a secret; see design/services.md.
	MethodSecretsSet = "secrets.set"
	// MethodSecretsGrant grants access; see design/services.md.
	MethodSecretsGrant = "secrets.grant"
	// MethodSecretsRevoke revokes access; see design/services.md.
	MethodSecretsRevoke = "secrets.revoke"
	// MethodSecretsReveal reveals a secret; see design/services.md.
	MethodSecretsReveal = "secrets.reveal"
	// MethodKeysList lists bindings; see design/keybindings.md.
	MethodKeysList = "keys.list"
	// MethodProfileList lists profiles; see design/services.md.
	MethodProfileList = "profile.list"
	// MethodProfileUse switches profile; see design/services.md.
	MethodProfileUse = "profile.use"
	// MethodPluginList lists plugins; see design/pods.md.
	MethodPluginList = "plugin.list"
	// MethodPluginLog reads plugin logs; see design/pods.md.
	MethodPluginLog = "plugin.log"
	// MethodPluginRemove unloads one plugin from the running instance (a CLI
	// request, protocol minor 2.1); see design/pods.md "Removing a plugin".
	MethodPluginRemove = "plugin.remove"
	// MethodPluginAdd loads plugins from a path into the running instance (a
	// CLI request, protocol minor 2.2); see design/pods.md "Adding a plugin".
	MethodPluginAdd = "plugin.add"
	// MethodConfigReload reloads configuration; see design/services.md.
	MethodConfigReload = "config.reload"
)

// Methods returns all defined method names in specification order.
func Methods() []string {
	return []string{MethodSessionPing, MethodProtocolError, MethodActionInvoke, MethodActionList,
		MethodEventEmit, MethodEventDeliver, MethodEventWatch, MethodHookCall, MethodHookList,
		MethodStateGet, MethodStateSet, MethodStatePatch, MethodStateSnapshot, MethodStateWatch, MethodStateClear, MethodStateChanged,
		MethodSurfaceUpdate, MethodSurfaceClose, MethodSurfaceResized, MethodInputKey, MethodInputMouse, MethodInputKeyState,
		MethodThemeChanged, MethodThemeGet, MethodThemeCheck, MethodSlotContribute, MethodSlotContributions, MethodToastShow,
		MethodSettingsGet, MethodSettingsList, MethodSettingsSchema, MethodSettingsValidate,
		MethodSecretsGet, MethodSecretsNames, MethodSecretsSet, MethodSecretsGrant, MethodSecretsRevoke, MethodSecretsReveal,
		MethodKeysList, MethodProfileList, MethodProfileUse, MethodPluginList, MethodPluginLog, MethodPluginRemove, MethodPluginAdd, MethodConfigReload}
}

// MaxMethodBytes is the maximum encoded method-name length.
const MaxMethodBytes = 256

// ValidateMethod accepts exactly two nonempty [a-z0-9_-]+ segments.
// A syntactically valid but unknown method is left for the router to reject.
func ValidateMethod(name string) error {
	if len(name) == 0 || len(name) > MaxMethodBytes {
		return invalid("method must be between 1 and 256 bytes")
	}
	dots, segment := 0, 0
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '.' {
			if segment == 0 || dots != 0 {
				return invalid("method must have exactly two nonempty segments")
			}
			dots++
			segment = 0
			continue
		}
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return invalid("method segments must match [a-z0-9_-]+")
		}
		segment++
	}
	if dots != 1 || segment == 0 {
		return invalid("method must have exactly two nonempty segments")
	}
	return nil
}

// SessionPingParams is empty; session.ping has no required parameters.
type SessionPingParams struct{}

// SessionPingResult identifies the instance. HostVersion is informational;
// compatibility decisions use ProtocolVersion only.
type SessionPingResult struct {
	ID              string `json:"id"`
	PID             int    `json:"pid"`
	ProtocolVersion string `json:"protocol_version"`
	HostVersion     string `json:"host_version"`
	UptimeMS        int64  `json:"uptime_ms"`
}

// EmptyResult is the successful result of a method with nothing to return.
type EmptyResult struct{}
