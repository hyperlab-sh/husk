package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// errObject decodes the one JSON error object a failed command printed.
func errObject(t *testing.T, h *harness) (code, message string, data json.RawMessage) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(h.errb.String(), "\n"), "\n")
	var e struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &e); err != nil || e.Error.Code == "" {
		t.Fatalf("stderr %q does not end in an error object: %v", h.errb.String(), err)
	}
	return e.Error.Code, e.Error.Message, e.Error.Data
}

// withRegistered runs a test with an empty Register table and restores it.
func withRegistered(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	saved := registered
	registered = map[string]output.Command{}
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		registered = saved
		registryMu.Unlock()
	})
}

func TestUnknownSubcommandIsUsage(t *testing.T) {
	t.Run("table mode: message and usage on stderr", func(t *testing.T) {
		h := newHarness(t, shortDir(t), nil)
		if got := h.run("frobnicate", "--format", "table"); got != output.ExitUsage {
			t.Fatalf("exit = %d, want %d", got, output.ExitUsage)
		}
		if !strings.Contains(h.errb.String(), `husk: unknown command "frobnicate"`) || !strings.Contains(h.errb.String(), "husk rpc") {
			t.Fatalf("stderr = %q", h.errb.String())
		}
		if h.out.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", h.out.String())
		}
	})
	t.Run("json mode: one error object per line, no usage text", func(t *testing.T) {
		h := newHarness(t, shortDir(t), nil)
		if got := h.run("frobnicate", "--format", "json"); got != output.ExitUsage {
			t.Fatalf("exit = %d, want %d", got, output.ExitUsage)
		}
		if code, _, _ := errObject(t, h); code != "usage" || strings.Count(h.errb.String(), "\n") != 1 {
			t.Fatalf("stderr = %q", h.errb.String())
		}
	})
}

func TestHelpIsUsageOnStdoutWithExit0(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"ping", "--help"}, {"instances", "-h"}} {
		h := newHarness(t, shortDir(t), nil)
		if got := h.run(args...); got != 0 || !strings.HasPrefix(h.out.String(), "usage: ") || h.errb.Len() != 0 {
			t.Fatalf("%v: exit %d stdout %q stderr %q", args, got, h.out.String(), h.errb.String())
		}
	}
}

func TestUsageErrorsAreReportedBeforeAnyLookup(t *testing.T) {
	big := `{"a":"` + strings.Repeat("x", protocol.MaxLineBytes) + `"}`
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
	}{
		{"both targets", []string{"ping", "--instance", "a", "--socket", "/x"}, ""},
		{"bad format", []string{"ping", "--format", "xml"}, ""},
		{"negative timeout", []string{"ping", "--timeout", "-1s"}, ""},
		{"unparsable timeout", []string{"ping", "--timeout", "soon"}, ""},
		{"unknown flag", []string{"ping", "--nope"}, ""},
		{"missing flag value", []string{"ping", "--instance"}, ""},
		{"ping with args", []string{"ping", "extra"}, ""},
		{"instances with args", []string{"instances", "extra"}, ""},
		{"instances rejects --instance", []string{"instances", "--instance", "a"}, ""},
		{"instances rejects --socket", []string{"instances", "--socket", "/x"}, ""},
		{"instances rejects --timeout", []string{"instances", "--timeout", "1s"}, ""},
		{"instances rejects --timeout 0", []string{"instances", "--timeout=0"}, ""},
		{"rpc without method", []string{"rpc"}, ""},
		{"rpc bad method", []string{"rpc", "Nope"}, ""},
		{"rpc one segment", []string{"rpc", "state"}, ""},
		{"rpc invalid json", []string{"rpc", "a.b", "{"}, ""},
		{"rpc params not an object", []string{"rpc", "a.b", "[1]"}, ""},
		{"rpc params null", []string{"rpc", "a.b", "null"}, ""},
		{"rpc too many args", []string{"rpc", "a.b", "{}", "{}"}, ""},
		{"rpc stdin not an object", []string{"rpc", "a.b", "-"}, "[1]"},
		{"rpc request over the line limit", []string{"rpc", "a.b", "-"}, big},
		{"invalid instance name", []string{"ping", "--instance", "Foo_1"}, ""},
		{"invalid HUSK_INSTANCE", []string{"ping"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortDir(t)
			srv := serve(t, dir, "a", pingOK("a", nil))
			var env map[string]string
			if tc.name == "invalid HUSK_INSTANCE" {
				env = map[string]string{instance.EnvInstance: "no such"}
			}
			h := newHarness(t, dir, env)
			h.env.Stdin = strings.NewReader(tc.stdin)
			h.env.RuntimeDir = func() (string, bool, error) {
				// Resolution of an invalid id is the instance package's
				// usage error and may look at the directory; every other
				// usage error must come first.
				if tc.name != "invalid HUSK_INSTANCE" && tc.name != "invalid instance name" {
					t.Error("the runtime directory was looked up before the usage error")
				}
				return dir, true, nil
			}
			if got := h.run(tc.args...); got != output.ExitUsage {
				t.Fatalf("exit = %d, want %d; stderr %q", got, output.ExitUsage, h.errb.String())
			}
			if h.out.Len() != 0 || srv.connections() != 0 {
				t.Fatalf("stdout %q, %d connections: nothing may be sent", h.out.String(), srv.connections())
			}
			if code, _, _ := errObject(t, h); code != "usage" {
				t.Fatalf("code = %q", code)
			}
		})
	}
}

