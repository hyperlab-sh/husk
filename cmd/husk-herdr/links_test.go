package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/adapters/herdr/shim"
	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/workbench"
)

type linkOpenerProcess struct{}

func (linkOpenerProcess) PID() int                    { return 900 }
func (linkOpenerProcess) Wait() (int, string)         { return 0, "" }
func (linkOpenerProcess) Signal(syscall.Signal) error { return nil }

func runLinkShim() int {
	exe, _ := os.Executable()
	o, err := runtimeOptions(os.Args[2], childEnvironment(exe))
	if err == nil {
		o.SpawnOpener = func(c shim.Command) (shim.Process, error) {
			conn, err := net.Dial("unix", os.Getenv("CAPTURE_SOCKET"))
			if err != nil {
				return nil, err
			}
			defer conn.Close()
			err = json.NewEncoder(conn).Encode(linkCapture{Opener: c.Argv})
			return linkOpenerProcess{}, err
		}
		err = shim.Run(context.Background(), o, os.Stdin, os.Stdout, os.Stderr)
	}
	return reportRun(err, os.Stderr)
}

type linkCapture struct {
	Env    []string `json:"env"`
	Opener []string `json:"opener"`
}

type linkFrameHost struct {
	host.Looped
	cfg      contracts.Config
	clicked  bool
	captures []linkCapture
}

func (h *linkFrameHost) Update(msg any) bool {
	if capture, ok := msg.(linkCapture); ok {
		h.captures = append(h.captures, capture)
	}
	handled := h.Looped.Update(msg)
	fe := h.Frontend()
	for _, r := range fe.RuntimeSurfaces() {
		terminal := fe.TerminalView(r.Surface)
		if terminal == nil || terminal.Title() != "LinksReady" {
			continue
		}
		if !h.clicked {
			var geo struct {
				Rects map[protocol.SurfaceID]struct{ Inner struct{ X, Y int } }
			}
			_ = json.Unmarshal(fe.Snapshot().Slices["husk.geometry"].Value, &geo)
			rect, ok := geo.Rects[r.Surface]
			if !ok {
				continue
			}
			h.clicked = true
			// The real workbench resolves OSC 8 first, then a detected row URL,
			// then a nonmatching HTTP URL. All gestures use actual geometry.
			go func() {
				for row := 0; row < 3; row++ {
					h.cfg.Post(tea.MouseClickMsg{X: rect.Inner.X + 2, Y: rect.Inner.Y + row, Button: tea.MouseLeft, Mod: tea.ModCtrl})
					h.cfg.Post(tea.MouseReleaseMsg{X: rect.Inner.X + 2, Y: rect.Inner.Y + row, Button: tea.MouseLeft, Mod: tea.ModCtrl})
				}
			}()
		}
		if len(h.captures) == 3 {
			h.cfg.Content(r.Plugin, protocol.SurfaceUpdateParams{Surface: r.Surface, Lines: []string{"ready"}})
		}
	}
	return handled
}

func TestLinkHostClicks(t *testing.T) {
	root := t.TempDir()
	for k, part := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_DATA_HOME": "storage"} {
		t.Setenv(k, filepath.Join(root, part))
	}
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	sockets, err := os.MkdirTemp("/tmp", "hl-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	fakeUpstream(t, filepath.Join(sockets, "u.sock"), fakeherdr.Options{})
	listener, err := net.Listen("unix", filepath.Join(sockets, "capture.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("CAPTURE_SOCKET", listener.Addr().String())
	exe, _ := os.Executable()
	env := childEnvironment(exe)
	if err := os.MkdirAll(filepath.Dir(env.configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.configPath, []byte(fmt.Sprintf("herdr_bin=%q\nhooks='always'\n", exe)), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "plugin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf("id='pane-fixture'\nname='Links'\nversion='1'\nmin_herdr_version='0.9.0'\n[[startup]]\ncommand=[%q,'pane-open']\n[[panes]]\nid='pane'\ntitle='Links'\nplacement='overlay'\ncommand=[%q,'link-output']\n[[actions]]\nid='go'\ntitle='Go'\ncommand=[%q,'capture']\n[[link_handlers]]\nid='first'\ntitle='First'\npattern='handled'\naction='go'\n[[link_handlers]]\nid='later'\ntitle='Later'\npattern='.*'\naction='go'\nplatforms=['windows']\n", exe, exe, exe)
	if err := os.WriteFile(filepath.Join(dir, "herdr-plugin.toml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"manifest_version": 2, "protocol": 2, "id": "pane-fixture", "name": "Links", "version": "1", "command": []string{exe, "link-shim", dir}, "actions": []map[string]any{{"id": "pane-fixture.go", "title": "Go", "public": true}}}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, "husk-plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var harness *linkFrameHost
	var receiverDone chan struct{}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	_, err = workbench.DumpFrame(ctx, workbench.Options{Log: io.Discard, NewHost: func(cfg contracts.Config, opts ...host.Option) (host.Looped, error) {
		cfg.InstanceID = "links"
		cfg.RuntimeDir = filepath.Join(sockets, "runtime")
		cfg.InstanceDocs = map[string]map[string]any{"config": {"husk": map[string]any{"plugins": map[string]any{"paths": []string{dir}}}}}
		if err := os.MkdirAll(cfg.RuntimeDir, 0700); err != nil {
			return nil, err
		}
		h, err := host.New(cfg, opts...)
		if err != nil {
			return nil, err
		}
		harness = &linkFrameHost{Looped: h.(host.Looped), cfg: cfg}
		receiverDone = make(chan struct{})
		go func() {
			defer close(receiverDone)
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				var capture linkCapture
				err = json.NewDecoder(conn).Decode(&capture)
				conn.Close()
				if err == nil {
					cfg.Post(capture)
				}
			}
		}()
		return harness, nil
	}})
	listener.Close()
	if receiverDone != nil {
		<-receiverDone
	}
	if err != nil {
		t.Fatal(err)
	}
	if !harness.clicked || len(harness.captures) != 3 {
		t.Fatalf("clicks=%t captures=%d", harness.clicked, len(harness.captures))
	}
	seen := map[string]bool{}
	for _, c := range harness.captures {
		if c.Opener != nil {
			if !reflect.DeepEqual(c.Opener, []string{"xdg-open", "https://external.test"}) {
				t.Fatalf("opener=%v", c.Opener)
			}
			seen["opener"] = true
			continue
		}
		env := map[string]string{}
		for _, pair := range c.Env {
			for i := 0; i < len(pair); i++ {
				if pair[i] == '=' {
					env[pair[:i]] = pair[i+1:]
					break
				}
			}
		}
		url := env["HERDR_PLUGIN_CLICKED_URL"]
		var context shim.InvocationContext
		if err := json.Unmarshal([]byte(env["HERDR_PLUGIN_CONTEXT_JSON"]), &context); err != nil {
			t.Fatal(err)
		}
		want := shim.InvocationContext{WorkspaceID: "workspace-1", TabID: "tab-1", FocusedPaneID: "pane-1", FocusedPaneStatus: "idle", InvocationSource: "link_click", CorrelationID: "link_click", ClickedURL: url, LinkHandlerID: "first"}
		if !reflect.DeepEqual(context, want) || env["HERDR_PLUGIN_LINK_HANDLER_ID"] != "first" || env["HERDR_PLUGIN_ACTION_ID"] != "go" {
			t.Fatalf("context=%+v env=%v", context, env)
		}
		seen[url] = true
	}
	for _, key := range []string{"custom://handled", "https://handled.test", "opener"} {
		if !seen[key] {
			t.Errorf("missing %q", key)
		}
	}
}
