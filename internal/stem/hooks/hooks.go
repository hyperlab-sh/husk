// Package hooks is the Stem Hook runner. A hook is an extension point inside
// its owner's operation: the owner fires it, the hook's declared kind says what
// the subscribers may answer, and the runner returns one Outcome. The runner
// asks every live subscriber through internal/stem/calls and reads their
// replies by kind: collect and gate fan out at once, transform runs a chain of
// one leg at a time.
//
// The runner is confined to the host loop: Call and Complete are called from
// Update and never block, and onDone runs on the loop goroutine exactly once
// per accepted Call, when the last leg has resolved (reply, timeout, plugin
// exit or shutdown). It reads the hook catalog and plugin liveness through
// small injected views and never writes either. It imports Calls and the
// protocol package and nothing else from Husk.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Requester is the name this module registers with Calls.
const Requester = "hooks"

// Method is the wire method every hook subscriber receives.
const Method = protocol.MethodHookCall

// HostOwner is the owner of every host-declared hook (ids under "husk.").
const HostOwner = "husk"

// MaxInFlight is the default per-hook in-flight cap.
const MaxInFlight = 16

// MaxPayload is the size limit, in bytes of compact JSON, of one subscriber
// reply.
const MaxPayload = 1 << 20

// maxIDLen is the longest well-formed hook id, in bytes.
const maxIDLen = 256

// Error codes this module produces itself. A subscriber's own error code is
// passed through verbatim in HookResult.Err.
const (
	// CodeInvalidParams rejects a malformed hook id or data, and marks a
	// subscriber reply that does not fit the hook's kind.
	CodeInvalidParams = protocol.CodeInvalidParams
	// CodeDenied rejects a caller that does not own the hook.
	CodeDenied = protocol.CodeDenied
	// CodeNotFound rejects a host firing of a hook the catalog does not know.
	CodeNotFound = protocol.CodeNotFound
	// CodeBusy reports that the in-flight cap or a pending-call cap refused
	// the whole call.
	CodeBusy = protocol.CodeBusy
	// CodeUnavailable reports that Calls cannot currently deliver.
	CodeUnavailable = protocol.CodeUnavailable
	// CodeInternal reports a runner bug or an unclassified Calls failure.
	CodeInternal = protocol.CodeInternal
)

// ReasonInFlight is the data.reason of a busy refusal from the in-flight cap.
const ReasonInFlight = "hook_in_flight"

// Caller says who started the hook call; it is the one shared identity type.
// The subscribers never see it.
type Caller = protocol.Caller

// CallerKind is the kind of a Caller.
type CallerKind = protocol.CallerKind

// The caller kinds.
const (
	CallerInput  = protocol.CallerInput
	CallerPlugin = protocol.CallerPlugin
	CallerCLI    = protocol.CallerCLI
	CallerHost   = protocol.CallerHost
)

// label names the caller for log lines.
func callerLabel(c Caller) string {
	if c.Kind == CallerPlugin {
		return "plugin " + c.PluginID
	}
	return string(c.Kind) + " caller"
}

// Kind is what a hook's subscribers may answer; the owner declares it.
type Kind string

const (
	// Transform replaces a value, one subscriber after another.
	Transform Kind = "transform"
	// Collect gathers each subscriber's items.
	Collect Kind = "collect"
	// Gate lets each subscriber allow or veto.
	Gate Kind = "gate"
)

// Valid reports whether k is one of the three kinds.
func (k Kind) Valid() bool { return k == Transform || k == Collect || k == Gate }

// HookDef is one declared hook, from a plugin's document or the host list.
type HookDef struct {
	ID string
	// Owner is the plugin id, or "husk" for a host hook.
	Owner string
	Kind  Kind
	// Timeout is the per-leg timeout; always set (a zero Timeout never
	// reaches the runner).
	Timeout     time.Duration
	Title       string
	Description string
}

// Subscription is one registration. The catalog returns them sorted.
type Subscription struct {
	Plugin   string
	Priority int
}

