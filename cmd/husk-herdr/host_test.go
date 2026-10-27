package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/shim"
	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Re-execute this binary as the real converter/shim or an argv command. No
// inherited HUSK_* switch is needed: Pods intentionally strips those variables.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--selection" {
		os.Exit(captureCommand())
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "plugin":
			if os.Getenv("HUSK_PLUGIN_TOKEN") != "" || os.Getenv("HUSK_INSTANCE") != "inherited-instance" {
				os.Exit(7)
			}
			fmt.Fprintln(os.Stdout, `[{"id":"example"}]`)
			os.Exit(0)
		case "detect", "discover", "convert", "run":
			exe, _ := os.Executable()
			os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr, childEnvironment(exe)))
		case "capture":
			os.Exit(captureCommand())
		case "pane-open":
			os.Exit(openPaneFixture())
		case "pane-output":
			fmt.Fprint(os.Stdout, "\x1b[32mPANE OUTPUT\x1b[0m\x1b]2;Ready\x07")
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}
func captureCommand() int {
	if os.Getenv("HERDR_PLUGIN_ID") == "sample:tools" {
		c, err := net.Dial("unix", os.Getenv("HERDR_SOCKET_PATH"))
		if err != nil {
			return 3
		}
		defer c.Close()
		enc, dec := json.NewEncoder(c), json.NewDecoder(c)
		for _, req := range []map[string]any{{"id": "notification", "method": "notification.show", "params": map[string]any{"title": "From command", "body": "Visible in Husk"}}, {"id": "other", "method": "plugin.action.invoke", "params": map[string]any{"plugin_id": "sample.empty", "action_id": "go", "context": map[string]any{"invocation_source": "command_palette", "correlation_id": "from-proxy"}}}} {
			if enc.Encode(req) != nil {
				return 4
			}
			var response map[string]any
			if dec.Decode(&response) != nil || response["error"] != nil {
				return 5
			}
		}
	}
	c, err := net.Dial("unix", os.Getenv("CAPTURE_SOCKET"))
	if err != nil {
		return 6
	}
	defer c.Close()
	wd, _ := os.Getwd()
	_ = json.NewEncoder(c).Encode(map[string]any{"env": os.Environ(), "cwd": wd})
	var release [1]byte
	_, _ = c.Read(release[:])
	return 0
}

