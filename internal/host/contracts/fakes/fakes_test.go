package fakes_test

import (
	"context"
	"errors"
	"reflect"
	"runtime"
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
	_ contracts.ConfigPort      = (*fakes.Config)(nil)
	_ contracts.EventPort       = (*fakes.Events)(nil)
	_ contracts.HookPort        = (*fakes.Hooks)(nil)
	_ contracts.PluginPort      = (*fakes.Plugins)(nil)
	_ contracts.SurfacePort     = (*fakes.Surfaces)(nil)
)

var (
	ctx    = context.Background()
	plugin = contracts.Caller{Kind: contracts.CallerPlugin, PluginID: "a"}
	cli    = contracts.Caller{Kind: contracts.CallerCLI}
	denied = contracts.NewError(protocol.CodeDenied, "no", nil)
)

func wantMethods(t *testing.T, rec *fakes.Recorder, want ...string) {
	t.Helper()
	if got := rec.Methods(); !reflect.DeepEqual(got, want) {
		t.Fatalf("methods = %v, want %v", got, want)
	}
}

func TestRecorderOrderCallerAndReset(t *testing.T) {
	rec := fakes.NewRecorder()
	st := fakes.NewState(rec)
	pr := fakes.NewProfile(rec)
	st.Get(ctx, plugin, "s", "/p")
	pr.Use(ctx, cli, "work")
	calls := rec.Calls()
	if len(calls) != 2 || calls[0].Method != "State.Get" || calls[0].Kind != contracts.CallerPlugin ||
		calls[0].PluginID != "a" || calls[1].Kind != contracts.CallerCLI || calls[1].Args[0] != "work" {
		t.Fatalf("calls = %+v", calls)
	}
	rec.Reset()
	if len(rec.Calls()) != 0 {
		t.Fatal("reset")
	}
	if fakes.NewState(nil).Recorder() == nil {
		t.Fatal("nil recorder must be replaced")
	}
	// Zero values work and keep a private log.
	z := &fakes.State{}
	z.Get(ctx, cli, "s", "")
	if got := z.Methods(); len(got) != 1 || got[0] != "State.Get" {
		t.Fatalf("zero-value log = %v", got)
	}
	if len((&fakes.Pods{}).Methods()) != 0 {
		t.Fatal("fresh log not empty")
	}
	if err := (&fakes.Pods{}).Reserve(ctx, "z"); err != nil {
		t.Fatal(err)
	}
	if (&fakes.Limiter{Clock: fakes.NewClock(time.Unix(0, 0)), Burst: 1}).Allow("k") != true {
		t.Fatal("limiter")
	}
}

