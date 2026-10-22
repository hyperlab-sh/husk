// Package hooks is the Stem Hook runner: it issues one hook.call to every
// registered, live subscriber through internal/stem/calls and aggregates the
// replies into the single result list the caller asked for.
//
// The runner is confined to the host loop: Call and Complete are called from
// Update and never block. Call fans out at most the subscribers the injected
// views hand it — all of them, in registration order, no ordering, veto,
// modification or priority semantics — and onDone runs on the loop goroutine
// exactly once, when the last leg of the call has resolved (reply, timeout,
// plugin exit or shutdown). It reads the hook subscription table and plugin
// liveness through small injected views and never writes either. It imports
// neither Store, Pods, Services nor the Server.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Requester is the name this module registers with Calls.
const Requester = "hooks"

// Method is the wire method every hook subscriber receives.
const Method = protocol.MethodHookCall

// Error codes this module produces itself. A subscriber's own error code is
// passed through verbatim in HookResult.Err.
const (
	// CodeNotFound rejects a hook id the declared catalog does not know.
	CodeNotFound = "not_found"
	// CodeBusy reports that a pending-call cap refused the whole call.
	CodeBusy = "busy"
	// CodeUnavailable reports that Calls cannot currently deliver.
	CodeUnavailable = "unavailable"
	// CodeInternal reports a runner bug.
	CodeInternal = "internal"
)

// CallerKind says who started the hook call.
type CallerKind uint8

const (
	// CallerInput is the user pressing a key or choosing a palette entry.
	CallerInput CallerKind = iota + 1
	// CallerPlugin is an authenticated plugin.
	CallerPlugin
	// CallerCLI is a shell outside any plugin: the user typing.
	CallerCLI
	// CallerHost is a host module acting on its own.
	CallerHost
)

// String returns the wire spelling of the kind.
func (k CallerKind) String() string {
	switch k {
	case CallerInput:
		return "input"
	case CallerPlugin:
		return "plugin"
	case CallerCLI:
		return "cli"
	case CallerHost:
		return "host"
	}
	return "unknown"
}

// Caller says who started the hook call. It is set by the module that accepts
// the request, never read from params; the subscribers never see it.
type Caller struct {
	Kind CallerKind
	// PluginID is set only when Kind is CallerPlugin.
	PluginID string
}

// label names the caller for log lines.
func (c Caller) label() string {
	if c.Kind == CallerPlugin {
		return "plugin " + c.PluginID
	}
	return c.Kind.String() + " caller"
}

// HookError is a failed hook call, before any subscriber was asked.
type HookError struct {
	// Code is a wire error code.
	Code string
	// Message explains the refusal.
	Message string
	// Data is the wire error's structured data member; nil when absent.
	Data map[string]any
}

// Error implements error.
func (e *HookError) Error() string { return e.Code + ": " + e.Message }

// HookResult is one subscriber's outcome, in the order the legs resolved.
type HookResult struct {
	// Hook is the hook id that was called.
	Hook string
	// Plugin is the subscriber's plugin id.
	Plugin string
	// Data is the subscriber's reply payload, its result object's "data"
	// member; nil when there is none.
	Data map[string]any
	// Err is the subscriber's failure — its own error, or timeout/cancelled —
	// nil on success.
	Err *HookError
}

// Subscriptions is the read-only view of hook registrations, derived from the
// declared catalog (manifest hook declarations and registrations) outside Stem.
type Subscriptions interface {
	// Subscribers returns the plugins registered for hook, in declaration
	// order. ok is false only when the hook is not declared at all.
	Subscribers(hook string) (subscribers []string, ok bool)
}

// Liveness is the read-only view of plugin liveness (the husk.pods slice).
type Liveness interface {
	// Live reports that the plugin's status is live.
	Live(pluginID string) bool
}

// Sender is the slice of *calls.Table the runner uses: legs leave through
// Send after the caps preflight in CanSend.
type Sender interface {
	// Send issues one request; see calls.Table.Send.
	Send(plugin calls.PluginID, method string, params json.RawMessage, opts calls.Options) (calls.Handle, error)
	// CanSend prechecks the pending-call caps; see calls.Table.CanSend.
	CanSend(plugins ...calls.PluginID) error
}

// Options configures a Runner. Subscriptions, Liveness and Calls are required.
// Zero Timeout selects calls.DefaultTimeout (10s, today's hook timeout); a
// negative Timeout is rejected by New. A nil Logger is a no-op and must not
// block or reenter the runner.
type Options struct {
	Subscriptions Subscriptions
	Liveness      Liveness
	Calls         Sender
	Timeout       time.Duration
	Logger        func(format string, args ...any)
}

// Runner runs hook calls. It is confined to the host loop; Call and Complete
// are called from Update and never block.
type Runner struct {
	opts Options
	legs map[calls.Handle]*fanout
}

// fanout is one hook call in flight: the legs not yet resolved and the
// aggregate they complete into.
type fanout struct {
	hook    string
	waiting int
	results []HookResult
	done    func([]HookResult)
}

// New returns a Runner over opts. It panics when a required view is missing
// and errors on a misconfigured timeout.
func New(opts Options) (*Runner, error) {
	if opts.Subscriptions == nil {
		panic("hooks: Options.Subscriptions is required")
	}
	if opts.Liveness == nil {
		panic("hooks: Options.Liveness is required")
	}
	if opts.Calls == nil {
		panic("hooks: Options.Calls is required")
	}
	if opts.Timeout < 0 {
		return nil, fmt.Errorf("hooks: Options.Timeout must not be negative (zero selects %s)", calls.DefaultTimeout)
	}
	return &Runner{opts: opts, legs: map[calls.Handle]*fanout{}}, nil
}

