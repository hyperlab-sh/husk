package calls

import (
	"reflect"
	"testing"
	"time"
)

func TestPlainTableCanBeDeepComparedWithValueClockState(t *testing.T) {
	a, clockA, _ := newTestTable(TableOptions{})
	b, clockB, _ := newTestTable(TableOptions{})
	check := func() {
		t.Helper()
		if !reflect.DeepEqual(a, b) {
			t.Fatal("identical model tables differ")
		}
	}
	check()
	ha := mustSend(t, a, "p")
	hb := mustSend(t, b, "p")
	check()
	a.NextSweep()
	b.NextSweep()
	check() // no scheduling function or command is stored in the model
	clockA.advance(time.Second)
	clockB.advance(time.Second)
	a.Resolve("p", replyFor(ha, `{}`))
	b.Resolve("p", replyFor(hb, `{}`))
	check()
	a.Sweep()
	b.Sweep()
	check()
}
