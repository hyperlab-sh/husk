package services

import (
	"context"
	"testing"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	services "github.com/hyperlab-sh/husk/internal/services"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/services/profile"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

func TestThemeGetOpenToPluginsCheckUserOnly(t *testing.T) {
	svc := theme.New()
	port := Theme{Svc: svc, Inputs: staticInputs{}}

	for _, c := range []host.Caller{userCaller, filesCaller, stemHost} {
		pal, err := port.Get(context.Background(), c)
		if err != nil || pal.Name == "" {
			t.Fatalf("get(%s) = %+v err=%v", string(c.Kind), pal, err)
		}
	}
	if _, err := port.Check(context.Background(), filesCaller); err == nil {
		t.Fatal("plugin theme.check allowed")
	} else if code, reason := codedOf(t, err); code != "denied" || reason != "user_only" {
		t.Fatalf("plugin check: %s/%s", code, reason)
	}
	// A dry check with good input applies nothing and reports no problems;
	// theme reads overrides from the Settings snapshot the coordinator wires.
	settingsSvc := newSet(settings.Options{})
	applyTo(t, settingsSvc, services.ReloadInput{Doc: store.Resolved{Doc: doc(t, `{}`), Origins: map[string]store.Layer{}}})
	in := services.ReloadInput{Doc: store.Resolved{Doc: doc(t, `{}`), Origins: map[string]store.Layer{}}, Settings: settingsSvc.Snapshot()}
	port.Inputs = staticInputs{in: in}
	port.Settings = settingsSvc
	res, err := port.Check(context.Background(), userCaller)
	if err != nil || !res.OK || len(res.Problems) != 0 {
		t.Fatalf("check = %+v err=%v", res, err)
	}
	if got := svc.Palette().Name; got == "" {
		t.Fatal("check disturbed the published palette")
	}
}

func TestProfileListAndUseUserOnly(t *testing.T) {
	tree := state.New(state.Options{})
	posts := make(chan func(), 4)
	svc, err := profile.New(profile.Options{
		Store: &fakeStoreReader{profiles: []string{"work", "personal"}},
		State: tree,
		Post:  func(f func()) bool { posts <- f; return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	applyTo(t, svc, services.ReloadInput{Doc: store.Resolved{Doc: doc(t, `{}`), Origins: map[string]store.Layer{}}})
	port := Profile{Svc: svc}
	ctx := context.Background()

	list, err := port.List(ctx, userCaller)
	if err != nil || len(list) != 2 || list[0].Name != "personal" || list[1].Name != "work" {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if _, err := port.List(ctx, filesCaller); err == nil {
		t.Fatal("plugin profile.list allowed")
	} else if code, reason := codedOf(t, err); code != "denied" || reason != "user_only" {
		t.Fatalf("plugin list: %s/%s", code, reason)
	}

	if _, err := port.Use(ctx, filesCaller, "work"); err == nil {
		t.Fatal("plugin profile.use allowed")
	}
	if _, err := port.Use(ctx, userCaller, "nope"); err == nil {
		t.Fatal("unknown profile accepted")
	} else if code, reason := codedOf(t, err); code != "not_found" || reason != "unknown_profile" {
		t.Fatalf("unknown profile: %s/%s", code, reason)
	}
	// The State mutation is loop-posted and the reply waits for the flight it
	// caused: run the post, then the fake flight answers the registration.
	svc.Bind(fakeReloader{reports: []c.Report{{Service: "profile", Gen: 7, Applied: true}}})
	type outcome struct {
		res host.ProfileUseResult
		err error
	}
	done := make(chan outcome, 1)
	go func() { res, err := port.Use(ctx, userCaller, "work"); done <- outcome{res, err} }()
	runPosted(t, posts)
	got := <-done
	if got.err != nil || got.res.Active != svc.Active() || len(got.res.Reports) != 1 || got.res.Reports[0].Gen != 7 {
		t.Fatalf("use = %+v err=%v", got.res, got.err)
	}
}

// A flight error reaches the wire code unchanged.
func TestProfileUseFlightErrorKeepsItsCode(t *testing.T) {
	posts := make(chan func(), 4)
	svc, err := profile.New(profile.Options{Store: &fakeStoreReader{profiles: []string{"work"}}, State: state.New(state.Options{}), Post: func(f func()) bool { posts <- f; return true }})
	if err != nil {
		t.Fatal(err)
	}
	applyTo(t, svc, services.ReloadInput{Doc: store.Resolved{Doc: doc(t, `{}`), Origins: map[string]store.Layer{}}})
	svc.Bind(errReloader{err: c.Failure("unavailable", "flight_boom")})
	done := make(chan error, 1)
	go func() { _, err := (Profile{Svc: svc}).Use(context.Background(), userCaller, "work"); done <- err }()
	runPosted(t, posts)
	if code, reason := codedOf(t, <-done); code != "unavailable" || reason != "flight_boom" {
		t.Fatalf("flight error = %s/%s", code, reason)
	}
}

type errReloader struct{ err error }

func (f errReloader) Settled(string) bool                { return false }
func (f errReloader) AwaitProfile(string) profile.Waiter { return f }
func (f errReloader) LastReports() []c.Report            { return nil }
func (f errReloader) Wait(context.Context) ([]c.Report, error) {
	return nil, f.err
}

// fakeReloader answers every profile.use registration with canned reports.
type fakeReloader struct{ reports []c.Report }

func (f fakeReloader) Settled(string) bool                { return false }
func (f fakeReloader) AwaitProfile(string) profile.Waiter { return f }
func (f fakeReloader) LastReports() []c.Report            { return f.reports }
func (f fakeReloader) Wait(context.Context) ([]c.Report, error) {
	return f.reports, nil
}

func TestKeysListScopesThePluginView(t *testing.T) {
	svc := keybinding.New()
	in := services.ReloadInput{
		Doc:    store.Resolved{Doc: doc(t, `{}`), Origins: map[string]store.Layer{}},
		Layers: []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(t, `{"version":1,"bind":[{"key":"g","action":"husk.home"},{"key":"t","action":"files.tree","plugin":"files","focus":"stage"}]}`)}},
		Declared: map[store.DocName]map[string]store.Doc{
			store.DeclaredPlugins: {"files": doc(t, `{"id":"files","actions":[{"id":"files.tree"}],"surfaces":[{"id":"stage"}]}`)},
		},
	}
	applyTo(t, svc, in)
	port := Keys{Svc: svc}
	ctx := context.Background()

	full, err := port.List(ctx, userCaller)
	if err != nil || len(full.Entries) == 0 || len(full.Problems) != 0 {
		t.Fatalf("user list = %d entries, %d problems, err=%v", len(full.Entries), len(full.Problems), err)
	}
	var ownAction, hostAction bool
	for _, e := range full.Entries {
		if e.Action == "files.tree" && e.Plugin == "files" {
			ownAction = true
		}
		if e.Action == "husk.home" {
			hostAction = true
		}
	}
	if !ownAction || !hostAction {
		t.Fatalf("user listing misses entries: own=%v host=%v", ownAction, hostAction)
	}
	// A plugin sees only its own entries; the Stem engine sees everything.
	own, err := port.List(ctx, filesCaller)
	if err != nil || len(own.Entries) != 1 || own.Entries[0].Action != "files.tree" || own.Entries[0].Plugin != "files" {
		t.Fatalf("plugin list = %+v err=%v", own, err)
	}
	stem, err := port.List(ctx, stemHost)
	if err != nil || len(stem.Entries) != len(full.Entries) {
		t.Fatalf("stem list = %d, want %d", len(stem.Entries), len(full.Entries))
	}
	// The copy is structural: mutating it never touches the service.
	full.Entries[0].Action = "mutated"
	again, _ := port.List(ctx, userCaller)
	if again.Entries[0].Action == "mutated" {
		t.Fatal("List shared state with its caller")
	}
}

// TestConfigReloadRunsTheCoordinator proves config.reload drives one real
// coordinator reload across all five services and refuses non-user callers.
func TestConfigReloadRunsTheCoordinator(t *testing.T) {
	tree := state.New(state.Options{})
	posts := make(chan func(), 8)
	settingsSvc := newSet(settings.Options{})
	themeSvc := theme.New()
	kb := keybinding.New()
	profSvc, err := profile.New(profile.Options{Store: &fakeStoreReader{profiles: []string{"work"}}, State: tree, Post: func(f func()) bool { posts <- f; return true }})
	if err != nil {
		t.Fatal(err)
	}
	secSvc, err := secrets.New(secrets.Options{State: tree, Clock: &fakeClock{}, Crypto: &memoryCrypto{data: map[string][]byte{}}, Post: func(f func()) bool { posts <- f; return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer secSvc.Close()
	storeReader := &fakeStoreReader{resolved: store.Resolved{Doc: doc(t, `{"theme":{"name":"CharmTone"}}`), Origins: map[string]store.Layer{}}}
	co, err := services.NewCoordinator(services.Options{
		Store: storeReader,
		Services: map[string]services.Reloadable{
			"profile": profSvc, "settings": settingsSvc, "theme": themeSvc, "keybinding": kb, "secrets": secSvc,
		},
		State:    tree,
		Settings: settingsSvc.Snapshot,
		Clock:    &fakeClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer co.Close()

	port := Config{Co: co}
	ctx := context.Background()
	reports, err := port.Reload(ctx, filesCaller)
	if err == nil {
		t.Fatal("plugin config.reload allowed")
	} else if code, reason := codedOf(t, err); code != "denied" || reason != "user_only" {
		t.Fatalf("plugin reload: %s/%s", code, reason)
	}
	reports, err = port.Reload(ctx, userCaller)
	if err != nil || len(reports) != 5 {
		t.Fatalf("reload = %+v err=%v", reports, err)
	}
	applied := 0
	for _, r := range reports {
		if r.Applied {
			applied++
		}
	}
	if applied == 0 {
		t.Fatalf("no service applied: %+v", reports)
	}
	if gen := settingsSvc.Generation(); gen == 0 {
		t.Fatal("settings generation did not advance")
	}
}

// The port copies KeyContext.Capture into the service's context: over a real
// service with a global q binding, a capturing context no longer resolves it.
func TestKeysResolveHonoursCapture(t *testing.T) {
	svc := keybinding.New()
	applyTo(t, svc, services.ReloadInput{
		Doc:    store.Resolved{Doc: doc(t, `{}`), Origins: map[string]store.Layer{}},
		Layers: []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(t, `{"version":1,"bind":[{"key":"q","action":"husk.quit"}]}`)}},
		Declared: map[store.DocName]map[string]store.Doc{
			store.DeclaredPlugins: {"files": doc(t, `{"id":"files","surfaces":[{"id":"stage"}]}`)},
		},
	})
	port := Keys{Svc: svc}
	ctx := host.KeyContext{FocusSurface: "main-stage"}
	if r := port.Resolve(ctx, []string{"q"}); r.Outcome != host.KeyMatch || r.Binding == nil || r.Binding.Action != "husk.quit" {
		t.Fatalf("free q = %+v, want match husk.quit", r)
	}
	ctx.Capture = true
	if r := port.Resolve(ctx, []string{"q"}); r.Outcome != host.KeyNoMatch {
		t.Fatalf("capturing q = %+v, want no match", r)
	}
}
