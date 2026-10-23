package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/services/profile"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	stheme "github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestReviewEmptyCallerDeniedOnEveryUserOnlyMethod(t *testing.T) {
	tree := state.New(state.Options{})
	post := func(func()) bool { return true }
	p, _ := profile.New(profile.Options{Store: &fakeStore{}, State: tree, Post: post})
	sec, _ := secrets.New(secrets.Options{State: tree, Post: post, Clock: clock(), Crypto: &memoryCrypto{data: map[string][]byte{}}})
	defer sec.Close()
	set := settings.New(settings.Options{})
	theme := stheme.New()
	co, _ := NewCoordinator(Options{Store: &fakeStore{}, Services: fakeServices(), Clock: clock()})
	defer co.Close()
	ctx := context.Background()
	zero := Caller{}
	cases := map[string]func() error{
		"profile.list": func() error { _, e := p.ListFor(zero); return e }, "profile.use": func() error { return p.Use(zero, "") },
		"settings.list": func() error { _, e := set.List(zero); return e }, "settings.schema": func() error { _, e := set.SchemaFor(zero, ""); return e }, "settings.validate": func() error { _, e := set.Validate(ctx, zero, input(`{}`)); return e }, "settings.preview": func() error { _, _, e := set.Preview(ctx, zero, input(`{}`)); return e },
		"theme.check":   func() error { _, e := theme.Check(ctx, zero, input(`{}`)); return e },
		"config.reload": func() error { _, e := co.ReloadFor(ctx, zero); return e }, "config.inputs": func() error { _, e := co.Inputs(ctx, zero); return e },
		"secrets.grant": func() error { return sec.Grant(zero, "a", "x") }, "secrets.revoke": func() error { return sec.Revoke(zero, "a", "x") }, "secrets.set": func() error { return sec.Set(zero, "x", nil) }, "secrets.reveal": func() error { _, e := sec.Reveal(zero, "x"); return e }, "secrets.grants": func() error { _, e := sec.Grants(zero); return e },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			var failure *c.Error
			if err := call(); !errors.As(err, &failure) || failure.Code != "denied" {
				t.Fatalf("empty Caller allowed: %v", err)
			}
		})
	}
	if _, _, err := set.Get(zero, "husk.input.hold_alt"); err == nil {
		t.Fatal("empty caller read host settings")
	}
	if _, ok := set.SnapshotFor(zero).Raw("husk.input.hold_alt"); ok {
		t.Fatal("empty caller got unfiltered snapshot")
	}
	if _, err := sec.Read(zero, "x"); err == nil {
		t.Fatal("empty caller read secrets")
	}
	for _, kind := range []protocol.CallerKind{"cli", "host", "input"} {
		if !c.IsUser(Caller{Kind: kind}) {
			t.Fatal("explicit user kind refused", kind)
		}
	}
}

