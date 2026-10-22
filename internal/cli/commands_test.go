package cli

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/instance"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestUnknownSubcommandIsUsage(t *testing.T) {
	h := newHarness(t, shortDir(t), nil)
	if got := h.run("frobnicate"); got != ExitUsage {
		t.Fatalf("exit = %d, want %d", got, ExitUsage)
	}
	if !strings.Contains(h.errb.String(), `unknown command "frobnicate"`) || !strings.Contains(h.errb.String(), "husk rpc") {
		t.Fatalf("stderr = %q", h.errb.String())
	}
	if h.out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", h.out.String())
	}
}

func TestUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"both targets", []string{"ping", "--instance", "a", "--socket", "/x"}},
		{"bad format", []string{"ping", "--format", "xml"}},
		{"negative timeout", []string{"ping", "--timeout", "-1s"}},
		{"unknown flag", []string{"ping", "--nope"}},
		{"ping with args", []string{"ping", "extra"}},
		{"instances with args", []string{"instances", "extra"}},
		{"rpc without method", []string{"rpc"}},
		{"rpc bad method", []string{"rpc", "Nope"}},
		{"rpc one segment", []string{"rpc", "state"}},
		{"rpc invalid json", []string{"rpc", "a.b", "{"}},
		{"rpc params not an object", []string{"rpc", "a.b", "[1]"}},
		{"rpc too many args", []string{"rpc", "a.b", "{}", "{}"}},
		{"invalid instance name", []string{"ping", "--instance", "Foo_1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No instance runs: a usage error must be reported before any
			// resolution or connection is attempted.
			h := newHarness(t, shortDir(t), nil)
			if got := h.run(tc.args...); got != ExitUsage {
				t.Fatalf("exit = %d, want %d; stderr %q", got, ExitUsage, h.errb.String())
			}
			if h.out.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", h.out.String())
			}
		})
	}
}

func TestGlobalFlagsMayFollowOrPrecedePositionals(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{"ok": true}) }))
	for _, args := range [][]string{
		{"rpc", "--socket", srv.Socket, "x.y", "{}"},
		{"rpc", "x.y", "--socket", srv.Socket, "{}"},
		{"rpc", "x.y", "{}", "--socket", srv.Socket},
		{"rpc", "--socket", srv.Socket, "--", "x.y", "{}"},
	} {
		h := newHarness(t, dir, nil)
		if got := h.run(args...); got != 0 || strings.TrimSpace(h.out.String()) != `{"ok":true}` {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q", args, got, h.out.String(), h.errb.String())
		}
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
		{name: "zero instances", args: []string{"ping"}, wantExit: ExitNoInstance, wantErr: "no instance is running"},
		{name: "one instance", running: []string{"a"}, args: []string{"ping"}, wantID: "a"},
		{name: "many instances", running: []string{"a", "b"}, args: []string{"ping"}, wantExit: ExitAmbiguous, wantErr: "2 instances are running; choose one with --instance <id>\n  a  started"},
		{name: "instance flag picks one of many", running: []string{"a", "b"}, args: []string{"ping", "--instance", "b"}, wantID: "b"},
		{name: "instance flag not registered", running: []string{"a"}, args: []string{"ping", "--instance", "zz"}, wantExit: ExitNoInstance, wantErr: `instance "zz" is not running`},
		{name: "socket flag", running: []string{"a", "b"}, args: []string{"ping", "--socket", "@b"}, wantID: "b"},
		{name: "socket flag dead", running: []string{"a"}, args: []string{"ping", "--socket", "@gone"}, wantExit: ExitNoInstance, wantErr: "nobody is listening"},
		{name: "env socket path picks one of many", running: []string{"a", "b"}, args: []string{"ping"},
			env: func(s env) env { return env{instance.EnvSocketPath: s["a"]} }, wantID: "a"},
		{name: "env instance picks one of many", running: []string{"a", "b"}, args: []string{"ping"},
			env: func(s env) env { return env{instance.EnvInstance: "b"} }, wantID: "b"},
		{name: "flag beats env", running: []string{"a", "b"}, args: []string{"ping", "--instance", "a"},
			env: func(s env) env { return env{instance.EnvInstance: "b"} }, wantID: "a"},
		{name: "dead env socket never falls through to discovery", running: []string{"a"}, args: []string{"ping"},
			env:      func(s env) env { return env{instance.EnvSocketPath: s["a"] + ".dead"} },
			wantExit: ExitNoInstance, wantErr: "HUSK_SOCKET_PATH points at a socket nobody is listening on"},
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
			args = append(args, "--format", "json")
			if got := h.run(args...); got != tc.wantExit {
				t.Fatalf("exit = %d, want %d; stdout %q stderr %q", got, tc.wantExit, h.out.String(), h.errb.String())
			}
			if tc.wantErr != "" {
				// JSON mode wraps the message in an object; decode to compare text.
				var e struct{ Error struct{ Message string } }
				if err := json.Unmarshal(h.errb.Bytes(), &e); err != nil {
					t.Fatalf("stderr %q is not one JSON object: %v", h.errb.String(), err)
				}
				if !strings.Contains(e.Error.Message, tc.wantErr) {
					t.Fatalf("message %q lacks %q", e.Error.Message, tc.wantErr)
				}
				return
			}
			var ping protocol.SessionPingResult
			if err := json.Unmarshal(h.out.Bytes(), &ping); err != nil || ping.ID != tc.wantID {
				t.Fatalf("stdout %q (%v): want ping from %q", h.out.String(), err, tc.wantID)
			}
			for id, s := range servers { // only the chosen instance got a command
				got := 0
				for _, r := range s.requests() {
					if r.Method == protocol.MethodSessionPing {
						got++
					}
				}
				if (id == tc.wantID) != (got > 0) {
					t.Fatalf("instance %s received %d pings (target %s)", id, got, tc.wantID)
				}
			}
		})
	}
}

