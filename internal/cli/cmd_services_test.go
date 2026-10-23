package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

const serviceReports = `{"reports":[{"service":"theme","gen":2,"applied":false,"changed":false,"problems":[{"severity":"error","code":"low_contrast","layer":"user","key":"theme.palette.ink","message":"too low"}]}]}`
const serviceProblems = `{"ok":false,"problems":[{"severity":"error","code":"low_contrast","layer":"user","key":"theme.palette.ink","message":"too low"}]}`

func serviceServer(t *testing.T, raw string, remote *protocol.Error) (*fakeServer, *harness) {
	t.Helper()
	dir := shortDir(t)
	srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) {
		if remote != nil {
			c.send(protocol.Message{ID: req.ID, Error: remote})
		} else {
			c.send(protocol.Message{ID: req.ID, Result: json.RawMessage(raw)})
		}
	}))
	return srv, newHarness(t, dir, nil)
}

func normalizedTable(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	return strings.Join(lines, "\n")
}

func TestServicesCommands(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		args                                     []string
		method, params, reply, jsonOut, tableOut string
	}{
		{"settings get", []string{"settings", "get", "theme.allow_low_contrast"}, protocol.MethodSettingsGet, `{"key":"theme.allow_low_contrast"}`, `{"key":"theme.allow_low_contrast","value":false,"source":"default"}`, "", "key theme.allow_low_contrast\nsource default\nvalue false"},
		{"settings list", []string{"settings", "list"}, protocol.MethodSettingsList, `{}`, `{"entries":[{"key":"a","value":"line\nnext","source":"user"}]}`, "", "KEY VALUE\na \"line\\nnext\""},
		{"settings source", []string{"settings", "list", "--source"}, protocol.MethodSettingsList, `{}`, `{"entries":[{"key":"a","value":true,"source":"user"}]}`, "", "KEY VALUE SOURCE\na true user"},
		{"settings null", []string{"settings", "list"}, protocol.MethodSettingsList, `{}`, `{"entries":[{"key":"a","value":null,"source":"user"}]}`, "", "KEY VALUE\na null"},
		{"settings schema", []string{"settings", "schema"}, protocol.MethodSettingsSchema, `{}`, `{"entries":[]}`, "", "entries []"},
		{"schema host", []string{"settings", "schema", "--owner", "husk"}, protocol.MethodSettingsSchema, `{"owner":"husk"}`, `{"entries":[]}`, "", "entries []"},
		{"schema plugin", []string{"settings", "schema", "--owner", "plugin:files"}, protocol.MethodSettingsSchema, `{"owner":"plugin:files"}`, `{"entries":[]}`, "", "entries []"},
		{"validate true", []string{"settings", "validate"}, protocol.MethodSettingsValidate, `{}`, `{"ok":true,"problems":[]}`, "", "ok true"},
		{"validate false", []string{"settings", "validate"}, protocol.MethodSettingsValidate, `{}`, serviceProblems, "", "ok false\nSEVERITY CODE LAYER KEY MESSAGE\nerror low_contrast user theme.palette.ink too low"},
		{"theme show", []string{"theme", "show"}, protocol.MethodThemeGet, `{}`, `{"palette":{"name":"Fake","ink":"#ffffff","accent":"#123456"}}`, "", "ROLE COLOR\naccent #123456\nink #ffffff"},
		{"theme roles", []string{"theme", "roles"}, protocol.MethodThemeGet, `{}`, `{"palette":{"name":"Fake","ink":"#ffffff","accent":"#123456"}}`, `["accent","ink"]`, "accent\nink"},
		{"theme check", []string{"theme", "check"}, protocol.MethodThemeCheck, `{}`, serviceProblems, "", "ok false\nSEVERITY CODE LAYER KEY MESSAGE\nerror low_contrast user theme.palette.ink too low"},
		{"theme list", []string{"theme", "list"}, protocol.MethodThemeList, `{}`, `{"themes":[{"name":"default","source":"built-in","current":true},{"name":"evening","source":"user","current":false}]}`, "", "NAME SOURCE CURRENT\ndefault built-in *\nevening user"},
		{"theme set", []string{"theme", "set", "evening"}, protocol.MethodSettingsSet, `{"key":"theme.name","value":"evening"}`, serviceReports, "", "SERVICE GEN APPLIED CHANGED\ntheme 2 false false\nSEVERITY CODE LAYER KEY MESSAGE\nerror low_contrast user theme.palette.ink too low"},
		{"settings set", []string{"settings", "set", "theme.name", "evening"}, protocol.MethodSettingsSet, `{"key":"theme.name","value":"evening"}`, serviceReports, "", "SERVICE GEN APPLIED CHANGED\ntheme 2 false false\nSEVERITY CODE LAYER KEY MESSAGE\nerror low_contrast user theme.palette.ink too low"},
		{"profile list", []string{"profile", "list"}, protocol.MethodProfileList, `{}`, `{"profiles":[]}`, "", "profiles []"},
		{"profile current", []string{"profile", "current"}, protocol.MethodProfileList, `{}`, `{"profiles":[{"name":"work","source":"user","active":true}]}`, `{"name":"work","source":"user","active":true}`, "active true\nname work\nsource user"},
		{"profile absent", []string{"profile", "current"}, protocol.MethodProfileList, `{}`, `{"profiles":[{"name":"work","active":false}]}`, `null`, ""},
		{"profile use", []string{"profile", "use", "work"}, protocol.MethodProfileUse, `{"name":"work"}`, `{"active":"work","reports":[{"service":"profile","gen":3,"applied":true,"changed":true}]}`, "", "SERVICE GEN APPLIED CHANGED\nprofile 3 true true"},
		{"profile clear", []string{"profile", "use", ""}, protocol.MethodProfileUse, `{"name":""}`, serviceReports, "", "SERVICE GEN APPLIED CHANGED\ntheme 2 false false\nSEVERITY CODE LAYER KEY MESSAGE\nerror low_contrast user theme.palette.ink too low"},
		{"secrets list", []string{"secrets", "list"}, protocol.MethodSecretsNames, `{}`, `{"storage":"unavailable","secrets":[{"name":"demo.token","declared":["a","b"],"granted":["a"],"set":null,"required":true}]}`, "", "storage: unavailable\nNAME DECLARED GRANTED SET REQUIRED\ndemo.token a,b a true"},
		{"secrets set metadata", []string{"secrets", "list"}, protocol.MethodSecretsNames, `{}`, `{"storage":"ok","secrets":[{"name":"a","declared":[],"granted":[],"set":true,"required":false},{"name":"b","declared":[],"granted":[],"set":false,"required":false}]}`, "", "storage: ok\nNAME DECLARED GRANTED SET REQUIRED\na true false\nb false false"},
		{"secrets set", []string{"secrets", "set", "demo.token"}, protocol.MethodSecretsSet, `{"name":"demo.token","value":""}`, `{}`, "", ""},
		{"secrets grant", []string{"secrets", "grant", "a", "demo.token"}, protocol.MethodSecretsGrant, `{"plugin":"a","name":"demo.token"}`, `{}`, "", ""},
		{"secrets revoke", []string{"secrets", "revoke", "a", "demo.token"}, protocol.MethodSecretsRevoke, `{"plugin":"a","name":"demo.token"}`, `{}`, "", ""},
		{"secrets reveal", []string{"secrets", "reveal", "demo.token"}, protocol.MethodSecretsReveal, `{"name":"demo.token"}`, `{"name":"demo.token","value":"fake-token-1"}`, "", "fake-token-1"},
		{"config reload", []string{"config", "reload"}, protocol.MethodConfigReload, `{}`, serviceReports, "", "SERVICE GEN APPLIED CHANGED\ntheme 2 false false\nSEVERITY CODE LAYER KEY MESSAGE\nerror low_contrast user theme.palette.ink too low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, format := range []string{"json", "table"} {
				t.Run(format, func(t *testing.T) {
					srv, h := serviceServer(t, tc.reply, nil)
					if code := h.run(append(append([]string{}, tc.args...), "--socket", srv.Socket, "--format", format)...); code != 0 {
						t.Fatalf("exit %d", code)
					}
					reqs := srv.requests()
					if len(reqs) != 2 || reqs[0].Method != protocol.MethodSessionPing || reqs[1].Method != tc.method {
						t.Fatal("unexpected request sequence")
					}
					// Never include secret params or reveal output in assertion failures.
					var got, want any
					if json.Unmarshal(reqs[1].Params, &got) != nil || json.Unmarshal([]byte(tc.params), &want) != nil || !reflect.DeepEqual(got, want) {
						t.Fatal("params mismatch")
					}
					if format == "json" {
						expected := tc.jsonOut
						if expected == "" {
							expected = tc.reply
						}
						if json.Unmarshal(h.out.Bytes(), &got) != nil || json.Unmarshal([]byte(expected), &want) != nil || !reflect.DeepEqual(got, want) {
							t.Fatal("JSON output mismatch")
						}
					} else if normalizedTable(h.out.String()) != tc.tableOut {
						t.Fatal("table output mismatch")
					}
				})
			}
			for _, code := range []protocol.Code{protocol.CodeNotFound, protocol.CodeInvalidParams, protocol.CodeDenied, protocol.CodeUnavailable, protocol.CodeTimeout, protocol.CodeCancelled} {
				for _, format := range []string{"json", "table"} {
					t.Run(string(code)+"/"+format, func(t *testing.T) {
						srv, h := serviceServer(t, "", &protocol.Error{Code: code, Message: "refused", Data: json.RawMessage(`{"reason":"fixture"}`)})
						if got := h.run(append(append([]string{}, tc.args...), "--socket", srv.Socket, "--format", format)...); got != output.ExitRemote {
							t.Fatalf("exit %d", got)
						}
						if h.out.Len() != 0 {
							t.Fatal("error wrote a result")
						}
						if format == "table" {
							if h.errb.String() != "husk: refused\n" {
								t.Fatal("table error mapping")
							}
						} else {
							c, _, data := errObject(t, h)
							if c != string(code) || string(data) != `{"reason":"fixture"}` {
								t.Fatal("JSON error mapping")
							}
						}
					})
				}
			}
		})
	}
}

