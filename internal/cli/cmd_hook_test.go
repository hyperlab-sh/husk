package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// hookCatalog is a hook.list result in reverse id order, so sorting shows.
const hookCatalog = `{"hooks":[` +
	`{"id":"notes.before-save","owner":"notes","kind":"gate","timeout_ms":2000,"subscribers":[{"plugin_id":"audit","priority":10,"live":true},{"plugin_id":"sync","priority":20,"live":false}]},` +
	`{"id":"husk.before-state-clear","owner":"husk","kind":"gate","timeout_ms":2000,"subscribers":[]},` +
	`{"id":"husk.on-render","owner":"husk","kind":"transform","timeout_ms":500,"subscribers":[{"plugin_id":"theme","priority":0,"live":true}]}` +
	`]}`

// hookServer answers hook.list with the catalog and state.clear with clear.
func hookServer(t *testing.T, clear func(c *srvConn, req protocol.Message)) (*fakeServer, *harness) {
	t.Helper()
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
		switch req.Method {
		case protocol.MethodHookList:
			c.send(protocol.Message{ID: req.ID, Result: json.RawMessage(hookCatalog)})
		case protocol.MethodStateClear:
			clear(c, req)
		default:
			c.fail(req, protocol.CodeDenied, "denied")
		}
	}))
	return srv, newHarness(t, dir, nil)
}

func hookIDs(t *testing.T, stdout string) []string {
	t.Helper()
	var res struct {
		Hooks []struct {
			ID string `json:"id"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout %q: %v", stdout, err)
	}
	ids := []string{}
	for _, h := range res.Hooks {
		ids = append(ids, h.ID)
	}
	return ids
}

func TestHookListSendsEmptyParamsAndSortsByID(t *testing.T) {
	srv, h := hookServer(t, nil)
	if got := h.run("hook", "list", "--format", "json"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	req := lastRequest(t, srv)
	if req.Method != protocol.MethodHookList {
		t.Fatalf("method = %s", req.Method)
	}
	assertJSONEqual(t, req.Params, `{}`)
	if got := strings.Join(hookIDs(t, h.out.String()), ","); got != "husk.before-state-clear,husk.on-render,notes.before-save" {
		t.Fatalf("ids = %s", got)
	}
	if !strings.Contains(h.out.String(), `"subscribers":[{"plugin_id":"audit","priority":10,"live":true}`) {
		t.Fatalf("entries were reshaped: %s", h.out.String())
	}
}

func TestHookListOwnerFiltersClientSide(t *testing.T) {
	srv, h := hookServer(t, nil)
	if got := h.run("hook", "list", "--owner", "husk", "--format", "json"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	assertJSONEqual(t, lastRequest(t, srv).Params, `{}`)
	if got := strings.Join(hookIDs(t, h.out.String()), ","); got != "husk.before-state-clear,husk.on-render" {
		t.Fatalf("ids = %s", got)
	}

	h = newHarness(t, h.dir, nil)
	if got := h.run("hook", "list", "--owner", "nobody", "--format", "json"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	if h.out.String() != `{"hooks":[]}`+"\n" {
		t.Fatalf("stdout = %q", h.out.String())
	}
}

func TestHookListTable(t *testing.T) {
	_, h := hookServer(t, nil)
	if got := h.run("hook", "list", "--format", "table"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	lines := strings.Split(strings.TrimRight(h.out.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("table:\n%s", h.out.String())
	}
	for i, want := range [][]string{
		{"HOOK", "OWNER", "KIND", "TIMEOUT", "SUBSCRIBERS"},
		{"husk.before-state-clear", "husk", "gate", "2s"},
		{"husk.on-render", "husk", "transform", "500ms", "theme (priority 0, live)"},
		{"notes.before-save", "notes", "gate", "2s", "audit (priority 10, live), sync (priority 20, not live)"},
	} {
		f := strings.Join(strings.Fields(lines[i]), " ")
		if w := strings.Join(want, " "); f != w {
			t.Fatalf("line %d = %q, want %q", i, f, w)
		}
	}
}

func TestHookListUnreadableReplyIsAProtocolError(t *testing.T) {
	dir := shortDir(t)
	serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, []int{1}) }))
	h := newHarness(t, dir, nil)
	if got := h.run("hook", "list", "--format", "json"); got != output.ExitProtocol {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
}

func TestHookCallIsNotACommandAndHelpDoesNotSuggestIt(t *testing.T) {
	h := newHarness(t, shortDir(t), nil)
	if got := h.run("hook", "call", "x.y"); got != output.ExitUsage {
		t.Fatalf("exit %d", got)
	}
	h = newHarness(t, shortDir(t), nil)
	if got := h.run("hook", "--help"); got != 0 {
		t.Fatalf("exit %d", got)
	}
	if !strings.Contains(h.out.String(), "husk hook list [--owner <id>]") || strings.Contains(h.out.String(), "call") {
		t.Fatalf("help:\n%s", h.out.String())
	}
	h = newHarness(t, shortDir(t), nil)
	if got := h.run("help"); got != 0 || strings.Contains(h.out.String(), "hook call") {
		t.Fatalf("exit %d, help:\n%s", got, h.out.String())
	}
}

func TestRPCHookCallSurfacesTheHostsDeniedUnchanged(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
		c.send(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: protocol.CodeDenied, Message: "not the owner",
			Data: json.RawMessage(`{"hook":"x.y"}`)}})
	}))
	h := newHarness(t, dir, nil)
	if got := h.run("rpc", "hook.call", `{"hook":"x.y"}`, "--format", "json"); got != output.ExitRemote {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	req := lastRequest(t, srv)
	if req.Method != protocol.MethodHookCall {
		t.Fatalf("method = %s", req.Method)
	}
	assertJSONEqual(t, req.Params, `{"hook":"x.y"}`)
	code, msg, data := errObject(t, h)
	if code != "denied" || msg != "not the owner" || string(data) != `{"hook":"x.y"}` {
		t.Fatalf("error = %s %q %s", code, msg, data)
	}
}

const vetoedData = `{"reason":"vetoed","vetoes":[{"plugin_id":"notes","reason":"unsaved edits"},{"plugin_id":"sync","reason":""}]}`

func vetoed(c *srvConn, req protocol.Message) {
	c.send(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: protocol.CodeDenied,
		Message: "state.clear vetoed", Data: json.RawMessage(vetoedData)}})
}

func TestStateClearForceSendsForceTrueAndOnlyThen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		params string
	}{
		{"without", []string{"state", "clear", "p.state"}, `{"namespace":"p.state"}`},
		{"with", []string{"state", "clear", "p.state", "--force"}, `{"namespace":"p.state","force":true}`},
		{"flag first", []string{"state", "clear", "--force", "p.state"}, `{"namespace":"p.state","force":true}`},
		{"explicit false", []string{"state", "clear", "p.state", "--force=false"}, `{"namespace":"p.state"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, h := hookServer(t, func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{"cleared": true}) })
			if got := h.run(append(tc.args, "--format", "json")...); got != 0 {
				t.Fatalf("exit %d: %s", got, h.errb.String())
			}
			req := lastRequest(t, srv)
			if req.Method != protocol.MethodStateClear {
				t.Fatalf("method = %s", req.Method)
			}
			assertJSONEqual(t, req.Params, tc.params)
			if h.out.String() != `{"cleared":true}`+"\n" {
				t.Fatalf("stdout = %q", h.out.String())
			}
		})
	}
}

