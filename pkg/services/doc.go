// Package services is the host-independent v2 Service composition root.
//
// Construct Runtime on the loop after Store/State exist and Store has been
// attached. Supply Store.Secrets("husk-secrets") as Crypto (obtaining the handle
// performs no I/O), a queued Post, a value-free Publisher, and a dialog callback
// that returns its answer channel promptly. Close Runtime at shutdown.
//
// Server adapter map (Caller is authenticated outside Services):
//
//   - config.reload: Coordinator.ReloadFor
//   - profile.list/use: Profile.ListFor / Profile.Use
//   - settings.get/list/schema: Settings.Get / List / SchemaFor
//   - settings.validate: Coordinator.Inputs then Settings.Validate
//   - theme.get: Theme.Get (Wire is also the notification palette)
//   - theme.check: Coordinator.Inputs, Settings.Preview, then Theme.Check with
//     the preview in input.Settings; neither live cache is changed
//   - keys.list: Keybinding.ListFor, then Server joins catalog/State liveness
//   - secrets.get/names/set/grant/revoke/reveal: Secrets.Read / Names / Set /
//     Grant / Revoke / Reveal. Names preserves metadata with Broken=true when
//     also returning unavailable; a list adapter can render that broken state.
//
// User mutations acknowledge enqueueing on Post; State changes are visible when
// that loop callback executes. Callers needing a loop completion reply wrap Post
// at the adapter boundary. The private dialog grant rechecks declaration digest.
// Exposures never accept identity from params. contracts.Error carries the wire
// code and reason for a mechanical protocol error adapter, without secret data.
// The Server, not Services, owns per-plugin request rate limiting.
package services