func TestInstancesAcceptsFormat(t *testing.T) {
	h := newHarness(t, shortDir(t), nil)
	if got := h.run("instances", "--format", "table"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
}

func TestUsageErrorFollowsTheRequestedFormatEvenWhenParsingFailed(t *testing.T) {
	h := newHarness(t, shortDir(t), nil)
	h.env.StdoutIsTTY = true // the default would be table
	if got := h.run("ping", "--format=json", "--nope"); got != output.ExitUsage {
		t.Fatalf("exit %d", got)
	}
	if code, _, _ := errObject(t, h); code != "usage" {
		t.Fatalf("stderr = %q", h.errb.String())
	}
}

func TestGlobalFlagsMayFollowOrPrecedePositionals(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{"ok": true}) }))
	for _, args := range [][]string{
		{"rpc", "--socket", srv.Socket, "x.y", "{}"},
		{"rpc", "x.y", "--socket", srv.Socket, "{}"},
		{"rpc", "x.y", "{}", "--socket", srv.Socket},
		{"rpc", "x.y", "{}", "--socket=" + srv.Socket},
		{"rpc", "--socket", srv.Socket, "--", "x.y", "{}"},
	} {
		h := newHarness(t, dir, nil)
		if got := h.run(args...); got != 0 || strings.TrimSpace(h.out.String()) != `{"ok":true}` {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q", args, got, h.out.String(), h.errb.String())
		}
	}
}

func TestRepeatedFlagLastValueWins(t *testing.T) {
	dir := shortDir(t)
	a := serve(t, dir, "a", pingOK("a", nil))
	b := serve(t, dir, "b", pingOK("b", nil))
	h := newHarness(t, dir, nil)
	if got := h.run("ping", "--socket", a.Socket, "--socket", b.Socket, "--format", "table", "--format", "json"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	var ping protocol.SessionPingResult
	if err := json.Unmarshal(h.out.Bytes(), &ping); err != nil || ping.ID != "b" || a.connections() != 0 {
		t.Fatalf("stdout %q (%v): want json from b; a saw %d connections", h.out.String(), err, a.connections())
	}
}

func TestLoneDashIsAnArgumentNotAFlag(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{}) }))
	h := newHarness(t, dir, nil)
	h.env.Stdin = strings.NewReader(`{"from":"stdin"}`)
	if got := h.run("rpc", "x.y", "-", "--socket", srv.Socket); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	reqs := srv.requests()
	if string(reqs[len(reqs)-1].Params) != `{"from":"stdin"}` {
		t.Fatalf("params = %s", reqs[len(reqs)-1].Params)
	}
}

func TestResolution(t *testing.T) {
	type env = map[string]string
	for _, tc := range []struct {
		name     string
		running  []string // instance ids with a live server
		args     []string
		env      func(socks map[string]string) env
		wantExit int
		wantID   string // instance the ping must have reached
		wantErr  string
	}{
		{name: "zero instances", args: []string{"ping"}, wantExit: output.ExitNoInstance, wantErr: "no instance is running"},
		{name: "one instance", running: []string{"a"}, args: []string{"ping"}, wantID: "a"},
		{name: "many instances", running: []string{"a", "b"}, args: []string{"ping"}, wantExit: output.ExitAmbiguous, wantErr: "2 instances are running; choose one with --instance <id>"},
		{name: "instance flag picks one of many", running: []string{"a", "b"}, args: []string{"ping", "--instance", "b"}, wantID: "b"},
		{name: "instance flag not registered", running: []string{"a"}, args: []string{"ping", "--instance", "zz"}, wantExit: output.ExitNoInstance, wantErr: `instance "zz" is not running`},
		{name: "socket flag", running: []string{"a", "b"}, args: []string{"ping", "--socket", "@b"}, wantID: "b"},
		{name: "socket flag dead", running: []string{"a"}, args: []string{"ping", "--socket", "@gone"}, wantExit: output.ExitNoInstance, wantErr: "nobody is listening on "},
		{name: "env socket path picks one of many", running: []string{"a", "b"}, args: []string{"ping"},
			env: func(s env) env { return env{instance.EnvSocketPath: s["a"]} }, wantID: "a"},
		{name: "env instance picks one of many", running: []string{"a", "b"}, args: []string{"ping"},
			env: func(s env) env { return env{instance.EnvInstance: "b"} }, wantID: "b"},
		{name: "flag beats env", running: []string{"a", "b"}, args: []string{"ping", "--instance", "a"},
			env: func(s env) env { return env{instance.EnvInstance: "b"} }, wantID: "a"},
		{name: "env socket path beats env instance", running: []string{"a", "b"}, args: []string{"ping"},
			env: func(s env) env { return env{instance.EnvSocketPath: s["a"], instance.EnvInstance: "b"} }, wantID: "a"},
		{name: "dead env socket never falls through to discovery", running: []string{"a"}, args: []string{"ping"},
			env:      func(s env) env { return env{instance.EnvSocketPath: s["a"] + ".dead"} },
			wantExit: output.ExitNoInstance, wantErr: "nobody is listening on "},
		{name: "unregistered env instance never falls through to discovery", running: []string{"a"}, args: []string{"ping"},
			env:      func(s env) env { return env{instance.EnvInstance: "zz"} },
			wantExit: output.ExitNoInstance, wantErr: `instance "zz" is not running`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortDir(t)
			socks := env{}
			servers := map[string]*fakeServer{}
			for _, id := range tc.running {
				servers[id] = serve(t, dir, id, pingOK(id, nil))
				socks[id] = servers[id].Socket
			}
			var getenv env
			if tc.env != nil {
				getenv = tc.env(socks)
			}
			args := append([]string(nil), tc.args...)
			for i, a := range args { // "@id" stands for that server's socket path
				if strings.HasPrefix(a, "@") {
					if s, ok := socks[a[1:]]; ok {
						args[i] = s
					} else {
						args[i] = dir + "/" + a[1:] + ".sock"
					}
				}
			}
			h := newHarness(t, dir, getenv)
			// Discovery's probe is a connection of its own; count the
			// command's only.
			h.env.Deps = &instance.Deps{Ping: func(string) error { return nil }}
			args = append(args, "--format", "json")
			if got := h.run(args...); got != tc.wantExit {
				t.Fatalf("exit = %d, want %d; stdout %q stderr %q", got, tc.wantExit, h.out.String(), h.errb.String())
			}
			if tc.wantErr != "" {
				if _, msg, _ := errObject(t, h); !strings.Contains(msg, tc.wantErr) {
					t.Fatalf("message %q lacks %q", msg, tc.wantErr)
				}
				return
			}
			var ping protocol.SessionPingResult
			if err := json.Unmarshal(h.out.Bytes(), &ping); err != nil || ping.ID != tc.wantID {
				t.Fatalf("stdout %q (%v): want ping from %q", h.out.String(), err, tc.wantID)
			}
			for id, s := range servers { // only the chosen instance was connected to, once
				want := 0
				if id == tc.wantID {
					want = 1
				}
				if s.connections() != want {
					t.Fatalf("instance %s saw %d connections, want %d", id, s.connections(), want)
				}
			}
		})
	}
}

