// Package host is the composition root. New builds Store, State, Services,
// Calls, Pods and the Server in dependency order, registers every adapter
// from pkg/host/handlers on the Server, wires the launch seams and
// returns a contracts.Host. It is the only package that imports Stem,
// Services, Substrate and Pods together, and it holds wiring only: every rule
// lives in the module it wires.
//
// # Loop
//
// The composition does not own the event loop. The caller posts what
// cfg.Post receives back into the host through Looped.Update on the loop
// goroutine; Update applies host closures, Calls replies, Pod lifecycle
// messages and the per-tick flushes (State, Surfaces, Event bus, Calls sweep
// scheduling) in that order.
//
// # Lifecycle
//
// New does not listen and starts no plugin. Run starts the Server, then adds
// every configured plugin as one launch batch and blocks until its context
// ends or Close is called. Close is idempotent and stops the Calls table, the
// Server (which unregisters the instance), the plugin processes and finally
// flushes the Store. A failure in any New stage or in Run closes everything
// already started.
//
// # Host-owned launch content
//
// The host decides what a Pod sees as it opens (theme.changed,
// surface.resized, input.key_state), registers each plugin's occupants and
// contributions declaration slices before the first reload, drops them with
// the Pod, and tells the Surfaces engine whenever the published-slot catalog
// changed.
package host
