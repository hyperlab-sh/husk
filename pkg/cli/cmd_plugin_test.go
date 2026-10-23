package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

const pluginRows = `[{"id":"alpha","name":"Alpha","status":"live","pid":42,"log":"/l/alpha.log"},{"id":"beta","name":"Beta","status":"failed","pid":0,"last_error":"boom"}]`

func pluginServer(t *testing.T, dir string) *fakeServer {
	return serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
		switch req.Method {
		case protocol.MethodPluginList:
			c.send(protocol.Message{ID: req.ID, Result: json.RawMessage(pluginRows)})
		case protocol.MethodPluginRemove:
			var p struct{ Plugin string }
			_ = json.Unmarshal(req.Params, &p)
			if p.Plugin != "alpha" {
				c.fail(req, protocol.CodeNotFound, "no plugin "+p.Plugin)
				return
			}
			c.reply(req, map[string]any{"plugin": "alpha", "status": "exited", "log": "/l/alpha.log",
				"reports": []map[string]any{{"service": "settings", "gen": 2, "applied": false}}})
		case protocol.MethodPluginLog:
			var p struct{ Plugin string }
			_ = json.Unmarshal(req.Params, &p)
			switch p.Plugin {
			case "alpha":
				c.reply(req, map[string]string{"path": "/l/alpha.log"})
			case "":
				c.fail(req, protocol.CodeInvalidParams, "plugin is required")
			default:
				c.fail(req, protocol.CodeNotFound, "no plugin "+p.Plugin)
			}
		default:
			c.fail(req, protocol.CodeDenied, "plugin callers are denied")
		}
	}))
}

func TestPluginListSendsEmptyParamsAndRendersBothFormats(t *testing.T) {
	dir := shortDir(t)
	srv := pluginServer(t, dir)
	h := newHarness(t, dir, nil)
	if got := h.run("plugin", "list", "--socket", srv.Socket, "--format", "json"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	if h.out.String() != pluginRows+"\n" {
		t.Fatalf("json stdout = %q", h.out.String())
	}
	if req := lastRequest(t, srv); req.Method != protocol.MethodPluginList || string(req.Params) != "{}" {
		t.Fatalf("request = %s %s", req.Method, req.Params)
	}

	h = newHarness(t, dir, nil)
	if got := h.run("plugin", "list", "--format", "table"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	lines := strings.Split(strings.TrimRight(h.out.String(), "\n"), "\n")
	if len(lines) != 3 || strings.Fields(lines[0])[0] != "id" || strings.Fields(lines[1])[0] != "alpha" || strings.Fields(lines[2])[0] != "beta" {
		t.Fatalf("table = %q", h.out.String())
	}
}

func TestPluginLogSendsThePluginIdAndRendersThePath(t *testing.T) {
	dir := shortDir(t)
	srv := pluginServer(t, dir)
	h := newHarness(t, dir, nil)
	if got := h.run("plugin", "log", "alpha", "--socket", srv.Socket, "--format", "json"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	if h.out.String() != `{"path":"/l/alpha.log"}`+"\n" {
		t.Fatalf("stdout = %q", h.out.String())
	}
	if req := lastRequest(t, srv); req.Method != protocol.MethodPluginLog || string(req.Params) != `{"plugin":"alpha"}` {
		t.Fatalf("request = %s %s", req.Method, req.Params)
	}
	h = newHarness(t, dir, nil)
	if got := h.run("plugin", "log", "alpha", "--format", "table"); got != 0 || strings.Fields(h.out.String())[1] != "/l/alpha.log" {
		t.Fatalf("table exit %d stdout %q", got, h.out.String())
	}
}

func TestPluginReplyErrorsAreForwardedWithExit1(t *testing.T) {
	dir := shortDir(t)
	pluginServer(t, dir)
	h := newHarness(t, dir, nil)
	if got := h.run("plugin", "log", "ghost", "--format", "json"); got != output.ExitRemote || h.out.Len() != 0 {
		t.Fatalf("exit %d stdout %q", got, h.out.String())
	}
	if code, _, _ := errObject(t, h); code != "not_found" {
		t.Fatalf("code %q", code)
	}
	// A plugin's token is denied by the instance, whatever the verb.
	h = newHarness(t, dir, nil)
	h.env.Getenv = func(k string) string {
		if k == EnvPluginToken {
			return "tok"
		}
		return ""
	}
	srv := serve(t, shortDir(t), "d", pingOK("d", func(c *srvConn, req protocol.Message) {
		if req.Token == "" {
			c.reply(req, []string{})
			return
		}
		c.fail(req, protocol.CodeDenied, "plugin.list is for the user")
	}))
	h.env.RuntimeDir = func() (string, bool, error) { return "", false, nil }
	if got := h.run("plugin", "list", "--socket", srv.Socket, "--format", "json"); got != output.ExitRemote {
		t.Fatalf("exit %d, stderr %q", got, h.errb.String())
	}
	if code, _, _ := errObject(t, h); code != "denied" {
		t.Fatalf("code %q", code)
	}
}

func TestPluginUsageErrorsSendNothing(t *testing.T) {
	dir := shortDir(t)
	srv := pluginServer(t, dir)
	for _, args := range [][]string{
		{"plugin"},
		{"plugin", "remove"},
		{"plugin", "remove", "a", "b"},
		{"plugin", "remove", ""},
		{"plugin", "purge", "alpha"},
		{"plugin", "list", "extra"},
		{"plugin", "log"},
		{"plugin", "log", "a", "b"},
		{"plugin", "log", ""},
	} {
		h := newHarness(t, dir, nil)
		if got := h.run(args...); got != output.ExitUsage {
			t.Fatalf("%v: exit %d, want %d", args, got, output.ExitUsage)
		}
	}
	if srv.connections() != 0 {
		t.Fatalf("%d connections for usage errors", srv.connections())
	}
}

// remove prints the reply whatever the reports say (exit 0), in both formats;
// an error reply is exit 1.
func TestPluginRemoveSendsTheIdAndPrintsTheReply(t *testing.T) {
	dir := shortDir(t)
	srv := pluginServer(t, dir)
	h := newHarness(t, dir, nil)
	if got := h.run("plugin", "remove", "alpha", "--socket", srv.Socket, "--format", "json"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	var rep struct {
		Plugin, Status string
		Reports        []struct{ Applied bool }
	}
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil || rep.Plugin != "alpha" || rep.Status != "exited" || len(rep.Reports) != 1 || rep.Reports[0].Applied {
		t.Fatalf("stdout = %q (%v)", h.out.String(), err)
	}
	if req := lastRequest(t, srv); req.Method != protocol.MethodPluginRemove || string(req.Params) != `{"plugin":"alpha"}` {
		t.Fatalf("request = %s %s", req.Method, req.Params)
	}
	h = newHarness(t, dir, nil)
	if got := h.run("plugin", "remove", "alpha", "--format", "table"); got != 0 || !strings.Contains(h.out.String(), "exited") {
		t.Fatalf("table exit %d stdout %q", got, h.out.String())
	}
	h = newHarness(t, dir, nil)
	if got := h.run("plugin", "remove", "ghost", "--format", "json"); got != output.ExitRemote {
		t.Fatalf("exit %d", got)
	}
	if code, _, _ := errObject(t, h); code != "not_found" {
		t.Fatalf("code %q", code)
	}
}
