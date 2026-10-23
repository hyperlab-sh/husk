package stem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/stem/eventbus"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Bus is the part of the Event bus the port uses.
type Bus interface {
	Publish(e eventbus.Event)
	Subscribe(n contracts.Notifier, pattern string) (cancel func(), err error)
}

// Events implements contracts.EventPort over the Event bus.
type Events struct {
	bus  Bus
	loop Loop
}

var _ contracts.EventPort = (*Events)(nil)

// NewEvents returns the event.* port. Emit publishes on the loop through loop;
// Watch subscribes directly (the bus is safe for that from any goroutine).
func NewEvents(bus Bus, loop Loop) *Events { return &Events{bus: bus, loop: loop} }

// Emit serves event.emit. The source is derived from the caller, never from
// params. It returns once the event is on the bus; delivery is not awaited.
func (e *Events) Emit(ctx context.Context, c contracts.Caller, name string, data json.RawMessage) error {
	source, err := eventbus.Authorize(c.Kind, c.PluginID, name)
	if err != nil {
		return err
	}
	obj, err := decodeData(data)
	if err != nil {
		return err
	}
	ev := eventbus.Event{Name: name, Source: source, Data: obj}
	if err := e.loop.Do(ctx, func() { e.bus.Publish(ev) }); err != nil {
		var coded contracts.CodedError
		if errors.As(err, &coded) {
			return err
		}
		return contracts.NewError(protocol.CodeCancelled, "event not published: "+err.Error(), nil)
	}
	return nil
}

// Watch serves event.watch: a plugin asking for a secrets. or services.
// pattern is denied (only cli watchers receive those events); otherwise it
// registers n for pattern, returns nil at once
// (the adapter replies {}), and removes the registration when ctx ends. A
// refused pattern is invalid_params and registers nothing.
func (e *Events) Watch(ctx context.Context, c contracts.Caller, pattern string, n contracts.Notifier) error {
	if c.Kind == contracts.CallerPlugin && protocol.CLIOnlyEventPattern(pattern) {
		return contracts.NewError(protocol.CodeDenied, "events matching "+pattern+" are delivered to cli watchers only", nil)
	}
	cancel, err := e.bus.Subscribe(n, pattern)
	if err != nil {
		return err
	}
	context.AfterFunc(ctx, cancel)
	return nil
}

func decodeData(raw json.RawMessage) (map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] != '{' {
		return nil, contracts.NewError(protocol.CodeInvalidParams, "event data must be a JSON object", nil)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // numbers round-trip verbatim
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, contracts.NewError(protocol.CodeInvalidParams, "event data is not valid JSON", nil)
	}
	return m, nil
}