// Catalog is the read-only view of declared hooks and their registrations,
// implemented outside Stem over the declared plugins documents and the host's
// own hook list.
type Catalog interface {
	// Lookup returns the declared hook.
	Lookup(hook string) (HookDef, bool)
	// Subscribers returns the registrations for hook sorted by priority
	// ascending, then plugin id ascending. Empty when none.
	Subscribers(hook string) []Subscription
	// List returns every declared hook.
	List() []HookDef
}

// Liveness is the read-only view of plugin liveness (the husk.pods slice).
type Liveness interface {
	// Live reports that the plugin's status is live.
	Live(pluginID string) bool
}

// Sender is the slice of *calls.Table the runner uses: legs leave through
// Send after the caps preflight in CanSend.
type Sender interface {
	// CanSend prechecks the pending-call caps; see calls.Table.CanSend.
	CanSend(plugins ...calls.PluginID) error
	// Send issues one request; see calls.Table.Send.
	Send(plugin calls.PluginID, method string, params json.RawMessage, opts calls.Options) (calls.Handle, error)
}

// HookError is a coded failure: a refusal of the whole Call, or one leg's
// failure (HookResult.Err).
type HookError struct {
	// Code is a protocol registry code.
	Code protocol.Code
	// Message explains the failure.
	Message string
	// Data is the wire error's structured data member; nil when absent.
	// Calls only delivers an error whose data is a JSON object (anything else
	// becomes its own internal/invalid_reply error), so this carries the
	// member whole.
	Data map[string]any
}

// Error implements error.
func (e *HookError) Error() string { return string(e.Code) + ": " + e.Message }

// HookResult is one subscriber's entry in the outcome.
type HookResult struct {
	// Plugin is the subscriber's plugin id, set from the leg's target.
	Plugin string
	// Data is a transform stage's replacement; nil on pass-through.
	Data map[string]any
	// Items is a collect subscriber's items; nil when none.
	Items []any
	// Veto is true when a gate subscriber vetoed.
	Veto bool
	// Reason is the veto's reason, possibly "".
	Reason string
	// Err is the subscriber's failure (its own error, timeout, cancelled,
	// busy, unavailable or invalid_params); nil on success.
	Err *HookError
}

// Veto is one gate objection.
type Veto struct {
	Plugin string
	Reason string
}

// Outcome is what the owner receives, once.
type Outcome struct {
	Kind Kind
	// Data is a transform's value after the last stage; nil otherwise.
	Data map[string]any
	// Allowed is false only for a gate with at least one veto.
	Allowed bool
	// Vetoes lists every gate veto in priority order; empty otherwise.
	Vetoes []Veto
	// Results has one entry per subscriber asked, in priority order.
	Results []HookResult
}

// Options configures a Runner. Catalog, Liveness and Calls are required. Zero
// MaxInFlight selects the default of 16; a negative one is rejected by New. A
// nil Logger is a no-op and must not block or reenter the runner. There is no
// default timeout: every HookDef carries one.
type Options struct {
	Catalog     Catalog
	Liveness    Liveness
	Calls       Sender
	MaxInFlight int
	Logger      func(format string, args ...any)
}

// Runner runs hook calls. It is confined to the host loop; Call and Complete
// are called from Update and never block.
type Runner struct {
	opts     Options
	limit    int
	legs     map[calls.Handle]leg
	inFlight map[string]int
	ignored  map[ignoredKey]struct{}
}

// ignoredKey names a subscriber/hook pair whose foreign reply field has been
// logged already.
type ignoredKey struct{ plugin, hook string }

// leg is one pending request: the call it belongs to and its result slot.
type leg struct {
	call *call
	slot int
}

// call is one accepted hook call in flight.
type call struct {
	def     HookDef
	waiting int
	entries []HookResult
	done    func(Outcome)
	// chain is the transform state; nil for collect and gate.
	chain *chain
}