func TestServicesUsageBeforeConnect(t *testing.T) {
	cases := [][]string{
		{"keys", "adopt"}, {"keys", "set"}, {"keys", "--problems", "--action", "a"}, {"keys", "--problems", "--focus", "main-stage"},
		{"settings", "get"}, {"settings", "get", "a", "b"}, {"settings", "list", "extra"}, {"settings", "schema", "extra"}, {"settings", "validate", "extra"}, {"settings", "set"}, {"settings", "set", "a"}, {"settings", "set", "a", "b", "c"},
		{"theme", "list", "extra"}, {"theme", "set"}, {"theme", "set", "a", "b"}, {"theme", "show", "extra"}, {"theme", "roles", "extra"}, {"theme", "check", "extra"},
		{"profile", "use"}, {"profile", "use", "a", "b"}, {"profile", "list", "extra"}, {"profile", "current", "extra"},
		{"secrets", "set"}, {"secrets", "set", "a", "b"}, {"secrets", "grant", "a"}, {"secrets", "revoke", "a"}, {"secrets", "reveal"}, {"secrets", "list", "extra"}, {"config", "reload", "extra"},
	}
	for _, noun := range []string{"keys", "settings", "theme", "profile", "secrets", "config"} {
		cases = append(cases, []string{noun, "unknown"})
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			srv, h := serviceServer(t, `{}`, nil)
			if got := h.run(append(append([]string{}, args...), "--socket", srv.Socket)...); got != output.ExitUsage {
				t.Fatalf("exit %d", got)
			}
			if srv.connections() != 0 {
				t.Fatal("connected for invalid syntax")
			}
		})
	}
	for _, args := range [][]string{{"keys", "--source"}, {"settings", "get", "a", "--source"}, {"settings", "list", "--owner", "husk"}, {"settings", "schema", "--action", "a"}, {"theme", "show", "--shadowed"}, {"profile", "list", "--problems"}, {"secrets", "list", "--focus", "a"}, {"config", "reload", "--source"}, {"settings", "set", "a", "b", "--source"}, {"theme", "list", "--owner", "husk"}, {"theme", "set", "a", "--owner", "husk"}} {
		h := newHarness(t, shortDir(t), nil)
		if got := h.run(args...); got != output.ExitUsage {
			t.Fatalf("flag acceptance: exit %d", got)
		}
	}
}

