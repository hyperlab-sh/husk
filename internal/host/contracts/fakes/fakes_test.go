package fakes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

var (
	_ contracts.Handler         = (*fakes.Handler)(nil)
	_ contracts.Notifier        = (*fakes.Notifier)(nil)
	_ contracts.StatePort       = (*fakes.State)(nil)
	_ contracts.CallsPort       = (*fakes.Calls)(nil)
	_ contracts.SessionPort     = (*fakes.Session)(nil)
	_ contracts.SettingsPort    = (*fakes.Settings)(nil)
	_ contracts.SecretsPort     = (*fakes.Secrets)(nil)
	_ contracts.ProfilePort     = (*fakes.Profile)(nil)
	_ contracts.ThemePort       = (*fakes.Theme)(nil)
	_ contracts.KeybindingPort  = (*fakes.Keybinding)(nil)
	_ contracts.PodRegistry     = (*fakes.Pods)(nil)
	_ contracts.CatalogDeclarer = (*fakes.Catalog)(nil)
	_ contracts.Reloader        = (*fakes.Reloader)(nil)
	_ contracts.SliceHandle     = (*fakes.Slice)(nil)
	_ contracts.SliceRegistrar  = (*fakes.Slices)(nil)
	_ contracts.WrapperStarter  = (*fakes.Wrapper)(nil)
	_ contracts.Clock           = (*fakes.Clock)(nil)
	_ contracts.Limiter         = (*fakes.Limiter)(nil)
	_ contracts.Host            = (*fakes.Host)(nil)
)

var ctx = context.Background()

func TestHandlerAndNotifier(t *testing.T) {
	h := &fakes.Handler{}
	reply := h.Serve(ctx, contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "a"}, protocol.Message{Method: "state.get"}, &fakes.Notifier{})
	if reply.Result == nil || len(h.Calls()) != 1 {
		t.Fatalf("reply %v calls %v", reply, h.Calls())
	}
	n := &fakes.Notifier{Err: errors.New("x")}
	if n.Notify(protocol.Message{Method: "state.changed"}) == nil || len(n.Calls()) != 1 {
		t.Fatal("notifier")
	}
}

func TestPortsRecordAndReturn(t *testing.T) {
	c := contracts.Caller{Kind: contracts.CallerCLI}
	boom := contracts.NewError(protocol.CodeDenied, "no", nil)

	st := &fakes.State{Revision: 7}
	if rev, err := st.Set(ctx, c, "a.state", nil); rev != 7 || err != nil {
		t.Fatal(rev, err)
	}
	st.Err = boom
	if err := st.Clear(ctx, c, "a.state"); contracts.CodeOf(err) != "denied" {
		t.Fatal(err)
	}
	if got := (&fakes.Calls{Result: []byte(`{}`)}); got.Calls() != nil {
		t.Fatal("fresh fake has calls")
	}
	if r, _ := (&fakes.Calls{Result: []byte(`1`)}).ActionInvoke(ctx, c, "x.y", nil); string(r) != "1" {
		t.Fatal(r)
	}
	if p, _ := (&fakes.Session{Result: protocol.SessionPingResult{ID: "i"}}).Ping(ctx, c); p.ID != "i" {
		t.Fatal(p)
	}
	if v, s, _ := (&fakes.Settings{Value: 3, Source: "user"}).Get(ctx, c, "k"); v != 3 || s != "user" {
		t.Fatal(v, s)
	}
	sec := &fakes.Secrets{Value: []byte("v")}
	if v, _ := sec.Reveal(ctx, c, "n"); string(v) != "v" || sec.Calls()[0] != "Reveal n" {
		t.Fatal(sec.Calls())
	}
	if l, _ := (&fakes.Profile{Profiles: []contracts.ProfileInfo{{Name: "p"}}}).List(ctx, c); len(l) != 1 {
		t.Fatal(l)
	}
	if p, _ := (&fakes.Theme{Palette: protocol.Palette{Name: "n"}}).Get(ctx, c); p.Name != "n" {
		t.Fatal(p)
	}
	if l, _ := (&fakes.Keybinding{Listing: contracts.KeyListing{Entries: make([]contracts.KeyEntry, 2)}}).List(ctx, c); len(l.Entries) != 2 {
		t.Fatal(l)
	}
}

func TestLaunchSeams(t *testing.T) {
	p := &fakes.Pods{}
	if err := p.Reserve("a"); err != nil {
		t.Fatal(err)
	}
	if err := p.Reserve("a"); contracts.CodeOf(err) != "conflict" {
		t.Fatalf("duplicate: %v", err)
	}
	if err := p.SetStatus("a", contracts.PodLive, ""); err != nil || p.Statuses["a"] != contracts.PodLive {
		t.Fatal(err)
	}
	if err := p.SetStatus("zz", contracts.PodLive, ""); contracts.CodeOf(err) != "not_found" {
		t.Fatal(err)
	}
	p.Release("a")

	s := &fakes.Slices{}
	h, err := s.RegisterSlice(state.Spec{Namespace: "a.state"})
	if err != nil {
		t.Fatal(err)
	}
	h.Unregister()
	if _, err := s.RegisterSlice(state.Spec{Namespace: "a.state"}); !errors.Is(err, state.ErrDuplicate) {
		t.Fatal(err)
	}
	if applied, _ := (&fakes.Reloader{Applied: true}).Reload(ctx); !applied {
		t.Fatal("reload")
	}
	if err := (&fakes.Catalog{}).Declare("a", nil, nil); err != nil {
		t.Fatal(err)
	}
	w := &fakes.Wrapper{}
	if err := w.Start("a", []protocol.Message{{}}); err != nil || w.Calls()[0] != "Start a 1" {
		t.Fatal(err, w.Calls())
	}
	hf := &fakes.Host{}
	if hf.Run(ctx) != nil || hf.Close() != nil {
		t.Fatal("host")
	}
}

func TestLimiterTwentyPerSecondPerKey(t *testing.T) {
	clk := fakes.NewClock(time.Unix(1000, 0))
	l := &fakes.Limiter{Clock: clk, Burst: contracts.RateLimitPerSecond}
	for i := 0; i < 20; i++ {
		if !l.Allow("a") {
			t.Fatalf("call %d refused", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("21st allowed")
	}
	if !l.Allow("b") {
		t.Fatal("keys must be independent")
	}
	clk.Advance(999 * time.Millisecond)
	if l.Allow("a") {
		t.Fatal("window not elapsed")
	}
	clk.Advance(time.Millisecond)
	if !l.Allow("a") {
		t.Fatal("new window")
	}
}
