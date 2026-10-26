package host

import (
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestOnLoopStartRunsOnceOnLoopAndLosesNoEvent(t *testing.T) {
	var calls atomic.Int32
	var onLoop atomic.Bool
	var got []protocol.EventDeliverParams
	var subErr error
	hook, ready := readyHook("")
	f := newFakes()
	var h *harness
	h, err := boot(t, func(c *contracts.Config) { pluginSettings(c)["paths"] = []string{alphaPlugin(t)} },
		withHook(hook), WithFactory(f.factory),
		WithOnLoopStart(func(fe contracts.Frontend) {
			calls.Add(1)
			onLoop.Store(h.root.onLoop())
			_, subErr = fe.WatchEvents("alpha.*", func(p protocol.EventDeliverParams) { got = append(got, p) })
		}))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	emitAs(t, h, "alpha", "alpha.saved", map[string]any{"n": 1})
	h.loop(func() {})
	h.loop(func() {
		if subErr != nil {
			t.Errorf("subscribe: %v", subErr)
		}
		if len(got) != 1 || got[0].Name != "alpha.saved" {
			t.Errorf("got %+v", got)
		}
	})
	// Later Updates do not run it again.
	h.loop(func() { h.root.Tick() })
	h.loop(func() {})
	if n := calls.Load(); n != 1 {
		t.Fatalf("hook ran %d times", n)
	}
	if !onLoop.Load() {
		t.Fatal("hook ran off the loop")
	}
}

func TestOnLoopStartRunsOnFirstTick(t *testing.T) {
	var calls int
	h, err := boot(t, nil, WithOnLoopStart(func(contracts.Frontend) { calls++ }))
	if err != nil {
		t.Fatal(err)
	}
	// Tick before any host message; the loop goroutine drains the harness queue.
	h.root.Tick()
	h.root.Tick()
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestOnLoopStartNilAndLastWins(t *testing.T) {
	var first, second int
	h, err := boot(t, nil,
		WithOnLoopStart(func(contracts.Frontend) { first++ }),
		WithOnLoopStart(func(contracts.Frontend) { second++ }))
	if err != nil {
		t.Fatal(err)
	}
	h.loop(func() {})
	h.loop(func() {})
	if first != 0 || second != 1 {
		t.Fatalf("first=%d second=%d", first, second)
	}
	h2, err := boot(t, nil, WithOnLoopStart(nil))
	if err != nil {
		t.Fatal(err)
	}
	h2.loop(func() {})
}
