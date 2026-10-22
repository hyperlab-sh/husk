// Package pods ties converted plugin specs to wrappers and loop-owned lifecycle
// state. Store, Services and State are reached only through the Loop seam.
package pods

import (
	tea "charm.land/bubbletea/v2"
	"context"
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

// ClosedMsg must be handled by the loop with Calls.DropPlugin before applying
// later exit liveness. Host.Handle deliberately does not consume this message.
type ClosedMsg = wrapper.ClosedMsg

// LogMsg is written to host logs only by Loop.Log on the loop goroutine.
type LogMsg = wrapper.LogMsg

// LivenessMsg is the explicit seam for writing husk.pods and publishing exits.
type LivenessMsg = wrapper.LivenessMsg

// Preparation separates fatal declaration/slice errors from a reload report.
// A failed or timed-out reload is completed, so ReloadError does not block launch.
type Preparation struct {
	Failed      map[string]error
	ReloadError error
}

// PreparedMsg is a preparation command's completion, handled on the loop.
type PreparedMsg struct {
	Gate        uint64
	Preparation Preparation
}

// StoppedMsg is the bounded shared shutdown command's completion.
type StoppedMsg struct{ Err error }

// Loop is the single interface Pods needs from the host. Post is worker-safe,
// may apply inbound backpressure, and must return during shutdown. Opening,
// Liveness and Log are called only on the loop and must not block. Liveness is
// the clearly named State/event seam; Pods itself never imports/writes State.
// Prepare runs in a tea.Cmd worker, may wait on Store/Services, and must marshal
// any State mutations back onto the owning loop rather than writing directly.
// It declares catalogs/registers slices and waits for the reload to complete.
type Loop interface {
	Post(tea.Msg)
	Opening(pluginID string) []protocol.Message
	Prepare(context.Context, []manifest.Spec) Preparation
	Liveness(LivenessMsg)
	Log(LogMsg)
}

// Factory constructs real or fake process wrappers before token registration.
type Factory func(manifest.Spec, wrapper.Options) (wrapper.Process, error)

// Options supplies the wrapper configuration and optional fake-wrapper factory.
// Context, Post and Replies are wired by Host, not by individual callers.
type Options struct {
	Wrapper wrapper.Options
	Factory Factory
}
