package services

import (
	"context"
	"errors"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/services/keybinding"
	"github.com/hyperlab-sh/husk/pkg/services/profile"
	"github.com/hyperlab-sh/husk/pkg/services/secrets"
	"github.com/hyperlab-sh/husk/pkg/services/settings"
	stheme "github.com/hyperlab-sh/husk/pkg/services/theme"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
)

// reason returns the Failure code and reason of err, or "" when err is nil.
func reason(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var f *c.Error
	if !errors.As(err, &f) {
		t.Fatalf("not a services failure: %v", err)
	}
	return f.Code + "/" + f.Reason
}

// The host and input kinds are trusted to read and reload but never handle a
// secret value: every secrets method but names answers denied/user_only.
func TestSecretsMethodsByCallerKind(t *testing.T) {
	cli := Caller{Kind: protocol.CallerCLI}
	host := Caller{Kind: protocol.CallerHost}
	input := Caller{Kind: protocol.CallerInput}
	plugin := Caller{Kind: protocol.CallerPlugin, PluginID: "a"}
	methods := map[string]func(*secrets.Service, Caller) error{
		"get": func(s *secrets.Service, k Caller) error {
			_, e := s.Read(context.Background(), k, "git.token")
			return e
		},
		"reveal": func(s *secrets.Service, k Caller) error {
			_, e := s.Reveal(context.Background(), k, "git.token")
			return e
		},
		"set":    func(s *secrets.Service, k Caller) error { return s.Set(k, "git.token", "v") },
		"grant":  func(s *secrets.Service, k Caller) error { return s.Grant(context.Background(), k, "a", "git.token") },
		"revoke": func(s *secrets.Service, k Caller) error { return s.Revoke(context.Background(), k, "a", "git.token") },
		"names":  func(s *secrets.Service, k Caller) error { _, e := s.Names(k); return e },
	}
	// want[method][kind] is the failure code/reason, "" for allowed.
	want := map[string]map[string]string{
		"get":    {"cli": "", "host": "denied/user_only", "input": "denied/user_only"},
		"reveal": {"cli": "", "host": "denied/user_only", "input": "denied/user_only", "plugin": "denied/user_only"},
		"set":    {"cli": "", "host": "denied/user_only", "input": "denied/user_only", "plugin": "denied/user_only"},
		"grant":  {"cli": "", "host": "denied/user_only", "input": "denied/user_only", "plugin": "denied/user_only"},
		"revoke": {"cli": "", "host": "denied/user_only", "input": "denied/user_only", "plugin": "denied/user_only"},
		"names":  {"cli": "", "host": "", "input": "", "plugin": ""},
	}
	kinds := map[string]Caller{"cli": cli, "host": host, "input": input, "plugin": plugin}
	for method, call := range methods {
		for kind, caller := range kinds {
			t.Run(method+"/"+kind, func(t *testing.T) {
				tree := state.New(state.Options{})
				s, _ := secrets.New(secrets.Options{State: tree, Post: func(f func()) bool { f(); return true }, Clock: clock(), Crypto: &memoryCrypto{data: map[string][]byte{"git.token": []byte("v")}}})
				defer s.Close()
				apply(t, s, catalog("a"))
				got := reason(t, call(s, caller))
				if w, ok := want[method][kind]; ok && got != w {
					t.Fatalf("%s as %s = %q, want %q", method, kind, got, w)
				}
				if kind == "plugin" && method == "get" && got == "denied/user_only" {
					t.Fatal("a declared plugin read must follow the grant rules, not user_only")
				}
			})
		}
	}
}

// A host caller sees every secret name, like the user.
func TestSecretNamesHostSeesEveryName(t *testing.T) {
	s, _ := secrets.New(secrets.Options{State: state.New(state.Options{}), Post: func(f func()) bool { f(); return true }, Clock: clock(), Crypto: &memoryCrypto{data: map[string][]byte{"stored.only": []byte("v")}}})
	defer s.Close()
	apply(t, s, catalog("a"))
	for _, kind := range []protocol.CallerKind{protocol.CallerCLI, protocol.CallerHost, protocol.CallerInput} {
		infos, err := s.Names(Caller{Kind: kind})
		if err != nil || len(infos.Secrets) != 2 {
			t.Fatalf("%s names = %+v, %v", kind, infos, err)
		}
	}
}

