package contracts

import "github.com/hyperlab-sh/husk/pkg/protocol"

// Outbound marks wire methods Husk only sends (a plugin's stdin, or a
// watching connection through a Notifier). They have no inbound port and no
// allowed caller kinds.
const Outbound = "outbound"

// Unrouted marks protocol.error, which never has an inbound handler.
const Unrouted = "unrouted"

// MethodHome says which port interface serves one wire method, under which
// Go method name, and which caller kinds it accepts. Any other kind is
// denied by the adapter before the port is called.
type MethodHome struct {
	// Port is the port interface name, or Outbound or Unrouted.
	Port string
	// Method is the port's Go method name; empty for Outbound and Unrouted.
	Method string
	// Kinds are the allowed caller kinds, from protocol.md's Direction
	// column; empty for Outbound and Unrouted.
	Kinds []CallerKind
}

var (
	anyCaller  = []CallerKind{CallerCLI, CallerPlugin}
	cliOnly    = []CallerKind{CallerCLI}
	pluginOnly = []CallerKind{CallerPlugin}
)

func home(port, method string, kinds []CallerKind) MethodHome {
	return MethodHome{Port: port, Method: method, Kinds: append([]CallerKind(nil), kinds...)}
}

// MethodHomes maps every protocol method to its one home. Tests keep it equal
// to protocol.Methods() and check that each named port and method exists.
//
// Reading of protocol.md: "plugin or CLI" is CLI and Plugin; "CLI to Husk"
// is CLI only; state.set and state.patch are plugin only (a bare CLI is
// denied, state.md); action.invoke also accepts Stem (Input and the palette
// act for the user); hook.call is plugin only (the CLI is not at any
// owner's point, hooks.md "Who fires a hook"); settings.list is CLI only
// (plugins use settings.get on their own keys).
func MethodHomes() map[string]MethodHome {
	return map[string]MethodHome{
		protocol.MethodProtocolError: {Port: Unrouted},

		protocol.MethodSessionPing: home("SessionPort", "Ping", anyCaller),

		protocol.MethodActionInvoke: home("CallsPort", "ActionInvoke", []CallerKind{CallerCLI, CallerPlugin, CallerHost}),
		protocol.MethodActionList:   home("CallsPort", "ActionList", anyCaller),

		protocol.MethodEventEmit:    home("EventPort", "Emit", anyCaller),
		protocol.MethodEventWatch:   home("EventPort", "Watch", anyCaller),
		protocol.MethodEventDeliver: {Port: Outbound},

		protocol.MethodHookCall: home("HookPort", "HookCall", pluginOnly),
		protocol.MethodHookList: home("HookPort", "HookList", anyCaller),

		protocol.MethodStateGet:      home("StatePort", "Get", anyCaller),
		protocol.MethodStateSet:      home("StatePort", "Set", pluginOnly),
		protocol.MethodStatePatch:    home("StatePort", "Patch", pluginOnly),
		protocol.MethodStateSnapshot: home("StatePort", "Snapshot", anyCaller),
		protocol.MethodStateWatch:    home("StatePort", "Watch", anyCaller),
		protocol.MethodStateClear:    home("StatePort", "Clear", cliOnly),
		protocol.MethodStateChanged:  {Port: Outbound},

		protocol.MethodSurfaceUpdate:  home("SurfacePort", "SurfaceUpdate", pluginOnly),
		protocol.MethodSurfaceClose:   home("SurfacePort", "SurfaceClose", pluginOnly),
		protocol.MethodSurfaceResized: {Port: Outbound},

		protocol.MethodTerminalAttach: home("TerminalPort", "TerminalAttach", pluginOnly),
		protocol.MethodTerminalWrite:  home("TerminalPort", "TerminalWrite", pluginOnly),
		protocol.MethodTerminalDetach: home("TerminalPort", "TerminalDetach", pluginOnly),
		protocol.MethodTerminalInput:  {Port: Outbound},

		protocol.MethodInputKey:      {Port: Outbound},
		protocol.MethodInputMouse:    {Port: Outbound},
		protocol.MethodInputKeyState: {Port: Outbound},

		protocol.MethodThemeChanged: {Port: Outbound},
		protocol.MethodThemeGet:     home("ThemePort", "Get", anyCaller),
		protocol.MethodThemeList:    home("ThemePort", "List", anyCaller),
		protocol.MethodThemeCheck:   home("ThemePort", "Check", cliOnly),

		protocol.MethodSlotContribute:    home("SurfacePort", "SlotContribute", pluginOnly),
		protocol.MethodSlotContributions: {Port: Outbound},
		protocol.MethodToastShow:         home("SurfacePort", "ToastShow", anyCaller),

		protocol.MethodSettingsGet:      home("SettingsPort", "Get", anyCaller),
		protocol.MethodSettingsList:     home("SettingsPort", "List", cliOnly),
		protocol.MethodSettingsSchema:   home("SettingsPort", "Schema", cliOnly),
		protocol.MethodSettingsValidate: home("SettingsPort", "Validate", cliOnly),
		protocol.MethodSettingsSet:      home("SettingsPort", "Set", cliOnly),

		protocol.MethodSecretsGet:    home("SecretsPort", "Get", anyCaller),
		protocol.MethodSecretsNames:  home("SecretsPort", "Names", anyCaller),
		protocol.MethodSecretsSet:    home("SecretsPort", "Set", cliOnly),
		protocol.MethodSecretsGrant:  home("SecretsPort", "Grant", cliOnly),
		protocol.MethodSecretsRevoke: home("SecretsPort", "Revoke", cliOnly),
		protocol.MethodSecretsReveal: home("SecretsPort", "Reveal", cliOnly),

		protocol.MethodKeysList: home("KeybindingPort", "List", anyCaller),

		protocol.MethodProfileList: home("ProfilePort", "List", cliOnly),
		protocol.MethodProfileUse:  home("ProfilePort", "Use", cliOnly),

		protocol.MethodPluginList:   home("PluginPort", "List", cliOnly),
		protocol.MethodPluginLog:    home("PluginPort", "Log", cliOnly),
		protocol.MethodPluginRemove: home("PluginPort", "Remove", cliOnly),
		protocol.MethodPluginAdd:    home("PluginPort", "Add", cliOnly),

		protocol.MethodConfigReload: home("ConfigPort", "Reload", cliOnly),
	}
}
