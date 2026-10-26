package host

import (
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/stem/eventbus"
)

// bootSolo builds a host whose Post drops every message and starts no loop
// goroutine, so the test goroutine is the only loop owner.
func bootSolo(t *testing.T, opts ...Option) *Root {
	t.Helper()
	state := t.TempDir()
	t.Setenv("HOME", state)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(state, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(state, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(state, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(state, "run"))
	id := "s" + filepath.Base(state)
	host, err := New(contracts.Config{InstanceID: id, RuntimeDir: runtimeDir(t, id), Post: func(any) bool { return true }}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	r := host.(*Root)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// publishSaved is a message that publishes an event the way a loop-side
// publisher does.
func publishSaved(r *Root) fnMsg {
	return fnMsg{fn: func() {
		r.bus.Publish(eventbus.Event{Name: "app.saved", Source: eventbus.SourceHost, Data: map[string]any{"n": 1}})
	}}
}

func TestOnLoopStartRunsOnceOnTheLoop(t *testing.T) {
	var calls atomic.Int32
	var gid atomic.Int64 // the hook must not touch h: the loop runs before boot returns
	hook, ready := readyHook("")
	f := newFakes()
	h, err := boot(t, func(c *contracts.Config) { pluginSettings(c)["paths"] = []string{alphaPlugin(t)} },
		withHook(hook), WithFactory(f.factory),
		WithOnLoopStart(func(contracts.Frontend) {
			calls.Add(1)
			gid.Store(goroutineID())
		}))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	h.loop(func() {})
	h.loop(func() { h.root.Tick() })
	h.loop(func() {})
	if n := calls.Load(); n != 1 {
		t.Fatalf("hook ran %d times", n)
	}
	if g := gid.Load(); g == 0 || g != h.root.loopGID.Load() {
		t.Fatalf("hook goroutine %d, loop %d", g, h.root.loopGID.Load())
	}
}

func TestOnLoopStartSubscriptionSeesFirstUpdateEvent(t *testing.T) {
	var calls int
	var got []protocol.EventDeliverParams
	var subErr error
	var r *Root
	r = bootSolo(t, WithOnLoopStart(func(fe contracts.Frontend) {
		calls++
		_, subErr = fe.WatchEvents("app.*", func(p protocol.EventDeliverParams) { got = append(got, p) })
	}))
	// The first message the host ever sees publishes the event.
	r.Update(publishSaved(r))
	if subErr != nil {
		t.Fatal(subErr)
	}
	if calls != 1 || len(got) != 1 || got[0].Name != "app.saved" {
		t.Fatalf("calls=%d got=%+v", calls, got)
	}
	r.Update(fnMsg{fn: func() {}})
	if calls != 1 {
		t.Fatalf("hook ran again: %d", calls)
	}
}

func TestOnLoopStartRunsOnFirstTick(t *testing.T) {
	var calls int
	var got []protocol.EventDeliverParams
	var r *Root
	r = bootSolo(t, WithOnLoopStart(func(fe contracts.Frontend) {
		calls++
		_, _ = fe.WatchEvents("app.*", func(p protocol.EventDeliverParams) { got = append(got, p) })
		r.bus.Publish(eventbus.Event{Name: "app.saved", Source: eventbus.SourceHost})
	}))
	r.Tick()
	r.Tick()
	if calls != 1 || len(got) != 1 {
		t.Fatalf("calls=%d got=%+v", calls, got)
	}
	if !r.onLoop() {
		t.Fatal("first Tick did not fix the loop goroutine")
	}
}

func TestOnLoopStartNilIsIgnored(t *testing.T) {
	var calls int
	r := bootSolo(t, WithOnLoopStart(func(contracts.Frontend) { calls++ }), WithOnLoopStart(nil))
	r.Update(fnMsg{fn: func() {}})
	r.Update(fnMsg{fn: func() {}})
	if calls != 1 {
		t.Fatalf("calls = %d, want the earlier fn kept", calls)
	}
	bootSolo(t, WithOnLoopStart(nil)).Update(fnMsg{fn: func() {}})
}

func TestOnLoopStartLastWins(t *testing.T) {
	var first, second int
	r := bootSolo(t,
		WithOnLoopStart(func(contracts.Frontend) { first++ }),
		WithOnLoopStart(func(contracts.Frontend) { second++ }))
	r.Update(fnMsg{fn: func() {}})
	if first != 0 || second != 1 {
		t.Fatalf("first=%d second=%d", first, second)
	}
}
