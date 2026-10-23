// Package launch holds the host adapters for the Pods launch seams: the real
// implementations over State, Store and the Services coordinator, plus the
// Preparer that composes launch steps 3 to 5 and the pods.Loop that carries
// them.
package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	portsservices "github.com/hyperlab-sh/husk/internal/host/ports/services"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/services"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// PodRegistry is the real husk.pods registry: launch steps 1 and 2 over the
// State slice husk.pods. Reservations are loop-local; status writes are
// merge patches on the slice value, so each update names only the fields it
// changes. Loop-only.
type PodRegistry struct {
	state    *state.State
	handle   *state.Handle
	reserved map[string]bool
}

var _ contracts.PodRegistry = (*PodRegistry)(nil)

// New assembles the composed host: pods.New over the Loop, with the two
// registry seams required to agree. Options.Registry answers Add's duplicate
// question while Loop.Registry receives the liveness writes; a host wired
// with only one of them reserves ids in one place and records liveness in
// another (the round-1 failure mode), and two different registries split
// the same way. New fails on either disagreement instead of launching a
// host that double-tracks ids (round-2 finding 4): both seams must be wired,
// or neither, and when both are they must be the same registry. Registry
// implementations are compared by pointer (or by value only when the
// dynamic type is comparable) because interface equality panics on
// uncomparable dynamic types.
func New(l Loop, opts pods.Options) (*pods.Host, error) {
	lSet, oSet := l.Registry != nil, opts.Registry != nil
	if lSet != oSet {
		return nil, errors.New("launch: Options.Registry and Loop.Registry must be wired together, both or neither")
	}
	if lSet && !sameRegistry(opts.Registry, l.Registry) {
		return nil, errors.New("launch: Options.Registry and Loop.Registry must be the same PodRegistry")
	}
	return pods.New(l, opts)
}

// sameRegistry reports whether two PodRegistry values are the same registry,
// without interface equality on uncomparable dynamic types.
func sameRegistry(a, b contracts.PodRegistry) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Kind() == reflect.Pointer && vb.Kind() == reflect.Pointer {
		return va.Pointer() == vb.Pointer()
	}
	if va.Comparable() && vb.Comparable() {
		return a == b
	}
	return false // uncomparable and not both pointers: identity cannot be proven
}

// NewPodRegistry registers husk.pods (owner husk, instance scope, not
// persisted) and returns its registry. Call it on the loop.
func NewPodRegistry(st *state.State) (*PodRegistry, error) {
	handle, err := st.Register(state.Spec{
		Namespace: state.PodsNamespace,
		Owner:     state.PodsOwner,
		Scope:     state.ScopeInstance,
		Initial:   json.RawMessage(`{"plugins":{}}`),
	})
	if err != nil {
		return nil, fmt.Errorf("launch: register husk.pods: %w", err)
	}
	return &PodRegistry{state: st, handle: handle, reserved: make(map[string]bool)}, nil
}

// Reserve claims pluginID for one launch; a duplicate returns the conflict
// code and changes nothing.
func (r *PodRegistry) Reserve(_ context.Context, pluginID string) error {
	if r.reserved[pluginID] {
		return contracts.NewError(protocol.CodeConflict, "duplicate plugin id", nil)
	}
	r.reserved[pluginID] = true
	return nil
}

// SetStatus merges upd into the plugin's husk.pods entry; an unreserved id is
// not_found.
func (r *PodRegistry) SetStatus(_ context.Context, pluginID string, upd contracts.PodStatusUpdate) error {
	if !r.reserved[pluginID] {
		return contracts.NewError(protocol.CodeNotFound, "unknown plugin id "+pluginID, nil)
	}
	entry := map[string]any{}
	if upd.Status != "" {
		entry["status"] = string(upd.Status)
	}
	if upd.Detail != nil {
		if *upd.Detail == "" {
			entry["last_error"] = nil // merge-patch null clears the field
		} else {
			entry["last_error"] = *upd.Detail
		}
	}
	if upd.PID != nil {
		entry["pid"] = *upd.PID
	}
	if upd.StartedAt != nil {
		entry["started_at"] = upd.StartedAt.Format(time.RFC3339Nano)
	}
	if upd.LogPath != nil {
		entry["log"] = *upd.LogPath
	}
	if upd.Dir != nil {
		entry["dir"] = *upd.Dir
	}
	return r.patchEntry(pluginID, entry)
}