func TestExplicitTargetsAreNotProbedBeforeTheCommandConnects(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", nil))
	for _, args := range [][]string{{"--socket", srv.Socket}, {"--instance", "a"}} {
		h := newHarness(t, dir, nil)
		h.env.Deps = &instance.Deps{Ping: func(p string) error {
			t.Errorf("probe of %s: an explicit target is checked by the command's own connection", p)
			return nil
		}}
		if got := h.run(append([]string{"ping"}, args...)...); got != 0 {
			t.Fatalf("%v: exit %d: %s", args, got, h.errb.String())
		}
	}
	if srv.connections() != 2 { // one per command, nothing else
		t.Fatalf("connections = %d, want 2", srv.connections())
	}
}

func TestOneConnectionPerCommandWithSessionPingFirst(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{}) }))
	h := newHarness(t, dir, nil)
	if got := h.run("rpc", "x.y", "--instance", "a"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	if srv.connections() != 1 || !slices.Equal(srv.methods(), []string{protocol.MethodSessionPing, "x.y"}) {
		t.Fatalf("connections %d methods %v: want one connection, ping then x.y", srv.connections(), srv.methods())
	}
}

func TestPingCommandSendsOnlyTheVersionCheckPing(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", nil))
	h := newHarness(t, dir, nil)
	if got := h.run("ping", "--instance", "a"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	if !slices.Equal(srv.methods(), []string{protocol.MethodSessionPing}) {
		t.Fatalf("methods = %v, want exactly one session.ping", srv.methods())
	}
}

func TestStaleEntryIsRemovedWhenTheConnectIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"--instance", []string{"ping", "--instance", "ghost"}, nil},
		{"HUSK_INSTANCE", []string{"ping"}, map[string]string{instance.EnvInstance: "ghost"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortDir(t)
			register(t, dir, "ghost", instance.SocketPath(dir, "ghost"), protocol.Version) // entry, no socket
			h := newHarness(t, dir, tc.env)
			if got := h.run(append(tc.args, "--format", "table")...); got != output.ExitNoInstance {
				t.Fatalf("exit = %d, want %d", got, output.ExitNoInstance)
			}
			if h.errb.String() != "husk: instance \"ghost\" is not running (stale entry removed)\n" {
				t.Fatalf("stderr = %q", h.errb.String())
			}
			if _, err := os.Stat(instance.EntryPath(dir, "ghost")); !os.IsNotExist(err) {
				t.Fatalf("stale entry still on disk: %v", err)
			}
		})
	}
}

func TestRefusedBareSocketPathRemovesNothing(t *testing.T) {
	dir := shortDir(t)
	register(t, dir, "other", instance.SocketPath(dir, "other"), protocol.Version)
	h := newHarness(t, dir, nil)
	gone := filepath.Join(dir, "gone.sock")
	if got := h.run("ping", "--socket", gone, "--format", "table"); got != output.ExitNoInstance {
		t.Fatalf("exit = %d", got)
	}
	if h.errb.String() != "husk: nobody is listening on "+gone+"\n" {
		t.Fatalf("stderr = %q", h.errb.String())
	}
	if _, err := os.Stat(instance.EntryPath(dir, "other")); err != nil {
		t.Fatalf("an entry was removed for a bare socket path: %v", err)
	}
}

func TestDeadEnvSocketPathMessageNamesTheVariable(t *testing.T) {
	dir := shortDir(t)
	gone := filepath.Join(dir, "gone.sock")
	h := newHarness(t, dir, map[string]string{instance.EnvSocketPath: gone})
	if got := h.run("ping", "--format", "table"); got != output.ExitNoInstance {
		t.Fatalf("exit = %d", got)
	}
	if want := "husk: nobody is listening on " + gone + "; unset HUSK_SOCKET_PATH or pass --instance\n"; h.errb.String() != want {
		t.Fatalf("stderr = %q, want %q", h.errb.String(), want)
	}
}

func TestProtocolMajorIsCheckedOnEveryTargetBeforeAnythingElseIsSent(t *testing.T) {
	for _, tc := range []struct {
		name string
		via  func(sock string) (args []string, env map[string]string)
	}{
		{"--socket", func(s string) ([]string, map[string]string) { return []string{"--socket", s}, nil }},
		{"HUSK_SOCKET_PATH", func(s string) ([]string, map[string]string) {
			return nil, map[string]string{instance.EnvSocketPath: s}
		}},
		{"--instance", func(string) ([]string, map[string]string) { return []string{"--instance", "a"}, nil }},
		{"discovery", func(string) ([]string, map[string]string) { return nil, nil }},
	} {
		for _, cmd := range [][]string{{"ping"}, {"rpc", "x.y"}} {
			t.Run(tc.name+" "+cmd[0], func(t *testing.T) {
				dir := shortDir(t)
				srv := serve(t, dir, "a", func(c *srvConn, req protocol.Message) {
					c.reply(req, protocol.SessionPingResult{ID: "a", PID: 1, ProtocolVersion: "3.1"})
				})
				register(t, dir, "a", srv.Socket, "2.0") // the entry's version is informational only
				args, env := tc.via(srv.Socket)
				h := newHarness(t, dir, env)
				h.env.Deps = &instance.Deps{Ping: func(string) error { return nil }}
				if got := h.run(append(append([]string(nil), cmd...), args...)...); got != output.ExitProtocol {
					t.Fatalf("exit = %d, want %d; stderr %q", got, output.ExitProtocol, h.errb.String())
				}
				if !strings.Contains(h.errb.String(), "instance speaks protocol 3.x, this CLI speaks 2.x") {
					t.Fatalf("stderr = %q", h.errb.String())
				}
				if !slices.Equal(srv.methods(), []string{protocol.MethodSessionPing}) {
					t.Fatalf("methods = %v: the command was sent to an instance of another major", srv.methods())
				}
			})
		}
	}
}

