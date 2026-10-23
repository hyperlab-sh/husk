// Package pods ties converted plugin specs to wrappers and loop-owned lifecycle
// state. Store, Services and State are reached only through the Loop seam and
// the host contracts launch seams (PodRegistry, WrapperStarter): Pods never
// imports a State, Store or Services implementation.
package pods

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// ExitMsg is emitted by a completed process launch.
type ExitMsg = wrapper.ExitMsg

// LaunchFailedMsg is emitted when a process could not start.
type LaunchFailedMsg = wrapper.LaunchFailedMsg

// ReapedMsg is the credential-revocation precursor to drained exit notices.
type ReapedMsg = wrapper.ReapedMsg

// ChannelClosedMsg revokes a closed pipe's credential before the reap grace.
type ChannelClosedMsg = wrapper.ChannelClosedMsg

// ClosedMsg must be handled by the loop with Calls.DropPlugin before applying
// later exit liveness. Host.Handle deliberately does not consume this message.
type ClosedMsg = wrapper.ClosedMsg

// LogMsg is written to host logs only by Loop.Log on the loop goroutine.
type LogMsg = wrapper.LogMsg

// LivenessMsg is the explicit seam for writing husk.pods and publishing exits.
type LivenessMsg = wrapper.LivenessMsg

// Preparation separates fatal declaration/slice errors from a reload report.
// A failed or timed-out reload is completed, so ReloadError does not block launch.
// Slice registrations are NOT carried here: the worker applies them earlier,
// through RegisterSlicesMsg on the loop, because the reload must observe them
// (store.md startup order) and State writes are loop-only.
type Preparation struct {
	Failed      map[string]error
	ReloadError error
	Reports     []contracts.ReloadReport
}

// RegisterSlicesMsg carries the batch's slice registrations to the loop. The
// preparation worker posts it and waits for the acknowledgment before
// requesting the reload, so every registered slice exists when the first
// reload runs. Applying the closures is a State write: loop-only. A failure
// is acknowledged and fails only that plugin, like a declaration failure.
// Gate names the launch gate that owns the batch: an id is accepted only
// while that gate still holds it, so a registration from a torn-down
// launch of the same id is refused instead of racing the new one.
type RegisterSlicesMsg struct {
	Gate     uint64
	Register map[string]func() (contracts.SliceHandle, error)
	Done     chan Registered
}

// Registered is the loop's acknowledgment of RegisterSlicesMsg: the applied
// handles, and the per-plugin registration failures (nil maps when none).
type Registered struct {
	Handles map[string]contracts.SliceHandle
	Failed  map[string]error
}

// TeardownMsg is the stop worker's completion in a plugin.remove, handled on
// the loop (pods.md "Removing a plugin", step 5): the handler revokes the
// Pod's credential, unregisters its State slice through the stored owner
// handle, releases the registry id, records the reply's status and log,
// undeclares the plugin and starts the reload worker. It runs only for an id
// with a removing entry and never after the shutdown mark; any other
// TeardownMsg is stale and ignored. Unreaped says the process survived
// SIGKILL past the kill wait.
type TeardownMsg struct {
	PluginID string
	Unreaped bool
}

// RemoveDoneMsg is the reload worker's completion in a plugin.remove (step
// 7). The handler records the reports, or the cancellation when the reload
// was cut short with no report, closes the removing entry's completion channel
// and deletes the entry. A message for an id with no entry is stale.
type RemoveDoneMsg struct {
	PluginID string
	Reports  []contracts.ReloadReport
	Err      error
}

// PreparedMsg is a preparation command's completion, handled on the loop.
type PreparedMsg struct {
	Gate        uint64
	Preparation Preparation
}

// StoppedMsg is the bounded shared shutdown command's completion.
type StoppedMsg struct{ Err error }

