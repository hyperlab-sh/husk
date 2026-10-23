package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// echoServer answers every request after the ping with result.
func echoServer(t *testing.T, result any) (*fakeServer, *harness) {
	t.Helper()
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, result) }))
	return srv, newHarness(t, dir, nil)
}

// lastRequest is the command's own request: the one right after its
// session.ping (discovery may have probed with another ping before).
func lastRequest(t *testing.T, srv *fakeServer) protocol.Message {
	t.Helper()
	reqs := srv.requests()
	if len(reqs) < 2 || reqs[len(reqs)-2].Method != protocol.MethodSessionPing || reqs[len(reqs)-1].Method == protocol.MethodSessionPing {
		t.Fatalf("requests = %+v, want ping then the command", reqs)
	}
	return reqs[len(reqs)-1]
}

func TestStemCommandsSendTheirMethodAndParams(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		stdin  string
		method string
		params string
	}{
		{"state", []string{"state"}, "", "state.snapshot", `{"values":false}`},
		{"state get", []string{"state", "get", "husk.focus"}, "", "state.get", `{"slice":"husk.focus"}`},
		{"state get with pointer", []string{"state", "get", "husk.pods", "/plugins/x/status"}, "", "state.get", `{"path":"/plugins/x/status","slice":"husk.pods"}`},
		{"state dump", []string{"state", "dump"}, "", "state.snapshot", `{"values":true}`},
		{"state set", []string{"state", "set", `{"slice":"p.state","value":{"a":1}}`}, "", "state.set", `{"slice":"p.state","value":{"a":1}}`},
		{"state set from stdin", []string{"state", "set", "-"}, `{"slice":"p.state","value":{}}`, "state.set", `{"slice":"p.state","value":{}}`},
		{"state patch", []string{"state", "patch", `{"slice":"p.state","patch":{"b":2}}`}, "", "state.patch", `{"slice":"p.state","patch":{"b":2}}`},
		{"state patch if-revision", []string{"state", "patch", `{"slice":"p.state","patch":{"b":2}}`, "--if-revision", "8"}, "", "state.patch", `{"if_revision":8,"patch":{"b":2},"slice":"p.state"}`},
		{"state patch if-revision 0", []string{"state", "patch", `{"slice":"p.state","patch":{}}`, "--if-revision=0"}, "", "state.patch", `{"if_revision":0,"patch":{},"slice":"p.state"}`},
		{"state clear", []string{"state", "clear", "p.state"}, "", "state.clear", `{"namespace":"p.state"}`},
		{"action list", []string{"action", "list"}, "", "action.list", `{}`},
		{"action invoke", []string{"action", "invoke", "husk.quit"}, "", "action.invoke", `{"action":"husk.quit"}`},
		{"action invoke --arg", []string{"action", "invoke", "n.new", "--arg", "title=a=b", "--arg", "x=1", "--arg", "x=2"}, "", "action.invoke", `{"action":"n.new","args":{"title":"a=b","x":"2"}}`},
		{"action invoke --args-json", []string{"action", "invoke", "n.new", "--args-json", `{"n":1}`}, "", "action.invoke", `{"action":"n.new","args":{"n":1}}`},
		{"action invoke --args-json stdin", []string{"action", "invoke", "n.new", "--args-json", "-"}, `{"n":2}`, "action.invoke", `{"action":"n.new","args":{"n":2}}`},
		{"event emit", []string{"event", "emit", "user.x"}, "", "event.emit", `{"name":"user.x"}`},
		{"event emit data", []string{"event", "emit", "user.x", "--data", `{"a":1}`}, "", "event.emit", `{"data":{"a":1},"name":"user.x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, h := echoServer(t, map[string]any{"ok": true, "value": 1})
			h.env.Stdin = strings.NewReader(tc.stdin)
			if got := h.run(append(tc.args, "--format", "json")...); got != 0 {
				t.Fatalf("exit %d: %s", got, h.errb.String())
			}
			req := lastRequest(t, srv)
			if req.Method != tc.method {
				t.Fatalf("method = %s, want %s", req.Method, tc.method)
			}
			assertJSONEqual(t, req.Params, tc.params)
			if h.errb.Len() != 0 {
				t.Fatalf("stderr = %q", h.errb.String())
			}
		})
	}
}

func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("params %q: %v", got, err)
	}
	_ = json.Unmarshal([]byte(want), &w)
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("params = %s, want %s", gb, wb)
	}
}

func TestStemCommandsJSONPrintsTheRawResult(t *testing.T) {
	for _, args := range [][]string{
		{"state"}, {"state", "get", "s"}, {"state", "dump"}, {"state", "set", `{"slice":"s","value":{}}`},
		{"state", "patch", `{"slice":"s","patch":{}}`}, {"state", "clear", "s"}, {"action", "invoke", "a.b"}, {"event", "emit", "user.x"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, h := echoServer(t, map[string]any{"slice": "s", "value": map[string]any{"k": 1}})
			if got := h.run(append(args, "--format", "json")...); got != 0 {
				t.Fatalf("exit %d: %s", got, h.errb.String())
			}
			if h.out.String() != `{"slice":"s","value":{"k":1}}`+"\n" {
				t.Fatalf("stdout = %q", h.out.String())
			}
		})
	}
}

func TestStateTableUsesTheSpecColumns(t *testing.T) {
	_, h := echoServer(t, map[string]any{"seq": 3, "slices": []map[string]any{
		{"slice": "husk.focus", "owner": "husk", "scope": "instance", "persistent": false, "revision": 42, "bytes": 61},
		{"slice": "husk.layout", "owner": "husk", "scope": "instance", "persistent": true, "retention": map[string]any{"policy": "keep-latest"}, "revision": 5, "bytes": 188},
		{"slice": "p.state", "owner": "p", "scope": "user", "persistent": true, "retention": map[string]any{"policy": "keep-n", "n": 5}, "revision": 1, "bytes": 2},
	}})
	if got := h.run("state", "--format", "table"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	want := "SLICE        OWNER  SCOPE     PERSISTENT  RETENTION    REVISION  BYTES\n" +
		"husk.focus   husk   instance  false                    42        61\n" +
		"husk.layout  husk   instance  true        keep-latest  5         188\n" +
		"p.state      p      user      true        keep-n:5     1         2\n"
	if h.out.String() != want {
		t.Fatalf("stdout:\n%s\nwant:\n%s", h.out.String(), want)
	}
}

func TestStateGetTableAndJSON(t *testing.T) {
	reply := map[string]any{"slice": "husk.focus", "owner": "husk", "revision": 42, "value": map[string]any{"surface": "main"}}
	t.Run("table prints the value as JSON", func(t *testing.T) {
		_, h := echoServer(t, reply)
		if got := h.run("state", "get", "husk.focus", "--format", "table"); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		if h.out.String() != `{"surface":"main"}`+"\n" {
			t.Fatalf("stdout = %q", h.out.String())
		}
	})
	t.Run("a string value stays JSON", func(t *testing.T) {
		_, h := echoServer(t, map[string]any{"value": "live"})
		if got := h.run("state", "get", "husk.pods", "/p", "--format", "table"); got != 0 || h.out.String() != "\"live\"\n" {
			t.Fatalf("exit %d, stdout = %q", got, h.out.String())
		}
	})
	t.Run("json prints the whole reply", func(t *testing.T) {
		_, h := echoServer(t, reply)
		if got := h.run("state", "get", "husk.focus", "--format", "json"); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		if !strings.Contains(h.out.String(), `"revision":42`) {
			t.Fatalf("stdout = %q", h.out.String())
		}
	})
}

func TestActionListOwnerFilterIsClientSide(t *testing.T) {
	list := []map[string]any{
		{"id": "husk.quit", "owner": "husk", "public": false, "live": true},
		{"id": "n.new", "owner": "n", "public": true, "live": true},
		{"id": "n.old", "owner": "n", "public": false, "live": false},
	}
	t.Run("filtered", func(t *testing.T) {
		srv, h := echoServer(t, list)
		if got := h.run("action", "list", "--owner", "n", "--format", "json"); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		if string(lastRequest(t, srv).Params) != `{}` {
			t.Fatalf("params = %s: the owner filter must not reach the instance", lastRequest(t, srv).Params)
		}
		want := `[{"id":"n.new","live":true,"owner":"n","public":true},{"id":"n.old","live":false,"owner":"n","public":false}]` + "\n"
		if h.out.String() != want {
			t.Fatalf("stdout = %q", h.out.String())
		}
	})
	t.Run("no match is an empty array", func(t *testing.T) {
		_, h := echoServer(t, list)
		if got := h.run("action", "list", "--owner", "zzz", "--format", "json"); got != 0 || h.out.String() != "[]\n" {
			t.Fatalf("exit %d, stdout = %q", got, h.out.String())
		}
	})
	t.Run("table is the generic array of objects", func(t *testing.T) {
		_, h := echoServer(t, list)
		if got := h.run("action", "list", "--format", "table"); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		want := "id         live   owner  public\n" +
			"husk.quit  true   husk   false\n" +
			"n.new      true   n      true\n" +
			"n.old      false  n      false\n"
		if h.out.String() != want {
			t.Fatalf("stdout:\n%s\nwant:\n%s", h.out.String(), want)
		}
	})
}

func TestStemCommandsForwardInstanceErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code protocol.Code
		exit int
	}{
		{[]string{"state", "set", `{"slice":"s","value":{}}`}, protocol.CodeDenied, output.ExitRemote},
		{[]string{"state", "patch", `{"slice":"s","patch":{}}`, "--if-revision", "1"}, protocol.CodeConflict, output.ExitRemote},
		{[]string{"state", "get", "nope"}, protocol.CodeNotFound, output.ExitRemote},
		{[]string{"state", "dump"}, protocol.CodeInvalidParams, output.ExitRemote},
		{[]string{"state", "clear", "x"}, protocol.CodeUnavailable, output.ExitRemote},
		{[]string{"action", "invoke", "nope.nope"}, protocol.CodeNotFound, output.ExitRemote},
		{[]string{"action", "list"}, protocol.CodeInternal, output.ExitRemote},
		{[]string{"event", "emit", "husk.x"}, protocol.CodeDenied, output.ExitRemote},
		{[]string{"event", "emit", "user.x"}, protocol.CodeInvalidParams, output.ExitRemote},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			dir := shortDir(t)
			serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
				c.send(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: tc.code, Message: "the instance says no", Data: json.RawMessage(`{"revision":9}`)}})
			}))
			h := newHarness(t, dir, nil)
			if got := h.run(append(tc.args, "--format", "json")...); got != tc.exit {
				t.Fatalf("exit = %d, want %d", got, tc.exit)
			}
			code, msg, data := errObject(t, h)
			if code != string(tc.code) || msg != "the instance says no" || string(data) != `{"revision":9}` {
				t.Fatalf("error = %s %q %s", code, msg, data)
			}
			if h.out.Len() != 0 {
				t.Fatalf("stdout = %q", h.out.String())
			}
		})
	}
}

func TestStemCommandsUsageErrorsSendNothing(t *testing.T) {
	big := `{"slice":"s","value":"` + strings.Repeat("x", protocol.MaxLineBytes) + `"}`
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
	}{
		{"state with an argument", []string{"state", "x"}, ""},
		{"unknown state verb", []string{"state", "frob"}, ""},
		{"unknown action verb", []string{"action", "frob"}, ""},
		{"action without a verb", []string{"action"}, ""},
		{"event without a verb", []string{"event"}, ""},
		{"state get without slice", []string{"state", "get"}, ""},
		{"state get with three arguments", []string{"state", "get", "a", "/b", "c"}, ""},
		{"state dump with an argument", []string{"state", "dump", "x"}, ""},
		{"state set non-object", []string{"state", "set", `[1]`}, ""},
		{"state set string", []string{"state", "set", `"x"`}, ""},
		{"state set null", []string{"state", "set", `null`}, ""},
		{"state set malformed", []string{"state", "set", `{`}, ""},
		{"state set stdin non-object", []string{"state", "set", "-"}, `[1]`},
		{"state set oversized", []string{"state", "set", "-"}, big},
		{"state patch non-object", []string{"state", "patch", `1`}, ""},
		{"state patch negative revision", []string{"state", "patch", `{}`, "--if-revision", "-1"}, ""},
		{"state patch non-numeric revision", []string{"state", "patch", `{}`, "--if-revision", "x"}, ""},
		{"state patch fractional revision", []string{"state", "patch", `{}`, "--if-revision", "1.5"}, ""},
		{"state clear without namespace", []string{"state", "clear"}, ""},
		{"state clear --force=maybe", []string{"state", "clear", "p.state", "--force=maybe"}, ""},
		{"state get takes no --force", []string{"state", "get", "s", "--force"}, ""},
		{"hook list takes no --force", []string{"hook", "list", "--force"}, ""},
		{"hook list with an argument", []string{"hook", "list", "x"}, ""},
		{"hook call does not exist", []string{"hook", "call", "x.y"}, ""},
		{"hook without a verb", []string{"hook"}, ""},
		{"state get takes no --if-revision", []string{"state", "get", "s", "--if-revision", "1"}, ""},
		{"state patch takes no --owner", []string{"state", "patch", `{}`, "--owner", "x"}, ""},
		{"action list with an argument", []string{"action", "list", "x"}, ""},
		{"action list takes no --arg", []string{"action", "list", "--arg", "a=b"}, ""},
		{"action invoke without id", []string{"action", "invoke"}, ""},
		{"action invoke --arg and --args-json", []string{"action", "invoke", "a.b", "--arg", "k=v", "--args-json", `{}`}, ""},
		{"action invoke --arg without =", []string{"action", "invoke", "a.b", "--arg", "kv"}, ""},
		{"action invoke --arg empty key", []string{"action", "invoke", "a.b", "--arg", "=v"}, ""},
		{"action invoke --args-json array", []string{"action", "invoke", "a.b", "--args-json", `[]`}, ""},
		{"action invoke --args-json stdin non-object", []string{"action", "invoke", "a.b", "--args-json", "-"}, `3`},
		{"event emit without name", []string{"event", "emit"}, ""},
		{"event emit --data non-object", []string{"event", "emit", "user.x", "--data", `"s"`}, ""},
		{"event emit --data stdin non-object", []string{"event", "emit", "user.x", "--data", "-"}, `true`},
		{"event emit takes no --args-json", []string{"event", "emit", "user.x", "--args-json", `{}`}, ""},
		{"event watch without pattern", []string{"event", "watch"}, ""},
		{"event watch two patterns", []string{"event", "watch", "a.*", "b.*"}, ""},
		{"state watch --timeout", []string{"state", "watch", "--timeout", "5s"}, ""},
		{"state watch --timeout 0", []string{"state", "watch", "--timeout=0"}, ""},
		{"event watch --timeout", []string{"event", "watch", "user.*", "--timeout", "1s"}, ""},
		{"negative timeout", []string{"state", "--timeout", "-1s"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, h := echoServer(t, map[string]any{})
			h.env.Stdin = strings.NewReader(tc.stdin)
			// The deadline only makes a regression fail fast: a watch that
			// wrongly connects would otherwise wait for its stream forever.
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if got := Main(ctx, append(tc.args, "--format", "json"), h.env); got != output.ExitUsage {
				t.Fatalf("exit = %d, want %d (stderr %q)", got, output.ExitUsage, h.errb.String())
			}
			if code, _, _ := errObject(t, h); code != "usage" {
				t.Fatalf("code = %s", code)
			}
			if srv.connections() != 0 {
				t.Fatalf("connected %d time(s); a usage error must send nothing", srv.connections())
			}
			if h.out.Len() != 0 {
				t.Fatalf("stdout = %q", h.out.String())
			}
		})
	}
}

func TestStemCommandsSendTheTokenWhenSet(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{}) }))
	h := newHarness(t, dir, map[string]string{EnvPluginToken: "tok-9"})
	if got := h.run("event", "emit", "user.x", "--format", "json"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	if req := lastRequest(t, srv); req.Token != "tok-9" {
		t.Fatalf("token = %q", req.Token)
	}
}

func TestWatchCommandsStream(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		method   string
		params   string
		own      string
		opening  any
		wantOpen string // json mode
		wantLine string
	}{
		{"state watch", []string{"state", "watch", "husk.focus", "husk.pods"}, "state.watch", `{"slices":["husk.focus","husk.pods"]}`,
			"state.changed", map[string]any{"seq": 3, "slices": []any{}}, `{"seq":3,"slices":[]}`, `{"slice":"husk.focus","revision":43}`},
		{"state watch all", []string{"state", "watch"}, "state.watch", `{"slices":[]}`,
			"state.changed", map[string]any{"seq": 3, "slices": []any{}}, `{"seq":3,"slices":[]}`, `{"slice":"husk.focus","revision":43}`},
		{"event watch", []string{"event", "watch", "user.*"}, "event.watch", `{"pattern":"user.*"}`,
			"event.deliver", map[string]any{}, `{}`, `{"name":"user.x","source":"user","data":{"a":1}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortDir(t)
			srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
				c.reply(req, tc.opening)
				c.send(protocol.Message{Method: "x.other", Params: json.RawMessage(`{"ignored":true}`)})
				c.send(protocol.Message{Method: tc.own, Params: json.RawMessage(tc.wantLine)})
				c.send(protocol.Message{Method: tc.own, Params: json.RawMessage(tc.wantLine)})
				c.send(protocol.Message{Method: tc.own, Params: json.RawMessage(tc.wantLine)})
				c.close()
			}))
			h := newHarness(t, dir, nil)
			if got := h.run(append(tc.args, "--format", "json")...); got != output.ExitTransport {
				t.Fatalf("exit = %d, want %d (a stream never exits 0)", got, output.ExitTransport)
			}
			req := lastRequest(t, srv)
			if req.Method != tc.method {
				t.Fatalf("method = %s", req.Method)
			}
			assertJSONEqual(t, req.Params, tc.params)
			want := tc.wantOpen + "\n" + strings.Repeat(tc.wantLine+"\n", 3)
			if h.out.String() != want {
				t.Fatalf("stdout:\n%s\nwant:\n%s", h.out.String(), want)
			}
			if code, msg, _ := errObject(t, h); code != "transport" || msg != "instance went away" {
				t.Fatalf("error = %s %q", code, msg)
			}
		})
	}
}