func TestMissingOrUnparsableVersionIsExit6(t *testing.T) {
	for _, version := range []string{"", "two", "x.0", "2."} {
		t.Run("version "+version, func(t *testing.T) {
			dir := shortDir(t)
			srv := serve(t, dir, "a", func(c *srvConn, req protocol.Message) {
				c.reply(req, protocol.SessionPingResult{ID: "a", PID: 1, ProtocolVersion: version})
			})
			h := newHarness(t, dir, nil)
			if got := h.run("rpc", "x.y", "--socket", srv.Socket); got != output.ExitProtocol {
				t.Fatalf("exit = %d, want %d; stderr %q", got, output.ExitProtocol, h.errb.String())
			}
			if code, _, _ := errObject(t, h); code != "protocol" || len(srv.methods()) != 1 {
				t.Fatalf("code %q methods %v", code, srv.methods())
			}
		})
	}
}

func TestUnreadablePingReplyIsExit6(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", func(c *srvConn, req protocol.Message) { c.reply(req, []int{1}) })
	h := newHarness(t, dir, nil)
	if got := h.run("ping", "--socket", srv.Socket); got != output.ExitProtocol {
		t.Fatalf("exit = %d, want %d; stderr %q", got, output.ExitProtocol, h.errb.String())
	}
}

func TestRPCResultAndErrors(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
		switch req.Method {
		case "state.get":
			c.reply(req, map[string]any{"revision": 3, "entries": []string{"a", "b"}})
		case "state.clear":
			c.send(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: protocol.CodeDenied, Message: "user only", Data: []byte(`{"reason":"needs_grant"}`)}})
		case "x.lineerr":
			c.fail(req, protocol.CodeLineTooLong, "too long")
		default:
			c.fail(req, protocol.CodeUnknownMethod, "no handler for "+req.Method)
		}
	}))
	t.Run("result as json", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		if got := h.run("rpc", "state.get", `{"slice":"x"}`, "--socket", srv.Socket); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		if want := `{"entries":["a","b"],"revision":3}` + "\n"; h.out.String() != want {
			t.Fatalf("stdout = %q, want %q", h.out.String(), want)
		}
		var params map[string]string
		for _, r := range srv.requests() {
			if r.Method == "state.get" {
				_ = json.Unmarshal(r.Params, &params)
			}
		}
		if params["slice"] != "x" {
			t.Fatalf("params sent = %v", params)
		}
	})
	t.Run("absent params are sent as an empty object, never omitted", func(t *testing.T) {
		quiet := serve(t, shortDir(t), "q", pingOK("q", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{}) }))
		newHarness(t, dir, nil).run("rpc", "x.y", "--socket", quiet.Socket)
		reqs := quiet.requests()
		if len(reqs) != 2 {
			t.Fatalf("requests = %d", len(reqs))
		}
		for _, r := range reqs {
			if string(r.Params) != "{}" {
				t.Fatalf("%s params = %q, want {}", r.Method, r.Params)
			}
		}
	})
	t.Run("error reply in json mode keeps code, message and data", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		if got := h.run("rpc", "state.clear", "--socket", srv.Socket); got != output.ExitRemote {
			t.Fatalf("exit = %d, want %d", got, output.ExitRemote)
		}
		want := `{"error":{"code":"denied","data":{"reason":"needs_grant"},"message":"user only"}}` + "\n"
		if h.errb.String() != want || h.out.Len() != 0 {
			t.Fatalf("stderr = %q stdout = %q", h.errb.String(), h.out.String())
		}
	})
	t.Run("error reply in table mode", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		if got := h.run("rpc", "no.such", "--socket", srv.Socket, "--format", "table"); got != output.ExitRemote {
			t.Fatalf("exit = %d, want %d", got, output.ExitRemote)
		}
		if h.errb.String() != "husk: no handler for no.such\n" || h.out.Len() != 0 {
			t.Fatalf("stderr = %q stdout = %q", h.errb.String(), h.out.String())
		}
	})
	t.Run("a reader error reply is exit 6 with its code", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		if got := h.run("rpc", "x.lineerr", "--socket", srv.Socket); got != output.ExitProtocol {
			t.Fatalf("exit = %d, want %d", got, output.ExitProtocol)
		}
		if code, _, _ := errObject(t, h); code != "line_too_long" {
			t.Fatalf("code = %q", code)
		}
	})
}

func TestOversizedRequestIsRefusedBeforeConnecting(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", nil))
	h := newHarness(t, dir, nil)
	h.env.Stdin = strings.NewReader(`{"a":"` + strings.Repeat("x", protocol.MaxLineBytes) + `"}`)
	if got := h.run("rpc", "x.y", "-", "--socket", srv.Socket, "--format", "table"); got != output.ExitUsage {
		t.Fatalf("exit = %d, want %d", got, output.ExitUsage)
	}
	if h.errb.String() != "husk: request exceeds the 4 MiB line limit\n" || srv.connections() != 0 {
		t.Fatalf("stderr %q, %d connections", h.errb.String(), srv.connections())
	}
}

func TestParamsThatFitButWithTheEnvelopeDoNotAreRefusedBeforeConnecting(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", nil))
	h := newHarness(t, dir, nil)
	// Exactly the limit for the params alone: the request line is longer.
	pad := protocol.MaxLineBytes - len(`{"a":""}`)
	h.env.Stdin = strings.NewReader(`{"a":"` + strings.Repeat("x", pad) + `"}`)
	if got := h.run("rpc", "x.y", "-", "--socket", srv.Socket, "--format", "table"); got != output.ExitUsage {
		t.Fatalf("exit = %d, want %d; stderr %.200q", got, output.ExitUsage, h.errb.String())
	}
	if h.errb.String() != "husk: request exceeds the 4 MiB line limit\n" || srv.connections() != 0 {
		t.Fatalf("stderr %.200q, %d connections", h.errb.String(), srv.connections())
	}
}