func TestSecretInput(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"lf", "fake-token-1\n", "fake-token-1"}, {"crlf", "fake-token-1\r\n", "fake-token-1"}, {"one newline", "fake-token-1\n\n", "fake-token-1\n"}, {"empty", "", ""}, {"bare cr", "fake-token-1\r", "fake-token-1\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, h := serviceServer(t, `{}`, nil)
			h.env.Stdin = strings.NewReader(tc.input)
			if code := h.run("secrets", "set", "demo.token"); code != 0 {
				t.Fatalf("exit %d", code)
			}
			var p struct{ Value string }
			if json.Unmarshal(lastRequest(t, srv).Params, &p) != nil || p.Value != tc.want {
				t.Fatal("secret value mismatch")
			}
		})
	}
	t.Run("invalid UTF-8", func(t *testing.T) {
		srv, h := serviceServer(t, `{}`, nil)
		h.env.Stdin = bytes.NewReader([]byte{0xff})
		if got := h.run("secrets", "set", "demo.token"); got != output.ExitUsage || srv.connections() != 0 {
			t.Fatal("invalid stdin was not rejected before connecting")
		}
	})
	t.Run("server owns size cap", func(t *testing.T) {
		srv, h := serviceServer(t, "", &protocol.Error{Code: protocol.CodeInvalidParams, Message: "too large", Data: json.RawMessage(`{"reason":"secret_limit"}`)})
		h.env.Stdin = strings.NewReader(strings.Repeat("x", 65537))
		if got := h.run("secrets", "set", "demo.token", "--socket", srv.Socket); got != output.ExitRemote || len(srv.requests()) != 2 {
			t.Fatal("secret cap was not left to instance")
		}
		code, _, data := errObject(t, h)
		if code != string(protocol.CodeInvalidParams) || string(data) != `{"reason":"secret_limit"}` {
			t.Fatal("secret limit reason lost")
		}
	})
	t.Run("request line limit", func(t *testing.T) {
		srv, h := serviceServer(t, `{}`, nil)
		h.env.Stdin = strings.NewReader(strings.Repeat("x", protocol.MaxLineBytes))
		if got := h.run("secrets", "set", "demo.token"); got != output.ExitUsage || srv.connections() != 0 {
			t.Fatal("oversize request connected")
		}
	})
	t.Run("terminal reader", func(t *testing.T) {
		srv, h := serviceServer(t, `{}`, nil)
		h.env.StdinIsTTY = true
		h.env.ReadPassword = func() ([]byte, error) { return []byte("fake-token-1"), nil }
		if got := h.run("secrets", "set", "demo.token"); got != 0 {
			t.Fatalf("exit %d", got)
		}
		var p struct{ Value string }
		if json.Unmarshal(lastRequest(t, srv).Params, &p) != nil || p.Value != "fake-token-1" {
			t.Fatal("secret value mismatch")
		}
		if h.errb.String() != "secret for demo.token: \n" {
			t.Fatal("prompt mismatch")
		}
	})
	t.Run("reader error is redacted", func(t *testing.T) {
		_, h := serviceServer(t, `{}`, nil)
		h.env.StdinIsTTY = true
		h.env.ReadPassword = func() ([]byte, error) { return nil, errors.New("fake-token-1") }
		if got := h.run("secrets", "set", "demo.token"); got != output.ExitUsage || strings.Contains(h.errb.String(), "fake-token-1") {
			t.Fatal("unsafe reader error")
		}
	})
}

func TestServicesTimeouts(t *testing.T) {
	for noun, cmd := range builtins() {
		if noun != "keys" && noun != "settings" && noun != "theme" && noun != "profile" && noun != "secrets" && noun != "config" {
			continue
		}
		commands := []command{cmd}
		if len(cmd.verbs) > 0 {
			commands = nil
			for _, v := range cmd.verbs {
				commands = append(commands, v)
			}
		}
		for _, c := range commands {
			t.Run(c.usage, func(t *testing.T) {
				want := 5 * time.Second
				if noun == "config" || c.usage == "husk settings set <key> <value>" || c.usage == "husk theme set <name>" {
					want = 10 * time.Second
				}
				if c.timeout != want {
					t.Fatalf("%s timeout", c.usage)
				}
				if got, _ := effectiveTimeout(123*time.Millisecond, c.timeout); got != 123*time.Millisecond {
					t.Fatal("override lost")
				}
			})
		}
	}
}
