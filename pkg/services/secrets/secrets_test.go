package secrets

import (
	"testing"
	"time"

	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

type writeClock struct{}

func (writeClock) Now() time.Time { return time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC) }
func (writeClock) NewTimer(time.Duration) c.Timer {
	panic("grant writes must not create timers")
}

// Test the execution-time guard independently of reconcile: it must remain
// safe even if snapshot publication precedes prompt reconciliation.
func TestAcceptWriteRechecksDeclaration(t *testing.T) {
	for _, removed := range []bool{false, true} {
		name := "changed"
		if removed {
			name = "removed"
		}
		t.Run(name, func(t *testing.T) {
			tree := state.New(state.Options{})
			s, err := New(Options{State: tree, Clock: writeClock{}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			old := c.SecretRequest{Name: "git.token", Purpose: "Push"}
			w := make(chan verdict, 1)
			d := &dialog{plugin: "a", name: old.Name, digest: Digest(old), accepting: true, waiters: []chan verdict{w}}
			s.byPlugin["a"] = d
			decls := map[string]c.SecretRequest{}
			if !removed {
				decls[old.Name] = c.SecretRequest{Name: old.Name, Purpose: "Changed"}
			}
			// Deliberately do not reconcile: pin acceptWrite's own digest check.
			s.current.Store(&snapshot{declarations: map[string]map[string]c.SecretRequest{"a": decls}})
			before := tree.Snapshot()
			s.acceptWrite(d)
			if got := <-w; got != vDenied {
				t.Fatalf("stale consent = %v, want denied", got)
			}
			if tree.Snapshot() != before {
				t.Fatal("stale consent wrote a grant")
			}
		})
	}
}