// chain is a transform call's state: the live subscribers in order, the next
// stage to send and the data the next stage receives.
type chain struct {
	asked []string
	next  int
	data  map[string]any
}

// New returns a Runner over opts. It panics when a required view is missing
// and errors on a negative MaxInFlight.
func New(opts Options) (*Runner, error) {
	if opts.Catalog == nil {
		panic("hooks: Options.Catalog is required")
	}
	if opts.Liveness == nil {
		panic("hooks: Options.Liveness is required")
	}
	if opts.Calls == nil {
		panic("hooks: Options.Calls is required")
	}
	if opts.MaxInFlight < 0 {
		return nil, fmt.Errorf("hooks: Options.MaxInFlight must not be negative (zero selects %d)", MaxInFlight)
	}
	limit := opts.MaxInFlight
	if limit == 0 {
		limit = MaxInFlight
	}
	return &Runner{
		opts: opts, limit: limit,
		legs:     map[calls.Handle]leg{},
		inFlight: map[string]int{},
		ignored:  map[ignoredKey]struct{}{},
	}, nil
}

func (r *Runner) logf(format string, args ...any) {
	if r.opts.Logger != nil {
		r.opts.Logger(format, args...)
	}
}

// Call fires hook for caller and returns without waiting. Resolution, in
// order: id syntax (invalid_params); catalog lookup and ownership (denied, or
// not_found for the host); data; the in-flight cap (busy, reason
// hook_in_flight); live subscribers (none: onDone runs at once with the kind's
// empty outcome); the caps preflight (busy); then the run for the hook's
// kind. A nil data is an empty object. onDone receives the Outcome exactly
// once on the loop goroutine, when the last leg resolved; a nil onDone
// discards it. If Call returns an error, onDone is never invoked and the call
// does not count against the in-flight cap.
func (r *Runner) Call(caller Caller, hook string, data map[string]any, onDone func(Outcome)) error {
	if !wellFormed(hook) {
		return &HookError{Code: CodeInvalidParams, Message: fmt.Sprintf("malformed hook id %q", hook)}
	}
	def, err := r.authorize(caller, hook)
	if err != nil {
		return err
	}
	params, err := encodeParams(def, data)
	if err != nil {
		return err
	}
	if r.inFlight[hook] >= r.limit {
		r.logf("hooks: %s: %d calls already in flight, refusing", hook, r.inFlight[hook])
		return &HookError{
			Code:    CodeBusy,
			Message: fmt.Sprintf("too many calls of hook %q in flight", hook),
			Data:    map[string]any{"reason": ReasonInFlight},
		}
	}
	var asked []string
	for _, sub := range r.opts.Catalog.Subscribers(hook) {
		if r.opts.Liveness.Live(sub.Plugin) {
			asked = append(asked, sub.Plugin)
		}
	}
	if len(asked) == 0 {
		r.inFlight[hook]++
		defer r.release(hook)
		if onDone != nil {
			onDone(emptyOutcome(def.Kind, data))
		}
		return nil
	}
	preflight := asked
	if def.Kind == Transform {
		preflight = asked[:1] // later stages check as they start
	}
	if err := r.opts.Calls.CanSend(pluginIDs(preflight)...); err != nil {
		return capError(err)
	}
	c := &call{def: def, done: onDone}
	r.inFlight[hook]++
	if def.Kind == Transform {
		if data == nil {
			data = map[string]any{}
		}
		c.entries = make([]HookResult, 0, len(asked))
		c.chain = &chain{asked: asked, data: data}
		r.advance(c, params)
		return nil
	}
	c.waiting = len(asked)
	c.entries = make([]HookResult, len(asked))
	r.fanOut(c, asked, params)
	return nil
}