func (r *Runner) logf(format string, args ...any) {
	if r.opts.Logger != nil {
		r.opts.Logger(format, args...)
	}
}

// Call fans hook out to every registered, live subscriber and returns without
// waiting. A hook nobody registered for is answered immediately with an empty
// result list; a hook the catalog does not know is refused with not_found.
// Cap preflights refuse the whole call and nothing is sent. Delivery is
// per-subscriber, as today's publisher (deliverPublications): a send the
// table answers unavailable becomes that subscriber's error entry — reason
// disconnected — while any other send failure answers with the table's code
// and message, and the remaining subscribers still get theirs.
// onDone receives the aggregate exactly once, on the loop goroutine, when the
// last leg resolved (reply, timeout, plugin exit, shutdown or failed send);
// a nil onDone discards it.
//
// If Call returns an error, onDone is never invoked.
func (r *Runner) Call(caller Caller, hook string, data map[string]any, onDone func([]HookResult)) error {
	if hook == "" {
		return &HookError{Code: CodeNotFound, Message: "hook id is empty"}
	}
	subs, ok := r.opts.Subscriptions.Subscribers(hook)
	if !ok {
		r.logf("hooks: %s called unknown hook %q", caller.label(), hook)
		return &HookError{Code: CodeNotFound, Message: fmt.Sprintf("unknown hook %q", hook)}
	}
	live := make([]calls.PluginID, 0, len(subs))
	for _, sub := range subs {
		if r.opts.Liveness.Live(sub) {
			live = append(live, calls.PluginID(sub))
		}
	}
	if len(live) == 0 {
		if onDone != nil {
			onDone([]HookResult{})
		}
		return nil
	}
	if err := r.opts.Calls.CanSend(live...); err != nil {
		return capError(err)
	}
	params, err := encodeParams(hook, data)
	if err != nil {
		return &HookError{Code: CodeInternal, Message: "cannot encode hook call: " + err.Error()}
	}
	f := &fanout{hook: hook, waiting: len(live), results: make([]HookResult, 0, len(live)), done: onDone}
	for _, target := range live {
		h, err := r.opts.Calls.Send(target, Method, params, calls.Options{Timeout: r.opts.Timeout, Requester: Requester})
		if err != nil {
			// Per-subscriber delivery: one undeliverable leg answers as that
			// subscriber's entry; the rest still get the call.
			r.logf("hooks: %s: send to %s failed: %v", hook, target, err)
			e := sendError(err)
			if errors.Is(err, calls.ErrUnavailable) {
				e.Data = map[string]any{"reason": "disconnected"}
				e.Message = "subscriber disconnected"
			}
			f.results = append(f.results, HookResult{Hook: hook, Plugin: string(target), Err: e})
			f.waiting--
			continue
		}
		r.legs[h] = f
	}
	if f.waiting == 0 && f.done != nil {
		f.done(f.results)
	}
	return nil
}

// Complete resolves the legs named by results — every Result whose Requester
// is "hooks" that Resolve, Sweep, DropPlugin or Close produced — and returns
// how many legs it resolved. Results from other requesters and unknown handles
// are ignored. The last leg of a fanout invokes its onDone on this goroutine.
func (r *Runner) Complete(results ...calls.Result) int {
	resolved := 0
	for _, res := range results {
		if res.Requester != Requester {
			continue
		}
		f, ok := r.legs[res.Handle]
		if !ok {
			continue
		}
		delete(r.legs, res.Handle)
		resolved++
		f.results = append(f.results, convert(res, f.hook))
		f.waiting--
		if f.waiting == 0 && f.done != nil {
			f.done(f.results)
		}
	}
	return resolved
}

// Pending reports how many hook legs are awaiting a reply.
func (r *Runner) Pending() int { return len(r.legs) }

// hookParams is the params object every subscriber receives: the hook id and
// the caller's data payload. The subscriber does not see who called the hook.
type hookParams struct {
	Hook string          `json:"hook"`
	Data json.RawMessage `json:"data,omitempty"`
}

func encodeParams(hook string, data map[string]any) (json.RawMessage, error) {
	p := hookParams{Hook: hook}
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		if string(b) != "null" {
			p.Data = b
		}
	}
	return json.Marshal(p)
}

// convert turns one leg's Result into its aggregate entry.
func convert(res calls.Result, hook string) HookResult {
	out := HookResult{Hook: hook, Plugin: string(res.Handle.Plugin)}
	if res.Err != nil {
		e := &HookError{Code: string(res.Err.Code), Message: res.Err.Message}
		if d := decodeObject(res.Err.Data); d != nil {
			e.Data = d
		}
		out.Err = e
		return out
	}
	out.Data = payloadData(res.Value)
	return out
}

// payloadData extracts the subscriber's payload from its result object's
// "data" member. An absent member, JSON null, or a non-object result or
// member yields nil; today's v1 host likewise forwards only the payload
// object a subscriber put under "data".
func payloadData(value json.RawMessage) map[string]any {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		return nil
	}
	raw, ok := obj["data"]
	if !ok {
		return nil
	}
	return decodeObject(raw)
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
// subscriber's aggregate entry, not a refusal of the call.
func sendError(err error) *HookError {
	switch {
	case errors.Is(err, calls.ErrUnavailable), errors.Is(err, calls.ErrClosed):
		return &HookError{Code: CodeUnavailable, Message: "cannot send hook call: " + err.Error()}
	default:
		return &HookError{Code: CodeInternal, Message: "hook send failed: " + err.Error()}
	}
}