func TestRealHostConverterAndShim(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	root := t.TempDir()
	for k, part := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_DATA_HOME": "storage"} {
		t.Setenv(k, filepath.Join(root, part))
	}
	sockets, err := os.MkdirTemp("/tmp", "he-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	runtimeDir := filepath.Join(sockets, "runtime")
	if err = os.Mkdir(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	fakeUpstream(t, filepath.Join(sockets, "u.sock"), fakeherdr.Options{Plugins: []map[string]any{{"plugin_id": "sample:tools", "enabled": true}, {"plugin_id": "sample.empty", "enabled": true}}})
	exe, _ := os.Executable()
	adapterConfig := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "husk-herdr")
	_ = os.MkdirAll(adapterConfig, 0700)
	if err = os.WriteFile(filepath.Join(adapterConfig, "config.toml"), []byte(fmt.Sprintf("herdr_bin=%q\n", exe)), 0600); err != nil {
		t.Fatal(err)
	}
	capture, err := net.Listen("unix", filepath.Join(sockets, "capture.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	t.Setenv("CAPTURE_SOCKET", capture.Addr().String())
	type captured struct {
		env  []string
		cwd  string
		conn net.Conn
	}
	captures := make(chan captured, 4)
	var captureWorkers sync.WaitGroup
	captureWorkers.Add(1)
	go func() {
		defer captureWorkers.Done()
		for {
			c, e := capture.Accept()
			if e != nil {
				return
			}
			var data struct {
				Env []string
				Cwd string
			}
			if json.NewDecoder(c).Decode(&data) != nil {
				_ = c.Close()
				continue
			}
			captures <- captured{data.Env, data.Cwd, c}
		}
	}()
	defer func() { _ = capture.Close(); captureWorkers.Wait() }()
	dirs := []string{}
	for _, name := range []string{"toolbox", "minimal"} {
		dir := filepath.Join(root, name)
		_ = os.MkdirAll(filepath.Join(dir, "tools"), 0700)
		data, e := os.ReadFile(filepath.Join("..", "..", "adapters", "herdr", "testdata", "manifests", name, "herdr-plugin.toml"))
		if e != nil {
			t.Fatal(e)
		}
		if name == "minimal" {
			data = append(data, []byte(fmt.Sprintf("\n[[actions]]\nid='go'\ntitle='Go'\ncommand=[%q,'capture']\n", exe))...)
		}
		if e = os.WriteFile(filepath.Join(dir, "herdr-plugin.toml"), data, 0600); e != nil {
			t.Fatal(e)
		}
		if e = os.Symlink(exe, filepath.Join(dir, "tools", "inspect")); e != nil {
			t.Fatal(e)
		}
		dirs = append(dirs, dir)
	}
	records := []map[string]any{{"plugin_id": "sample:tools", "plugin_root": dirs[0], "enabled": true}, {"plugin_id": "sample.empty", "plugin_root": dirs[1], "enabled": true}}
	_ = os.MkdirAll(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "herdr"), 0700)
	raw, _ := json.Marshal(records)
	_ = os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "herdr", "plugins.json"), raw, 0600)
	messages := make(chan any, 4096)
	stop := make(chan struct{})
	loopDone := make(chan struct{})
	ready := make(chan struct{})
	var readyOnce sync.Once
	cfg := contracts.Config{InstanceID: "adapter", RuntimeDir: runtimeDir, InstanceDocs: map[string]map[string]any{"config": {"husk": map[string]any{"plugins": map[string]any{"paths": dirs}}}}, Post: func(m any) bool {
		select {
		case <-stop:
			return false
		default:
		}
		select {
		case messages <- m:
			return true
		case <-stop:
			return false
		}
	}}
	rootHost, err := host.New(cfg, host.WithConverters([]string{exe}))
	if err != nil {
		t.Fatal(err)
	}
	r := rootHost.(*host.Root)
	go func() {
		defer close(loopDone)
		for {
			select {
			case m := <-messages:
				if fn, ok := m.(func()); ok {
					fn()
				} else {
					r.Update(m)
				}
				snap := r.Frontend().Snapshot()
				attached := true
				for _, id := range []string{"sample-tools", "sample.empty"} {
					if !strings.Contains(string(snap.Slices[id+".state"].Value), `"server":"unchecked"`) {
						attached = false
					}
				}
				if attached {
					readyOnce.Do(func() { close(ready) })
				}
			case <-stop:
				return
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	exit := make(chan error, 1)
	go func() { exit <- r.Run(ctx) }()
	t.Cleanup(func() {
		close(stop)
		<-loopDone
		_ = r.Close()
		cancel()
		select {
		case e := <-exit:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(10 * time.Second):
			t.Error("host did not stop")
		}
	})
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		t.Fatalf("shims not attached: %v", r.Frontend().Snapshot().Slices)
	}
	client := shim.SocketHost{Path: instance.SocketPath(runtimeDir, "adapter")}
	result, err := client.Call(t.Context(), protocol.MethodActionList, map[string]any{})
	if err != nil || !strings.Contains(string(result), "sample-tools.inspect-selection") || !strings.Contains(string(result), "sample.empty.go") {
		t.Fatalf("actions=%s err=%v", result, err)
	}
	result, err = client.Call(t.Context(), protocol.MethodActionInvoke, map[string]any{"action": "sample-tools.inspect-selection"})
	if err != nil || !strings.Contains(string(result), `"started":true`) {
		t.Fatalf("invoke=%s err=%v", result, err)
	}
	for range 2 {
		var got captured
		select {
		case got = <-captures:
		case <-time.After(10 * time.Second):
			t.Fatal("command did not capture environment")
		}
		env := map[string]string{}
		for _, v := range got.env {
			k, val, _ := strings.Cut(v, "=")
			env[k] = val
		}
		for k := range env {
			if strings.HasPrefix(k, "HUSK_") && k != "HUSK_SOCKET_PATH" && k != "HUSK_INSTANCE" && k != "HUSK_BIN_PATH" {
				t.Fatalf("credential/internals leaked: %s", k)
			}
		}
		if env["HERDR_ENV"] != "1" || env["HERDR_BIN_PATH"] != exe || env["HERDR_PLUGIN_ROOT"] != got.cwd {
			t.Fatal("command environment/cwd table drift")
		}
		var c shim.InvocationContext
		_ = json.Unmarshal([]byte(env["HERDR_PLUGIN_CONTEXT_JSON"]), &c)
		if env["HERDR_PLUGIN_ID"] == "sample.empty" {
			if c.InvocationSource != "command_palette" || c.CorrelationID != "from-proxy" {
				t.Fatalf("proxy context=%+v", c)
			}
		} else if c.InvocationSource != "api" {
			t.Fatalf("host context=%+v", c)
		}
		_, _ = io.WriteString(got.conn, "x")
		_ = got.conn.Close()
	}
}