// TestLaunchOrderAcrossFakes is the sequence host-launch has to assert.
func TestLaunchOrderAcrossFakes(t *testing.T) {
	rec := fakes.NewRecorder()
	pods, cat, rl := fakes.NewPods(rec), fakes.NewCatalog(rec), fakes.NewReloader(rec)
	sl, wr := fakes.NewSlices(rec), fakes.NewWrapper(rec)
	pid, started := 42, time.Unix(5, 0)

	if err := pods.Reserve(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	_ = pods.SetStatus(ctx, "a", contracts.PodStatusUpdate{Status: contracts.PodStarting})
	_ = cat.Declare("a", nil, nil)
	rl.Reload(ctx)
	if _, err := sl.RegisterSlice(state.Spec{Namespace: "a.state"}); err != nil {
		t.Fatal(err)
	}
	_ = wr.Start("a", nil)
	_ = pods.SetStatus(ctx, "a", contracts.PodStatusUpdate{Status: contracts.PodLive, PID: &pid, StartedAt: &started})
	wantMethods(t, rec, "Pods.Reserve", "Pods.SetStatus", "Catalog.Declare", "Reloader.Reload", "Slices.RegisterSlice", "Wrapper.Start", "Pods.SetStatus")

	e, _ := pods.Entry("a")
	if e.Status != contracts.PodLive || *e.PID != 42 || !e.StartedAt.Equal(started) {
		t.Fatalf("merged entry = %+v", e)
	}
	if err := pods.Reserve(ctx, "a"); contracts.CodeOf(err) != "conflict" {
		t.Fatalf("duplicate: %v", err)
	}
	if err := pods.SetStatus(ctx, "zz", contracts.PodStatusUpdate{}); contracts.CodeOf(err) != "not_found" {
		t.Fatal(err)
	}
	if _, err := sl.RegisterSlice(state.Spec{Namespace: "a.state"}); !errors.Is(err, state.ErrDuplicate) {
		t.Fatal(err)
	}
	detail := ""
	_ = pods.SetStatus(ctx, "a", contracts.PodStatusUpdate{Detail: &detail})
	if e, _ = pods.Entry("a"); e.Status != contracts.PodLive || e.Detail == nil {
		t.Fatalf("unchanged fields must survive: %+v", e)
	}
	pods.Release(ctx, "a")
	if _, ok := pods.Entry("a"); ok {
		t.Fatal("release")
	}
}

func TestHandlerAndNotifier(t *testing.T) {
	rec := fakes.NewRecorder()
	h := fakes.NewHandler(rec)
	if reply := h.Serve(ctx, plugin, protocol.Message{Method: "state.get"}, fakes.NewNotifier(rec)); reply.Result == nil {
		t.Fatal(reply)
	}
	n := fakes.NewNotifier(rec)
	n.Err = errors.New("x")
	if n.Notify(protocol.Message{Method: "state.changed"}) == nil {
		t.Fatal("notify err")
	}
	wantMethods(t, rec, "Handler.Serve", "Notifier.Notify")
}

func TestPortsRecordCallerAndReturn(t *testing.T) {
	rec := fakes.NewRecorder()

	st := fakes.NewState(rec)
	st.Revision = 7
	if rev, err := st.Set(ctx, plugin, "a.state", nil); rev != 7 || err != nil {
		t.Fatal(rev, err)
	}
	st.Err = denied
	if contracts.CodeOf(st.Clear(ctx, plugin, "a.state", true)) != "denied" {
		t.Fatal("clear")
	}
	cl := fakes.NewCalls(rec)
	cl.Result = []byte(`1`)
	if r, _ := cl.ActionInvoke(ctx, cli, "x.y", nil); string(r) != "1" {
		t.Fatal(r)
	}
	se := fakes.NewSession(rec)
	se.Result.ID = "i"
	if p, _ := se.Ping(ctx, cli); p.ID != "i" {
		t.Fatal(p)
	}
	sg := fakes.NewSettings(rec)
	sg.Value, sg.Source = 3, "user"
	sg.Entries = []contracts.SettingEntry{{Key: "k"}}
	sg.Validation = contracts.ValidationResult{OK: true}
	if v, s, _ := sg.Get(ctx, plugin, "k"); v != 3 || s != "user" {
		t.Fatal(v, s)
	}
	if e, _ := sg.Schema(ctx, cli, "plugin:a"); len(e) != 1 {
		t.Fatal(e)
	}
	if v, _ := sg.Validate(ctx, cli); !v.OK {
		t.Fatal(v)
	}
	sc := fakes.NewSecrets(rec)
	sc.Value = []byte("v")
	if v, _ := sc.Reveal(ctx, cli, "n"); string(v) != "v" {
		t.Fatal(v)
	}
	pf := fakes.NewProfile(rec)
	pf.Profiles = []contracts.ProfileInfo{{Name: "p"}}
	if l, _ := pf.List(ctx, cli); len(l) != 1 {
		t.Fatal(l)
	}
	th := fakes.NewTheme(rec)
	th.Palette.Name = "n"
	if p, _ := th.Get(ctx, plugin); p.Name != "n" {
		t.Fatal(p)
	}
	kb := fakes.NewKeybinding(rec)
	kb.Listing.Entries = make([]contracts.KeyEntry, 2)
	if l, _ := kb.List(ctx, plugin); len(l.Entries) != 2 {
		t.Fatal(l)
	}
	cf := fakes.NewConfig(rec)
	cf.Reports = []contracts.ReloadReport{{Service: "theme", Applied: true}}
	if r, _ := cf.Reload(ctx, cli); len(r) != 1 {
		t.Fatal(r)
	}
	hk := fakes.NewHooks(rec)
	hk.Outcome = contracts.HookOutcome{Kind: "collect", Results: []contracts.HookResult{{Plugin: "p", Err: contracts.NewError(protocol.CodeTimeout, "t", nil)}}}
	if o, _ := hk.HookCall(ctx, plugin, "a.h", nil); contracts.CodeOf(o.Results[0].Err) != "timeout" {
		t.Fatal(o)
	}
	hk.Infos = []contracts.HookInfo{{ID: "a.h"}}
	if i, _ := hk.HookList(ctx, cli); i[0].ID != "a.h" {
		t.Fatal(i)
	}
	pl := fakes.NewPlugins(rec)
	pl.Infos, pl.Path = []contracts.PluginInfo{{ID: "a", Status: contracts.PodLive}}, "/l"
	if i, _ := pl.List(ctx, cli); i[0].Status != contracts.PodLive {
		t.Fatal(i)
	}
	if p, _ := pl.Log(ctx, cli, "a"); p != "/l" {
		t.Fatal(p)
	}
	sf := fakes.NewSurfaces(rec)
	_ = sf.SurfaceUpdate(ctx, plugin, protocol.SurfaceUpdateParams{Surface: protocol.SurfaceMainStage})
	_ = sf.SurfaceClose(ctx, plugin, protocol.SurfaceCloseParams{Surface: protocol.SurfaceMainStage})
	_ = sf.SlotContribute(ctx, plugin, protocol.SlotContributeParams{SlotID: "a.s"})
	sf.Err = denied
	if contracts.CodeOf(sf.ToastShow(ctx, plugin, protocol.ToastShowParams{Title: "t"})) != "denied" {
		t.Fatal("toast")
	}
	hf := fakes.NewHost(rec)
	if hf.Run(ctx) != nil || hf.Close() != nil {
		t.Fatal("host")
	}

	// Every port call was attributed; the callerless seams are not in this log.
	for _, c := range rec.Calls() {
		if c.Method != "Host.Run" && c.Method != "Host.Close" && c.Kind == "" {
			t.Errorf("%s recorded no caller", c.Method)
		}
	}
}

// TestWatchRegistersReturnsAndUnregisters pins the shared Watch lifecycle.
func TestWatchRegistersReturnsAndUnregisters(t *testing.T) {
	rec := fakes.NewRecorder()
	st, ev := fakes.NewState(rec), fakes.NewEvents(rec)
	st.Snap = contracts.StateSnapshotResult{Seq: 9}
	n := fakes.NewNotifier(rec)
	wctx, cancel := context.WithCancel(ctx)

	snap, err := st.Watch(wctx, cli, []string{"husk.focus"}, n)
	if err != nil || snap.Seq != 9 {
		t.Fatal(snap, err)
	}
	if err := ev.Watch(wctx, cli, "a.*", n); err != nil {
		t.Fatal(err)
	}
	if st.Watching() != 1 || ev.Watching() != 1 {
		t.Fatal("not registered after Watch returned")
	}
	if ev.Push(protocol.Message{Method: protocol.MethodEventDeliver}) != 1 {
		t.Fatal("push")
	}
	cancel()
	// AfterFunc runs on its own goroutine; poll without sleeping.
	for i := 0; i < 1e6 && (st.Watching() != 0 || ev.Watching() != 0); i++ {
		runtime.Gosched()
	}
	if st.Watching() != 0 || ev.Watching() != 0 {
		t.Fatal("not unregistered after ctx end")
	}
	ev.Err = denied
	if contracts.CodeOf(ev.Watch(ctx, cli, "x", n)) != "denied" || ev.Watching() != 0 {
		t.Fatal("refused watch must register nothing")
	}
	if contracts.CodeOf(ev.Emit(ctx, plugin, "b.x", nil)) != "denied" {
		t.Fatal("emit")
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