func TestStaleEntryIsRemovedByFailedDial(t *testing.T) {
	dir := shortDir(t)
	register(t, dir, "ghost", instance.SocketPath(dir, "ghost"), protocol.Version) // entry, no socket
	h := newHarness(t, dir, nil)
	if got := h.run("ping", "--instance", "ghost", "--format", "table"); got != ExitNoInstance {
		t.Fatalf("exit = %d, want %d", got, ExitNoInstance)
	}
	if !strings.Contains(h.errb.String(), `instance "ghost" is not running (stale entry removed)`) {
		t.Fatalf("stderr = %q", h.errb.String())
	}
	if _, err := os.Stat(instance.EntryPath(dir, "ghost")); !os.IsNotExist(err) {
		t.Fatalf("stale entry still on disk: %v", err)
	}
}

func TestProtocolMajorMismatchIsExit6(t *testing.T) {
	t.Run("from the ping reply", func(t *testing.T) {
		dir := shortDir(t)
		srv := serve(t, dir, "a", func(c *srvConn, req protocol.Message) {
			c.reply(req, protocol.SessionPingResult{ID: "a", PID: 1, ProtocolVersion: "3.1"})
		})
		h := newHarness(t, dir, nil)
		if got := h.run("ping", "--socket", srv.Socket); got != ExitProtocol {
			t.Fatalf("exit = %d, want %d", got, ExitProtocol)
		}
		if !strings.Contains(h.errb.String(), "instance speaks protocol 3.x, this CLI speaks 2.x") {
			t.Fatalf("stderr = %q", h.errb.String())
		}
	})
	t.Run("from the registration entry, before any command is sent", func(t *testing.T) {
		dir := shortDir(t)
		srv := serve(t, dir, "a", pingOK("a", nil))
		register(t, dir, "a", srv.Socket, "3.0")
		h := newHarness(t, dir, nil)
		if got := h.run("rpc", "x.y", "--instance", "a"); got != ExitProtocol {
			t.Fatalf("exit = %d, want %d", got, ExitProtocol)
		}
		for _, r := range srv.requests() {
			if r.Method == "x.y" {
				t.Fatal("the command was sent to an instance of another major")
			}
		}
	})
}

func TestRPCResultAndErrors(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
		switch req.Method {
		case "state.get":
			c.reply(req, map[string]any{"revision": 3, "entries": []string{"a", "b"}})
		case "state.clear":
			c.send(protocol.Message{ID: req.ID, Error: &protocol.Error{Code: protocol.CodeDenied, Message: "user only", Data: []byte(`{"reason":"needs_grant"}`)}})
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
	t.Run("error reply in json mode", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		if got := h.run("rpc", "state.clear", "--socket", srv.Socket); got != ExitRemote {
			t.Fatalf("exit = %d, want %d", got, ExitRemote)
		}
		want := `{"error":{"code":"denied","data":{"reason":"needs_grant"},"message":"user only"}}` + "\n"
		if h.errb.String() != want || h.out.Len() != 0 {
			t.Fatalf("stderr = %q stdout = %q", h.errb.String(), h.out.String())
		}
	})
	t.Run("error reply in table mode", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		if got := h.run("rpc", "no.such", "--socket", srv.Socket, "--format", "table"); got != ExitRemote {
			t.Fatalf("exit = %d, want %d", got, ExitRemote)
		}
		if h.errb.String() != "husk: no handler for no.such\n" || h.out.Len() != 0 {
			t.Fatalf("stderr = %q stdout = %q", h.errb.String(), h.out.String())
		}
	})
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
				return
			}
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if len(lines) != 3 || strings.Fields(lines[0])[0] != "n" || strings.Fields(lines[1])[1] != "x" {
				t.Fatalf("table = %q", out)
			}
		})
	}
}

