// Package state holds an instance's live, owner-registered JSON slice tree.
//
// Register, Get, Handle methods, Clear, OnFlush and Flush are loop-only: a
// single goroutine must serialize them. Snapshot is the only cross-goroutine
// read API. Each mutation publishes a new snapshot before returning. Published
// maps and byte slices are read-only and are never mutated by this package.
// Listeners run synchronously on the loop and must neither block nor mutate State.
// State does no I/O and imports no other Husk package.
package state

import (
	"encoding/json"
	"errors"
	"time"
)

// Scope determines which instances share a slice's persisted value.
type Scope string

const (
	// ScopeUser is shared by every instance of this user.
	ScopeUser Scope = "user"
	// ScopeInstance belongs to this instance, keyed by instance name.
	ScopeInstance Scope = "instance"
	// ScopeWorkspace belongs to the named instance's workspace snapshot.
	ScopeWorkspace Scope = "workspace"
)

// Policy tells Store how to retain persisted copies, never in-memory values.
type Policy string

const (
	// KeepLatest retains only the latest persisted value.
	KeepLatest Policy = "keep-latest"
	// KeepLastN retains the newest N persisted generations.
	KeepLastN Policy = "keep-last-n"
	// TTL expires persisted values after a duration since the last write.
	TTL Policy = "ttl"
	// Manual retains persisted values until explicitly deleted.
	Manual Policy = "manual"
)

// Retention is the persistence policy; it is zero for non-persistent slices.
type Retention struct {
	Policy Policy
	N      int           // KeepLastN only: 2..20.
	TTL    time.Duration // TTL only: 1m..8760h.
}

// Spec is a slice's immutable registration. Initial must be a JSON object;
// nil means {}. MaxBytes zero selects the class cap; a positive value lowers it.
type Spec struct {
	Namespace  string
	Owner      string
	Scope      Scope
	Persistent bool
	Retention  Retention
	MaxBytes   int
	Initial    json.RawMessage
}

// Descriptor is a slice's metadata as readers see it.
type Descriptor struct {
	Spec
	Revision uint64
	Bytes    int
	Hydrated bool
}

// Entry is one slice in a snapshot. Value and Spec.Initial are read-only.
type Entry struct {
	Descriptor
	Value json.RawMessage
}

// Snapshot is a read-only full view, safe on any goroutine. Neither Slices nor
// its entries' byte slices may be modified. Retaining a snapshot is safe: State
// never mutates it, even after subsequent writes or unregistration.
type Snapshot struct {
	Seq    uint64
	Slices map[string]Entry
}

// Change describes one slice's latest change in an Update call.
type Change struct {
	Namespace string
	Owner     string
	Revision  uint64
	Removed   bool
}

// Hydrator supplies persisted values inside Register. It must be a preloaded
// in-memory lookup: no blocking or I/O is allowed on the loop. It is called only
// for persistent slices, including user-scope slices in anonymous instances.
type Hydrator interface {
	Hydrate(spec Spec) (value json.RawMessage, ok bool)
}

// Listener receives a nonempty, namespace-ordered batch during Flush at the end
// of an Update call. It must not block, write to State, modify the arguments,
// or retain changes past the call. It may retain the read-only snapshot.
type Listener func(changes []Change, snap *Snapshot)

// Options supplies preloaded hydration and the distribution's workspace owner.
type Options struct {
	Hydrator       Hydrator
	WorkspaceOwner string // Empty permits only husk to register workspace scope.
}

var (
	// ErrDuplicate means the namespace is already registered.
	ErrDuplicate = errors.New("state: namespace already registered")
	// ErrInvalidSpec means a registration field is invalid.
	ErrInvalidSpec = errors.New("state: invalid slice spec")
	// ErrTooMany means the registered slice limit was reached.
	ErrTooMany = errors.New("state: slice limit reached")
	// ErrTooLarge means an encoded value exceeds the slice cap.
	ErrTooLarge = errors.New("state: value exceeds slice size cap")
	// ErrInvalidValue means a value or patch result is not a JSON object.
	ErrInvalidValue = errors.New("state: value is not a JSON object")
	// ErrConflict means a patch's expected revision is stale.
	ErrConflict = errors.New("state: revision mismatch")
	// ErrUnregistered means a handle's registration no longer exists.
	ErrUnregistered = errors.New("state: slice was unregistered")
)
