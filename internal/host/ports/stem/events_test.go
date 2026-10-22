package stem_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/internal/host/ports/stem"
	"github.com/hyperlab-sh/husk/internal/stem/eventbus"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// fakeLoop runs fn inline unless Err is set, and counts runs.
type fakeLoop struct {
	err  error
	runs int
}

func (l *fakeLoop) Do(ctx context.Context, fn func()) error {
	if l.err != nil {
		return l.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.runs++
	fn()
	return nil
}

type recBus struct {
	pub    []eventbus.Event
	subs   map[string]int
	cancel chan string // receives the pattern when its subscription is cancelled
}

func (b *recBus) Publish(e eventbus.Event) { b.pub = append(b.pub, e) }
func (b *recBus) Subscribe(n contracts.Notifier, p string) (func(), error) {
	if err := eventbus.ValidatePattern(p); err != nil {
		return nil, err
	}
	if b.subs == nil {
		b.subs, b.cancel = map[string]int{}, make(chan string, 8)
	}
	b.subs[p]++
	return func() { b.cancel <- p }, nil
}

var (
	plugin = contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "notes"}
	cli    = contracts.Caller{Kind: contracts.CallerCLI}
)

func TestEmitSetsSourceFromCaller(t *testing.T) {
	b, l := &recBus{}, &fakeLoop{}
	p := stem.NewEvents(b, l)
	if err := p.Emit(context.Background(), plugin, "notes.saved", json.RawMessage(`{"path":"/x","n":12345678901234567890}`)); err != nil {
		t.Fatal(err)
	}
	if err := p.Emit(context.Background(), cli, "user.ping", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.pub) != 2 || l.runs != 2 {
		t.Fatalf("%+v", b.pub)
	}
	if b.pub[0].Source != "notes" || b.pub[0].Data["path"] != "/x" {
		t.Fatalf("%+v", b.pub[0])
	}
	if raw, _ := json.Marshal(b.pub[0].Data["n"]); string(raw) != "12345678901234567890" {
		t.Fatalf("number not preserved: %s", raw)
	}
	if b.pub[1].Source != "user" || b.pub[1].Data != nil {
		t.Fatalf("%+v", b.pub[1])
	}
}

func TestEmitRefusals(t *testing.T) {
	tests := []struct {
		name string
		c    contracts.Caller
		ev   string
		data string
		code string
	}{
		{"other namespace", plugin, "other.saved", "", "denied"},
		{"core name", plugin, "state.changed", "", "denied"},
		{"cli outside user", cli, "notes.saved", "", "denied"},
		{"malformed", plugin, "notes.*", "", "invalid_params"},
		{"array data", plugin, "notes.saved", "[1]", "invalid_params"},
		{"scalar data", plugin, "notes.saved", `"x"`, "invalid_params"},
		{"broken data", plugin, "notes.saved", `{"a":`, "invalid_params"},
	}
	for _, tt := range tests {
		b, l := &recBus{}, &fakeLoop{}
		err := stem.NewEvents(b, l).Emit(context.Background(), tt.c, tt.ev, json.RawMessage(tt.data))
		if contracts.CodeOf(err) != tt.code || len(b.pub) != 0 {
			t.Errorf("%s: %v published=%d", tt.name, err, len(b.pub))
		}
	}
}

func TestEmitNullDataAndLoopFailure(t *testing.T) {
	b, l := &recBus{}, &fakeLoop{}
	p := stem.NewEvents(b, l)
	if err := p.Emit(context.Background(), plugin, "notes.saved", json.RawMessage(`null`)); err != nil || b.pub[0].Data != nil {
		t.Fatal(err)
	}
	l.err = errors.New("loop gone")
	if err := p.Emit(context.Background(), plugin, "notes.saved", nil); contracts.CodeOf(err) != "cancelled" {
		t.Fatalf("%v", err)
	}
	coded := contracts.NewError(protocol.CodeUnavailable, "shutting down", nil)
	l.err = coded
	if err := p.Emit(context.Background(), plugin, "notes.saved", nil); contracts.CodeOf(err) != "unavailable" {
		t.Fatalf("%v", err)
	}
	l.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n := len(b.pub)
	if err := p.Emit(ctx, plugin, "notes.saved", nil); contracts.CodeOf(err) != "cancelled" || len(b.pub) != n {
		t.Fatalf("%v", err)
	}
}

func TestWatchRegistersReturnsAtOnceAndUnregistersOnCtxEnd(t *testing.T) {
	b := &recBus{}
	p := stem.NewEvents(b, &fakeLoop{})
	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Watch(ctx, cli, "notes.*", fakes.NewNotifier(nil)); err != nil {
		t.Fatal(err)
	}
	if b.subs["notes.*"] != 1 || len(b.cancel) != 0 {
		t.Fatal("registered state wrong")
	}
	cancel()
	if got := <-b.cancel; got != "notes.*" { // context.AfterFunc runs on its own goroutine
		t.Fatal("not unregistered on ctx end")
	}
}

func TestWatchRefusedPattern(t *testing.T) {
	b := &recBus{}
	p := stem.NewEvents(b, &fakeLoop{})
	for _, pat := range []string{"", "*foo", "a.*.b"} {
		err := p.Watch(context.Background(), cli, pat, fakes.NewNotifier(nil))
		if contracts.CodeOf(err) != "invalid_params" {
			t.Errorf("%q: %v", pat, err)
		}
	}
	if len(b.subs) != 0 {
		t.Fatal("refused pattern registered")
	}
}

// With the real bus: watch, emit, flush, deliver, ctx end, silence.
func TestWatchEndToEndWithRealBus(t *testing.T) {
	bus := eventbus.New(eventbus.Options{})
	sb := &signalBus{Bus: bus, done: make(chan struct{})}
	p := stem.NewEvents(sb, &fakeLoop{})
	rec := fakes.NewRecorder()
	n := fakes.NewNotifier(rec)
	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Watch(ctx, cli, "notes.*", n); err != nil {
		t.Fatal(err)
	}
	if err := p.Emit(context.Background(), plugin, "notes.saved", json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	bus.Flush()
	if !slices.Equal(rec.Methods(), []string{"Notifier.Notify"}) {
		t.Fatalf("%v", rec.Calls())
	}
	cancel()
	<-sb.done
	if err := p.Emit(context.Background(), plugin, "notes.saved", nil); err != nil {
		t.Fatal(err)
	}
	bus.Flush()
	if len(rec.Calls()) != 1 {
		t.Fatal("delivery after ctx end")
	}
}

// signalBus closes done once the subscription's cancel has run.
type signalBus struct {
	*eventbus.Bus
	done chan struct{}
}

func (s *signalBus) Subscribe(n contracts.Notifier, p string) (func(), error) {
	c, err := s.Bus.Subscribe(n, p)
	return func() { c(); close(s.done) }, err
}