// Loop is the single interface Pods needs from the host, and its seam onto
// the host's Bubble Tea loop: the one goroutine that may read and mutate the
// Host's Pod maps. Post is worker-safe, may apply inbound backpressure, and
// must return during shutdown. Opening, Liveness and Log are called only on
// the loop and must not block; Liveness is the clearly named State/event
// seam, and Pods itself never imports or writes State. Prepare runs in a
// tea.Cmd worker, may wait on Store/Services, and must marshal any State
// mutations back onto the owning loop rather than writing directly; it
// declares catalogs, registers slices and waits for the reload to complete.
// Do runs fn on the loop goroutine and returns once it has run (an error,
// without running fn, when ctx ends first or the loop has shut down); the
// ports use it to read loop-owned state from off-loop callers such as
// Server handlers.
type Loop interface {
	Post(tea.Msg)
	Opening(pluginID string) []protocol.Message
	Prepare(context.Context, uint64, []manifest.Spec) Preparation
	Liveness(LivenessMsg)
	Log(LogMsg)
	Do(context.Context, func()) error
}

// Publisher accepts events from the loop. Pods publish `pods.plugin-exited`
// through it and import nothing from the Stem; the host adapts it to the Event
// bus. A nil Publisher drops events.
type Publisher interface {
	Publish(name string, data any)
}

// PluginExitedEvent is the name of the event published when a Pod exits or
// fails; its payload is the Pod's husk.pods entry plus `plugin_id`.
const PluginExitedEvent = "pods.plugin-exited"

// PluginRemovedEvent is the name of the event published after a plugin.remove
// undeclared the plugin; its payload is {plugin_id, status}.
const PluginRemovedEvent = "pods.plugin-removed"

// Workers is the composition root's lifecycle seam: what the teardown
// tick needs beyond Pods' own state, and the workers that follow it. Without
// one, teardown undeclares nothing, workers are plain goroutines posting
// through Loop.Post, and the reload reports are empty.
type Workers interface {
	// Added attaches loop-side delivery bookkeeping to a newly registered Pod.
	Added(pluginID string)
	// Undeclare runs on the loop inside the teardown tick, after the Pod's
	// slices are unregistered: it removes the plugin's declared documents
	// (Store.Undeclare) and refreshes every reader derived from them.
	Undeclare(pluginID string)
	// Reload runs in the reload worker: one Services reload with reason
	// plugin, under ctx. Reports come back whether or not every service
	// applied; the error is set only when no reload completed: a context error
	// means the Host is shutting down, anything else is an internal failure.
	Reload(ctx context.Context) ([]contracts.ReloadReport, error)
	// Go starts cmd as a tracked worker and posts the message it returns. It
	// does nothing once shutdown began.
	Go(cmd tea.Cmd)
}

// Converter is the handler-side, path-only conversion seam shared with startup.
// Implementations are configured once before use and safe for concurrent reads.
type Converter interface {
	LoadPath(string) (*manifest.Spec, []manifest.Warning, error)
	LoadDir(string) ([]manifest.Result, error)
}

// PluginAddedEvent announces a live or failed runtime addition after its gate.
const PluginAddedEvent = "pods.plugin-added"

// Factory constructs real or fake process wrappers before token registration.
type Factory func(manifest.Spec, wrapper.Options) (wrapper.Process, error)

// Options supplies the wrapper configuration and optional fake-wrapper factory.
// Context, Post and Replies are wired by Host, not by individual callers.
// Registry is launch step 1 (duplicate id check) and Starter overrides launch
// step 6's built-in Wrapper start; both default to the built-in behaviour.
type Options struct {
	// Converter reads plugin inputs without mutating instance state.
	Converter Converter
	Wrapper   wrapper.Options
	Factory   Factory
	// Registry, when set, replaces the local duplicate-id check: Add calls
	// Reserve and accepts its conflict as the duplicate answer.
	Registry contracts.PodRegistry
	// Starter, when set, starts processes after the launch gate instead of
	// the built-in Factory/Wrapper path (Host.Start).
	Starter contracts.WrapperStarter
	// Publisher receives `pods.plugin-exited` and `pods.plugin-removed` from
	// Handle.
	Publisher Publisher
	// Workers supplies tracked lifecycle workers and loop-side bookkeeping.
	Workers Workers
}

var _ contracts.WrapperStarter = (*Host)(nil)
