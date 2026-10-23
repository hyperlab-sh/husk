package calls

import (
	"errors"
	"testing"
)

func TestCanSendPreflightsRemainingIDSpaceWithoutPartialFanout(t *testing.T) {
	table, _, box := newTestTable(TableOptions{})
	table.lastID = maxID - 1
	if err := table.CanSend("a", "b"); !errors.Is(err, ErrBusy) {
		t.Fatal("preflight allowed a partially representable fan-out", err)
	}
	if table.lastID != maxID-1 || len(box.messages) != 0 || table.Stats().Pending != 0 {
		t.Fatal("preflight mutated or sent")
	}
	if err := table.CanSend("a"); err != nil {
		t.Fatal(err)
	}
	h := mustSend(t, table, "a")
	if h.ID != maxID {
		t.Fatal(h)
	}
	table.Cancel(h)
	if err := table.CanSend("b"); !errors.Is(err, ErrBusy) {
		t.Fatal("exhausted counter appeared available", err)
	}
	if err := table.CanSend(); err != nil {
		t.Fatal("empty preflight should need no ids", err)
	}
}

func TestCapFailureCanBurnTheLastIDButNeverReuseIt(t *testing.T) {
	table, _, box := newTestTable(TableOptions{MaxPending: 1})
	first := mustSend(t, table, "p")
	table.lastID = maxID - 1
	if _, err := table.Send("q", "sample.call", nil, Options{Requester: "r"}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if table.lastID != maxID || len(box.messages) != 1 {
		t.Fatal("cap failure did not burn its id without writing")
	}
	table.Cancel(first)
	if _, err := table.Send("q", "sample.call", nil, Options{Requester: "r"}); !errors.Is(err, ErrBusy) {
		t.Fatal("burnt last id was reused", err)
	}
	assertCounts(t, table)
}