func TestTableMode(t *testing.T) {
	dir := shortDir(t)
	results := map[string]any{"s.rec": []map[string]any{{"a": 1}, {"a": 2, "b": "x"}}, "s.str": "hello", "s.num": 7, "s.bool": true, "s.null": nil, "s.obj": map[string]any{"b": []int{1}, "a": "x"}}
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, results[req.Method]) }))
	for method, want := range map[string]string{
		"s.str":  "hello\n",
		"s.num":  "7\n",
		"s.bool": "true\n",
		"s.null": "\n",
		"s.obj":  "a  x\nb  [1]\n",
		"s.rec":  "a  b\n1  \n2  x\n",
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
	t.Run("table", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		if got := h.run("ping", "--socket", srv.Socket, "--format", "table"); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		lines := strings.Split(strings.TrimRight(h.out.String(), "\n"), "\n")
		if len(lines) != 2 || strings.Fields(lines[0])[0] != "ID" ||
			strings.Join(strings.Fields(lines[1]), " ") != "a 4242 2.0 9.9.9 1.5s" {
			t.Fatalf("table = %q", h.out.String())
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
	if len(raw) != 2 || raw[0]["id"] != "hung" || raw[0]["status"] != "unresponsive" || raw[1]["id"] != "notes" || raw[1]["status"] != "live" ||
		raw[1]["protocol_version"] != "2.0" || raw[1]["started_at"] == nil {
		t.Fatalf("rows = %v", raw)
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
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "ID NAME PID STARTED PROTO STATUS" ||
		!strings.HasPrefix(lines[1], "hung") || !strings.HasSuffix(strings.TrimSpace(lines[1]), "unresponsive") ||
		!strings.HasPrefix(lines[2], "notes") || !strings.HasSuffix(strings.TrimSpace(lines[2]), "live") {
		t.Fatalf("table = %q", h.out.String())
	}
}

func TestInstancesEmptyIsAnEmptyArray(t *testing.T) {
	h := newHarness(t, shortDir(t), nil)
	if got := h.run("instances", "--format", "json"); got != 0 || h.out.String() != "[]\n" {
		t.Fatalf("exit %d stdout %q", got, h.out.String())
	}
}

func TestInstancesIgnoresTargetFlags(t *testing.T) {
	dir := shortDir(t)
	serve(t, dir, "a", pingOK("a", nil))
	h := newHarness(t, dir, nil)
	if got := h.run("instances", "--instance", "zz"); got != 0 {
		t.Fatalf("exit = %d, want 0 (instances never resolves)", got)
	}
}

func TestRuntimeDirFailureIsReported(t *testing.T) {
	h := newHarness(t, "", nil)
	h.env.RuntimeDir = func() (string, error) { return "", os.ErrPermission }
	for _, cmd := range []string{"ping", "instances"} {
		h.errb.Reset()
		if got := h.run(cmd); got != ExitTransport || !strings.HasPrefix(h.errb.String(), "{") {
			t.Fatalf("%s: exit %d stderr %q", cmd, got, h.errb.String())
		}
	}
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

func TestRPCToASocketOfAnotherMajorIsRefusedBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		name string
		via  func(sock string) (args []string, env map[string]string)
	}{
		{"--socket", func(s string) ([]string, map[string]string) { return []string{"--socket", s}, nil }},
		{"HUSK_SOCKET_PATH", func(s string) ([]string, map[string]string) {
			return nil, map[string]string{instance.EnvSocketPath: s}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortDir(t)
			srv := serve(t, dir, "a", func(c *srvConn, req protocol.Message) {
				c.reply(req, protocol.SessionPingResult{ID: "a", PID: 1, ProtocolVersion: "3.0"})
			})
			args, env := tc.via(srv.Socket)
			h := newHarness(t, dir, env)
			if got := h.run(append([]string{"rpc", "x.y"}, args...)...); got != ExitProtocol {
				t.Fatalf("exit = %d, want %d; stderr %q", got, ExitProtocol, h.errb.String())
			}
			for _, r := range srv.requests() {
				if r.Method == "x.y" {
					t.Fatal("the command reached an instance of another major")
				}
			}
		})
	}
}

func TestTimeoutStartsAfterResolution(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{"ok": true}) }))
	h := newHarness(t, dir, nil)
	// Resolve has no clock seam, so this is the one deliberate wait: the
	// resolution ping outlasts --timeout, and the command must still get
	// its full budget afterwards.
	h.env.Deps = &instance.Deps{Ping: func(string) error { time.Sleep(300 * time.Millisecond); return nil }}
	if got := h.run("rpc", "x.y", "--socket", srv.Socket, "--timeout", "200ms"); got != 0 {
		t.Fatalf("exit = %d; stderr %q", got, h.errb.String())
	}
	if strings.TrimSpace(h.out.String()) != `{"ok":true}` {
		t.Fatalf("stdout = %q", h.out.String())
	}
}

