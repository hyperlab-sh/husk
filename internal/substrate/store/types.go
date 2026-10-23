// Package store owns Husk's on-disk persistence mechanics. It merges layered
// configuration without interpreting keys, stores encrypted secret namespaces,
// and hydrates/persists State through its public hooks. Except Attach, Hydrate
// and the State listener, methods run off the loop. No disk I/O runs in those
// loop callbacks. All health writes are posted back through Options.Post.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
)

// Doc is a decoded JSON object; arrays replace wholesale during layer merges.
type Doc = map[string]any

// DocName identifies a layered document: lowercase segments joined by '/'.
type DocName string

// Layer identifies fixed configuration precedence.
type Layer string

const (
	// LayerDistribution is the one read-only distribution layer.
	LayerDistribution Layer = "distribution"
	// LayerUser contains user overrides.
	LayerUser Layer = "user"
	// LayerProfile contains active-profile overrides.
	LayerProfile Layer = "profile"
	// LayerInstance contains named-instance files followed by flags.
	LayerInstance Layer = "instance"
	// DeclaredPlugins holds per-plugin catalogs, separate from config layers.
	DeclaredPlugins DocName = "plugins"
	// DeclaredKeymap holds manifest focus-local bindings (D64), never user
	// keymaps. Its documents are manifest.Keymap values, written by
	// Spec.KeymapJSON through launch.CatalogDeclarer. The layered
	// (distribution/user) keymap (D90) is the "keymap" layer files read by
	// Layers/Resolve and consumed by internal/services/keybinding; declared
	// documents are a separate namespace (see Declared) and never enter layers.
	DeclaredKeymap DocName = "keymap"
	// ConflictLastWriterWins is the reserved multi-source reconciliation policy.
	ConflictLastWriterWins = "last-writer-wins"
	// MaxDocBytes bounds a config document's input bytes.
	MaxDocBytes = 1024 * 1024
	// MaxDepth bounds nesting during parsing and merging.
	MaxDepth = 64
)

// Problem describes a skipped layer or type clash, without aborting resolution.
type Problem struct {
	// Code categorizes the problem.
	Code string `json:"code"`
	// Message explains the failure.
	Message string `json:"message"`
	// File names the source file, if any.
	File string `json:"file,omitempty"`
	// Path is a dotted key path, when known.
	Path string `json:"path,omitempty"`
	// Layer identifies the affected or higher layer.
	Layer Layer `json:"layer,omitempty"`
	// LowerLayer identifies the overwritten layer for a type clash.
	LowerLayer Layer `json:"lower_layer,omitempty"`
	// Line is the one-based source line, when known.
	Line int `json:"line,omitempty"`
	// Column is the one-based source column, when known.
	Column int `json:"column,omitempty"`
}

// Resolved is one layered document plus leaf provenance and load problems.
type Resolved struct {
	// Doc is an independent merged object.
	Doc Doc
	// Origins maps dotted leaf paths to their winning layers.
	Origins map[string]Layer
	// Problems lists skipped layers and type clashes.
	Problems []Problem
}

// LayerDoc is one unmerged layer, tagged with its source.
type LayerDoc struct {
	// Layer identifies precedence.
	Layer Layer
	// File is config-relative and slash-separated; distribution files are relative
	// to their root. It is empty for a layer with no single file (flag-folded instance).
	File string
	// Doc is an independent unmerged object.
	Doc Doc
	// MemberOrder records object keys in file order, keyed by JSON pointer.
	// Programmatic objects without an order use lexical key order.
	MemberOrder map[string][]string
}

// InstanceRef selects persistence eligibility and the instance storage key.
type InstanceRef struct {
	// ID is a validated name or a positive decimal pid.
	ID string
}

// Named reports whether the id is a name rather than a pid id, decided by its
// spelling alone (instance.ValidName). Only a named instance persists
// instance-scoped and workspace-scoped slices; user scope always persists.
func (r InstanceRef) Named() bool { return instance.ValidName(r.ID) == nil }