func TestRequestJustUnderTheLimitIsSent(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{}) }))
	h := newHarness(t, dir, nil)
	// The envelope around the params is a few dozen bytes.
	h.env.Stdin = strings.NewReader(`{"a":"` + strings.Repeat("x", protocol.MaxLineBytes-200) + `"}`)
	if got := h.run("rpc", "x.y", "-", "--socket", srv.Socket); got != 0 {
		t.Fatalf("exit %d: %.200s", got, h.errb.String())
	}
}

func TestOutputFormatFollowsTTYUnlessOverridden(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
		c.reply(req, []map[string]any{{"name": "x", "n": 1}, {"name": "y", "n": 2}})
	}))
	for _, tc := range []struct {
		name   string
		tty    bool
		format string
		json   bool
	}{
		{"piped defaults to json", false, "", true},
		{"tty defaults to table", true, "", false},
		{"flag forces table when piped", false, "table", false},
		{"flag forces json on a tty", true, "json", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, dir, nil)
			h.env.StdoutIsTTY = tc.tty
			args := []string{"rpc", "list.all", "--socket", srv.Socket}
			if tc.format != "" {
				args = append(args, "--format", tc.format)
			}
			if got := h.run(args...); got != 0 {
				t.Fatalf("exit %d: %s", got, h.errb.String())
			}
			out := h.out.String()
			if tc.json {
				var v []map[string]any
				if err := json.Unmarshal([]byte(out), &v); err != nil || len(v) != 2 {
					t.Fatalf("stdout %q is not the JSON result: %v", out, err)
				}
				if indented := strings.Contains(out, "\n  "); indented != tc.tty {
					t.Fatalf("indented = %v on tty = %v: %q", indented, tc.tty, out)
				}
				return
			}
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if len(lines) != 3 || strings.Fields(lines[0])[0] != "n" || strings.Fields(lines[1])[1] != "x" {
				t.Fatalf("table = %q", out)
			}
		})
	}
}

func TestRPCTableModeUsesTheGenericRendering(t *testing.T) {
	dir := shortDir(t)
	results := map[string]any{"s.rec": []map[string]any{{"a": 1}, {"a": 2, "b": "x"}}, "s.str": "hello", "s.num": 7, "s.bool": true, "s.null": nil, "s.obj": map[string]any{"b": []int{1}, "a": "x"}}
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, results[req.Method]) }))
	for method, want := range map[string]string{
		"s.str":  "hello\n",
		"s.num":  "7\n",
		"s.bool": "true\n",
		"s.null": "\n",
		"s.obj":  "a  x\nb  [1]\n",
		"s.rec":  "a  b\n1\n2  x\n",
	} {
		h := newHarness(t, dir, nil)
		if got := h.run("rpc", method, "--socket", srv.Socket, "--format", "table"); got != 0 {
			t.Fatalf("%s: exit %d: %s", method, got, h.errb.String())
		}
		if h.out.String() != want {
			t.Fatalf("%s: stdout = %q, want %q", method, h.out.String(), want)
		}
	}
}

func TestPingOutput(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", nil))
	t.Run("json is the raw result", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		if got := h.run("ping", "--socket", srv.Socket); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		var res protocol.SessionPingResult
		if err := json.Unmarshal(h.out.Bytes(), &res); err != nil || res.ID != "a" || res.PID != 4242 || res.HostVersion != "9.9.9" || res.UptimeMS != 1500 {
			t.Fatalf("stdout = %q (%v)", h.out.String(), err)
		}
	})
	t.Run("table rounds the uptime to whole seconds", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		if got := h.run("ping", "--socket", srv.Socket, "--format", "table"); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		if want := "ID  PID   PROTO  HOST   UPTIME\na   4242  2.0    9.9.9  2s\n"; h.out.String() != want {
			t.Fatalf("table = %q, want %q", h.out.String(), want)
		}
	})
	t.Run("a minute and more, and a blank host", func(t *testing.T) {
		quiet := serve(t, shortDir(t), "q", func(c *srvConn, req protocol.Message) {
			c.reply(req, protocol.SessionPingResult{ID: "q", PID: 5, ProtocolVersion: protocol.Version, UptimeMS: 72_400})
		})
		h := newHarness(t, dir, nil)
		if got := h.run("ping", "--socket", quiet.Socket, "--format", "table"); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		if want := "ID  PID  PROTO  HOST  UPTIME\nq   5    2.0          1m12s\n"; h.out.String() != want {
			t.Fatalf("table = %q, want %q", h.out.String(), want)
		}
	})
}

