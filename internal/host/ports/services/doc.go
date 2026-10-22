// Package services implements the host's Settings, Secrets, Profile, Theme,
// Keybinding and Config ports over the real internal/services packages. It is
// the only place that imports both host contracts and Services: each wrapper
// converts a contracts.Caller into the services caller, calls the service and
// maps services errors onto contracts-coded errors, so the adapters above
// stay wire-only and Services stays host-free.
//
// Authorization stays where services.md puts it: the services enforce
// user-only mutation and the plugin view (exact declared keys plus public
// husk.*, granted secrets only); the wrappers translate, they do not widen.
// The Settings wrapper additionally narrows services' denied/undeclared_setting
// refusal to the port contract's not_found, so a plugin cannot even learn
// whether another owner's key exists.
package services
