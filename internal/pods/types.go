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
}

// RegisterSlicesMsg carries the batch's slice registrations to the loop. The
// preparation worker posts it and waits for the acknowledgment before
// requesting the reload, so every registered slice exists when the first
// reload runs. Applying the closures is a State write: loop-only. A failure
// is acknowledged and fails only that plugin, like a declaration failure.
type RegisterSlicesMsg struct {
	Register map[string]func() (contracts.SliceHandle, error)
	Done     chan Registered
}

// Registered is the loop's acknowledgment of RegisterSlicesMsg: the applied
// handles, and the per-plugin registration failures (nil maps when none).
type Registered struct {
	Handles map[string]contracts.SliceHandle
	Failed  map[string]error
}

// TeardownMsg tears one Pod down on the loop: revokes its credential,
// unregisters its State slice through the stored owner handle and releases
// the registry id. The process must already have exited or been stopped by
// the shared shutdown; Store undeclaration and the follow-up reload stay
// with the composition root (pods.md teardown).
type TeardownMsg struct{ PluginID string }

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
	Prepare(context.Context, []manifest.Spec) Preparation
	Liveness(LivenessMsg)
	Log(LogMsg)
	Do(context.Context, func()) error
}

// Factory constructs real or fake process wrappers before token registration.
type Factory func(manifest.Spec, wrapper.Options) (wrapper.Process, error)

// Options supplies the wrapper configuration and optional fake-wrapper factory.
// Context, Post and Replies are wired by Host, not by individual callers.
// Registry is launch step 1 (duplicate id check) and Starter overrides launch
// step 6's built-in Wrapper start; both default to the built-in behaviour.
type Options struct {
	Wrapper wrapper.Options
	Factory Factory
	// Registry, when set, replaces the local duplicate-id check: Add calls
	// Reserve and accepts its conflict as the duplicate answer.
	Registry contracts.PodRegistry
	// Starter, when set, starts processes after the launch gate instead of
	// the built-in Factory/Wrapper path (Host.Start).
	Starter contracts.WrapperStarter
}

var _ contracts.WrapperStarter = (*Host)(nil)
