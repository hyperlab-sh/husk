package pods

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/pods/internal/testclock"
	"github.com/hyperlab-sh/husk/pkg/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

type reservationRegistry struct {
	contracts.PodRegistry
	reserve func(string) error
}

func (r reservationRegistry) Reserve(_ context.Context, id string) error { return r.reserve(id) }

type addedHook struct {
	Workers
	added func(string)
}

func (w addedHook) Added(id string) { w.added(id) }

func TestPrepareReturnsOnlyItsNewGate(t *testing.T) {
	h, _, _, _ := hostFixture(t)
	addPod(t, h, "alpha")
	addPod(t, h, "beta")
	first, cmd := h.prepare("alpha")
	if first == 0 || cmd == nil {
		t.Fatal("no gate opened")
	}
	if gate, cmd := h.prepare("alpha", "missing"); gate != 0 || cmd != nil {
		t.Fatalf("no-work batch reused gate %d", gate)
	}
	second, cmd := h.prepare("beta")
	if second == 0 || second == first || cmd == nil {
		t.Fatalf("gates=%d,%d", first, second)
	}
	h.closing = true
	if gate, cmd := h.prepare("beta"); gate != 0 || cmd != nil {
		t.Fatalf("closing batch returned gate %d", gate)
	}
}

func TestAddWithoutNewGateDoesNotReplaceAnOlderEntry(t *testing.T) {
	f, pending := additionFixture(t)
	path := writeAddPlugin(t, t.TempDir(), "alpha", "alpha")
	var older *addition
	f.onLoop(func() {
		addPod(t, f.h, "older")
		gate, _ := f.h.prepare("older")
		older = &addition{done: make(chan struct{}), result: emptyAddition()}
		f.h.adding[gate] = older
		// Force the defensive no-work branch through the loop-side hook.
		f.h.opts.Workers = addedHook{Workers: f.rec, added: func(id string) { f.h.preparing[id] = true }}
	})
	got := testclock.Receive(t, startAdd(f, t.Context(), contracts.PluginSource{Path: path}))
	if got.err != nil || len(got.result.Added) != 0 || len(got.result.Reports) != 0 {
		t.Fatalf("reply=%+v err=%v", got.result, got.err)
	}
	f.onLoop(func() {
		if len(f.h.adding) != 1 || f.h.adding[1] != older {
			t.Error("older adding entry replaced")
		}
	})
	select {
	case <-pending:
		t.Fatal("no-work batch started preparation")
	default:
	}
}

func TestAddRegistryErrorsDistinguishConflictsFromInternalFailures(t *testing.T) {
	for _, dirForm := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			err  error
			code protocol.Code
		}{
			{"conflict", contracts.NewError(protocol.CodeConflict, "reserved", nil), protocol.CodeConflict},
			{"uncoded failure", errors.New("registry offline"), protocol.CodeInternal},
			{"coded failure", contracts.NewError(protocol.CodeUnavailable, "registry offline", nil), protocol.CodeInternal},
		} {
			t.Run(tc.name+map[bool]string{false: "/path", true: "/directory"}[dirForm], func(t *testing.T) {
				f, pending := additionFixture(t)
				root := t.TempDir()
				path := writeAddPlugin(t, root, "alpha", "alpha")
				f.onLoop(func() {
					f.h.opts.Registry = reservationRegistry{PodRegistry: f.rec, reserve: func(string) error { return tc.err }}
				})
				source := contracts.PluginSource{Path: path}
				if dirForm {
					source = contracts.PluginSource{Dir: root}
				}
				got := testclock.Receive(t, startAdd(f, t.Context(), source))
				if dirForm && tc.code == protocol.CodeConflict {
					if got.err != nil || len(got.result.Refused) != 1 || got.result.Refused[0].Code != "duplicate_plugin_id" {
						t.Fatalf("reply=%+v err=%v", got.result, got.err)
					}
				} else {
					wantAddError(t, got.err, tc.code, "")
				}
				f.onLoop(func() {
					if len(f.h.pods) != 0 || len(f.h.adding) != 0 {
						t.Error("failed reservation admitted a Pod")
					}
				})
				select {
				case <-pending:
					t.Fatal("failed reservation prepared a batch")
				default:
				}
			})
		}
	}
}

func TestAddInternalRegistryFailureSettlesEarlierAdmissions(t *testing.T) {
	f, pending := additionFixture(t)
	root := t.TempDir()
	writeAddPlugin(t, root, "alpha", "alpha")
	writeAddPlugin(t, root, "beta", "beta")
	f.onLoop(func() {
		f.h.opts.Registry = reservationRegistry{PodRegistry: f.rec, reserve: func(id string) error {
			if id == "beta" {
				return errors.New("registry offline")
			}
			return nil
		}}
	})
	reply := startAdd(f, t.Context(), contracts.PluginSource{Dir: root})
	gate := testclock.Receive(t, pending)
	if len(gate.ids) != 1 || gate.ids[0] != "alpha" {
		t.Fatal(gate.ids)
	}
	select {
	case <-reply:
		t.Fatal("internal failure abandoned an admitted batch")
	default:
	}
	gate.release <- Preparation{Reports: addReports()}
	got := testclock.Receive(t, reply)
	wantAddError(t, got.err, protocol.CodeInternal, "")
	data, ok := got.err.(contracts.DataError)
	if !ok || !reflect.DeepEqual(data.Data()["added"], []string{"alpha"}) {
		t.Fatalf("internal error omitted admitted ids: %v", got.err)
	}
	f.onLoop(func() {
		if f.h.pods["alpha"].info.Status != wrapper.Live || f.h.pods["beta"] != nil || len(f.h.adding) != 0 {
			t.Error("partial admission did not settle")
		}
	})
}
