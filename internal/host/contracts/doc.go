// Package contracts holds the Go-level contracts the host's adapters and
// composition root code against. Every interface is defined by its consumer;
// the owning module is adapted to it. The package imports only pkg/protocol,
// internal/substrate/state, internal/instance and the standard library. It
// never imports internal/services, internal/pods or internal/pods/server
// (internal/pods will import it).
//
// # Wire methods
//
// MethodHomes maps every protocol method to one port, its Go method and the
// caller kinds it accepts; tests keep it equal to protocol.Methods(). Outbound
// methods (event.deliver, state.changed, surface.resized, input.*,
// theme.changed, slot.contributions) are written by Pod.Notify or a Notifier
// and have no port. protocol.error is unrouted.
//
//	session.                               SessionPort     host-adapters-stem
//	action.                                CallsPort       host-adapters-stem
//	event.emit, event.watch                EventPort       host-adapters-stem
//	hook.call                              HookPort        host-adapters-stem
//	state.                                 StatePort       host-adapters-stem
//	surface.update/close, slot.contribute,
//	toast.show                             SurfacePort     host-adapters-stem
//	plugin.                                PluginPort      host-launch
//	settings. secrets. profile. theme.
//	keys. config.                          SettingsPort, SecretsPort, ProfilePort,
//	                                       ThemePort, KeybindingPort, ConfigPort
//	                                       host-adapters-services
//
// Port implementations are the adapters: host-adapters-stem implements the
// ports over State, Surfaces, the Dispatcher, Calls, the Event bus and the
// Hook runner (StatePort.Clear deletes Store's persisted copies before State
// clears, state.md); host-adapters-services implements its ports over
// Services plus the Limiter rule; host-launch implements PluginPort over
// pods.Host.
//
// # Other interfaces
//
//	Handler, Caller, Notifier   each adapter implements Handler; host-root
//	                            converts to the Server's types
//	PodRegistry, SliceRegistrar,
//	CatalogDeclarer, Reloader,
//	WrapperStarter              host-launch (over State, Store, Services, Pods)
//	Limiter, Clock              host-adapters-services
//	Config, Host                host-root
//	Occupant, Contribution      value types; host-state owns the State slices
//	                            and keymap data that carry them, adapters read
//
// host-state owns data (slices, keymap), never a port.
//
// # Launch seams and pods.Host
//
// The launch sequence in pods.md runs inside pods.Host.Add and Handle, which
// already reject duplicate ids, wait for the gate and start wrappers. The
// seams do not replace them; host-launch's pods.Loop implementation sits on
// them:
//
//	pods.Loop.Prepare   = CatalogDeclarer.Declare + SliceRegistrar.RegisterSlice
//	                      + Reloader.Reload (step 3 to 6, one reload per batch)
//	pods.Loop.Liveness  = PodRegistry.SetStatus (the husk.pods writes);
//	                      Reserve and Release guard the entry itself
//	pods.Host.Handle    starts the wrappers after the gate through
//	                      WrapperStarter.Start with the opening messages
//	pods.Loop.Opening   produces those opening messages: theme.changed,
//	                      surface.resized and input.key_state. Pods owns
//	                      them; host-launch fills them from the theme
//	                      service and the surface geometry.
//
// Subpackage fakes holds a recording fake per interface. Build every fake
// with fakes.NewX(rec) from one shared *fakes.Recorder to assert call order
// and caller attribution across fakes.
package contracts
