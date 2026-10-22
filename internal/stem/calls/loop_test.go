package calls

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// This test-only host demonstrates the integration contract without creating a
// production router or giving Table any references to requester handlers.
type testLoop struct {
	table    *Table
	ticker   *fakeTicker
	handlers map[Requester]func(Result) tea.Cmd
	missing  []Requester
}

type testPluginExitMsg struct {
	plugin PluginID
	reason string
}
type testShutdownMsg struct{}

func (l *testLoop) Update(msg tea.Msg) []tea.Cmd {
	var results []Result
	switch m := msg.(type) {
	case ReplyMsg:
		if result, ok := l.table.Resolve(m.Plugin, m.Reply); ok {
			results = append(results, result)
		}
	case SweepMsg:
		results = l.table.Sweep()
	case testPluginExitMsg:
		results = l.table.DropPlugin(m.plugin, m.reason)
	case testShutdownMsg:
		results = l.table.Close()
	}
	var cmds []tea.Cmd
	for _, result := range results {
		if handler := l.handlers[result.Requester]; handler != nil {
			if cmd := handler(result); cmd != nil {
				cmds = append(cmds, cmd)
			}
		} else {
			l.missing = append(l.missing, result.Requester)
		}
	}
	if cmd := sweepCommand(l.table, l.ticker.tick); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return cmds
}

func TestLoopDeliversReplyWithinSameUpdateAndPermitsReentrancy(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{MaxPending: 2, MaxPendingPerPlugin: 1})
	first := mustSend(t, table, "p", Options{Requester: "requester"})
	other := mustSend(t, table, "q")
	loop := testLoop{table: table, ticker: &fakeTicker{clock: clock}, handlers: make(map[Requester]func(Result) tea.Cmd)}
	delivered, commandRan := false, false
	var next Handle
	loop.handlers["requester"] = func(r Result) tea.Cmd {
		delivered = true
		if r.Handle != first || table.Stats().Pending != 1 {
			t.Fatal("reply delivered before removal")
		}
		if table.Cancel(first) || !table.Cancel(other) {
			t.Fatal("handler could not cancel safely")
		}
		if err := table.CanSend("p", "q"); err != nil {
			t.Fatal(err)
		}
		next = mustSend(t, table, "p", Options{Requester: "next"})
		return func() tea.Msg { commandRan = true; return "follow-up" }
	}
	cmds := loop.Update(ReplyMsg{Plugin: "p", Reply: replyFor(first, `{}`)})
	if !delivered || commandRan || next.ID == 0 || table.Stats().Pending != 1 || len(cmds) != 2 || len(loop.missing) != 0 {
		t.Fatal("result was not synchronously routed, or commands ran inside Update")
	}
	if cmds[0]() != "follow-up" || !commandRan {
		t.Fatal("handler command lost")
	}
	assertCounts(t, table)
}

func TestLoopSweepFinishesWholeMutationBeforeDeliveringOrderedResults(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{MaxPending: 2, MaxPendingPerPlugin: 2})
	first := mustSend(t, table, "p", Options{Requester: "first", Timeout: MinTimeout})
	second := mustSend(t, table, "p", Options{Requester: "second", Timeout: MinTimeout})
	loop := testLoop{table: table, ticker: &fakeTicker{clock: clock}, handlers: make(map[Requester]func(Result) tea.Cmd)}
	var order []Handle
	var next Handle
	loop.handlers["first"] = func(r Result) tea.Cmd {
		order = append(order, r.Handle)
		if table.Stats().Pending != 0 || table.Cancel(second) {
			t.Fatal("bulk expiry had not removed every expired entry")
		}
		next = mustSend(t, table, "p", Options{Requester: "next"})
		return nil
	}
	loop.handlers["second"] = func(r Result) tea.Cmd {
		order = append(order, r.Handle)
		if table.Stats().Pending != 1 || table.Cancel(second) {
			t.Fatal("handler-created request lost or old request survived")
		}
		return nil
	}
	cmd := sweepCommand(table, loop.ticker.tick)
	clock.advance(MinTimeout)
	commands := loop.Update(cmd())
	if len(order) != 2 || order[0] != first || order[1] != second || next.ID == 0 || len(commands) != 1 || len(loop.ticker.delays) != 2 {
		t.Fatalf("order=%v next=%v commands=%d", order, next, len(commands))
	}
	assertCounts(t, table)
}

func TestLoopExitAndShutdownDeliverBeforeUpdateReturns(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{})
	p := mustSend(t, table, "p", Options{Requester: "r"})
	q := mustSend(t, table, "q", Options{Requester: "r"})
	loop := testLoop{table: table, ticker: &fakeTicker{clock: clock}, handlers: make(map[Requester]func(Result) tea.Cmd)}
	var results []Result
	loop.handlers["r"] = func(r Result) tea.Cmd { results = append(results, r); return nil }
	loop.Update(testPluginExitMsg{plugin: "p", reason: "disconnected"})
	if len(results) != 1 || results[0].Handle != p || table.Stats().Pending != 1 {
		t.Fatal(results)
	}
	loop.Update(testShutdownMsg{})
	if len(results) != 2 || results[1].Handle != q || table.Stats().Pending != 0 {
		t.Fatal(results)
	}
	if table.CanSend("p") != ErrClosed {
		t.Fatal("shutdown not finalized before delivery")
	}
}

func TestLoopMissingRequesterIsAHostBugNotTableRouting(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{})
	h := mustSend(t, table, "p", Options{Requester: "not-registered"})
	loop := testLoop{table: table, ticker: &fakeTicker{clock: clock}}
	loop.Update(ReplyMsg{Plugin: "p", Reply: replyFor(h, `{}`)})
	if len(loop.missing) != 1 || loop.missing[0] != "not-registered" || table.Stats().Pending != 0 {
		t.Fatal("host did not record missing requester")
	}
	if len(table.Expire(clock.Now().Add(time.Hour))) != 0 {
		t.Fatal("dropped host result reappeared")
	}
}