// authorize looks hook up and applies the firing table: only the owner fires.
func (r *Runner) authorize(caller Caller, hook string) (HookDef, error) {
	def, ok := r.opts.Catalog.Lookup(hook)
	denied := func() (HookDef, error) {
		r.logf("hooks: %s denied firing %q", callerLabel(caller), hook)
		return HookDef{}, &HookError{Code: CodeDenied, Message: fmt.Sprintf("only the owner of %q may fire it", hook)}
	}
	switch caller.Kind {
	case CallerPlugin:
		// A plugin never fires a husk.* hook, whatever its id says.
		if !ok || caller.PluginID == "" || caller.PluginID == HostOwner || def.Owner != caller.PluginID {
			return denied()
		}
	case CallerHost:
		if !ok {
			r.logf("hooks: host fired unknown hook %q", hook)
			return HookDef{}, &HookError{Code: CodeNotFound, Message: fmt.Sprintf("unknown hook %q", hook)}
		}
		if def.Owner != HostOwner {
			return denied()
		}
	default:
		return denied()
	}
	def.ID = hook
	return def, nil
}

// advance sends the next transform stage, or finishes the call when none is
// left. Stages that fail without a leg (a cap hit, an undeliverable send) are
// recorded and skipped in the same loop; a stage that gets a leg parks the
// chain until Complete resolves it. params is the first stage's encoded
// params, nil afterwards. A closed Calls table ends the chain.
func (r *Runner) advance(c *call, params json.RawMessage) {
	ch := c.chain
	for ch.next < len(ch.asked) {
		plugin := ch.asked[ch.next]
		ch.next++
		entry := HookResult{Plugin: plugin}
		if err := r.opts.Calls.CanSend(calls.PluginID(plugin)); err != nil {
			r.logf("hooks: %s: stage %s skipped: %v", c.def.ID, plugin, err)
			if errors.Is(err, calls.ErrClosed) {
				entry.Err = shutdownError()
				c.entries = append(c.entries, entry)
				break
			}
			entry.Err = capError(err)
			c.entries = append(c.entries, entry)
			continue
		}
		if params == nil {
			var err error
			if params, err = encodeParams(c.def, ch.data); err != nil {
				var he *HookError
				if !errors.As(err, &he) {
					he = &HookError{Code: CodeInternal, Message: err.Error()}
				}
				entry.Err = he
				c.entries = append(c.entries, entry)
				continue
			}
		}
		h, err := r.opts.Calls.Send(calls.PluginID(plugin), Method, params, calls.Options{Timeout: c.def.Timeout, Requester: Requester})
		params = nil
		if err != nil {
			r.logf("hooks: %s: send to %s failed: %v", c.def.ID, plugin, err)
			entry.Err = legSendError(err)
			c.entries = append(c.entries, entry)
			if isShutdown(entry.Err) {
				break
			}
			continue
		}
		r.legs[h] = leg{call: c, slot: len(c.entries)}
		c.entries = append(c.entries, entry)
		return
	}
	r.finish(c)
}

// shutdownError is the entry of a leg that Calls closing cut short.
func shutdownError() *HookError {
	return &HookError{Code: protocol.CodeCancelled, Message: "husk is shutting down", Data: map[string]any{"reason": "shutdown"}}
}

// isShutdown reports whether e is the cancelled/shutdown error: the host is
// closing, so a transform chain ends instead of going on.
func isShutdown(e *HookError) bool {
	return e != nil && e.Code == protocol.CodeCancelled && e.Data["reason"] == "shutdown"
}

// legSendError maps a failed Send to the subscriber's entry error. A closed
// table is a shutdown; an unavailable writer is a disconnect.
func legSendError(err error) *HookError {
	switch {
	case errors.Is(err, calls.ErrClosed):
		return shutdownError()
	case errors.Is(err, calls.ErrUnavailable):
		e := sendError(err)
		e.Data = map[string]any{"reason": "disconnected"}
		e.Message = "subscriber disconnected"
		return e
	}
	return sendError(err)
}

