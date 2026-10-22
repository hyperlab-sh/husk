// Package contracts holds the Go-level contracts the host's adapters and
// composition root code against. Every interface is defined by its consumer;
// the owning module is adapted to it. The package imports only pkg/protocol,
// internal/substrate/state, internal/instance and the standard library, never
// internal/services or internal/pods/server.
//
// Who implements what:
//
//   - Handler, Caller, Notifier: the adapters (host-adapters-stem for the
//     state, action and session families; host-adapters-services for the
//     settings, secrets, profile, theme and keys families). host-root converts
//     them to the Server's own types.
//   - StatePort: host-state. CallsPort and SessionPort: host-adapters-stem
//     (Dispatcher and Calls wiring). SettingsPort, SecretsPort, ProfilePort,
//     ThemePort, KeybindingPort: host-adapters-services over Services.
//   - PodRegistry, SliceRegistrar: host-state. CatalogDeclarer, Reloader,
//     WrapperStarter: host-launch.
//   - Limiter, Clock: host-adapters-services (production limiter and fake
//     clock in tests).
//   - Config, Host: host-root.
//   - Occupant, Contribution: host-state, read by the adapters.
//
// Subpackage fakes holds a recording or no-op fake per interface.
package contracts