func TestStateClearVetoedExitsOneWithTheVetoes(t *testing.T) {
	t.Run("table lists each plugin_id: reason on stderr", func(t *testing.T) {
		srv, h := hookServer(t, vetoed)
		if got := h.run("state", "clear", "p.state", "--format", "table"); got != output.ExitRemote {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		if h.out.Len() != 0 {
			t.Fatalf("stdout = %q", h.out.String())
		}
		assertJSONEqual(t, lastRequest(t, srv).Params, `{"namespace":"p.state"}`)
		if want := "husk: state.clear vetoed\n  notes: unsaved edits\n  sync: \n"; h.errb.String() != want {
			t.Fatalf("stderr = %q, want %q", h.errb.String(), want)
		}
	})
	t.Run("a reason with control characters stays on one line", func(t *testing.T) {
		_, h := hookServer(t, func(c *srvConn, req protocol.Message) {
			c.send(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: protocol.CodeDenied, Message: "state.clear vetoed",
				Data: json.RawMessage(`{"reason":"vetoed","vetoes":[{"plugin_id":"notes","reason":"line1\n\u001b[31mred"},{"plugin_id":"sync","reason":"ok"}]}`)}})
		})
		if got := h.run("state", "clear", "p.state", "--format", "table"); got != output.ExitRemote {
			t.Fatalf("exit %d", got)
		}
		if n := len(strings.Split(strings.TrimRight(h.errb.String(), "\n"), "\n")); n != 3 {
			t.Fatalf("stderr has %d lines:\n%q", n, h.errb.String())
		}
		if strings.ContainsRune(h.errb.String(), 0x1b) {
			t.Fatalf("raw ESC in stderr: %q", h.errb.String())
		}
		if !strings.Contains(h.errb.String(), `notes: "line1\n\u001b[31mred"`) {
			t.Fatalf("stderr = %q", h.errb.String())
		}
	})
	t.Run("json prints the error object with data.vetoes", func(t *testing.T) {
		_, h := hookServer(t, vetoed)
		if got := h.run("state", "clear", "p.state", "--format", "json"); got != output.ExitRemote {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		code, msg, data := errObject(t, h)
		if code != "denied" || msg != "state.clear vetoed" {
			t.Fatalf("error = %s %q", code, msg)
		}
		assertJSONEqual(t, data, vetoedData)
	})
	t.Run("a denied that is not a veto is left alone", func(t *testing.T) {
		_, h := hookServer(t, func(c *srvConn, req protocol.Message) { c.fail(req, protocol.CodeDenied, "user only") })
		if got := h.run("state", "clear", "p.state", "--format", "table"); got != output.ExitRemote {
			t.Fatalf("exit %d", got)
		}
		if h.errb.String() != "husk: user only\n" {
			t.Fatalf("stderr = %q", h.errb.String())
		}
	})
}