// fanOut sends one leg per subscriber, all in this Update and in priority
// order. Delivery is per-subscriber: a send the table answers unavailable
// becomes that subscriber's error entry (reason disconnected), any other send
// failure answers with the table's code, and the rest still get their leg.
func (r *Runner) fanOut(c *call, asked []string, params json.RawMessage) {
	for i, plugin := range asked {
		c.entries[i].Plugin = plugin
		h, err := r.opts.Calls.Send(calls.PluginID(plugin), Method, params, calls.Options{Timeout: c.def.Timeout, Requester: Requester})
		if err != nil {
			r.logf("hooks: %s: send to %s failed: %v", c.def.ID, plugin, err)
			e := sendError(err)
			if errors.Is(err, calls.ErrUnavailable) {
				e.Data = map[string]any{"reason": "disconnected"}
				e.Message = "subscriber disconnected"
			}
			c.entries[i].Err = e
			c.waiting--
			continue
		}
		r.legs[h] = leg{call: c, slot: i}
	}
	if c.waiting == 0 {
		r.finish(c)
	}
}

// Complete resolves the legs named by results — every Result whose Requester
// is "hooks" that Resolve, Sweep, DropPlugin or Close produced — and returns
// how many legs it resolved. Results from other requesters and unknown handles
// are ignored. The last leg of a call invokes its onDone on this goroutine.
func (r *Runner) Complete(results ...calls.Result) int {
	resolved := 0
	for _, res := range results {
		if res.Requester != Requester {
			continue
		}
		l, ok := r.legs[res.Handle]
		if !ok {
			continue
		}
		delete(r.legs, res.Handle)
		resolved++
		c := l.call
		entry := r.interpret(c.def, c.entries[l.slot].Plugin, res)
		c.entries[l.slot] = entry
		if c.chain != nil {
			if entry.Data != nil {
				c.chain.data = entry.Data
			}
			// A cancelled/shutdown entry ends the chain only when Calls itself
			// is closed; a subscriber can send the same error as its reply.
			if isShutdown(entry.Err) && errors.Is(r.opts.Calls.CanSend(), calls.ErrClosed) {
				r.finish(c)
			} else {
				r.advance(c, nil)
			}
			continue
		}
		c.waiting--
		if c.waiting == 0 {
			r.finish(c)
		}
	}
	return resolved
}

// finish delivers the call's outcome, then frees its in-flight slot.
func (r *Runner) finish(c *call) {
	defer r.release(c.def.ID)
	if c.done != nil {
		out := buildOutcome(c.def.Kind, c.entries)
		if c.chain != nil {
			out.Data = c.chain.data
		}
		c.done(out)
	}
}

// release frees one in-flight slot of hook, dropping the entry at zero.
func (r *Runner) release(hook string) {
	if r.inFlight[hook]--; r.inFlight[hook] <= 0 {
		delete(r.inFlight, hook)
	}
}

// Listed is one hook.list entry: a declared hook with its subscribers.
type Listed struct {
	HookDef
	// Subscribers is sorted as Catalog.Subscribers returns it.
	Subscribers []ListedSubscriber
}

// ListedSubscriber is one registration with its plugin's liveness.
type ListedSubscriber struct {
	Plugin   string
	Priority int
	// Live is true when the plugin's status is live now.
	Live bool
}

// List backs hook.list: every declared hook in the catalog's order, with its
// subscribers and their liveness. Loop-confined, like Call.
func (r *Runner) List() []Listed {
	defs := r.opts.Catalog.List()
	out := make([]Listed, len(defs))
	for i, def := range defs {
		subs := r.opts.Catalog.Subscribers(def.ID)
		out[i] = Listed{HookDef: def, Subscribers: make([]ListedSubscriber, len(subs))}
		for j, sub := range subs {
			out[i].Subscribers[j] = ListedSubscriber{Plugin: sub.Plugin, Priority: sub.Priority, Live: r.opts.Liveness.Live(sub.Plugin)}
		}
	}
	return out
}

// Pending reports how many hook legs are awaiting a reply.
func (r *Runner) Pending() int { return len(r.legs) }

