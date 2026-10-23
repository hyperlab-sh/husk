package services

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	servicesh "github.com/hyperlab-sh/husk/internal/host/handlers/services"
	services "github.com/hyperlab-sh/husk/internal/services"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// tickClock is a manual clock whose timers fire on advance.
type tickClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*tickTimer
	created chan struct{}
}

type tickTimer struct {
	at   time.Time
	ch   chan time.Time
	done bool
}

func newTickClock() *tickClock {
	return &tickClock{now: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), created: make(chan struct{}, 16)}
}
func (k *tickClock) Now() time.Time { k.mu.Lock(); defer k.mu.Unlock(); return k.now }
func (k *tickClock) NewTimer(d time.Duration) c.Timer {
	k.mu.Lock()
	t := &tickTimer{at: k.now.Add(d), ch: make(chan time.Time, 1)}
	k.timers = append(k.timers, t)
	k.mu.Unlock()
	k.created <- struct{}{}
	return &tickHandle{k, t}
}
func (k *tickClock) advance(d time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.now = k.now.Add(d)
	for _, t := range k.timers {
		if !t.done && !k.now.Before(t.at) {
			t.done = true
			t.ch <- k.now
		}
	}
}

type tickHandle struct {
	k *tickClock
	t *tickTimer
}

func (h *tickHandle) C() <-chan time.Time { return h.t.ch }
func (h *tickHandle) Stop() bool {
	h.k.mu.Lock()
	defer h.k.mu.Unlock()
	was := !h.t.done
	h.t.done = true
	return was
}

// stubService rebuilds to an always-applying candidate unless told otherwise.
type stubService struct {
	gen     c.Generation
	fail    error
	entered chan struct{} // closed on entry when set; Rebuild then waits for ctx
}

func (s *stubService) Generation() c.Generation { return s.gen }
func (s *stubService) Rebuild(ctx context.Context, _ services.ReloadInput) (c.Candidate, c.Report, error) {
	if s.entered != nil {
		close(s.entered)
		<-ctx.Done()
		return nil, c.Report{}, ctx.Err()
	}
	if s.fail != nil {
		return nil, c.Report{}, s.fail
	}
	return stubCandidate{}, c.Report{}, nil
}

type stubCandidate struct{}

func (stubCandidate) Apply() c.Report { return c.Report{Applied: true} }

// config.reload's error replies, decoded from the handler's JSON: the code and
// data.reason a client sees, and a flight in which one Service merely kept its
// snapshot is a result, not an error.
func TestConfigReloadReplyMapping(t *testing.T) {
	for _, tc := range []struct {
		name       string
		store      *fakeStoreReader
		hold       bool // settings holds its Rebuild until the flight ends
		act        func(*tickClock, *services.Coordinator)
		code, why  string
		wantResult bool
	}{
		{name: "store error", store: &fakeStoreReader{err: errors.New("disk")}, code: "unavailable", why: "store_error"},
		{name: "deadline", hold: true, act: func(k *tickClock, _ *services.Coordinator) { k.advance(2 * time.Second) }, code: "timeout", why: "reload_deadline"},
		{name: "close", hold: true, act: func(_ *tickClock, co *services.Coordinator) { co.Close() }, code: "cancelled", why: "shutdown"},
		{name: "a Service keeps its snapshot", wantResult: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newTickClock()
			held := make(chan struct{})
			set := map[string]services.Reloadable{
				"profile": &stubService{}, "settings": &stubService{}, "theme": &stubService{}, "keybinding": &stubService{}, "secrets": &stubService{},
			}
			switch {
			case tc.hold:
				set["settings"] = &stubService{entered: held}
			case tc.wantResult:
				set["settings"] = &stubService{fail: errors.New("bad value")}
			}
			reader := tc.store
			if reader == nil {
				reader = &fakeStoreReader{resolved: store.Resolved{Doc: doc(t, `{}`), Origins: map[string]store.Layer{}}}
			}
			co, err := services.NewCoordinator(services.Options{Store: reader, Services: set, Clock: clk})
			if err != nil {
				t.Fatal(err)
			}
			defer co.Close()
			id, _ := protocol.NewIntID(1)
			req := protocol.Message{ID: &id, Method: protocol.MethodConfigReload, Params: json.RawMessage(`{}`)}
			reply := make(chan protocol.Message, 1)
			h := servicesh.Config{Port: Config{Co: co}}
			go func() { reply <- h.Serve(context.Background(), userCaller, req, nil) }()
			if tc.hold {
				<-held
				tc.act(clk, co)
			}
			m := <-reply
			if tc.wantResult {
				var res struct {
					Reports []struct{ Service string }
				}
				if m.Error != nil || json.Unmarshal(m.Result, &res) != nil || len(res.Reports) != 5 {
					t.Fatalf("reply = error %+v result %s, want a result with five reports", m.Error, m.Result)
				}
				return
			}
			if m.Error == nil {
				t.Fatalf("reply = result %s, want error %s/%s", m.Result, tc.code, tc.why)
			}
			var data struct{ Reason string }
			if err := json.Unmarshal(m.Error.Data, &data); err != nil || string(m.Error.Code) != tc.code || data.Reason != tc.why {
				t.Fatalf("error = %s data %s, want %s/%s", m.Error.Code, m.Error.Data, tc.code, tc.why)
			}
		})
	}
}
