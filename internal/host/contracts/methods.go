package contracts

import "github.com/hyperlab-sh/husk/pkg/protocol"

// Outbound marks wire methods Husk only sends (stdin of a plugin, or a
// watching connection through a Notifier). They have no inbound port.
const Outbound = "outbound"

// Unrouted marks protocol.error, which no handler serves.
const Unrouted = "unrouted"

// MethodHomes maps every protocol method to the one port interface that
// serves it. A test keeps it equal to protocol.Methods().
func MethodHomes() map[string]string {
	h := map[string]string{
		protocol.MethodProtocolError: Unrouted,

		protocol.MethodSessionPing: "SessionPort",

		protocol.MethodActionInvoke: "CallsPort",
		protocol.MethodActionList:   "CallsPort",

		protocol.MethodEventEmit:    "EventPort",
		protocol.MethodEventWatch:   "EventPort",
		protocol.MethodEventDeliver: Outbound,

		protocol.MethodHookCall: "HookPort",

		protocol.MethodStateGet:      "StatePort",
		protocol.MethodStateSet:      "StatePort",
		protocol.MethodStatePatch:    "StatePort",
		protocol.MethodStateSnapshot: "StatePort",
		protocol.MethodStateWatch:    "StatePort",
		protocol.MethodStateClear:    "StatePort",
		protocol.MethodStateChanged:  Outbound,

		protocol.MethodSurfaceUpdate:  "SurfacePort",
		protocol.MethodSurfaceClose:   "SurfacePort",
		protocol.MethodSurfaceResized: Outbound,

		protocol.MethodInputKey:      Outbound,
		protocol.MethodInputMouse:    Outbound,
		protocol.MethodInputKeyState: Outbound,

		protocol.MethodThemeChanged: Outbound,
		protocol.MethodThemeGet:     "ThemePort",
		protocol.MethodThemeCheck:   "ThemePort",

		protocol.MethodSlotContribute:    "SurfacePort",
		protocol.MethodSlotContributions: Outbound,
		protocol.MethodToastShow:         "SurfacePort",

		protocol.MethodSettingsGet:      "SettingsPort",
		protocol.MethodSettingsList:     "SettingsPort",
		protocol.MethodSettingsSchema:   "SettingsPort",
		protocol.MethodSettingsValidate: "SettingsPort",

		protocol.MethodSecretsGet:    "SecretsPort",
		protocol.MethodSecretsNames:  "SecretsPort",
		protocol.MethodSecretsSet:    "SecretsPort",
		protocol.MethodSecretsGrant:  "SecretsPort",
		protocol.MethodSecretsRevoke: "SecretsPort",
		protocol.MethodSecretsReveal: "SecretsPort",

		protocol.MethodKeysList: "KeybindingPort",

		protocol.MethodProfileList: "ProfilePort",
		protocol.MethodProfileUse:  "ProfilePort",

		protocol.MethodPluginList: "PluginPort",
		protocol.MethodPluginLog:  "PluginPort",

		protocol.MethodConfigReload: "ConfigPort",
	}
	return h
}