// legParams is the params object every subscriber receives: the hook id, its
// kind and the owner's data. Nothing about the firer or other subscribers.
type legParams struct {
	Hook string          `json:"hook"`
	Kind Kind            `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// encodeParams marshals the leg params once; a nil data is an empty object.
func encodeParams(def HookDef, data map[string]any) (json.RawMessage, error) {
	if data == nil {
		data = map[string]any{}
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, &HookError{Code: CodeInvalidParams, Message: "hook data cannot be encoded: " + err.Error()}
	}
	p, err := json.Marshal(legParams{Hook: def.ID, Kind: def.Kind, Data: b})
	if err != nil {
		return nil, &HookError{Code: CodeInternal, Message: "cannot encode hook call: " + err.Error()}
	}
	return p, nil
}

// emptyOutcome is the outcome of a call nobody was asked.
func emptyOutcome(kind Kind, data map[string]any) Outcome {
	out := Outcome{Kind: kind, Allowed: true, Results: []HookResult{}}
	switch kind {
	case Transform:
		if data == nil {
			data = map[string]any{}
		}
		out.Data = data
	case Gate:
		out.Vetoes = []Veto{}
	}
	return out
}

// buildOutcome assembles the outcome from entries already in priority order.
func buildOutcome(kind Kind, entries []HookResult) Outcome {
	out := Outcome{Kind: kind, Allowed: true, Results: entries}
	if kind == Gate {
		out.Vetoes = []Veto{}
		for _, e := range entries {
			if e.Veto {
				out.Vetoes = append(out.Vetoes, Veto{Plugin: e.Plugin, Reason: e.Reason})
			}
		}
		out.Allowed = len(out.Vetoes) == 0
	}
	return out
}

// interpret turns one leg's Result into its outcome entry, reading the reply
// through the shape of the hook's kind.
func (r *Runner) interpret(def HookDef, plugin string, res calls.Result) HookResult {
	out := HookResult{Plugin: plugin}
	if res.Err != nil {
		e := &HookError{Code: res.Err.Code, Message: res.Err.Message}
		if d := decodeObject(res.Err.Data); d != nil {
			e.Data = d
		}
		out.Err = e
		return out
	}
	invalid := func(format string, args ...any) HookResult {
		out.Err = &HookError{Code: CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
		return out
	}
	if compactLen(res.Value) > MaxPayload {
		return invalid("reply exceeds %d bytes", MaxPayload)
	}
	fields, ok := replyFields(res.Value)
	if !ok {
		return invalid("reply must be a JSON object")
	}
	if foreign := foreignFields(def.Kind, fields); len(foreign) > 0 {
		r.ignoreForeign(plugin, def.ID, foreign)
	}
	switch def.Kind {
	case Transform:
		if raw, has := fields["data"]; has && !isNull(raw) {
			data := decodeObject(raw)
			if data == nil {
				return invalid("data must be an object")
			}
			out.Data = data
		}
	case Collect:
		if raw, has := fields["items"]; has {
			items, ok := decodeArray(raw)
			if !ok {
				return invalid("items must be an array")
			}
			if len(items) > 0 {
				out.Items = items
			}
		}
	case Gate:
		var veto bool
		if raw, has := fields["veto"]; has {
			if veto, ok = decodeBool(raw); !ok {
				return invalid("veto must be a boolean")
			}
		}
		if veto {
			// A reason belongs to a veto; without one it is not read.
			if raw, has := fields["reason"]; has {
				if out.Reason, ok = decodeString(raw); !ok {
					out.Reason = ""
					return invalid("reason must be a string")
				}
			}
			out.Veto = true
		}
	}
	return out
}

// foreignFields lists the reply fields that belong to another kind.
func foreignFields(kind Kind, fields map[string]json.RawMessage) []string {
	switch kind {
	case Transform:
		return present(fields, "items", "veto", "reason")
	case Collect:
		return present(fields, "data", "veto", "reason")
	case Gate:
		return present(fields, "data", "items")
	}
	return nil
}

// isNull reports whether raw is the JSON null.
func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// ignoreForeign logs, once per (subscriber, hook) per process, that a reply
// carried fields of another kind.
func (r *Runner) ignoreForeign(plugin, hook string, fields []string) {
	k := ignoredKey{plugin, hook}
	if _, seen := r.ignored[k]; seen {
		return
	}
	r.ignored[k] = struct{}{}
	r.logf("hooks: %s replied to %s with %s, which its kind does not read; ignored", plugin, hook, strings.Join(fields, ", "))
}

// present returns which of names appear in fields, in the order given.
func present(fields map[string]json.RawMessage, names ...string) []string {
	var out []string
	for _, n := range names {
		if _, ok := fields[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// replyFields reads a reply's result as an object; null and absent are {}.
func replyFields(value json.RawMessage) (map[string]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]json.RawMessage{}, true
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// compactLen is the byte length of value's compact JSON encoding.
func compactLen(value json.RawMessage) int {
	if len(value) <= MaxPayload {
		return len(value) // compact is never longer than the raw bytes
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, value); err != nil {
		return len(value)
	}
	return buf.Len()
}

// decodeArray decodes a JSON array member, preserving number lexemes.
func decodeArray(raw json.RawMessage) ([]any, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var items []any
	if err := dec.Decode(&items); err != nil {
		return nil, false
	}
	return items, true
}

// decodeBool decodes a JSON true or false; null is not a boolean.
func decodeBool(raw json.RawMessage) (value, ok bool) {
	switch string(bytes.TrimSpace(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// decodeString decodes a JSON string; null is not a string.
func decodeString(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	var s string
	if len(trimmed) == 0 || trimmed[0] != '"' || json.Unmarshal(trimmed, &s) != nil {
		return "", false
	}
	return s, true
}

// decodeObject decodes a JSON object member, preserving number lexemes.
// Anything that is not an object yields nil.
func decodeObject(raw json.RawMessage) map[string]any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil
	}
	return m
}

// wellFormed applies the hook-id rule: at least two non-empty dot-separated
// segments, no whitespace, ';', '=', backslash or '*', at most 256 bytes.
func wellFormed(id string) bool {
	if id == "" || len(id) > maxIDLen {
		return false
	}
	segments := 1
	segmentLen := 0
	for _, ch := range id {
		switch {
		case ch == '.':
			if segmentLen == 0 {
				return false
			}
			segments++
			segmentLen = 0
			continue
		case unicode.IsSpace(ch), ch == ';', ch == '=', ch == '\\', ch == '*':
			return false
		}
		segmentLen++
	}
	return segments >= 2 && segmentLen > 0
}

func pluginIDs(plugins []string) []calls.PluginID {
	out := make([]calls.PluginID, len(plugins))
	for i, p := range plugins {
		out[i] = calls.PluginID(p)
	}
	return out
}

// capError maps a cap or closure refusal from a preflight CanSend.
func capError(err error) *HookError {
	switch {
	case errors.Is(err, calls.ErrPluginBusy):
		return &HookError{Code: CodeBusy, Message: "too many pending hook calls for one subscriber"}
	case errors.Is(err, calls.ErrBusy):
		return &HookError{Code: CodeBusy, Message: "too many pending hook calls"}
	case errors.Is(err, calls.ErrClosed):
		return &HookError{Code: CodeUnavailable, Message: "the call table is closed"}
	default:
		return &HookError{Code: CodeInternal, Message: "hook call preflight failed: " + err.Error()}
	}
}

// sendError maps a Send failure after the preflight passed. It becomes that
// subscriber's outcome entry, not a refusal of the call.
func sendError(err error) *HookError {
	switch {
	case errors.Is(err, calls.ErrUnavailable), errors.Is(err, calls.ErrClosed):
		return &HookError{Code: CodeUnavailable, Message: "cannot send hook call: " + err.Error()}
	default:
		return &HookError{Code: CodeInternal, Message: "hook send failed: " + err.Error()}
	}
}
