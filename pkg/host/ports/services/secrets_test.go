package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	host "github.com/hyperlab-sh/husk/pkg/host/contracts"
	services "github.com/hyperlab-sh/husk/pkg/services"
	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/services/secrets"
	"github.com/hyperlab-sh/husk/pkg/substrate/state"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

// newSecrets wires a real secrets service with an in-memory crypto doc, a
// prompt seam and loop posts, declaring one git.token request for "files".
func newSecrets(t *testing.T) (Secrets, *secrets.Service, chan func(), chan secrets.Answer, *fakeClock) {
	port, svc, posts, answers, clock, _ := newSecretsWithCrypto(t)
	return port, svc, posts, answers, clock
}

// newSecretsWithCrypto also hands back the store crypto so tests can inject
// read errors or plant stored bytes directly. The dialog blocks on answers
// until its ctx is cancelled, like the real one.
func newSecretsWithCrypto(t *testing.T) (Secrets, *secrets.Service, chan func(), chan secrets.Answer, *fakeClock, *memoryCrypto) {
	t.Helper()
	posts := make(chan func(), 8)
	answers := make(chan secrets.Answer, 1)
	clock := &fakeClock{}
	crypto := &memoryCrypto{data: map[string][]byte{}}
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
		Crypto: crypto,
		Post:   func(f func()) bool { posts <- f; return true },
		Prompt: func(ctx context.Context, _ secrets.Prompt) secrets.Answer {
			select {
			case a := <-answers:
				return a
			case <-ctx.Done():
				return secrets.Dismissed
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	applyTo(t, svc, in)
	return Secrets{Svc: svc}, svc, posts, answers, clock, crypto
}

type getResult struct {
	value string
	err   error
}

// getAsync runs a port read on its own goroutine: a plugin's ungranted read
// blocks until the dialog is answered.
func getAsync(port Secrets, caller host.Caller, name string) <-chan getResult {
	out := make(chan getResult, 1)
	go func() {
		v, err := port.Get(context.Background(), caller, name)
		out <- getResult{v, err}
	}()
	return out
}

func TestSecretsUserLifecycleAndViews(t *testing.T) {
	port, _, posts, _, _ := newSecrets(t)
	ctx := context.Background()

	if err := port.Set(ctx, userCaller, "git.token", "tok"); err != nil {
		t.Fatalf("set: %v", err)
	}
	value, err := port.Reveal(ctx, userCaller, "git.token")
	if err != nil || value != "tok" {
		t.Fatalf("reveal = %q err=%v", value, err)
	}
	names, err := port.Names(ctx, userCaller)
	yes := true
	want := host.SecretNames{Storage: "ok", Secrets: []host.SecretInfo{{Name: "git.token", Declared: []string{"files"}, Granted: []string{}, Set: &yes, Required: true}}}
	if err != nil || !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %+v err=%v, want %+v", names, err, want)
	}

	// Grant and revoke return once the posted write ran.
	granted := make(chan error, 1)
	go func() { granted <- port.Grant(ctx, userCaller, "files", "git.token") }()
	runPosted(t, posts)
	if err := <-granted; err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Plugin-mutating methods are user-only.
	for _, op := range []struct {
		name string
		call func() error
	}{
		{"set", func() error { return port.Set(ctx, filesCaller, "git.token", "x") }},
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
}

func TestSecretsPluginReadBlocksOnTheDialog(t *testing.T) {
	port, _, posts, answers, _ := newSecrets(t)
	ctx := context.Background()
	if err := port.Set(ctx, userCaller, "git.token", "tok"); err != nil {
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

	// A declared, ungranted read waits for the answer; accept writes the
	// grant on the loop and the same call returns the value.
	read := getAsync(port, filesCaller, "git.token")
	select {
	case r := <-read:
		t.Fatalf("ungranted read returned before the dialog was answered: %+v", r)
	default:
	}
	answers <- secrets.Accepted
	runPosted(t, posts)
	if r := <-read; r.err != nil || r.value != "tok" {
		t.Fatalf("accepted read = %q err=%v", r.value, r.err)
	}
	// The grant is recorded: the next read does not ask.
	if v, err := port.Get(ctx, filesCaller, "git.token"); err != nil || v != "tok" {
		t.Fatalf("granted read = %q err=%v", v, err)
	}
}

func TestSecretsDeclinedPromptSticksUntilGranted(t *testing.T) {
	port, _, posts, answers, _ := newSecrets(t)
	ctx := context.Background()
	if err := port.Set(ctx, userCaller, "git.token", "tok"); err != nil {
		t.Fatal(err)
	}
	read := getAsync(port, filesCaller, "git.token")
	answers <- secrets.Declined
	if r := <-read; r.err == nil {
		t.Fatal("declined read allowed")
	} else if code, reason := codedOf(t, r.err); code != "denied" || reason != "user_denied" {
		t.Fatalf("declined read: %s/%s", code, reason)
	}
	// The decline is remembered: the next read fails without a dialog (no
	// answer is queued, so a dialog would block here).
	if _, err := port.Get(ctx, filesCaller, "git.token"); err == nil {
		t.Fatal("declined grant forgotten")
	} else if code, reason := codedOf(t, err); code != "denied" || reason != "user_denied" {
		t.Fatalf("post-decline read: %s/%s", code, reason)
	}
	// A CLI grant clears the memory.
	granted := make(chan error, 1)
	go func() { granted <- port.Grant(ctx, userCaller, "files", "git.token") }()
	runPosted(t, posts)
	if err := <-granted; err != nil {
		t.Fatal(err)
	}
	if v, err := port.Get(ctx, filesCaller, "git.token"); err != nil || v != "tok" {
		t.Fatalf("read after grant = %q err=%v", v, err)
	}
}

func TestSecretsUnsetValueIsNotLeaked(t *testing.T) {
	port, _, _, _, _ := newSecrets(t)
	ctx := context.Background()
	if _, err := port.Get(ctx, userCaller, "git.token"); err == nil {
		t.Fatal("unset read allowed")
	} else if code, reason := codedOf(t, err); code != "not_found" || reason != "unset" {
		t.Fatalf("unset read: %s/%s", code, reason)
	}
}

func TestCodedDeadlineAndMutation(t *testing.T) {
	// A raw deadline outside the input methods is the caller's context
	// expiring: plain timeout, no input_deadline reason.
	err := coded(context.DeadlineExceeded)
	if code, reason := codedOf(t, err); code != "timeout" || reason != "" {
		t.Fatalf("plain deadline: %s/%s, want timeout/\"\"", code, reason)
	}
	// The input methods (settings.validate, theme.check) spell the input
	// load's deadline as input_deadline.
	err = codedInput(context.DeadlineExceeded)
	if code, reason := codedOf(t, err); code != "timeout" || reason != "input_deadline" {
		t.Fatalf("input deadline: %s/%s, want timeout/input_deadline", code, reason)
	}
	// coded never mutates the services error it inspects: an empty Message
	// is filled into the reply, not written back into serr.
	serr := &c.Error{Code: "denied", Reason: "user_only"}
	if err := coded(serr); err.Error() != "denied: user_only" {
		t.Fatalf("filled message = %q", err.Error())
	}
	if serr.Message != "" {
		t.Fatalf("services error mutated: Message = %q", serr.Message)
	}
	if err := coded(serr); err.Error() != "denied: user_only" {
		t.Fatalf("second conversion = %q", err.Error())
	}
}

func TestSecretValueNeverAppearsInErrorStrings(t *testing.T) {
	const value = "hunter2-super-secret"
	port, _, posts, answers, _, crypto := newSecretsWithCrypto(t)
	ctx := context.Background()
	if err := port.Set(ctx, userCaller, "git.token", value); err != nil {
		t.Fatal(err)
	}
	// Grant through the prompt flow: the read waits, the answer posts the
	// grant to the loop and the same call returns the value.
	read := getAsync(port, filesCaller, "git.token")
	answers <- secrets.Accepted
	runPosted(t, posts)

	// The granted read returns the value, so the scan below is not vacuous.
	if r := <-read; r.err != nil {
		t.Fatalf("granted read failed: %v", r.err)
	} else if r.value != value {
		t.Fatalf("granted read = %q", r.value)
	}

	// Paths where the value (or its bytes) is actually in scope when the
	// error is built — the round-1 rows only ever failed before the read.
	inScope := []error{
		// Crypto read error whose own text contains the value: services must
		// answer unavailable and never pass the store error through.
		mustErr(t, func() error {
			crypto.getErr = errors.New("store get failed for " + value)
			defer func() { crypto.getErr = nil }()
			_, err := port.Reveal(ctx, userCaller, "git.token")
			return err
		}),
		// Stored bytes past the 64 KiB read cap: the oversize branch must
		// answer unavailable without quoting what it refused.
		mustErr(t, func() error {
			saved := crypto.data["git.token"]
			oversize := bytes.Repeat([]byte("x"), 64*1024+1)
			copy(oversize, value)
			crypto.data["git.token"] = oversize
			defer func() { crypto.data["git.token"] = saved }()
			_, err := port.Reveal(ctx, userCaller, "git.token")
			return err
		}),
	}
	for i, err := range inScope {
		if code, _ := codedOf(t, err); code != "unavailable" {
			t.Fatalf("in-scope error %d: code = %s, want unavailable", i, code)
		}
	}

	// Pre-read denials kept from round 1: they fail before the value is
	// touched, and must stay value-free too.
	errs := append(inScope,
		mustErr(t, func() error { _, err := port.Reveal(ctx, filesCaller, "git.token"); return err }),
		mustErr(t, func() error { _, err := port.Get(ctx, filesCaller, "nope"); return err }),
		mustErr(t, func() error {
			_, err := port.Get(ctx, host.Caller{Kind: host.CallerPlugin, PluginID: "other"}, "git.token")
			return err
		}),
		mustErr(t, func() error { return port.Set(ctx, filesCaller, "git.token", "x") }),
		mustErr(t, func() error { return port.Grant(ctx, filesCaller, "files", "git.token") }),
	)
	for i, err := range errs {
		text := err.Error()
		if de, ok := err.(host.DataError); ok {
			if raw, marshalErr := json.Marshal(de.Data()); marshalErr == nil {
				text += " " + string(raw)
			}
		}
		if strings.Contains(text, value) {
			t.Fatalf("error %d leaks the secret value: %s", i, text)
		}
	}
}

func mustErr(t *testing.T, f func() error) error {
	t.Helper()
	err := f()
	if err == nil {
		t.Fatal("call unexpectedly succeeded")
	}
	return err
}

func runPosted(t *testing.T, posts <-chan func()) {
	t.Helper()
	// Blocking receive, like the services tests: the answer guarantees a
	// post is coming; no timer races.
	(<-posts)()
}