func TestInstancesListsWithStatusAndRemovesStale(t *testing.T) {
	dir := shortDir(t)
	serve(t, dir, "notes", pingOK("notes", nil))
	register(t, dir, "ghost", instance.SocketPath(dir, "ghost"), protocol.Version) // stale
	serve(t, dir, "hung", func(c *srvConn, req protocol.Message) {})               // accepts, never answers
	deps := &instance.Deps{Ping: func(sock string) error {
		switch {
		case strings.HasSuffix(sock, "ghost.sock"):
			return instance.ErrSocketMissing
		case strings.HasSuffix(sock, "hung.sock"):
			return instance.ErrPingTimeout
		}
		return nil
	}}

	h := newHarness(t, dir, nil)
	h.env.Deps = deps
	if got := h.run("instances", "--format", "json"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	var raw []map[string]any
	_ = json.Unmarshal(h.out.Bytes(), &raw)
	if len(raw) != 2 || raw[0]["id"] != "hung" || raw[0]["status"] != "unresponsive" || raw[1]["id"] != "notes" || raw[1]["status"] != "live" {
		t.Fatalf("rows = %v", raw)
	}
	var keys []string
	for k := range raw[1] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"host_version", "id", "pid", "protocol_version", "socket", "started_at", "status"}; !slices.Equal(keys, want) {
		t.Fatalf("row keys = %v, want the spec's entry keys plus status: %v", keys, want)
	}
	if _, err := os.Stat(instance.EntryPath(dir, "ghost")); !os.IsNotExist(err) {
		t.Fatalf("stale entry survived `husk instances`: %v", err)
	}
	if _, err := os.Stat(instance.EntryPath(dir, "hung")); err != nil {
		t.Fatalf("unresponsive entry was removed: %v", err)
	}

	h = newHarness(t, dir, nil)
	h.env.Deps = deps
	if got := h.run("instances", "--format", "table"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	lines := strings.Split(strings.TrimRight(h.out.String(), "\n"), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "ID PID STARTED PROTO STATUS" ||
		!strings.HasPrefix(lines[1], "hung") || !strings.HasSuffix(lines[1], "unresponsive") ||
		!strings.HasPrefix(lines[2], "notes") || !strings.HasSuffix(lines[2], "live") {
		t.Fatalf("table = %q", h.out.String())
	}
	if h.errb.Len() != 0 {
		t.Fatalf("stderr = %q: `husk instances` lists unresponsive entries instead of warning", h.errb.String())
	}
}

func TestInstancesHostVersionIsAlwaysPresent(t *testing.T) {
	dir := shortDir(t)
	serve(t, dir, "a", pingOK("a", nil))
	h := newHarness(t, dir, nil)
	if got := h.run("instances", "--format", "json"); got != 0 {
		t.Fatalf("exit %d: %s", got, h.errb.String())
	}
	if !strings.Contains(h.out.String(), `"host_version":""`) {
		t.Fatalf("stdout = %q, want host_version present and empty", h.out.String())
	}
}

func TestInstancesEmptyRegistry(t *testing.T) {
	h := newHarness(t, shortDir(t), nil)
	if got := h.run("instances", "--format", "json"); got != 0 || h.out.String() != "[]\n" {
		t.Fatalf("exit %d stdout %q", got, h.out.String())
	}
}

func TestAbsentRuntimeDirectory(t *testing.T) {
	absent := filepath.Join(shortDir(t), "never-created")
	newAbsent := func(env map[string]string) *harness {
		h := newHarness(t, absent, env)
		h.env.RuntimeDir = func() (string, bool, error) { return absent, false, nil }
		return h
	}
	t.Run("instances prints an empty list, exit 0", func(t *testing.T) {
		h := newAbsent(nil)
		if got := h.run("instances", "--format", "json"); got != 0 || h.out.String() != "[]\n" {
			t.Fatalf("exit %d stdout %q stderr %q", got, h.out.String(), h.errb.String())
		}
		h = newAbsent(nil)
		if got := h.run("instances", "--format", "table"); got != 0 || h.errb.Len() != 0 {
			t.Fatalf("exit %d stderr %q", got, h.errb.String())
		}
	})
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		msg  string
	}{
		{"discovery", []string{"ping"}, nil, "no instance is running"},
		{"--instance", []string{"ping", "--instance", "a"}, nil, `instance "a" is not running`},
		{"HUSK_INSTANCE", []string{"ping"}, map[string]string{instance.EnvInstance: "a"}, `instance "a" is not running`},
	} {
		t.Run(tc.name+" finds no instance, exit 3", func(t *testing.T) {
			h := newAbsent(tc.env)
			if got := h.run(append(tc.args, "--format", "json")...); got != output.ExitNoInstance {
				t.Fatalf("exit %d stderr %q", got, h.errb.String())
			}
			if code, msg, _ := errObject(t, h); code != "no_instance" || msg != tc.msg {
				t.Fatalf("code %q message %q", code, msg)
			}
		})
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatalf("the CLI created the runtime directory: %v", err)
	}
}

func TestUnusableRuntimeDirectoryIsExit7(t *testing.T) {
	unusable := func(env map[string]string) *harness {
		h := newHarness(t, "", env)
		h.env.RuntimeDir = func() (string, bool, error) {
			return "", false, errors.New("runtime dir /run/x/husk is not private to uid 1000")
		}
		return h
	}
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"instances", []string{"instances"}, nil},
		{"discovery", []string{"ping"}, nil},
		{"--instance", []string{"ping", "--instance", "a"}, nil},
		{"HUSK_INSTANCE", []string{"ping"}, map[string]string{instance.EnvInstance: "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := unusable(tc.env)
			if got := h.run(append(tc.args, "--format", "json")...); got != output.ExitEnvironment {
				t.Fatalf("exit %d stderr %q", got, h.errb.String())
			}
			if code, msg, _ := errObject(t, h); code != "environment" || msg != "runtime dir /run/x/husk is not private to uid 1000" {
				t.Fatalf("code %q message %q", code, msg)
			}
		})
	}
	t.Run("a socket path needs no registry", func(t *testing.T) {
		dir := shortDir(t)
		srv := serve(t, dir, "a", pingOK("a", nil))
		for _, tc := range []struct {
			args []string
			env  map[string]string
		}{
			{[]string{"ping", "--socket", srv.Socket}, nil},
			{[]string{"ping"}, map[string]string{instance.EnvSocketPath: srv.Socket}},
		} {
			h := unusable(tc.env)
			if got := h.run(tc.args...); got != 0 {
				t.Fatalf("%v: exit %d stderr %q", tc.args, got, h.errb.String())
			}
		}
	})
}

func TestParseInterspersed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantPos []string
		wantFmt string
	}{
		{"flags first", []string{"--format", "json", "a", "b"}, []string{"a", "b"}, "json"},
		{"flags between", []string{"a", "--format", "json", "b"}, []string{"a", "b"}, "json"},
		{"flags last", []string{"a", "b", "--format=json"}, []string{"a", "b"}, "json"},
		{"double dash ends flags", []string{"--format", "json", "--", "a", "--format", "x"}, []string{"a", "--format", "x"}, "json"},
		{"lone dash is positional", []string{"a", "-", "--format", "json"}, []string{"a", "-"}, "json"},
		{"repeated flag: last wins", []string{"--format", "table", "a", "--format", "json"}, []string{"a"}, "json"},
		{"nothing", nil, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			format := fs.String("format", "", "")
			pos, err := parseInterspersed(fs, tc.args)
			if err != nil || !slices.Equal(pos, tc.wantPos) || *format != tc.wantFmt {
				t.Fatalf("pos %q format %q err %v; want %q %q", pos, *format, err, tc.wantPos, tc.wantFmt)
			}
		})
	}
}