func TestWatchTableModeIsGenericThenOneLinePerNotification(t *testing.T) {
	dir := shortDir(t)
	serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
		c.reply(req, map[string]any{"seq": 3, "slices": []any{}})
		c.send(protocol.Message{Method: "state.changed", Params: json.RawMessage(`{ "slice": "s",  "revision": 2 }`)})
		c.close()
	}))
	h := newHarness(t, dir, nil)
	if got := h.run("state", "watch", "--format", "table"); got != output.ExitTransport {
		t.Fatalf("exit = %d", got)
	}
	want := "seq     3\nslices  []\n" + `{"slice":"s","revision":2}` + "\n"
	if h.out.String() != want {
		t.Fatalf("stdout = %q, want %q", h.out.String(), want)
	}
	if h.errb.String() != "husk: instance went away\n" {
		t.Fatalf("stderr = %q", h.errb.String())
	}
}

func TestWatchOpeningErrorIsTheInstancesError(t *testing.T) {
	dir := shortDir(t)
	serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
		c.fail(req, protocol.CodeInvalidParams, "bad pattern")
	}))
	h := newHarness(t, dir, nil)
	if got := h.run("event", "watch", "*foo", "--format", "json"); got != output.ExitRemote {
		t.Fatalf("exit = %d", got)
	}
	if code, msg, _ := errObject(t, h); code != "invalid_params" || msg != "bad pattern" || h.out.Len() != 0 {
		t.Fatalf("error = %s %q, stdout %q", code, msg, h.out.String())
	}
}

