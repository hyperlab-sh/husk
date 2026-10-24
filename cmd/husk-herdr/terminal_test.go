package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

func TestTerminalAttachFailuresUseCoreExitReporting(t *testing.T) {
	for _, mode := range []string{"binary", "start", "minimum", "floor", "ping", "plugin.list", "events.subscribe"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			for k, part := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_DATA_HOME": "storage"} {
				t.Setenv(k, filepath.Join(root, part))
			}
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			sockets, err := os.MkdirTemp("/tmp", "hf-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(sockets)
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("HERDR_SOCKET_PATH", filepath.Join(sockets, "u.sock"))
			options := fakeherdr.Options{}
			want := "herdr_protocol: " + mode
			minimum := "0.9.0"
			switch mode {
			case "binary":
				want = "herdr binary not found"
			case "start":
				want = "herdr server did not start"
			case "minimum":
				minimum, want = "0.10.0", "herdr_too_old: needs 0.10.0, server is 0.9.5"
			case "floor":
				minimum, options.Version, want = "0.8.0", "0.8.2", "herdr_too_old: needs 0.9.0, server is 0.8.2"
			default:
				options.Reply = func(req fakeherdr.Request, result map[string]any) *fakeherdr.ReplyError {
					if req.Method == mode {
						result["type"] = "malformed_fixture"
					}
					return nil
				}
			}
			if mode != "start" {
				server, err := fakeherdr.Start(os.Getenv("HERDR_SOCKET_PATH"), options)
				if err != nil {
					t.Fatal(err)
				}
				defer server.Close()
			}
			exe, _ := os.Executable()
			binary := exe
			if mode == "binary" {
				binary = filepath.Join(root, "missing-executable")
			}
			env := captureEnvironment(exe, runtime.GOOS, os.Getenv, os.UserHomeDir, os.LookupEnv)
			if err := os.MkdirAll(filepath.Dir(env.configPath), 0700); err != nil {
				t.Fatal(err)
			}
			settings := fmt.Sprintf("herdr_bin=%q\nhooks='always'\n", binary)
			if mode == "start" {
				settings += "server='start'\n"
			}
			if err := os.WriteFile(env.configPath, []byte(settings), 0600); err != nil {
				t.Fatal(err)
			}
			plugin := filepath.Join(root, "plugin")
			if err := os.Mkdir(plugin, 0700); err != nil {
				t.Fatal(err)
			}
			manifest := fmt.Sprintf("id='demo'\nname='Demo'\nversion='1'\nmin_herdr_version=%q\n[[events]]\non='pane.focused'\ncommand=['unused']\n", minimum)
			if err := os.WriteFile(filepath.Join(plugin, "herdr-plugin.toml"), []byte(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			var converted, diagnostics bytes.Buffer
			if code := execute([]string{"convert", plugin}, &converted, &diagnostics, env); code != 0 {
				t.Fatalf("convert=%d: %s", code, &diagnostics)
			}
			var doc map[string]any
			if err := json.Unmarshal(converted.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if mode == "start" {
				doc["command"] = []string{exe, "terminal-start-failure", plugin}
			}
			raw, _ := json.Marshal(doc)
			native := filepath.Join(plugin, "husk-plugin.json")
			if err := os.WriteFile(native, raw, 0600); err != nil {
				t.Fatal(err)
			}
			type podInfo struct {
				Status    string `json:"status"`
				LastError string `json:"last_error"`
				Log       string `json:"log"`
			}
			posts := make(chan any, 1024)
			stop, loopDone := make(chan struct{}), make(chan struct{})
			failed := make(chan podInfo, 1)
			cfg := contracts.Config{InstanceID: "failure", RuntimeDir: filepath.Join(sockets, "runtime"), InstanceDocs: map[string]map[string]any{"config": {"husk": map[string]any{"plugins": map[string]any{"paths": []string{native}}}}}, Post: func(m any) bool {
				select {
				case <-stop:
					return false
				default:
				}
				select {
				case posts <- m:
					return true
				case <-stop:
					return false
				}
			}}
			if err := os.MkdirAll(cfg.RuntimeDir, 0700); err != nil {
				t.Fatal(err)
			}
			h, err := host.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			r := h.(*host.Root)
			go func() {
				defer close(loopDone)
				for {
					select {
					case m := <-posts:
						if fn, ok := m.(func()); ok {
							fn()
						} else {
							r.Update(m)
						}
						var pods struct {
							Plugins map[string]podInfo `json:"plugins"`
						}
						_ = json.Unmarshal(r.Frontend().Snapshot().Slices["husk.pods"].Value, &pods)
						if p := pods.Plugins["demo"]; p.Status == "failed" {
							select {
							case failed <- p:
							default:
							}
						}
					case <-stop:
						return
					}
				}
			}()
			ctx, cancel := context.WithCancel(t.Context())
			exit := make(chan error, 1)
			go func() { exit <- r.Run(ctx) }()
			defer func() {
				close(stop)
				<-loopDone
				_ = r.Close()
				cancel()
				select {
				case <-exit:
				case <-time.After(10 * time.Second):
					t.Error("host did not stop")
				}
			}()
			select {
			case p := <-failed:
				if p.LastError != "exit status 1" {
					t.Fatalf("adapter message replaced core exit policy: %+v", p)
				}
				data, err := os.ReadFile(p.Log)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				last := lines[len(lines)-1]
				if !strings.HasPrefix(last, want) || strings.HasPrefix(last, "{") {
					t.Fatalf("last stderr line=%q want terminal message %q", last, want)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("terminal failure did not reach core: %s", r.Frontend().Snapshot().Slices["husk.pods"].Value)
			}
		})
	}
}
