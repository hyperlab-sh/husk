package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestPluginAddAbsolutizesPathAndDirectoryAndPrintsRefusals(t *testing.T) {
	for _, dirForm := range []bool{false, true} {
		t.Run(map[bool]string{false: "path", true: "directory"}[dirForm], func(t *testing.T) {
			dir := shortDir(t)
			srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
				c.reply(req, map[string]any{"added": []any{}, "refused": []any{map[string]string{"path": "/broken", "plugin": "", "code": "conversion", "message": "bad manifest"}}, "reports": []any{map[string]any{"service": "settings", "applied": false}}})
			}))
			h := newHarness(t, dir, nil)
			cwd := t.TempDir()
			h.env.Getwd = func() (string, error) { return cwd, nil }
			args := []string{"plugin", "add", "./x", "--socket", srv.Socket}
			key := "path"
			if dirForm {
				args = []string{"plugin", "add", "--dir", "./x", "--socket", srv.Socket}
				key = "dir"
			}
			if code := h.run(args...); code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, h.errb.String())
			}
			request := lastRequest(t, srv)
			var params map[string]string
			if err := json.Unmarshal(request.Params, &params); err != nil || len(params) != 1 || params[key] != filepath.Join(cwd, "x") || request.Method != protocol.MethodPluginAdd {
				t.Fatalf("request=%s %s (%v)", request.Method, request.Params, err)
			}
			var result struct {
				Refused []any
				Reports []struct{ Applied bool }
			}
			if json.Unmarshal(h.out.Bytes(), &result) != nil || len(result.Refused) != 1 || result.Reports[0].Applied {
				t.Fatal(h.out.String())
			}
		})
	}
	if cmd := pluginCommand().verbs["add"]; cmd.timeout != 10*time.Second {
		t.Fatal("add timeout", cmd.timeout)
	}
}

func TestPluginAddUsageErrorsDoNotConnectAndDirIsCommandSpecific(t *testing.T) {
	dir := shortDir(t)
	srv := pluginServer(t, dir)
	for _, args := range [][]string{
		{"plugin", "add"}, {"plugin", "add", "a", "b"}, {"plugin", "add", ""},
		{"plugin", "add", "a", "--dir", "b"}, {"plugin", "add", "a", "--dir", ""},
		{"plugin", "add", "--dir", ""}, {"plugin", "add", "--dir"},
		{"plugin", "list", "--dir", "x"}, {"plugin", "remove", "a", "--dir", "x"},
		{"state", "get", "husk.pods", "--dir", "x"},
	} {
		h := newHarness(t, dir, nil)
		if code := h.run(args...); code != output.ExitUsage {
			t.Fatalf("%v: exit=%d stderr=%s", args, code, h.errb.String())
		}
	}
	if srv.connections() != 0 {
		t.Fatal("usage error connected")
	}
}

func TestPluginAddErrorReplyIsExitOne(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.fail(req, protocol.CodeConflict, "already loaded") }))
	h := newHarness(t, dir, nil)
	if code := h.run("plugin", "add", "/plugins/alpha", "--socket", srv.Socket); code != output.ExitRemote || h.out.Len() != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, h.out.String(), h.errb.String())
	}
}
