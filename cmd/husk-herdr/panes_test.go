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
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/workbench"
)

func openPaneFixture() int {
	c, err := net.Dial("unix", os.Getenv("HERDR_SOCKET_PATH"))
	if err != nil {
		return 2
	}
	defer c.Close()
	if err = json.NewEncoder(c).Encode(map[string]any{"id": "open", "method": "plugin.pane.open", "params": map[string]any{"plugin_id": "pane-fixture", "entrypoint": "pane"}}); err != nil {
		return 3
	}
	var response struct{ Error any }
	if json.NewDecoder(c).Decode(&response) != nil || response.Error != nil {
		return 4
	}
	return 0
}

type paneFrameHost struct {
	host.Looped
	content  contracts.ContentSink
	observed map[protocol.SurfaceID]bool
	records  []contracts.RuntimeSurface
}

func (h *paneFrameHost) Update(msg any) bool {
	handled := h.Looped.Update(msg)
	fe := h.Frontend()
	for _, r := range fe.RuntimeSurfaces() {
		terminal := fe.TerminalView(r.Surface)
		if !h.observed[r.Surface] && terminal != nil && terminal.Title() == "Ready" {
			h.observed[r.Surface] = true
			h.records = append(h.records, r)
			// The dump driver currently settles on line-content traffic. Signal its
			// readiness only after the real terminal emulator ingested our scripted
			// bytes and final OSC title. Rendering still comes from TerminalView.
			h.content(r.Plugin, protocol.SurfaceUpdateParams{Surface: r.Surface, Lines: []string{"ready"}})
		}
	}
	return handled
}

func TestPaneHostFrames(t *testing.T) {
	for _, placement := range []string{"popup", "overlay", "zoomed", "tab", "split"} {
		t.Run(placement, func(t *testing.T) {
			root := t.TempDir()
			for k, part := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_DATA_HOME": "storage"} {
				t.Setenv(k, filepath.Join(root, part))
			}
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			sockets, err := os.MkdirTemp("/tmp", "hp-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(sockets)
			fakeUpstream(t, filepath.Join(sockets, "u.sock"), fakeherdr.Options{})
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "husk-herdr")
			if err = os.MkdirAll(config, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(config, "config.toml"), []byte(fmt.Sprintf("herdr_bin=%q\nhooks='always'\n", exe)), 0600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "plugin")
			if err = os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			manifest := fmt.Sprintf("id='pane-fixture'\nname='Pane fixture'\nversion='1'\nmin_herdr_version='0.9.0'\n[[startup]]\ncommand=[%q,'pane-open']\n[[panes]]\nid='pane'\ntitle='Pane fixture'\nplacement=%q\nwidth='60%%'\nheight=16\ncommand=[%q,'pane-output']\n", exe, placement, exe)
			if err = os.WriteFile(filepath.Join(dir, "herdr-plugin.toml"), []byte(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			var harness *paneFrameHost
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			frame, err := workbench.DumpFrame(ctx, workbench.Options{Log: io.Discard, NewHost: func(cfg contracts.Config, opts ...host.Option) (host.Looped, error) {
				cfg.InstanceID = "panes"
				cfg.RuntimeDir = filepath.Join(sockets, "runtime")
				cfg.InstanceDocs = map[string]map[string]any{"config": {"husk": map[string]any{"plugins": map[string]any{"paths": []string{dir}}}}}
				if err := os.MkdirAll(cfg.RuntimeDir, 0700); err != nil {
					return nil, err
				}
				h, err := host.New(cfg, append(opts, host.WithConverters([]string{exe}))...)
				if err != nil {
					return nil, err
				}
				harness = &paneFrameHost{Looped: h.(host.Looped), content: cfg.Content, observed: map[protocol.SurfaceID]bool{}}
				return harness, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(ansi.Strip(frame), "PANE OUTPUT") {
				t.Fatal("scripted PTY output missing from host frame")
			}
			if len(harness.records) != 1 {
				t.Fatalf("runtime opens=%d", len(harness.records))
			}
			r := harness.records[0]
			want := protocol.SurfacePlacementCanvas
			if placement == "popup" {
				want = protocol.SurfacePlacementModal
			}
			if r.Placement != want || string(r.Width) != `"60%"` || string(r.Height) != "16" || r.Frame == nil || !r.Frame.Border || !r.Frame.Close {
				t.Fatalf("surface.open params lost: %+v", r)
			}
			golden.RequireEqual(t, frame)
		})
	}
}