func TestTimeoutStartsWhenTheFirstByteIsSentNotWhenConnectingStarts(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{"ok": true}) }))
	slow := dialSocket
	dialSocket = func(ctx context.Context, socket string) (net.Conn, error) {
		time.Sleep(300 * time.Millisecond) // the connect alone outlasts --timeout
		return slow(ctx, socket)
	}
	t.Cleanup(func() { dialSocket = slow })
	h := newHarness(t, dir, nil)
	if got := h.run("rpc", "x.y", "--socket", srv.Socket, "--timeout", "200ms"); got != 0 {
		t.Fatalf("exit = %d; stderr %q", got, h.errb.String())
	}
}

func TestTimeoutExpiryIsTransportExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    handler
		args []string
	}{
		{"the ping is never answered", func(c *srvConn, req protocol.Message) {}, []string{"ping"}},
		{"the request is never answered", pingOK("a", func(c *srvConn, req protocol.Message) {}), []string{"rpc", "x.y"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortDir(t)
			srv := serve(t, dir, "a", tc.h)
			h := newHarness(t, dir, nil)
			// The parent context is a safety net so a missing timeout fails
			// the test instead of hanging it.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			args := append(tc.args, "--socket", srv.Socket, "--timeout", "50ms", "--format", "table")
			if got := Main(ctx, args, h.env); got != output.ExitTransport {
				t.Fatalf("exit = %d, want %d", got, output.ExitTransport)
			}
			if ctx.Err() != nil {
				t.Fatal("the command ran until the safety net: --timeout was not applied")
			}
			if h.errb.String() != "husk: instance did not answer within 50ms\n" {
				t.Fatalf("stderr = %q", h.errb.String())
			}
		})
	}
}

func TestEffectiveTimeout(t *testing.T) {
	for _, tc := range []struct {
		flag, def, want time.Duration
		wantErr         bool
	}{
		{0, 2 * time.Second, 2 * time.Second, false},
		{0, 0, 0, false},
		{50 * time.Millisecond, 2 * time.Second, 50 * time.Millisecond, false},
		{-time.Second, 2 * time.Second, 0, true},
	} {
		got, err := effectiveTimeout(tc.flag, tc.def)
		var oe *output.Error
		if tc.wantErr != errors.As(err, &oe) || (err == nil && got != tc.want) || (tc.wantErr && oe.Exit != output.ExitUsage) {
			t.Errorf("effectiveTimeout(%s, %s) = %s, %v; want %s (error %v)", tc.flag, tc.def, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestDefaultTimeoutsAndConnectBound(t *testing.T) {
	for name, want := range map[string]time.Duration{"ping": 2 * time.Second, "rpc": 30 * time.Second} {
		if got := builtins()[name].timeout; got != want {
			t.Errorf("%s default timeout = %s, want %s", name, got, want)
		}
	}
	if !builtins()["instances"].local {
		t.Error("instances must be local: it takes no --timeout")
	}
	if ConnectTimeout != 2*time.Second {
		t.Errorf("ConnectTimeout = %s, want 2s", ConnectTimeout)
	}
}

func TestSkippedUnresponsiveInstancesAreWarnedAbout(t *testing.T) {
	dir := shortDir(t)
	serve(t, dir, "a", pingOK("a", nil))
	serve(t, dir, "hung", func(c *srvConn, req protocol.Message) {})
	deps := &instance.Deps{Ping: func(sock string) error {
		if strings.HasSuffix(sock, "hung.sock") {
			return instance.ErrPingTimeout
		}
		return nil
	}}
	t.Run("table: one line, exit 0", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		h.env.Deps = deps
		if got := h.run("ping", "--format", "table"); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		if h.errb.String() != "husk: instance \"hung\" did not answer within 500ms; skipped\n" {
			t.Fatalf("stderr = %q", h.errb.String())
		}
	})
	t.Run("json: one warning object", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		h.env.Deps = deps
		if got := h.run("ping", "--format", "json"); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		var w struct {
			Warning struct{ Code, Message string }
		}
		if err := json.Unmarshal(h.errb.Bytes(), &w); err != nil || w.Warning.Code != "unresponsive" || !strings.Contains(w.Warning.Message, `"hung"`) {
			t.Fatalf("stderr = %q (%v)", h.errb.String(), err)
		}
	})
	t.Run("json: warnings come before the error, one object per line", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		h.env.Deps = deps
		if got := h.run("rpc", "no.such", "--format", "json"); got != output.ExitRemote {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		lines := strings.Split(strings.TrimRight(h.errb.String(), "\n"), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], `{"warning":`) || !strings.HasPrefix(lines[1], `{"error":`) {
			t.Fatalf("stderr = %q", h.errb.String())
		}
		if h.out.Len() != 0 {
			t.Fatalf("stdout = %q: diagnostics never reach stdout", h.out.String())
		}
	})
}

func TestOnlyUnresponsiveInstancesIsExit5NamingThem(t *testing.T) {
	deps := &instance.Deps{Ping: func(string) error { return instance.ErrPingTimeout }}
	dir := shortDir(t)
	serve(t, dir, "hung", func(c *srvConn, req protocol.Message) {})
	t.Run("table", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		h.env.Deps = deps
		if got := h.run("ping", "--format", "table"); got != output.ExitTransport {
			t.Fatalf("exit = %d, want %d", got, output.ExitTransport)
		}
		if want := "husk: 1 instance is registered but none answered within 500ms: hung\n"; h.errb.String() != want {
			t.Fatalf("stderr = %q, want %q", h.errb.String(), want)
		}
	})
	t.Run("json", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		h.env.Deps = deps
		if got := h.run("ping", "--format", "json"); got != output.ExitTransport {
			t.Fatalf("exit = %d", got)
		}
		if code, _, _ := errObject(t, h); code != "transport" || strings.Count(h.errb.String(), "\n") != 1 {
			t.Fatalf("stderr = %q", h.errb.String())
		}
	})
}

