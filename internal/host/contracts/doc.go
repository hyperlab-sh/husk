// Package contracts holds the Go-level contracts the host's adapters and
// composition root code against. Every interface is defined by its consumer;
// the owning module is adapted to it. The package imports only pkg/protocol,
// internal/substrate/state, internal/instance and the standard library, never
// internal/services or internal/pods/server.
//
// Method prefix to port to implementing unit (MethodHomes keeps this table
// equal to protocol.Methods(); outbound methods have no inbound port):
//
//	session.             SessionPort      host-adapters-stem
//	action.              CallsPort        host-adapters-stem
//	event.emit/watch     EventPort        host-adapters-stem
//	hook.call            HookPort         host-adapters-stem
//	state.               StatePort        host-state
//	surface. slot.contribute toast.  SurfacePort  host-adapters-stem
//	plugin.              PluginPort       host-launch
//	settings. secrets. profile. theme. keys. config.
//	                     SettingsPort, SecretsPort, ProfilePort, ThemePort,
//	                     KeybindingPort, ConfigPort   host-adapters-services
//	event.deliver, state.changed, surface.resized, input., theme.changed,
//	slot.contributions   outbound (Pod.Notify or Notifier), no port
//	protocol.error       unrouted
//
// Other implementers: Handler, Caller, Notifier belong to the adapters
// (host-root converts them to the Server's types); PodRegistry and
// SliceRegistrar to host-state; CatalogDeclarer, Reloader and WrapperStarter
// to host-launch; Limiter and Clock to host-adapters-services; Config and
// Host to host-root; Occupant and Contribution to host-state, read by the
// adapters. Hook and event declarations are manifest only, so there is no
// registration port.
//
// Subpackage fakes holds a recording or no-op fake per interface.
package contracts
