package services

import (
	"context"
	"errors"
	"testing"

	services "github.com/hyperlab-sh/husk/pkg/services"
	"github.com/hyperlab-sh/husk/pkg/services/keybinding"
	"github.com/hyperlab-sh/husk/pkg/services/profile"
	"github.com/hyperlab-sh/husk/pkg/services/secrets"
	"github.com/hyperlab-sh/husk/pkg/services/settings"
	"github.com/hyperlab-sh/husk/pkg/services/theme"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func reloadPort(t *testing.T, reader *fakeStoreReader) Config {
	t.Helper()
	tree := state.New(state.Options{})
	post := func(f func()) bool { f(); return true }
	settingsSvc := newSet(settings.Options{})
	profSvc, err := profile.New(profile.Options{Store: reader, State: tree, Post: post})
	if err != nil {
		t.Fatal(err)
	}
	secSvc, err := secrets.New(secrets.Options{State: tree, Clock: &fakeClock{}, Crypto: &memoryCrypto{data: map[string][]byte{}}, Post: post})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secSvc.Close)
	co, err := services.NewCoordinator(services.Options{
		Store: reader,
		Services: map[string]services.Reloadable{
			"profile": profSvc, "settings": settingsSvc, "theme": theme.New(), "keybinding": keybinding.New(), "secrets": secSvc,
		},
		State: tree, Settings: settingsSvc.Snapshot, Clock: &fakeClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(co.Close)
	return Config{Co: co}
}

// The port copies Report.Changed: the first reload that applies a palette
// override says theme changed, the second identical one says nothing did.
func TestConfigReloadCopiesChanged(t *testing.T) {
	reader := &fakeStoreReader{resolved: store.Resolved{
		Doc:     doc(t, `{"theme":{"palette":{"canvas":"#101010"}}}`),
		Origins: map[string]store.Layer{"theme.palette.canvas": store.LayerUser},
	}}
	port := reloadPort(t, reader)
	changed := func() map[string]bool {
		reports, err := port.Reload(context.Background(), userCaller)
		if err != nil || len(reports) != 5 {
			t.Fatalf("reload = %+v err=%v", reports, err)
		}
		out := map[string]bool{}
		for _, r := range reports {
			if !r.Applied {
				t.Fatalf("%s not applied: %+v", r.Service, r.Problems)
			}
			out[r.Service] = r.Changed
		}
		return out
	}
	first := changed()
	if !first["theme"] || !first["settings"] {
		t.Fatalf("first reload changed = %v, want theme and settings", first)
	}
	for svc, ch := range changed() {
		if ch {
			t.Fatalf("second identical reload: %s changed", svc)
		}
	}
}

// A reload whose configuration cannot be loaded answers unavailable with
// reason store_error.
func TestConfigReloadStoreFailureIsCoded(t *testing.T) {
	port := reloadPort(t, &fakeStoreReader{err: errors.New("disk on fire")})
	_, err := port.Reload(context.Background(), userCaller)
	if err == nil {
		t.Fatal("reload succeeded without a store")
	}
	if code, reason := codedOf(t, err); code != "unavailable" || reason != "store_error" {
		t.Fatalf("reload error = %s/%s, want unavailable/store_error", code, reason)
	}
}