func TestWatchProtocolErrorNotificationIsExit6(t *testing.T) {
	dir := shortDir(t)
	serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
		c.reply(req, map[string]any{})
		c.send(protocol.Message{Method: protocol.MethodProtocolError, Params: json.RawMessage(`{"code":"invalid_message","message":"nope"}`)})
	}))
	h := newHarness(t, dir, nil)
	if got := h.run("event", "watch", "a.*", "--format", "json"); got != output.ExitProtocol {
		t.Fatalf("exit = %d", got)
	}
}

func TestWatchHelpListsEveryVerb(t *testing.T) {
	h := newHarness(t, shortDir(t), nil)
	if got := h.run("state", "--help"); got != 0 {
		t.Fatalf("exit %d", got)
	}
	for _, s := range []string{"husk state get <slice>", "husk state watch [<slice>...]", "husk state patch <json> [--if-revision <n>]", "husk state clear <namespace> [--force]"} {
		if !strings.Contains(h.out.String(), s) {
			t.Fatalf("help lacks %q:\n%s", s, h.out.String())
		}
	}
}

func TestStateDumpHintsAtStateGetWhenTooLarge(t *testing.T) {
	for _, tc := range []struct {
		name, data, want string
	}{
		{"limit present", `{"limit":4194304}`, "snapshot with values exceeds one protocol line; use husk state get <slice>"},
		{"no limit, no hint", `{"other":1}`, "snapshot with values exceeds one protocol line"},
		{"no data, no hint", ``, "snapshot with values exceeds one protocol line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortDir(t)
			serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
				e := &protocol.Error{Code: protocol.CodeInvalidParams, Message: "snapshot with values exceeds one protocol line"}
				if tc.data != "" {
					e.Data = json.RawMessage(tc.data)
				}
				c.send(protocol.Message{ID: req.ID, Error: e})
			}))
			h := newHarness(t, dir, nil)
			if got := h.run("state", "dump", "--format", "json"); got != output.ExitRemote {
				t.Fatalf("exit = %d", got)
			}
			code, msg, data := errObject(t, h)
			if code != "invalid_params" || msg != tc.want || string(data) != tc.data {
				t.Fatalf("error = %s %q %s", code, msg, data)
			}
		})
	}
	t.Run("other codes get no hint", func(t *testing.T) {
		dir := shortDir(t)
		serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
			c.send(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: protocol.CodeInternal, Message: "boom", Data: json.RawMessage(`{"limit":1}`)}})
		}))
		h := newHarness(t, dir, nil)
		h.run("state", "dump", "--format", "json")
		if _, msg, _ := errObject(t, h); msg != "boom" {
			t.Fatalf("message = %q", msg)
		}
	})
}