func TestAmbiguousErrorCarriesCandidates(t *testing.T) {
	dir := shortDir(t)
	serve(t, dir, "b", pingOK("b", nil))
	serve(t, dir, "a", pingOK("a", nil))
	t.Run("json data", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		if got := h.run("ping", "--format", "json"); got != output.ExitAmbiguous {
			t.Fatalf("exit = %d, want %d", got, output.ExitAmbiguous)
		}
		_, _, data := errObject(t, h)
		var d struct {
			Candidates []struct {
				ID        string `json:"id"`
				PID       int    `json:"pid"`
				StartedAt string `json:"started_at"`
			}
		}
		if err := json.Unmarshal(data, &d); err != nil {
			t.Fatal(err)
		}
		c := d.Candidates
		if len(c) != 2 || c[0].ID != "a" || c[1].ID != "b" || c[0].PID != os.Getpid() {
			t.Fatalf("candidates = %+v", c)
		}
	})
	t.Run("table lists them under the message", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		if got := h.run("ping", "--format", "table"); got != output.ExitAmbiguous {
			t.Fatalf("exit = %d", got)
		}
		lines := strings.Split(strings.TrimRight(h.errb.String(), "\n"), "\n")
		if len(lines) != 3 || lines[0] != "husk: 2 instances are running; choose one with --instance <id>" ||
			!strings.HasPrefix(lines[1], "  a  started ") || !strings.HasPrefix(lines[2], "  b  started ") {
			t.Fatalf("stderr = %q", h.errb.String())
		}
	})
}

func TestPluginTokenIsSentWhateverTheTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(sock string) []string
		env  func(sock string) map[string]string
	}{
		{"discovery", func(string) []string { return nil }, nil},
		{"--instance", func(string) []string { return []string{"--instance", "a"} }, nil},
		{"--socket", func(s string) []string { return []string{"--socket", s} }, nil},
		{"HUSK_INSTANCE", nil, func(string) map[string]string { return map[string]string{instance.EnvInstance: "a"} }},
		{"HUSK_SOCKET_PATH", nil, func(s string) map[string]string { return map[string]string{instance.EnvSocketPath: s} }},
	} {
		for _, token := range []string{"tok", ""} {
			t.Run(tc.name+" token="+token, func(t *testing.T) {
				dir := shortDir(t)
				srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{}) }))
				env := map[string]string{EnvPluginToken: token}
				if tc.env != nil {
					for k, v := range tc.env(srv.Socket) {
						env[k] = v
					}
				}
				var args []string
				if tc.args != nil {
					args = tc.args(srv.Socket)
				}
				h := newHarness(t, dir, env)
				h.env.Deps = &instance.Deps{Ping: func(string) error { return nil }}
				if got := h.run(append([]string{"rpc", "x.y"}, args...)...); got != 0 {
					t.Fatalf("exit %d: %s", got, h.errb.String())
				}
				reqs := srv.requests()
				if len(reqs) != 2 {
					t.Fatalf("requests = %d, want ping and x.y", len(reqs))
				}
				for _, r := range reqs { // the version-check ping carries it too
					if r.HasToken() != (token != "") || r.Token != token {
						t.Fatalf("%s token = %q (present %v), want %q", r.Method, r.Token, r.HasToken(), token)
					}
				}
			})
		}
	}
}

func TestRegisterAddsALocalCommand(t *testing.T) {
	withRegistered(t)
	var gotArgs []string
	var gotOpts output.Options
	Register("fake", output.Command{
		Noun:  "fake",
		Usage: "husk fake <x>",
		Run: func(_ context.Context, args []string, o output.Options) error {
			gotArgs, gotOpts = args, o
			if len(args) > 0 && args[0] == "fail" {
				return output.Store(nil, "store says no")
			}
			_, err := o.Stdout.Write([]byte("ran\n"))
			return err
		},
	})
	t.Run("dispatch gives the command its positionals and options", func(t *testing.T) {
		h := newHarness(t, shortDir(t), nil)
		h.env.Stdin = strings.NewReader("in")
		if got := h.run("fake", "one", "--format", "table", "two"); got != 0 || h.out.String() != "ran\n" {
			t.Fatalf("exit %d stdout %q stderr %q", got, h.out.String(), h.errb.String())
		}
		if !slices.Equal(gotArgs, []string{"one", "two"}) || gotOpts.Format != output.Table || gotOpts.Stdin == nil {
			t.Fatalf("args %v opts %+v", gotArgs, gotOpts)
		}
	})
	t.Run("its error is reported with its exit code and code", func(t *testing.T) {
		h := newHarness(t, shortDir(t), nil)
		if got := h.run("fake", "fail", "--format", "json"); got != output.ExitRemote {
			t.Fatalf("exit %d", got)
		}
		if code, msg, _ := errObject(t, h); code != "store" || msg != "store says no" {
			t.Fatalf("code %q message %q", code, msg)
		}
	})
	t.Run("it takes no target flags and no timeout", func(t *testing.T) {
		for _, flag := range [][]string{{"--instance", "a"}, {"--socket", "/s"}, {"--timeout", "1s"}} {
			h := newHarness(t, shortDir(t), nil)
			if got := h.run(append([]string{"fake"}, flag...)...); got != output.ExitUsage {
				t.Fatalf("%v: exit %d", flag, got)
			}
		}
	})
	t.Run("it appears in the usage text", func(t *testing.T) {
		h := newHarness(t, shortDir(t), nil)
		h.run("help")
		if !strings.Contains(h.out.String(), "husk fake <x>") {
			t.Fatalf("usage = %q", h.out.String())
		}
	})
}

func TestRegisterRefusesWiringMistakes(t *testing.T) {
	withRegistered(t)
	cmd := output.Command{Noun: "x", Usage: "husk x", Run: func(context.Context, []string, output.Options) error { return nil }}
	Register("once", cmd)
	for _, noun := range []string{"once", "ping", "instances", "rpc", "help", ""} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Register(%q) did not panic", noun)
				}
			}()
			Register(noun, cmd)
		}()
	}
}