func TestTimeoutExpiryIsTransportExit(t *testing.T) {
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {})) // never answers x.y
	h := newHarness(t, dir, nil)
	// The parent context is a safety net so a missing timeout fails the
	// test instead of hanging it.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if got := Main(ctx, []string{"rpc", "x.y", "--socket", srv.Socket, "--timeout", "50ms", "--format", "table"}, h.env); got != ExitTransport {
		t.Fatalf("exit = %d, want %d", got, ExitTransport)
	}
	if ctx.Err() != nil {
		t.Fatal("the command ran until the safety net: --timeout was not applied")
	}
	if h.errb.String() != "husk: instance did not answer within 50ms\n" {
		t.Fatalf("stderr = %q", h.errb.String())
	}
}

func TestDefaultTimeouts(t *testing.T) {
	for name, want := range map[string]time.Duration{"instances": 2 * time.Second, "ping": 2 * time.Second, "rpc": 30 * time.Second} {
		if got := commands()[name].timeout; got != want {
			t.Errorf("%s default timeout = %s, want %s", name, got, want)
		}
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
	t.Run("one live: warning, exit 0", func(t *testing.T) {
		h := newHarness(t, dir, nil)
		h.env.Deps = deps
		if got := h.run("ping", "--format", "table"); got != 0 {
			t.Fatalf("exit %d: %s", got, h.errb.String())
		}
		if h.errb.String() != "husk: instance \"hung\" did not answer within 500ms; skipped\n" {
			t.Fatalf("stderr = %q", h.errb.String())
		}
	})
	t.Run("json mode warns as an object", func(t *testing.T) {
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
	t.Run("only unresponsive: exit 3 naming it", func(t *testing.T) {
		only := shortDir(t)
		serve(t, only, "hung", func(c *srvConn, req protocol.Message) {})
		h := newHarness(t, only, nil)
		h.env.Deps = deps
		if got := h.run("ping", "--format", "table"); got != ExitNoInstance {
			t.Fatalf("exit = %d, want %d", got, ExitNoInstance)
		}
		if h.errb.String() != "husk: instance \"hung\" did not answer within 500ms; skipped\nhusk: no instance is running\n" {
			t.Fatalf("stderr = %q", h.errb.String())
		}
	})
}

func TestAmbiguousErrorCarriesCandidatesAsData(t *testing.T) {
	dir := shortDir(t)
	serve(t, dir, "b", pingOK("b", nil))
	serve(t, dir, "a", pingOK("a", nil))
	h := newHarness(t, dir, nil)
	if got := h.run("ping", "--format", "json"); got != ExitAmbiguous {
		t.Fatalf("exit = %d, want %d", got, ExitAmbiguous)
	}
	var e struct {
		Error struct {
			Data struct {
				Candidates []struct {
					ID        string `json:"id"`
					PID       int    `json:"pid"`
					StartedAt string `json:"started_at"`
				}
			}
		}
	}
	if err := json.Unmarshal(h.errb.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	c := e.Error.Data.Candidates
	if len(c) != 2 || c[0].ID != "a" || c[1].ID != "b" || c[0].PID != os.Getpid() || c[0].StartedAt == "" {
		t.Fatalf("candidates = %+v", c)
	}
}

func TestPluginTokenFromEnvironmentIsSent(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"set", map[string]string{EnvPluginToken: "tok"}, true},
		{"unset", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortDir(t)
			srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, map[string]any{}) }))
			h := newHarness(t, dir, tc.env)
			if got := h.run("rpc", "x.y", "--socket", srv.Socket); got != 0 {
				t.Fatalf("exit %d: %s", got, h.errb.String())
			}
			for _, r := range srv.requests() {
				if r.Method == "x.y" {
					if r.HasToken() != tc.want || (tc.want && r.Token != "tok") {
						t.Fatalf("token = %q (present %v)", r.Token, r.HasToken())
					}
					return
				}
			}
			t.Fatal("x.y never reached the server")
		})
	}
}

func TestUsageMessagesHaveNoDoublePrefix(t *testing.T) {
	h := newHarness(t, shortDir(t), nil)
	if got := h.run("ping", "extra", "--format", "table"); got != ExitUsage || h.errb.String() != "husk: ping takes no arguments\n" {
		t.Fatalf("exit %d stderr %q", got, h.errb.String())
	}
}

func TestIsTTYIsFalseForCharacterDevicesThatAreNotTerminals(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTTY(f) {
		t.Fatal("/dev/null reported as a terminal")
	}
}