func TestWatchOpeningBoundIsTransportExit5(t *testing.T) {
	saved := openingBound
	openingBound = 100 * time.Millisecond
	t.Cleanup(func() { openingBound = saved })
	for _, tc := range []struct {
		name string
		h    func(*srvConn, protocol.Message)
	}{
		{"ping answered, watch never", pingOK("a", func(*srvConn, protocol.Message) {})},
		{"ping never answered", func(*srvConn, protocol.Message) {}},
	} {
		for _, args := range [][]string{{"state", "watch"}, {"event", "watch", "user.*"}} {
			t.Run(tc.name+" "+args[0], func(t *testing.T) {
				dir := shortDir(t)
				srv := serve(t, dir, "a", tc.h)
				h := newHarness(t, dir, nil)
				// An explicit socket skips discovery, whose own probe would
				// find the silent instance unresponsive first.
				// The outer deadline only makes a lost bound fail fast.
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				start := time.Now()
				got := Main(ctx, append(args, "--socket", srv.Socket, "--format", "json"), h.env)
				if elapsed := time.Since(start); elapsed > time.Second {
					t.Fatalf("took %s: the opening bound did not end the wait, the outer deadline did", elapsed)
				}
				if got != output.ExitTransport {
					t.Fatalf("exit = %d, want %d", got, output.ExitTransport)
				}
				if code, msg, _ := errObject(t, h); code != "transport" || msg != "instance did not answer within 100ms" {
					t.Fatalf("error = %s %q", code, msg)
				}
				if h.out.Len() != 0 {
					t.Fatalf("stdout = %q", h.out.String())
				}
			})
		}
	}
}