// Env makes environment resolution injectable, without changing process globals.
type Env struct {
	// LookupEnv defaults to os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// HomeDir defaults to os.UserHomeDir.
	HomeDir func() (string, error)
}

// Timer is a stoppable clock event used for debounce and periodic collection.
type Timer interface {
	// C returns the timer's delivery channel.
	C() <-chan time.Time
	// Stop prevents a pending delivery when possible.
	Stop() bool
}

// Clock supplies time and timers; tests inject a deterministic implementation.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// NewTimer creates a one-shot timer.
	NewTimer(time.Duration) Timer
}

// Keyring isolates the OS credential store for deterministic tests.
type Keyring interface {
	// Get returns the stored credential or an error.
	Get(service, account string) (string, error)
	// Set stores a credential.
	Set(service, account, value string) error
	// Delete removes a credential; a missing one is an ErrNotFound-style
	// absence, not a failure.
	Delete(service, account string) error
}

// Options configures paths, layered input, clock and loop delivery.
type Options struct {
	// Instance identifies this process.
	Instance InstanceRef
	// Distribution is the one read-only distribution layer; nil means none.
	Distribution fs.FS
	// InstanceDocs contains flag-derived instance-layer documents.
	InstanceDocs map[DocName]Doc
	// Env supplies XDG lookups and home directory resolution.
	Env Env
	// RuntimeDir is the instance runtime directory, resolved once by the
	// opener: start mode passes what instance.RuntimeDir returned (the same
	// string the Server gets), a local command passes what
	// instance.LookupRuntimeDir found. "" means no runtime directory exists,
	// so no instance is registered and liveness probes find none. Store
	// never resolves or creates the directory itself.
	RuntimeDir string
	// Clock supplies debounce and GC time; nil uses the real clock.
	Clock Clock
	// Post enqueues a callback on the loop, returning false after shutdown.
	// It must not execute the callback on the calling background goroutine.
	Post func(func()) bool
	// Keyring overrides the OS keyring, normally only in tests.
	Keyring Keyring
	// Passive makes Open write nothing on its own: no startup or periodic GC,
	// no preload quarantine (an unparsable record stays and blocks its slice's
	// persistence) and no metadata reset or write. Explicit GC and ResetSecrets
	// still act.
	Passive bool
	// InstanceDeps injects log-GC liveness probes; nil uses instance defaults.
	InstanceDeps *instance.Deps
}

// Paths contains every resolved base directory Store owns.
type Paths struct {
	// Config holds user/profile/named-instance config files.
	Config string
	// State holds slices and plugin logs.
	State string
	// Data holds encrypted namespaces and the optional identity file.
	Data string
	// Runtime holds sockets/entries and is owned by the instance package.
	Runtime string
}

// Scope is State's storage scope, shared without conversion.
type Scope = state.Scope

const (
	// ScopeUser shares records across this user's instances.
	ScopeUser = state.ScopeUser
	// ScopeInstance isolates records by named instance.
	ScopeInstance = state.ScopeInstance
	// ScopeWorkspace holds a named instance's workspace batch.
	ScopeWorkspace = state.ScopeWorkspace
)

// Key addresses one file inside a source's scoped storage root.
type Key struct {
	// Scope selects user, instance or workspace storage.
	Scope Scope
	// Instance is required for instance/workspace scope.
	Instance string
	// Path contains validated, individual relative path components.
	Path []string
}

// Entry describes one stored file.
type Entry struct {
	// Key is the file's source-relative address.
	Key Key
	// Size is the file size in bytes.
	Size int64
	// Modified is the last write time.
	Modified time.Time
}

var (
	// ErrNotFound means a document, record or secret is missing.
	ErrNotFound = errors.New("store: not found")
	// ErrClosed means the Store has begun shutdown.
	ErrClosed = errors.New("store: closed")
	// ErrUnavailable means a persisted value or identity cannot currently be read.
	ErrUnavailable = errors.New("store: unavailable")
	// ErrInvalidPath means a source key or document name is unsafe.
	ErrInvalidPath = errors.New("store: invalid path")
)