// A zero, unknown or malformed caller is denied/invalid_caller on every
// method, including the read paths that return no error today.
func TestInvalidCallersDeniedEverywhere(t *testing.T) {
	tree := state.New(state.Options{})
	post := func(f func()) bool { f(); return true }
	p, _ := profile.New(profile.Options{Store: &fakeStore{}, State: tree, Post: post})
	sec, _ := secrets.New(secrets.Options{State: tree, Post: post, Clock: clock(), Crypto: &memoryCrypto{data: map[string][]byte{}}})
	defer sec.Close()
	set := newSet(settings.Options{})
	th := stheme.New()
	kb := keybinding.New()
	co, _ := NewCoordinator(Options{Store: &fakeStore{}, Services: fakeServices(), Clock: clock()})
	defer co.Close()
	ctx := context.Background()
	bad := map[string]Caller{
		"zero":             {},
		"unknown kind":     {Kind: "bogus"},
		"plugin no id":     {Kind: protocol.CallerPlugin},
		"cli with id":      {Kind: protocol.CallerCLI, PluginID: "a"},
		"host with id":     {Kind: protocol.CallerHost, PluginID: "a"},
		"input with id":    {Kind: protocol.CallerInput, PluginID: "a"},
		"upper-case kind":  {Kind: "CLI"},
		"padded plugin id": {Kind: "plugin ", PluginID: "a"},
	}
	for name, k := range bad {
		calls := map[string]func() error{
			"profile.list":      func() error { _, e := p.ListFor(k); return e },
			"profile.use":       func() error { _, _, e := p.Use(ctx, k, ""); return e },
			"settings.get":      func() error { _, _, e := set.Get(k, "husk.x"); return e },
			"settings.list":     func() error { _, e := set.List(k); return e },
			"settings.schema":   func() error { _, e := set.SchemaFor(k, ""); return e },
			"theme.check":       func() error { _, e := th.Check(ctx, k, input(`{}`)); return e },
			"config.reload":     func() error { _, e := co.ReloadFor(ctx, k); return e },
			"secrets.get":       func() error { _, e := sec.Read(context.Background(), k, "x.y"); return e },
			"secrets.names":     func() error { _, e := sec.Names(k); return e },
			"secrets.reveal":    func() error { _, e := sec.Reveal(context.Background(), k, "x.y"); return e },
			"secrets.set":       func() error { return sec.Set(k, "x.y", "") },
			"secrets.grant":     func() error { return sec.Grant(context.Background(), k, "a", "x.y") },
			"secrets.revoke":    func() error { return sec.Revoke(context.Background(), k, "a", "x.y") },
			"keys.list":         func() error { _, e := kb.ListFor(k); return e },
			"theme.get":         func() error { _, _, e := th.Get(k); return e },
			"settings.validate": func() error { _, e := set.Validate(ctx, k, input(`{}`)); return e },
		}
		for method, call := range calls {
			t.Run(name+"/"+method, func(t *testing.T) {
				if got := reason(t, call()); got != "denied/invalid_caller" {
					t.Fatalf("got %q, want denied/invalid_caller", got)
				}
			})
		}
	}
}

// The user-only helpers separate the kinds exactly as the matrix does.
func TestCallerHelpers(t *testing.T) {
	for kind, want := range map[protocol.CallerKind]struct{ user, cli bool }{
		protocol.CallerCLI: {true, true}, protocol.CallerHost: {true, false}, protocol.CallerInput: {true, false},
		protocol.CallerPlugin: {false, false}, "": {false, false}, "bogus": {false, false},
	} {
		caller := Caller{Kind: kind}
		if kind == protocol.CallerPlugin {
			caller.PluginID = "a"
		}
		if got := c.IsUser(caller); got != want.user {
			t.Errorf("IsUser(%q) = %v", kind, got)
		}
		if got := c.RequireCLI(caller) == nil; got != want.cli {
			t.Errorf("RequireCLI(%q) = %v", kind, got)
		}
	}
}

// SnapshotFor never serves a plugin's view to an invalid caller, even one
// that carries a plugin id.
func TestSnapshotForInvalidCallerIsEmpty(t *testing.T) {
	s := newSet(settings.Options{Schema: []settings.Entry{{Key: "dist.theme", Type: "string", Default: "Public", Owner: "dist", Apply: "live", Public: true}}})
	apply(t, s, catalog("a"))
	for name, k := range map[string]Caller{"bogus with id": {Kind: "bogus", PluginID: "a"}, "cli with id": {Kind: protocol.CallerCLI, PluginID: "a"}, "zero": {}} {
		view := s.SnapshotFor(k)
		if _, ok := view.Raw("plugins.a.enabled"); ok || view.String("dist.theme") != "" {
			t.Errorf("%s got a filtered view", name)
		}
	}
	if _, ok := s.SnapshotFor(Caller{Kind: protocol.CallerPlugin, PluginID: "a"}).Raw("plugins.a.enabled"); !ok {
		t.Fatal("valid plugin lost its own key")
	}
}

// A plugin caller carrying a foreign id never sees another plugin's keys.list
// entries through an invalid kind.
func TestKeysListInvalidKindWithIDGetsNoEntries(t *testing.T) {
	kb := keybinding.New()
	apply(t, kb, catalog("a"))
	listing, err := kb.ListFor(Caller{Kind: "bogus", PluginID: "a"})
	if reason(t, err) != "denied/invalid_caller" || len(listing.Entries) != 0 || len(listing.Problems) != 0 {
		t.Fatalf("listing=%+v err=%v", listing, err)
	}
}
