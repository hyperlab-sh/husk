package host

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/services"
	sc "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/services/secrets"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
	"github.com/zalando/go-keyring"
)

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) add(level, msg string) {
	l.mu.Lock()
	l.lines = append(l.lines, level+" "+msg)
	l.mu.Unlock()
}
func (l *logSink) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// memRing is an in-memory OS keyring so tests never touch the real one.
type memRing struct {
	mu sync.Mutex
	m  map[string]string
}

func (r *memRing) Get(service, account string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.m[service+"/"+account]; ok {
		return v, nil
	}
	return "", keyring.ErrNotFound
}

func (r *memRing) Set(service, account, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[service+"/"+account] = value
	return nil
}

func (r *memRing) Delete(service, account string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, service+"/"+account)
	return nil
}

// failureOf returns "code/reason" of a Services failure; it matches codes,
// never messages.
func failureOf(t *testing.T, err error) string {
	t.Helper()
	var f *sc.Error
	if !errors.As(err, &f) {
		t.Fatalf("not a services failure: %v", err)
	}
	return f.Code + "/" + f.Reason
}

const secretPurpose = "PURPOSE-must-stay-out-of-logs"

// bootWithSecret runs a host whose catalog declares alpha's secret.
func bootWithSecret(t *testing.T, logs *logSink, opts ...Option) *harness {
	t.Helper()
	hook, ready := readyHook("")
	h, err := boot(t, nil, append(opts, withHook(hook), WithLogger(logs.add), func(o *options) { o.keyring = &memRing{m: map[string]string{}} })...)
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	h.root.store.Declare(store.DeclaredPlugins, "alpha", map[string]any{
		"id": "alpha", "name": "Alpha",
		"secret_requests": []any{map[string]any{"name": "tok.en", "purpose": secretPurpose, "required": true}},
	})
	if _, err := h.root.rt.Coordinator.Reload(t.Context(), services.ReasonPlugin); err != nil {
		t.Fatal(err)
	}
	return h
}

// The CLI reveals through the one audited path: a log line names the secret
// and never the value.
func TestCLIRevealIsLogged(t *testing.T) {
	logs := &logSink{}
	h := bootWithSecret(t, logs)
	if code, _, stderr := cliRun(h, "rpc", "secrets.set", `{"name":"tok.en","value":"s3cr3t-value"}`, "--format", "json"); code != 0 {
		t.Fatalf("set: %d %s", code, stderr)
	}
	for _, method := range []string{"secrets.get", "secrets.reveal"} {
		code, stdout, stderr := cliRun(h, "rpc", method, `{"name":"tok.en"}`, "--format", "json")
		if code != 0 || !strings.Contains(stdout, "s3cr3t-value") {
			t.Fatalf("%s: %d %q %s", method, code, stdout, stderr)
		}
	}
	got := logs.all()
	if n := strings.Count(got, "secrets: tok.en revealed to cli"); n != 2 {
		t.Fatalf("reveal log lines = %d in:\n%s", n, got)
	}
	if strings.Contains(got, "s3cr3t-value") {
		t.Fatalf("value in log:\n%s", got)
	}
}

// The dialog-shown line names the plugin id and the secret, never the purpose.
func TestDialogShownLogNamesNoPurpose(t *testing.T) {
	logs := &logSink{}
	seen := make(chan secrets.Prompt, 1)
	h := bootWithSecret(t, logs, WithPrompt(func(_ context.Context, p secrets.Prompt) secrets.Answer {
		seen <- p
		return secrets.Declined
	}))
	_, err := h.root.rt.Secrets.Read(t.Context(), secrets.Caller{Kind: protocol.CallerPlugin, PluginID: "alpha"}, "tok.en")
	if got := failureOf(t, err); got != "denied/user_denied" {
		t.Fatalf("declined read = %q", got)
	}
	if p := <-seen; p.Plugin != "alpha" || p.PluginName != "Alpha" || p.Purpose != secretPurpose || !p.Required {
		t.Fatalf("prompt = %+v", p)
	}
	got := logs.all()
	if !strings.Contains(got, "secrets: dialog shown to grant tok.en to alpha") || strings.Contains(got, secretPurpose) {
		t.Fatalf("log:\n%s", got)
	}
}

// A host without a dialog answers user_denied at once.
func TestNoPromptWiredDeniesUngrantedRead(t *testing.T) {
	logs := &logSink{}
	h := bootWithSecret(t, logs)
	_, err := h.root.rt.Secrets.Read(t.Context(), secrets.Caller{Kind: protocol.CallerPlugin, PluginID: "alpha"}, "tok.en")
	if got := failureOf(t, err); got != "denied/user_denied" {
		t.Fatalf("err = %q", got)
	}
}

// The sentinel value travels set, a declined prompt, a CLI grant, a plugin
// read and a reveal through the real host; it leaves only in the authorized
// replies, never in the log or the grants slice.
func TestValueStaysOutOfLogAndGrantsSlice(t *testing.T) {
	const value = "SENTINEL-host-v4lue-do-not-leak"
	logs := &logSink{}
	answers := make(chan secrets.Answer, 1)
	h := bootWithSecret(t, logs, WithPrompt(func(_ context.Context, _ secrets.Prompt) secrets.Answer { return <-answers }))
	plugin := secrets.Caller{Kind: protocol.CallerPlugin, PluginID: "alpha"}
	if code, _, stderr := cliRun(h, "rpc", "secrets.set", `{"name":"tok.en","value":"`+value+`"}`, "--format", "json"); code != 0 {
		t.Fatalf("set: %d %s", code, stderr)
	}
	answers <- secrets.Declined
	if _, err := h.root.rt.Secrets.Read(t.Context(), plugin, "tok.en"); failureOf(t, err) != "denied/user_denied" {
		t.Fatalf("declined read = %v", err)
	}
	if code, _, stderr := cliRun(h, "rpc", "secrets.grant", `{"plugin":"alpha","name":"tok.en"}`, "--format", "json"); code != 0 {
		t.Fatalf("grant: %d %s", code, stderr)
	}
	got, err := h.root.rt.Secrets.Read(t.Context(), plugin, "tok.en")
	if err != nil || got != value {
		t.Fatalf("granted read = %q, %v", got, err)
	}
	code, revealed, stderr := cliRun(h, "rpc", "secrets.reveal", `{"name":"tok.en"}`, "--format", "json")
	if code != 0 || !strings.Contains(revealed, value) {
		t.Fatalf("reveal: %d %q %s", code, revealed, stderr)
	}
	code, grants, stderr := cliRun(h, "state", "get", "husk.grants", "--format", "json")
	if code != 0 || !strings.Contains(grants, "tok.en") {
		t.Fatalf("state get: %d %q %s", code, grants, stderr)
	}
	if strings.Contains(grants, value) || strings.Contains(logs.all(), value) {
		t.Fatalf("value leaked:\nlog:\n%s\ngrants: %s", logs.all(), grants)
	}
}