// Source stores scoped byte records with atomic writes.
type Source interface {
	// ID returns the stable source identifier.
	ID() string
	// Get reads one record and its metadata.
	Get(context.Context, Key) ([]byte, Entry, error)
	// Put atomically replaces one record.
	Put(context.Context, Key, []byte) error
	// Delete removes a record; missing is success.
	Delete(context.Context, Key) error
	// List lists records under a scoped prefix.
	List(context.Context, Key) ([]Entry, error)
}

// SyncReport reports reconciliation activity; v1 is always empty.
type SyncReport struct {
	// Copied counts transferred records.
	Copied int
	// Deleted counts propagated tombstones.
	Deleted int
}

// Syncer is the future multi-source boundary; no cloud implementation ships.
type Syncer interface {
	// Reconcile uses later mtime, then lexically first source ID to break ties.
	Reconcile(context.Context, Source, Source, Key) (SyncReport, error)
}

// NoopSyncer implements the single-local-source v1 policy.
type NoopSyncer struct{}

// Reconcile deliberately does nothing, respecting caller cancellation.
func (NoopSyncer) Reconcile(ctx context.Context, _, _ Source, _ Key) (SyncReport, error) {
	return SyncReport{}, ctx.Err()
}

// Sealer encrypts and decrypts a complete secret namespace.
type Sealer interface {
	// Seal encrypts plaintext with fresh randomness.
	Seal([]byte) ([]byte, error)
	// Open decrypts ciphertext without modifying it.
	Open([]byte) ([]byte, error)
}

// SecretDoc stores byte values in a single encrypted user-scope namespace.
type SecretDoc interface {
	// Get returns an independent value and its presence flag.
	Get(string) ([]byte, bool, error)
	// Put replaces a value, encrypting the whole namespace.
	Put(string, []byte) error
	// Delete removes a value; missing is success.
	Delete(string) error
	// Keys lists names, never values.
	Keys() ([]string, error)
}

// GCReport lists files removed by policy and any failures.
type GCReport struct {
	// Removed names successfully removed files/directories.
	Removed []string
	// Problems records files left untouched after failure.
	Problems []Problem
}

// CheckReport reports corrupt, quarantined and unregistered records without writes.
type CheckReport struct {
	// Problems records corrupt or unreadable files.
	Problems []Problem
	// Quarantined lists retained corrupt copies.
	Quarantined []string
	// Orphans lists slice namespaces not registered in 90 days.
	Orphans []string
}

type persistedRetention struct {
	// Policy is the saved GC policy.
	Policy state.Policy `json:"policy"`
	// N bounds kept generations, when applicable.
	N int `json:"n,omitempty"`
	// TTL is the duration in nanoseconds, when applicable.
	TTL time.Duration `json:"ttl,omitempty"`
}

func retentionMetadata(r state.Retention) *persistedRetention {
	return &persistedRetention{Policy: r.Policy, N: r.N, TTL: r.TTL}
}
func (r *persistedRetention) policy() state.Retention {
	if r == nil {
		return state.Retention{}
	}
	return state.Retention{Policy: r.Policy, N: r.N, TTL: r.TTL}
}

type envelope struct {
	// Version is the on-disk envelope format, independent of wire protocol.
	Version int `json:"husk_store"`
	// Namespace identifies the State slice.
	Namespace string `json:"namespace"`
	// Scope identifies the persisted sharing boundary.
	Scope Scope `json:"scope"`
	// Instance identifies a named instance; absent for user scope.
	Instance string `json:"instance,omitempty"`
	// WrittenAt records the last write time.
	WrittenAt time.Time `json:"written_at"`
	// RegisteredAt records the last observed owner registration.
	RegisteredAt time.Time `json:"registered_at,omitempty"`
	// Retention is optional policy metadata for startup collection.
	Retention *persistedRetention `json:"retention,omitempty"`
	// Value is the plain JSON slice object; slices never contain secrets.
	Value json.RawMessage `json:"value"`
}
