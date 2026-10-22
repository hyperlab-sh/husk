package calls

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type fakeTicker struct {
	clock  Clock
	delays []time.Duration
}

func (f *fakeTicker) tick(delay time.Duration, emit func(time.Time) tea.Msg) tea.Cmd {
	f.delays = append(f.delays, delay)
	return func() tea.Msg { return emit(f.clock.Now()) }
}

func TestNextSweepClaimsAtMostOneSlotOnlyWhilePending(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{})
	if d, ok := table.NextSweep(); ok || d != 0 {
		t.Fatalf("idle sweep: %s %v", d, ok)
	}
	if table.SweepCmd() != nil {
		t.Fatal("idle Bubble Tea adapter scheduled a timer")
	}
	h := mustSend(t, table, "p")
	if d, ok := table.NextSweep(); !ok || d != SweepInterval {
		t.Fatalf("pending sweep: %s %v", d, ok)
	}
	if _, ok := table.NextSweep(); ok {
		t.Fatal("duplicate sweep")
	}
	clock.advance(SweepInterval)
	if len(table.Sweep()) != 0 {
		t.Fatal("expired too early")
	}
	if _, ok := table.NextSweep(); !ok {
		t.Fatal("sweep did not clear in-flight marker")
	}
	table.Cancel(h)
	if _, ok := table.NextSweep(); ok {
		t.Fatal("empty table created tick")
	}
	if !table.sweepInFlight {
		t.Fatal("cancel forgot a real outstanding tick")
	}
	if len(table.Sweep()) != 0 || table.sweepInFlight {
		t.Fatal("outstanding empty tick not acknowledged")
	}
	if _, ok := table.NextSweep(); ok {
		t.Fatal("idle table rearmed")
	}
}

func TestSweepAdapterUsesFixedIntervalAndFakeTick(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{})
	ticker := fakeTicker{clock: clock}
	mustSend(t, table, "p", Options{Timeout: MinTimeout, Requester: "r"})
	for i := 1; i <= 4; i++ {
		cmd := sweepCommand(table, ticker.tick)
		if cmd == nil || sweepCommand(table, ticker.tick) != nil {
			t.Fatal("missing or duplicate tick")
		}
		if ticker.delays[i-1] != 250*time.Millisecond {
			t.Fatal(ticker.delays)
		}
		clock.advance(SweepInterval)
		if _, ok := cmd().(SweepMsg); !ok {
			t.Fatal("wrong sweep message")
		}
		results := table.Sweep()
		if i < 4 && len(results) != 0 {
			t.Fatal("expired early")
		}
		if i == 4 && len(results) != 1 {
			t.Fatal("failed to expire at deadline")
		}
	}
	if sweepCommand(table, ticker.tick) != nil || len(ticker.delays) != 4 {
		t.Fatal("idle tick chain continued")
	}
	assertCounts(t, table)
}

func TestOutstandingSweepCoversNewCallsWithoutDuplicateTick(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{})
	ticker := fakeTicker{clock: clock}
	first := mustSend(t, table, "p")
	cmd := sweepCommand(table, ticker.tick)
	table.Cancel(first)
	mustSend(t, table, "q")
	if sweepCommand(table, ticker.tick) != nil || len(ticker.delays) != 1 {
		t.Fatal("duplicated outstanding tick")
	}
	clock.advance(SweepInterval)
	if _, ok := cmd().(SweepMsg); !ok {
		t.Fatal("wrong message")
	}
	table.Sweep()
	if sweepCommand(table, ticker.tick) == nil || len(ticker.delays) != 2 {
		t.Fatal("new call not covered by chain")
	}
}

func TestCloseDoesNotRearmOutstandingSweep(t *testing.T) {
	table, clock, _ := newTestTable(TableOptions{})
	ticker := fakeTicker{clock: clock}
	mustSend(t, table, "p")
	cmd := sweepCommand(table, ticker.tick)
	table.Close()
	if sweepCommand(table, ticker.tick) != nil {
		t.Fatal("closed table rearmed")
	}
	if _, ok := cmd().(SweepMsg); !ok {
		t.Fatal("wrong message")
	}
	if len(table.Sweep()) != 0 || sweepCommand(table, ticker.tick) != nil {
		t.Fatal("shutdown sweep produced results or ticks")
	}
}
