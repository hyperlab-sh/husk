// Package stem adapts Stem modules to the consumer-defined ports in
// internal/host/contracts. The adapters own no logic: they enforce the
// homes' caller kinds, derive plugin identity from the authenticated
// caller, cross the host Loop seam (loop.go) for loop-confined modules,
// and map refusals to contracts.CodedError values carrying the wire code.
package stem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/dispatcher"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Post runs fn on the loop goroutine. It must not block the caller on the
// loop's work and returns false when the loop no longer runs (shutdown), in
// which case fn is not run.
type Post func(fn func()) bool

// Calls implements contracts.CallsPort over a Dispatcher. The Dispatcher is
// loop-confined, so every call posts onto the loop and waits for its answer
// off the loop or until ctx ends.
type Calls struct {
	d    *dispatcher.Dispatcher
	post Post
}

var _ contracts.CallsPort = (*Calls)(nil)

// NewCalls returns the port. Both arguments are required.
func NewCalls(d *dispatcher.Dispatcher, post Post) *Calls {
	if d == nil || post == nil {
		panic("stem: NewCalls needs a Dispatcher and a Post")
	}
	return &Calls{d: d, post: post}
}

func coded(code protocol.Code, msg string) error { return contracts.NewError(code, msg, nil) }

// callerOf maps the port's Caller to the Dispatcher's. CallerHost is a Host
// caller: only Input keeps its own focus snapshot, so a Stem caller gets
// State's.
func callerOf(c contracts.Caller) (dispatcher.Caller, bool) {
	switch c.Kind {
	case contracts.CallerCLI:
		return dispatcher.Caller{Kind: dispatcher.CallerCLI}, true
	case contracts.CallerHost:
		return dispatcher.Caller{Kind: dispatcher.CallerHost}, true
	case contracts.CallerPlugin:
		if c.PluginID != "" {
			return dispatcher.Caller{Kind: dispatcher.CallerPlugin, PluginID: c.PluginID}, true
		}
	}
	return dispatcher.Caller{}, false
}

// parseArgs decodes args as a JSON object. Absent, empty and null are nil.
func parseArgs(raw json.RawMessage) (map[string]any, error) { return parseObject(raw, "args") }

// parseObject decodes one JSON object parameter, preserving number lexemes;
// what names the parameter in the error messages. Absent, empty and null
// are nil.
func parseObject(raw json.RawMessage, what string) (map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, coded(protocol.CodeInvalidParams, what+" must be a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, coded(protocol.CodeInvalidParams, what+" must be a JSON object")
	}
	return m, nil
}

// ActionInvoke implements contracts.CallsPort. It blocks until the owner's
// reply, a refusal, or ctx ends; an ended ctx abandons the wait only, the
// request stays pending in Calls until its own deadline.
func (p *Calls) ActionInvoke(ctx context.Context, c contracts.Caller, action string, args json.RawMessage) (json.RawMessage, error) {
	caller, ok := callerOf(c)
	if !ok {
		return nil, coded(protocol.CodeDenied, "unknown caller")
	}
	parsed, err := parseArgs(args)
	if err != nil {
		return nil, err
	}
	done := make(chan dispatcher.ActionReply, 1)
	if !p.post(func() {
		p.d.Invoke(dispatcher.ActionRequest{
			Action: action,
			Args:   parsed,
			Caller: caller,
			Reply:  func(r dispatcher.ActionReply) { done <- r },
		})
	}) {
		return nil, coded(protocol.CodeUnavailable, "host is shutting down")
	}
	r, ok := await(ctx, done)
	if !ok {
		return nil, ctxError(ctx)
	}
	if r.Err != nil {
		return nil, contracts.NewError(protocol.Code(r.Err.Code), r.Err.Message, r.Err.Data)
	}
	res := r.Result
	if res == nil {
		res = map[string]any{}
	}
	out, err := json.Marshal(res)
	if err != nil {
		return nil, coded(protocol.CodeInternal, "cannot encode result")
	}
	return out, nil
}

// ctxError maps an ended context: a passed deadline is timeout, a host
// shutdown is cancelled with {"reason":"shutdown"} (protocol.md), anything
// else cancelled because the caller went away.
func ctxError(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), contracts.ErrShutdown) {
		return contracts.NewError(protocol.CodeCancelled, "instance is shutting down", map[string]any{"reason": "shutdown"})
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return coded(protocol.CodeTimeout, "caller deadline passed")
	}
	return coded(protocol.CodeCancelled, "caller went away")
}

// ActionList implements contracts.CallsPort: the catalog entries the caller
// may invoke, with owner liveness, sorted by id.
func (p *Calls) ActionList(ctx context.Context, c contracts.Caller) ([]contracts.ActionInfo, error) {
	caller, ok := callerOf(c)
	if !ok {
		return nil, coded(protocol.CodeDenied, "unknown caller")
	}
	done := make(chan []contracts.ActionInfo, 1)
	if !p.post(func() {
		listed := p.d.List(caller)
		out := make([]contracts.ActionInfo, 0, len(listed))
		for _, l := range listed {
			out = append(out, contracts.ActionInfo{ID: l.ID, Owner: l.Owner, Public: l.Public, Live: l.Live})
		}
		done <- out
	}) {
		return nil, coded(protocol.CodeUnavailable, "host is shutting down")
	}
	out, ok := await(ctx, done)
	if !ok {
		return nil, ctxError(ctx)
	}
	return out, nil
}
