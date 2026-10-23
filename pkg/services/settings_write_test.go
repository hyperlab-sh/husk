package services

import (
	"context"
	"sync"
	"testing"

	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/services/settings"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

type writableStore struct {
	fakeStore
	lock    sync.Mutex
	config  store.Doc
	writes  int
	capture chan struct{}
	release <-chan struct{}
}

func (s *writableStore) Patch(_ store.DocName, _ store.Layer, _ string, patch store.Doc) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	var merge func(store.Doc, store.Doc)
	merge = func(dst, src store.Doc) {
		for k, v := range src {
			if obj, ok := v.(store.Doc); ok {
				target, _ := dst[k].(store.Doc)
				if target == nil {
					target = store.Doc{}
					dst[k] = target
				}
				merge(target, obj)
			} else {
				dst[k] = v
			}
		}
	}
	merge(s.config, patch)
	s.writes++
	return nil
}
func (s *writableStore) Resolve(name store.DocName, _ string) (store.Resolved, error) {
	if name != "config" {
		return store.Resolved{}, nil
	}
	s.lock.Lock()
	d := c.Clone(s.config)
	entered, release := s.capture, s.release
	s.capture = nil
	s.lock.Unlock()
	if entered != nil {
		close(entered)
		<-release
	}
	origins := map[string]store.Layer{"theme.name": store.LayerUser, "theme.allow_low_contrast": store.LayerUser}
	return store.Resolved{Doc: d, Origins: origins}, nil
}
func writeCoordinator(t *testing.T, st *writableStore) (*settings.Service, *Coordinator, chan func()) {
	t.Helper()
	posts := make(chan func(), 8)
	s, err := settings.New(settings.Options{Store: st, Post: func(f func()) bool { posts <- f; return true }})
	if err != nil {
		t.Fatal(err)
	}
	modules := fakeServices()
	modules["settings"] = s
	co, err := NewCoordinator(Options{Store: st, Services: modules, Settings: s.Snapshot, Clock: clock()})
	if err != nil {
		t.Fatal(err)
	}
	s.Bind(co)
	t.Cleanup(co.Close)
	return s, co, posts
}

type settingReply struct {
	source  string
	reports []Report
	err     error
}

func writeAsync(s *settings.Service, key string, value any) <-chan settingReply {
	done := make(chan settingReply, 1)
	go func() {
		source, reports, err := s.Set(context.Background(), Caller{Kind: "cli"}, key, value)
		done <- settingReply{source, reports, err}
	}()
	return done
}
func TestSettingsSetTwoInOneTickWaitOnOneFlight(t *testing.T) {
	st := &writableStore{config: store.Doc{}}
	s, co, posts := writeCoordinator(t, st)
	events := make(chan Reason, 1)
	co.opts.Publisher = c.PublishFunc(func(name string, value any) {
		if name == "services.reloaded" {
			events <- value.(map[string]any)["reason"].(Reason)
		}
	})
	first := writeAsync(s, "theme.name", "evening")
	receiveBound(t, "first registration", posts)()
	second := writeAsync(s, "theme.allow_low_contrast", true)
	receiveBound(t, "second registration", posts)()
	if got := st.trace(); len(got) != 0 {
		t.Fatalf("captured before tick: %v", got)
	}
	co.Tick()
	for _, done := range []<-chan settingReply{first, second} {
		r := receiveBound(t, "setting reply", done)
		if r.err != nil || len(r.reports) != 5 || r.reports[1].Gen != 1 || r.source != "user" {
			t.Fatalf("%+v", r)
		}
	}
	if reason := receiveBound(t, "reload reason", events); reason != ReasonSettings {
		t.Fatalf("reason=%s", reason)
	}
	if len(st.trace()) != 1 || st.writes != 2 || s.Snapshot().String("theme.name") != "evening" || !s.Snapshot().Bool("theme.allow_low_contrast") {
		t.Fatalf("captures=%v writes=%d", st.trace(), st.writes)
	}
}
func TestSettingsSetReplyFlightCapturedAfterWrite(t *testing.T) {
	gate, release := releaseGate(t)
	defer release()
	entered := make(chan struct{})
	st := &writableStore{config: store.Doc{}, capture: entered, release: gate}
	s, co, posts := writeCoordinator(t, st)
	old := startReload(co)
	receiveBound(t, "old captured config", entered)
	done := writeAsync(s, "theme.name", "evening")
	receiveBound(t, "write registration", posts)()
	co.mu.Lock()
	queued := len(co.queue)
	co.mu.Unlock()
	if queued != 1 {
		t.Fatalf("queued=%d", queued)
	}
	release()
	assertApplied(t, receiveBound(t, "old flight", old))
	r := receiveBound(t, "fresh setting reply", done)
	if r.err != nil || len(r.reports) != 5 || r.reports[1].Gen != 2 || s.Snapshot().String("theme.name") != "evening" || len(st.trace()) != 2 {
		t.Fatalf("%+v trace=%v", r, st.trace())
	}
}
