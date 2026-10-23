package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
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

func TestPluginAddLaunchOutcomesControlExitAndPreserveResult(t *testing.T) {
	for _, format := range []string{"json", "table"} {
		for _, tc := range []struct {
			name     string
			statuses []string
			wantExit int
		}{
			{"failed path", []string{"failed"}, output.ExitRemote},
			{"mixed directory", []string{"live", "failed"}, output.ExitRemote},
			{"multiple failures", []string{"failed", "failed"}, output.ExitRemote},
			{"live and removed", []string{"live", "removed"}, output.ExitOK},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				dir := shortDir(t)
				added := []map[string]any{}
				failed := []string{}
				for i, status := range tc.statuses {
					id := string(rune('a' + i))
					added = append(added, map[string]any{"plugin": id, "status": status, "log": "/logs/" + id + ".log", "warnings": []any{}})
					if status == "failed" {
						failed = append(failed, id)
					}
				}
				result := map[string]any{"added": added, "refused": []any{map[string]any{"path": "/bad", "code": "conversion", "message": "bad manifest"}}, "reports": []any{map[string]any{"service": "settings", "applied": false}}}
				srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, result) }))
				h := newHarness(t, dir, nil)
				args := []string{"plugin", "add", "/plugins/a"}
				if len(tc.statuses) > 1 {
					args = []string{"plugin", "add", "--dir", "/plugins"}
				}
				code := h.run(append(args, "--socket", srv.Socket, "--format", format)...)
				if code != tc.wantExit {
					t.Fatalf("exit=%d stdout=%s stderr=%s", code, h.out.String(), h.errb.String())
				}
				if format == "json" {
					var got, want any
					raw, _ := json.Marshal(result)
					_ = json.Unmarshal(raw, &want)
					if json.Unmarshal(h.out.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("result changed: %s", h.out.String())
					}
				} else if !strings.Contains(h.out.String(), "/logs/a.log") || !strings.Contains(h.out.String(), "bad manifest") {
					t.Fatalf("missing outcomes: %s", h.out.String())
				}
				if len(failed) == 0 {
					if h.errb.Len() != 0 {
						t.Fatal(h.errb.String())
					}
					return
				}
				message := "plugins failed to launch: " + strings.Join(failed, ", ")
				if format == "json" {
					var diagnostic struct {
						Error struct{ Code, Message string }
					}
					if json.Unmarshal(h.errb.Bytes(), &diagnostic) != nil || diagnostic.Error.Code != string(protocol.CodeFailed) || diagnostic.Error.Message != message {
						t.Fatalf("diagnostic=%s", h.errb.String())
					}
				} else if h.errb.String() != "husk: "+message+"\n" {
					t.Fatalf("diagnostic=%s", h.errb.String())
				}
			})
		}
	}
}

func TestPluginAddDistinguishesLaunchFailureFromRefusalsAndReloadProblems(t *testing.T) {
	for _, format := range []string{"json", "table"} {
		for _, tc := range []struct {
			name, status     string
			refused, applied bool
			exit             int
		}{
			{"failed with applied reload", "failed", false, true, output.ExitRemote},
			{"live with reload problems", "live", false, false, output.ExitOK},
			{"refusal only", "", true, true, output.ExitOK},
			{"empty batch", "", false, true, output.ExitOK},
			{"removed with reload problems", "removed", false, false, output.ExitOK},
			{"failed with refusal and reload problems", "failed", true, false, output.ExitRemote},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				added, refused, reports := []any{}, []any{}, []any{}
				if tc.status != "" {
					added = append(added, map[string]any{"plugin": "alpha", "status": tc.status, "log": "/logs/alpha.log", "warnings": []any{}})
					for _, service := range []string{"profile", "settings", "theme", "keybinding", "secrets"} {
						reports = append(reports, map[string]any{"service": service, "gen": 1, "applied": tc.applied, "changed": false})
					}
				}
				if tc.refused {
					refused = append(refused, map[string]string{"path": "/plugins/bad", "plugin": "", "code": "conversion", "message": "bad manifest"})
				}
				result := map[string]any{"added": added, "refused": refused, "reports": reports}
				dir := shortDir(t)
				srv := serve(t, dir, "a", pingOK("a", func(c *srvConn, req protocol.Message) { c.reply(req, result) }))
				h := newHarness(t, dir, nil)
				if code := h.run("plugin", "add", "--dir", "/plugins", "--socket", srv.Socket, "--format", format); code != tc.exit {
					t.Fatalf("exit=%d stdout=%s stderr=%s", code, h.out.String(), h.errb.String())
				}
				raw, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				var want bytes.Buffer
				if err := (output.Options{Format: output.Format(format), Stdout: &want}).Result(raw); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(want.Bytes(), h.out.Bytes()) {
					t.Fatalf("batch result changed: %s", h.out.String())
				}
				if tc.exit == output.ExitOK {
					if h.errb.Len() != 0 {
						t.Fatalf("nonfailure produced stderr: %s", h.errb.String())
					}
				} else {
					if !strings.Contains(h.errb.String(), "plugins failed to launch: alpha") {
						t.Fatalf("missing failed plugin: %s", h.errb.String())
					}
					if format == "json" && !strings.Contains(h.errb.String(), `"code":"failed"`) {
						t.Fatalf("missing failed code: %s", h.errb.String())
					}
				}
			})
		}
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