// Release forgets pluginID and removes its husk.pods entry (Pod teardown).
func (r *PodRegistry) Release(_ context.Context, pluginID string) {
	if !r.reserved[pluginID] {
		return
	}
	delete(r.reserved, pluginID)
	_ = r.patchEntry(pluginID, nil) // teardown is best effort; Store declarations were already removed
}

// patchEntry applies a merge patch for one plugin's entry: an object merges
// field by field, nil removes the entry.
func (r *PodRegistry) patchEntry(pluginID string, entry any) error {
	patch := map[string]any{"plugins": map[string]any{pluginID: entry}}
	raw, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	if _, err := r.handle.Patch(raw, 0); err != nil {
		return fmt.Errorf("launch: patch husk.pods for %s: %w", pluginID, err)
	}
	return nil
}

// CatalogDeclarer is launch step 3's Store half: one atomic Declare per
// document name and plugin id, replacing the plugin's earlier declarations.
// It may run off the loop.
type CatalogDeclarer struct {
	Store *store.Store
}

var _ contracts.CatalogDeclarer = CatalogDeclarer{}

// Declare stores the plugin's catalog and keymap documents.
func (c CatalogDeclarer) Declare(pluginID string, catalog, keymap json.RawMessage) error {
	catalogDoc, err := decodeDoc(catalog)
	if err != nil {
		return fmt.Errorf("launch: plugin %s catalog: %w", pluginID, err)
	}
	keymapDoc, err := decodeDoc(keymap)
	if err != nil {
		return fmt.Errorf("launch: plugin %s keymap: %w", pluginID, err)
	}
	c.Store.Declare(store.DeclaredPlugins, pluginID, catalogDoc)
	c.Store.Declare(store.DeclaredKeymap, pluginID, keymapDoc)
	return nil
}

// Undeclare removes both declared documents (Pod teardown only).
func (c CatalogDeclarer) Undeclare(pluginID string) {
	c.Store.Undeclare(pluginID)
}

func decodeDoc(raw json.RawMessage) (store.Doc, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty document")
	}
	var doc store.Doc
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// Reloader is launch step 3's Services half: one reload with reason "plugin".
type Reloader struct {
	Coordinator *services.Coordinator
}

var _ contracts.Reloader = Reloader{}

// Reload blocks until the reload has completed (failed and timed-out reloads
// included) and translates reports into host problems.
func (r Reloader) Reload(ctx context.Context) (bool, []protocol.Problem, []contracts.ReloadReport) {
	reports, err := r.Coordinator.Reload(ctx, services.ReasonPlugin)
	applied, problems := reloadProblems(reports, err)
	return applied, problems, portsservices.Reports(reports)
}

// reloadProblems translates the Coordinator's reports into host problems. A
// failing service is named by its own coded problems (the Coordinator makes
// sure every failed report carries one), so the aggregate "incomplete" error
// adds nothing when reports exist. Only a context that ended before the
// flight produced any report is reported on its own: cancelled for
// context.Canceled, timed_out for context.DeadlineExceeded.
func reloadProblems(reports []services.Report, err error) (bool, []protocol.Problem) {
	applied := err == nil
	var problems []protocol.Problem
	for _, report := range reports {
		if !report.Applied {
			applied = false
		}
		for _, p := range report.Problems {
			problems = append(problems, withService(p, report.Service))
		}
	}
	if err != nil && len(reports) == 0 {
		code := ""
		switch {
		case errors.Is(err, context.Canceled):
			code = "cancelled"
		case errors.Is(err, context.DeadlineExceeded):
			code = "timed_out"
		}
		if code != "" {
			problems = append(problems, protocol.Problem{Severity: "error", Code: code, Layer: "services", Message: err.Error()})
		}
	}
	return applied, problems
}

// withService prefixes a problem's message with the service that reported it.
func withService(p protocol.Problem, service string) protocol.Problem {
	p.Message = service + ": " + p.Message
	return p
}

// SliceRegistrar is launch step 4 over State.
type SliceRegistrar struct {
	State *state.State
}

var _ contracts.SliceRegistrar = SliceRegistrar{}

// RegisterSlice registers spec and returns the owner handle.
func (s SliceRegistrar) RegisterSlice(spec state.Spec) (contracts.SliceHandle, error) {
	return s.State.Register(spec)
}

