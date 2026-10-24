package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/pkg/cli"
	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

func syncFixture(t *testing.T, root, id string, valid bool) string {
	t.Helper()
	dir := filepath.Join(root, strings.ReplaceAll(id, ":", "-"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("id=%q\nname='Fixture'\nversion='1.0.0'\nmin_herdr_version='0.9.0'\n[[actions]]\nid='go'\ntitle='Go'\ncommand=['true']\n", id)
	if !valid {
		data += "[[actions]]\nid='go'\ntitle='Duplicate'\ncommand=['true']\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "herdr-plugin.toml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSyncLiveRegistryAndDoctor(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	root := t.TempDir()
	for key, part := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_DATA_HOME": "storage"} {
		t.Setenv(key, filepath.Join(root, part))
	}
	exe, _ := os.Executable()
	env := captureEnvironment(exe, runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err := os.MkdirAll(filepath.Dir(env.registryPath), 0700); err != nil {
		t.Fatal(err)
	}
	writeRegistry := func(records any) {
		t.Helper()
		raw, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(env.registryPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeRegistry([]any{})
	// Doctor and launched shims use only the schema-backed fixture upstream.
	if err := os.MkdirAll(filepath.Dir(env.configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.configPath, []byte(fmt.Sprintf("herdr_bin=%q\n", exe)), 0600); err != nil {
		t.Fatal(err)
	}
	sockets, err := os.MkdirTemp("/tmp", "hs-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	fakeUpstream(t, filepath.Join(sockets, "u.sock"), fakeherdr.Options{})
	env = guardUpstream(t, env)
	runtimeDir := filepath.Join(sockets, "runtime")
	if err := os.Mkdir(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	messages := make(chan any, 4096)
	stop, done, ready := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cfg := contracts.Config{InstanceID: "sync-unit", RuntimeDir: runtimeDir, Post: func(m any) bool {
		select {
		case messages <- m:
			return true
		case <-stop:
			return false
		}
	}}
	h, err := host.New(cfg, host.WithConverters([]string{exe}))
	if err != nil {
		t.Fatal(err)
	}
	r := h.(*host.Root)
	go func() {
		defer close(done)
		first := true
		for {
			select {
			case m := <-messages:
				r.Update(m)
				if first {
					close(ready)
					first = false
				}
			case <-stop:
				return
			}
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	exit := make(chan error, 1)
	go func() { exit <- r.Run(ctx) }()
	t.Cleanup(func() {
		close(stop)
		<-done
		_ = r.Close()
		cancel()
		if err := <-exit; err != nil {
			t.Error(err)
		}
	})
	select {
	case <-ready:
	case <-t.Context().Done():
		t.Fatal("host not ready")
	}
	// The injected runner executes the actual CLI against the real host socket.
	env.runCLI = func(args []string) ([]byte, []byte, int) {
		var out, diagnostic bytes.Buffer
		code := cli.Main(t.Context(), args, cli.Env{Stdout: &out, Stderr: &diagnostic, Stdin: strings.NewReader(""), Getenv: func(string) string { return "" }, RuntimeDir: func() (string, bool, error) { return runtimeDir, true, nil }})
		return out.Bytes(), diagnostic.Bytes(), code
	}
	dir := syncFixture(t, root, "fresh:plugin", true)
	disabled := syncFixture(t, root, "disabled", true)
	writeRegistry([]map[string]any{{"plugin_id": "fresh:plugin", "plugin_root": dir, "enabled": true}, {"plugin_id": "disabled", "plugin_root": disabled, "enabled": false}})
	run := func(args ...string) (int, string, string) {
		var out, diagnostic bytes.Buffer
		code := execute(args, &out, &diagnostic, env)
		return code, out.String(), diagnostic.String()
	}
	if code, out, diagnostic := run("doctor", "--instance", "sync-unit"); code != 0 || !strings.Contains(out, "plugin fresh:plugin absent from instance; run husk-herdr sync") || strings.Contains(out, "plugin disabled absent") {
		t.Fatalf("before: %d %s %s", code, out, diagnostic)
	}
	if code, out, diagnostic := run("sync", "--instance", "sync-unit"); code != 0 || out != "added fresh-plugin\n" {
		t.Fatalf("sync: %d %s %s", code, out, diagnostic)
	}
	raw, diagnostic, code := env.huskCLI([]string{"action", "list", "--format", "json", "--instance", "sync-unit"})
	if code != 0 || !strings.Contains(string(raw), "fresh-plugin.go") || strings.Contains(string(raw), "disabled.go") {
		t.Fatalf("actions: %d %s %s", code, raw, diagnostic)
	}
	if code, out, diagnostic := run("sync"); code != 0 || out != "present fresh-plugin\n" {
		t.Fatalf("again: %d %s %s", code, out, diagnostic)
	}
	if code, out, diagnostic := run("doctor"); code != 0 || strings.Contains(out, "absent from instance") {
		t.Fatalf("after: %d %s %s", code, out, diagnostic)
	}
	bad := syncFixture(t, root, "invalid", false)
	writeRegistry([]map[string]any{{"plugin_id": "invalid", "plugin_root": bad, "enabled": true}})
	if code, out, diagnostic := run("sync"); code != 1 || !strings.HasPrefix(out, "refused ") || !strings.Contains(out, "duplicate_action") {
		t.Fatalf("refusal: %d %s %s", code, out, diagnostic)
	}
	// Sync never removes the plugin omitted from the new registry.
	if present, problem, code := env.instancePlugins(nil); code != 0 || present["fresh-plugin"].ID == "" {
		t.Fatalf("removed existing plugin: %v %s %d", present, problem, code)
	}
	if code, _, _ := run("sync", "--instance", "missing"); code != 3 {
		t.Fatalf("explicit instance exit=%d", code)
	}
}

func TestSyncCLIOutcomesAndTargeting(t *testing.T) {
	sockets, err := os.MkdirTemp("/tmp", "ho-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	fakeUpstream(t, filepath.Join(sockets, "u.sock"), fakeherdr.Options{})
	root := t.TempDir()
	dir := syncFixture(t, root, "example", true)
	registryPath := filepath.Join(root, "plugins.json")
	raw, _ := json.Marshal([]map[string]any{{"plugin_id": "example", "plugin_root": dir, "enabled": true}})
	if err := os.WriteFile(registryPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, reply, diagnostic string
		exit, want              int
		text                    string
	}{
		{"live", `{"added":[{"plugin":"example","status":"live"}]}`, "", 0, 0, "added example"},
		{"failed", `{"added":[{"plugin":"example","status":"failed"}]}`, "failed launch", 1, 1, "refused example: failed launch: exec denied"},
		{"removed", `{"added":[{"plugin":"example","status":"removed"}]}`, "", 0, 1, "refused example"},
		{"transport", ``, "connection lost", 5, 5, "refused example: connection lost"},
		{"malformed", `not json`, "", 0, 1, "refused example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			env := guardUpstream(t, environment{executable: "/adapter", platform: runtime.GOOS, registryPath: registryPath})
			env.runCLI = func(args []string) ([]byte, []byte, int) {
				calls++
				if !reflect.DeepEqual(args[len(args)-2:], []string{"--instance", "named"}) {
					t.Fatalf("target lost: %v", args)
				}
				if args[1] == "list" {
					reply := `[]`
					if calls > 1 && tc.name == "failed" {
						reply = `[{"id":"example","last_error":"exec denied"}]`
					}
					return []byte(reply), nil, 0
				}
				if !reflect.DeepEqual(args, []string{"plugin", "add", dir, "--format", "json", "--instance", "named"}) {
					t.Fatalf("not path add: %v", args)
				}
				return []byte(tc.reply), []byte(tc.diagnostic), tc.exit
			}
			var out, diagnostic bytes.Buffer
			if code := execute([]string{"sync", "--instance", "named"}, &out, &diagnostic, env); code != tc.want || !strings.Contains(out.String(), tc.text) {
				t.Fatalf("code=%d out=%s diagnostic=%s", code, &out, &diagnostic)
			}
		})
	}
	for _, args := range [][]string{{"sync", "--bad"}, {"sync", "extra"}, {"sync", "--instance"}} {
		if code := execute(args, io.Discard, io.Discard, environment{}); code != 2 {
			t.Fatalf("accepted %v: %d", args, code)
		}
	}
}