func TestReviewKeyListingExactCatalogOwnership(t *testing.T) {
	kb := keybinding.New()
	in := catalog("a")
	in.Declared[store.DeclaredPlugins]["a.b"] = catalog("a.b").Declared[store.DeclaredPlugins]["a.b"]
	in.Declared[store.DeclaredKeymap] = map[string]store.Doc{"a": doc(`{"version":1,"bind":[{"key":"x","focus":"stage","action":"a.run"}]}`), "a.b": doc(`{"version":1,"bind":[{"key":"b","focus":"stage","action":"a.b.run"}]}`)}
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1,"bind":[{"key":"1","action":"a.run"},{"key":"2","action":"a.b.run"}]}`)}}
	apply(t, kb, in)
	// Ownership and scope were enforced by manifest conversion; the service
	// trusts the declared document and sets the plugin from its key.
	for _, problem := range kb.Listing().Problems {
		if problem.Code == "foreign_surface" || problem.Code == "foreign_action" || problem.Code == "global_not_allowed" {
			t.Fatal("service re-checked conversion's rules", problem)
		}
	}
	for _, id := range []string{"a", "a.b"} {
		view, _ := kb.ListFor(Caller{Kind: "plugin", PluginID: id})
		if len(view.Entries) != 2 {
			t.Fatalf("wrong listing for %s: %+v", id, view.Entries)
		}
		for _, e := range view.Entries {
			if e.Action != id+".run" {
				t.Fatal("foreign action exposed", id, e.Action)
			}
		}
		for _, problem := range view.Problems {
			if problem.Source.Plugin != id {
				t.Fatal("foreign problem exposed")
			}
		}
	}
	if full, _ := kb.ListFor(Caller{Kind: "cli"}); len(full.Entries) <= 2 {
		t.Fatal("trusted listing filtered")
	}
	if none, err := kb.ListFor(Caller{}); err == nil || len(none.Entries) != 0 {
		t.Fatal("empty caller listing")
	}
}

func TestReviewSecretSettingsChecksAreExactAndOwnerScoped(t *testing.T) {
	set := settings.New(settings.Options{})
	in := catalog("a")
	in.Declared[store.DeclaredPlugins]["a"]["secret_requests"] = []any{map[string]any{"name": "name", "purpose": "Own secret"}, map[string]any{"name": "default"}, map[string]any{"name": "paths"}, map[string]any{"name": "enabled"}}
	in.Declared[store.DeclaredPlugins]["a"]["settings_declared"] = nil
	in.Declared[store.DeclaredPlugins]["b"] = catalog("b").Declared[store.DeclaredPlugins]["b"]
	in.Doc.Doc = doc(`{"theme":{"name":"Allowed"},"profile":{"default":"work"},"husk":{"plugins":{"paths":["example"]}},"plugins":{"b":{"enabled":true}}}`)
	apply(t, set, in)
	if set.Snapshot().String("theme.name") != "Allowed" || !set.Snapshot().Bool("plugins.b.enabled") {
		t.Fatal("foreign secret broke host or another owner")
	}
	in.Declared[store.DeclaredPlugins]["a"]["settings_declared"] = []any{map[string]any{"key": "plugins.a.name", "type": "string", "default": ""}}
	if candidate, report, err := set.Rebuild(context.Background(), in); err == nil || candidate != nil || !hasMessage(report, "secrets are not settings") {
		t.Fatal("own exact secret/settings collision was accepted", report, err)
	}
	in.Declared[store.DeclaredPlugins]["a"]["secret_requests"] = []any{map[string]any{"name": "git.token"}}
	in.Declared[store.DeclaredPlugins]["a"]["settings_declared"] = []any{map[string]any{"key": "plugins.a.token", "type": "string", "default": ""}}
	apply(t, set, in) // no suffix match: git.token is not the whole key token
}
func hasMessage(r Report, part string) bool {
	for _, p := range r.Problems {
		if strings.Contains(p.Message, part) {
			return true
		}
	}
	return false
}

func TestReviewProfileKeymapCacheDoesNotCrossProfiles(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	work := "work"
	in.Profile = &work
	in.Layers = []store.LayerDoc{{Layer: store.LayerProfile, Doc: doc(`{"version":1,"bind":[{"key":"g","action":"husk.home"}]}`)}}
	apply(t, kb, in)
	if kb.Resolve(keybinding.KeyContext{}, []string{"g"}).Outcome != keybinding.Match {
		t.Fatal("seed profile missing")
	}
	other := "other"
	in.Profile = &other
	in.Layers[0].Doc = doc(`{"version":2,"bind":[]}`)
	apply(t, kb, in)
	if kb.Resolve(keybinding.KeyContext{}, []string{"g"}).Outcome != keybinding.NoMatch {
		t.Fatal("reused previous profile keymap")
	}
}

func TestReviewDisabledChordFallsThroughAndUserDisableWins(t *testing.T) {
	kb := keybinding.New()
	in := catalog("a")
	in.Declared[store.DeclaredKeymap] = map[string]store.Doc{"a": doc(`{"version":1,"bind":[{"key":"r","focus":"stage","action":"a.run"}]}`)}
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1,"bind":[{"keys":["a","b"],"disable":true},{"key":"r","focus":"stage","disable":true}]}`)}}
	apply(t, kb, in)
	for _, keys := range [][]string{{"a"}, {"a", "b"}} {
		if kb.Resolve(keybinding.KeyContext{}, keys).Outcome != keybinding.NoMatch {
			t.Fatal("disabled chord became a prefix", keys)
		}
	}
	if kb.Resolve(keybinding.KeyContext{FocusSurface: "stage", FocusOwner: "a"}, []string{"r"}).Outcome != keybinding.NoMatch {
		t.Fatal("user disable lost to focus-local key")
	}
}