// Preparer composes launch steps 3 to 5 for one batch against the seams: for
// each spec the catalog and keymap documents are declared atomically,
// then the plugin slices are registered — on the loop, before the reload is
// requested — and one shared Services reload is awaited. The reload's
// completion — applied or not — is the launch gate, so a failed or timed-out
// reload never blocks the batch; per-plugin declaration or registration
// failures mark only that plugin failed. It runs in a tea.Cmd worker and may
// block.
type Preparer struct {
	Catalog contracts.CatalogDeclarer
	Slices  contracts.SliceRegistrar
	Reload  contracts.Reloader
	// Post delivers loop messages from the worker; NewLoop wires it. It is
	// required when slices must register: the registrations are State writes,
	// so they run on the loop through RegisterSlicesMsg and the worker waits
	// for the acknowledgment before requesting the reload (store.md startup
	// order: slices exist before the first reload reads them).
	Post func(tea.Msg)
}

// Prepare implements pods.Loop.Prepare. It runs in a tea.Cmd worker: the
// Store declarations and the Services reload are off-loop work. State writes
// are loop-only, so slice registrations are posted to the loop as
// RegisterSlicesMsg and the worker blocks on the loop's acknowledgment —
// never executing a State write itself. gate is the launch gate that owns
// the batch, handed to the loop so it can refuse a registration whose
// launch was torn down while the worker was in flight.
func (p Preparer) Prepare(ctx context.Context, gate uint64, specs []manifest.Spec) pods.Preparation {
	failed := make(map[string]error)
	register := make(map[string]func() (contracts.SliceHandle, error))
	for _, spec := range specs {
		if !state.ValidNamespace(spec.ID + ".state") {
			failed[spec.ID] = fmt.Errorf("launch: plugin id %q cannot own a state slice (namespace %q invalid)", spec.ID, spec.ID+".state")
			continue
		}
		catalog, err := spec.CatalogJSON()
		if err != nil {
			failed[spec.ID] = fmt.Errorf("catalog: %w", err)
			continue
		}
		keymapJSON, err := spec.KeymapJSON()
		if err != nil {
			failed[spec.ID] = fmt.Errorf("keymap: %w", err)
			continue
		}
		if p.Catalog != nil {
			if err := p.Catalog.Declare(spec.ID, catalog, keymapJSON); err != nil {
				failed[spec.ID] = err
				continue
			}
		}
		if p.Slices != nil {
			slice := sliceSpec(spec.ID, spec.Slice)
			register[spec.ID] = func() (contracts.SliceHandle, error) {
				return p.Slices.RegisterSlice(slice)
			}
		}
	}
	// Step 4 on the loop, acknowledged before the reload is requested: the
	// first reload must observe every registered slice.
	if len(register) > 0 {
		if p.Post == nil {
			for id := range register {
				failed[id] = errors.New("launch: Preparer.Post is required to register plugin slices before the reload")
			}
		} else {
			done := make(chan pods.Registered, 1)
			p.Post(pods.RegisterSlicesMsg{Gate: gate, Register: register, Done: done})
			select {
			case res := <-done:
				maps.Copy(failed, res.Failed)
			case <-ctx.Done():
				for id := range register {
					failed[id] = fmt.Errorf("shut down before slice registration: %w", ctx.Err())
				}
			}
		}
	}
	var reloadErr error
	var reports []contracts.ReloadReport
	if p.Reload != nil {
		var applied bool
		var problems []protocol.Problem
		applied, problems, reports = p.Reload.Reload(ctx)
		if !applied || len(problems) > 0 {
			reloadErr = reloadError{applied: applied, problems: problems}
		}
	}
	return pods.Preparation{Failed: failed, ReloadError: reloadErr, Reports: reports}
}

// reloadError is the non-blocking launch-gate error carried by Preparation.
type reloadError struct {
	applied  bool
	problems []protocol.Problem
}

func (e reloadError) Error() string {
	if len(e.problems) == 0 {
		return "plugin reload completed without being applied"
	}
	out := "plugin reload problems:"
	for _, p := range e.problems {
		out += fmt.Sprintf(" [%s/%s%s] %s", p.Severity, p.Layer, p.Key, p.Message)
	}
	return out
}

