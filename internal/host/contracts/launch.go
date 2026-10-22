package contracts

import (
	"context"
	"encoding/json"

	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// PodStatus is a husk.pods status value.
type PodStatus string

const (
	// PodStarting means launch began and the process has not started.
	PodStarting PodStatus = "starting"
	// PodLive means the process started.
	PodLive PodStatus = "live"
	// PodExited means a clean exit or a stop by Husk.
	PodExited PodStatus = "exited"
	// PodFailed means a launch failure or crash.
	PodFailed PodStatus = "failed"
)

// PodRegistry is launch steps 1 and 2 and the later status writes: the
// duplicate-id check and the husk.pods slice. Loop-only.
type PodRegistry interface {
	// Reserve claims pluginID. A duplicate returns a CodedError (conflict)
	// and changes nothing; the first loaded wins.
	Reserve(pluginID string) error
	// SetStatus writes status for a reserved id. detail is a last-error text,
	// empty when none.
	SetStatus(pluginID string, status PodStatus, detail string) error
	// Release forgets pluginID when the Pod is torn down.
	Release(pluginID string)
}

// CatalogDeclarer is launch step 3's Store half. The JSON comes from the
// manifest Spec's CatalogJSON and FocusKeymapJSON. It may run off the loop.
type CatalogDeclarer interface {
	// Declare stores the plugin's catalog document and focus-keymap document
	// as its declared documents, replacing earlier ones.
	Declare(pluginID string, catalog, focusKeymap json.RawMessage) error
	// Undeclare removes both documents (Pod teardown only).
	Undeclare(pluginID string)
}

// Reloader is launch step 3's Services half: one coalesced Services reload
// with reason "plugin". Overlapping callers share a flight. A failed or timed
// out reload still completes, so the launch gate opens either way.
type Reloader interface {
	// Reload blocks until the reload requested now has completed. applied is
	// false when any service kept its old snapshot.
	Reload(ctx context.Context) (applied bool, problems []Problem)
}

// SliceHandle is the write capability a Pod holds on its plugin's slice.
// *state.Handle satisfies it.
type SliceHandle interface {
	Set(value json.RawMessage) (uint64, error)
	Patch(patch json.RawMessage, ifRevision uint64) (uint64, error)
	Unregister()
}

var _ SliceHandle = (*state.Handle)(nil)

// SliceRegistrar is launch step 4: the plugin slice, owned by the Pod.
// Loop-only. A duplicate namespace or invalid spec returns the State error.
type SliceRegistrar interface {
	// RegisterSlice registers spec and returns the owner handle.
	RegisterSlice(spec state.Spec) (SliceHandle, error)
}

// WrapperStarter is launch step 6: start the process after the gate.
type WrapperStarter interface {
	// Start launches the plugin's process with opening queued on stdin before
	// anything else. A nil error means the process started (the caller then
	// writes PodLive).
	Start(pluginID string, opening []protocol.Message) error
}