func TestReviewRevealAuditAndEveryReadOutcome(t *testing.T) {
	tree := state.New(state.Options{})
	clk := clock()
	posts := make(chan func(), 4)
	events := []map[string]string{}
	audits := []string{}
	backend := &memoryCrypto{data: map[string][]byte{"git.token": []byte("TOP_SECRET_VALUE")}}
	sec, _ := secrets.New(secrets.Options{State: tree, Clock: clk, Crypto: backend, Post: func(f func()) bool { posts <- f; return true }, Publisher: c.PublishFunc(func(name string, data any) {
		if name != "secrets.read" {
			t.Error(name)
		}
		events = append(events, data.(map[string]string))
	}), Audit: func(name string) { audits = append(audits, name) }})
	defer sec.Close()
	apply(t, sec, catalog("a"))
	plugin := Caller{Kind: "plugin", PluginID: "a"}
	user := Caller{Kind: "cli"}
	sec.Read(plugin, "other")
	sec.Read(plugin, "git.token")
	timer := <-clk.created
	clk.advance(time.Minute)
	<-timer.stopped
	sec.Read(plugin, "git.token")
	if err := sec.Grant(user, "a", "git.token"); err != nil {
		t.Fatal(err)
	}
	(<-posts)()
	sec.Read(plugin, "git.token")
	value, err := sec.Reveal(user, "git.token")
	if err != nil || string(value) != "TOP_SECRET_VALUE" || len(audits) != 1 || audits[0] != "git.token" {
		t.Fatal("reveal/audit failed", err)
	}
	sec.Read(user, "unset")
	backend.err = errors.New("TOP_SECRET_VALUE")
	sec.Read(user, "git.token")
	outcomes := map[string]bool{}
	for _, e := range events {
		if len(e) != 3 || e["name"] == "TOP_SECRET_VALUE" || e["outcome"] == "TOP_SECRET_VALUE" {
			t.Fatal("invalid read event", e)
		}
		outcomes[e["outcome"]] = true
	}
	for _, want := range []string{"not_declared", "needs_grant", "user_denied", "allowed", "not_found", "unavailable"} {
		if !outcomes[want] {
			t.Fatal("missing outcome", want, outcomes)
		}
	}
}

func TestReviewSchemaForExposureAndLocalKeyBoundaries(t *testing.T) {
	in := catalog("a")
	set := settings.New(settings.Options{Schema: []settings.Entry{{Key: "dist.key", Type: "bool", Default: false, Owner: "dist", Apply: "live"}}})
	apply(t, set, in)
	schema, err := set.SchemaFor(Caller{Kind: "cli"}, "plugin:a")
	if err != nil || len(schema) != 1 || schema[0].Key != "plugins.a.enabled" {
		t.Fatal(schema, err)
	}
	plugin := Caller{Kind: "plugin", PluginID: "a"}
	for _, key := range []string{"theme.name", "profile.default", "dist.key"} {
		if _, _, err := set.Get(plugin, key); err == nil {
			t.Fatal("plugin saw nonpublic setting", key)
		}
		if _, ok := set.SnapshotFor(plugin).Raw(key); ok {
			t.Fatal("filtered snapshot leaked key", key)
		}
	}
	for _, key := range []string{"husk.injected", "plugins.other.key"} {
		bad := catalog("a")
		bad.Declared[store.DeclaredPlugins]["a"]["settings_declared"] = []any{map[string]any{"key": key, "type": "bool", "default": false}}
		if _, _, err := set.Rebuild(context.Background(), bad); err == nil {
			t.Fatal("foreign schema accepted", key)
		}
	}
	for _, local := range []string{"", "Upper", "with.dot", "with space", "with/slash", "ümlaut", "a", "0", "_", "-", "snake_key", "with-hyphen"} {
		t.Run(local, func(t *testing.T) {
			fixture := catalog("a")
			fixture.Declared[store.DeclaredPlugins]["a"]["settings_declared"] = []any{map[string]any{"key": "plugins.a." + local, "type": "bool", "default": false}}
			_, _, err := set.Rebuild(context.Background(), fixture)
			valid := local == "a" || local == "0" || local == "_" || local == "-" || local == "snake_key" || local == "with-hyphen"
			if (err == nil) != valid {
				t.Fatal("local boundary", local, err)
			}
		})
	}
}

// A plugin-supplied name that is not a well-formed secret name never reaches
// the secrets.read event; the read is reported as not declared.
func TestSecretsReadEventOmitsInvalidRequestedName(t *testing.T) {
	var events []map[string]string
	sec, _ := secrets.New(secrets.Options{State: state.New(state.Options{}), Clock: clock(), Crypto: &memoryCrypto{data: map[string][]byte{}}, Post: func(f func()) bool { return true }, Publisher: c.PublishFunc(func(_ string, data any) {
		events = append(events, data.(map[string]string))
	})})
	defer sec.Close()
	apply(t, sec, catalog("a"))
	for _, name := range []string{"Bad Name\x1b[31m", "", strings.Repeat("x", 65), "UPPER"} {
		if _, err := sec.Read(Caller{Kind: "plugin", PluginID: "a"}, name); err == nil {
			t.Fatalf("read of %q succeeded", name)
		}
	}
	if len(events) != 4 {
		t.Fatalf("events = %v", events)
	}
	for _, e := range events {
		if e["name"] != "" || e["outcome"] != "not_declared" || e["plugin_id"] != "a" {
			t.Errorf("event %v carries the requested name or the wrong outcome", e)
		}
	}
}