// sliceSpec converts the manifest's slice declaration into the State
// registration the Pod owns: namespace <plugin_id>.state, owner the plugin.
// An absent declaration selects an instance-scoped, non-persistent slice;
// zero MaxBytes lets State apply the plugin class default.
func sliceSpec(pluginID string, decl *manifest.SliceDecl) state.Spec {
	if decl == nil {
		decl = &manifest.SliceDecl{Scope: string(state.ScopeInstance)}
	}
	spec := state.Spec{
		Namespace:  pluginID + ".state",
		Owner:      pluginID,
		Scope:      state.Scope(decl.Scope),
		Persistent: decl.Persistent,
		MaxBytes:   decl.MaxBytes,
		Initial:    json.RawMessage(`{}`),
	}
	if decl.Retention != nil {
		spec.Retention = state.Retention{
			Policy: state.Policy(decl.Retention.Policy),
			N:      decl.Retention.N,
			TTL:    time.Duration(decl.Retention.TTL),
		}
	}
	return spec
}

// Loop is the pods.Loop for the composed host: Prepare composes the launch
// seams, Liveness writes husk.pods through the PodRegistry, and Post, Opening
// and Log are injected by the composition root.
type Loop struct {
	// Context backs the seam calls Liveness makes; nil means Background.
	Context context.Context
	// Registry receives Liveness translations; nil drops them.
	Registry contracts.PodRegistry
	// Preparer serves Prepare.
	Preparer Preparer
	// PostFn delivers worker messages; nil drops them.
	PostFn func(tea.Msg)
	// OpeningFn builds a plugin's opening queue; nil means none.
	OpeningFn func(pluginID string) []protocol.Message
	// LogFn receives host log notices; nil drops them.
	LogFn func(pods.LogMsg)
	// DoFn runs fn on the host loop and returns once it has run; nil means
	// no loop runs, and every Do call answers that error. Off-loop callers
	// (Server handlers) read loop-owned state through it.
	DoFn func(context.Context, func()) error
}

var _ pods.Loop = Loop{}

// Post implements pods.Loop.Post.
func (l Loop) Post(m tea.Msg) {
	if l.PostFn != nil {
		l.PostFn(m)
	}
}

// Opening implements pods.Loop.Opening.
func (l Loop) Opening(pluginID string) []protocol.Message {
	if l.OpeningFn == nil {
		return nil
	}
	return l.OpeningFn(pluginID)
}

// Prepare implements pods.Loop.Prepare. The Preparer's Post is wired from
// the Loop's own delivery function when the composition did not set one.
func (l Loop) Prepare(ctx context.Context, gate uint64, specs []manifest.Spec) pods.Preparation {
	p := l.Preparer
	if p.Post == nil {
		p.Post = l.PostFn
	}
	return p.Prepare(ctx, gate, specs)
}

// Do implements pods.Loop.Do: fn runs on the host loop when a loop is
// wired; without one, nothing runs and the error says so, so ports answer
// unavailable rather than reading loop-owned state off-loop.
func (l Loop) Do(ctx context.Context, fn func()) error {
	if l.DoFn == nil {
		return errors.New("launch: no loop Do seam wired")
	}
	return l.DoFn(ctx, fn)
}

// Liveness implements pods.Loop.Liveness by writing husk.pods.
func (l Loop) Liveness(m pods.LivenessMsg) {
	if l.Registry == nil {
		return
	}
	upd := contracts.PodStatusUpdate{Status: contracts.PodStatus(m.Info.Status), Dir: m.Dir}
	if m.Info.LastError != "" {
		upd.Detail = &m.Info.LastError
	}
	if m.Info.PID != 0 {
		upd.PID = &m.Info.PID
	}
	if !m.Info.StartedAt.IsZero() {
		upd.StartedAt = &m.Info.StartedAt
	}
	if m.Info.LogPath != "" {
		upd.LogPath = &m.Info.LogPath
	}
	ctx := l.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := l.Registry.SetStatus(ctx, m.Info.PluginID, upd); err != nil {
		if l.LogFn != nil {
			l.LogFn(pods.LogMsg{Level: "warn", Message: fmt.Sprintf("husk.pods write failed for %s: %v", m.Info.PluginID, err)})
		}
	}
}

// Log implements pods.Loop.Log.
func (l Loop) Log(m pods.LogMsg) {
	if l.LogFn != nil {
		l.LogFn(m)
	}
}
