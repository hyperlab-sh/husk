package services

import (
	"context"
	"testing"

	host "github.com/hyperlab-sh/husk/internal/host/contracts"
	services "github.com/hyperlab-sh/husk/internal/services"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// newSecrets wires a real secrets service with an in-memory crypto doc, a
// prompt seam and loop posts, declaring one git.token request for "files".
func newSecrets(t *testing.T) (Secrets, *secrets.Service, chan func(), chan bool, *fakeClock) {
	t.Helper()
	posts := make(chan func(), 8)
	answers := make(chan bool, 1)
	clock := &fakeClock{}
	in := services.ReloadInput{
		Doc: store.Resolved{Doc: doc(t, `{}`), Origins: map[string]store.Layer{}},
		Declared: map[store.DocName]map[string]store.Doc{
			store.DeclaredPlugins: {
				"files": doc(t, `{"id":"files","name":"Files","secret_requests":[{"name":"git.token","purpose":"Push","required":true}]}`),
			},
		},
	}
	svc, err := secrets.New(secrets.Options{
		State:  state.New(state.Options{}),
		Clock:  clock,
		Crypto: &memoryCrypto{data: map[string][]byte{}},
		Post:   func(f func()) bool { posts <- f; return true },
		Prompt: func(secrets.Prompt) <-chan bool { return answers },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	applyTo(t, svc, in)
	return Secrets{Svc: svc}, svc, posts, answers, clock
}

func TestSecretsUserLifecycleAndViews(t *testing.T) {
	port, svc, _, _, _ := newSecrets(t)
	ctx := context.Background()

	if err := port.Set(ctx, userCaller, "git.token", []byte("tok")); err != nil {
		t.Fatalf("set: %v", err)
	}
	value, err := port.Reveal(ctx, userCaller, "git.token")
	if err != nil || string(value) != "tok" {
		t.Fatalf("reveal = %q err=%v", value, err)
	}
	names, err := port.Names(ctx, userCaller)
	if err != nil || len(names) != 1 || names[0].Name != "git.token" || !names[0].Set || len(names[0].Declared) != 1 || names[0].Declared[0] != "files" {
		t.Fatalf("names = %+v err=%v", names, err)
	}

	// Plugin-mutating methods are user-only.
	for _, op := range []struct {
		name string
		call func() error
	}{
		{"set", func() error { return port.Set(ctx, filesCaller, "git.token", []byte("x")) }},
		{"grant", func() error { return port.Grant(ctx, filesCaller, "files", "git.token") }},
		{"revoke", func() error { return port.Revoke(ctx, filesCaller, "files", "git.token") }},
		{"reveal", func() error { _, err := port.Reveal(ctx, filesCaller, "git.token"); return err }},
	} {
		if err := op.call(); err == nil {
			t.Fatalf("plugin %s allowed", op.name)
		} else if code, reason := codedOf(t, err); code != "denied" || reason != "user_only" {
			t.Fatalf("plugin %s: %s/%s", op.name, code, reason)
		}
	}
	_ = svc
}

func TestSecretsPluginReadRequiresDeclarationAndGrant(t *testing.T) {
	port, _, posts, answers, _ := newSecrets(t)
	ctx := context.Background()
	if err := port.Set(ctx, userCaller, "git.token", []byte("tok")); err != nil {
		t.Fatal(err)
	}

	// Undeclared name: narrowed to not_found, whatever the caller.
	other := host.Caller{Kind: host.CallerPlugin, PluginID: "other"}
	if _, err := port.Get(ctx, other, "git.token"); err == nil {
		t.Fatal("undeclared plugin read allowed")
	} else if code, reason := codedOf(t, err); code != "not_found" || reason != "not_declared" {
		t.Fatalf("undeclared read: %s/%s", code, reason)
	}
	if _, err := port.Get(ctx, filesCaller, "never.declared"); err == nil {
		t.Fatal("undeclared name allowed")
	} else if code, _ := codedOf(t, err); code != "not_found" {
		t.Fatalf("undeclared name: %s", code)
	}

	// First declared read: no grant yet, so the service prompts and the
	// call returns denied/needs_grant.
	if _, err := port.Get(ctx, filesCaller, "git.token"); err == nil {
		t.Fatal("ungranted read allowed")
	} else if code, reason := codedOf(t, err); code != "denied" || reason != "needs_grant" {
		t.Fatalf("ungranted read: %s/%s", code, reason)
	}
	answers <- true
	runPosted(t, posts)

	// Second read: grant recorded, value delivered.
	value, err := port.Get(ctx, filesCaller, "git.token")
	if err != nil || string(value) != "tok" {
		t.Fatalf("granted read = %q err=%v", value, err)
	}
}

func TestSecretsDeniedPromptSticksUntilRevoked(t *testing.T) {
	port, _, _, answers, clock := newSecrets(t)
	ctx := context.Background()
	if err := port.Set(ctx, userCaller, "git.token", []byte("tok")); err != nil {
		t.Fatal(err)
	}
	if _, err := port.Get(ctx, filesCaller, "git.token"); err == nil {
		t.Fatal("ungranted read allowed")
	}
	answers <- false
	// The deny path posts nothing; the prompt goroutine's deferred Stop
	// runs after its pending-marker write, so it is the sync point.
	<-clock.timers[0].stopped
	// The denial is remembered: the next read fails without a new prompt.
	if _, err := port.Get(ctx, filesCaller, "git.token"); err == nil {
		t.Fatal("denied grant forgotten")
	} else if code, reason := codedOf(t, err); code != "denied" || reason != "user_denied" {
		t.Fatalf("post-denial read: %s/%s", code, reason)
	}
}

func TestSecretsUnsetValueIsNotLeaked(t *testing.T) {
	port, _, _, _, _ := newSecrets(t)
	ctx := context.Background()
	if _, err := port.Get(ctx, userCaller, "git.token"); err == nil {
		t.Fatal("unset read allowed")
	} else if code, reason := codedOf(t, err); code != "not_found" || reason != "secret_unset" {
		t.Fatalf("unset read: %s/%s", code, reason)
	}
}

func runPosted(t *testing.T, posts <-chan func()) {
	t.Helper()
	// Blocking receive, like the services tests: the answer guarantees a
	// post is coming; no timer races.
	(<-posts)()
}
