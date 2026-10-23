package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	ports "github.com/hyperlab-sh/husk/internal/host/ports/services"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
)

func bootDottedSettingsHost(t *testing.T) *harness {
	t.Helper()
	dir := writePlugin(t, "acme.git", false)
	raw := `{"manifest_version":2,"protocol":2,"id":"acme.git","name":"Dotted settings","version":"1","command":["./plugin"],"settings":[{"key":"show_hidden","type":"bool","default":false}]}`
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	hook, ready := readyHook("")
	f := newFakes()
	h, err := boot(t, func(c *contracts.Config) { pluginSettings(c)["paths"] = []string{dir} }, withHook(hook), WithFactory(f.factory))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root.store.Paths().Config, "themes.json"), []byte(`{"version":1,"themes":{"evening":{"palette":{"accent":"#f1effe"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	ctx := boundCtx(t)
	select {
	case id := <-f.started:
		if id != "acme.git" {
			t.Fatalf("started %q", id)
		}
	case <-ctx.Done():
		t.Fatal("plugin did not start")
	}
	return h
}

func TestSettingsSetDottedPluginKeyReturnsUserSource(t *testing.T) {
	h := bootDottedSettingsHost(t)
	const key = "plugins.acme.git.show_hidden"
	r := hostSet(t, h, key, true)
	p := ports.Settings{Svc: h.root.rt.Settings, Inputs: h.root.rt.Coordinator}
	value, source, err := p.Get(t.Context(), contracts.Caller{Kind: protocol.CallerPlugin, PluginID: "acme.git"}, key)
	if err != nil || value != true || source != "user" || r.Source != "user" || r.Value != true {
		t.Fatalf("reply=%+v get=%v/%s err=%v", r, value, source, err)
	}
	b, err := os.ReadFile(filepath.Join(h.root.store.Paths().Config, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	want := map[string]any{"plugins": map[string]any{"acme": map[string]any{"git": map[string]any{"show_hidden": true}}}}
	if err := json.Unmarshal(b, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("nested patch=%s err=%v", b, err)
	}
}

func TestSettingsWriteCLIAgainstRealHost(t *testing.T) {
	h := bootDottedSettingsHost(t)
	list := func(t *testing.T, current string) {
		t.Helper()
		code, out, stderr := cliRun(h, "theme", "list", "--format", "json")
		var reply struct{ Themes []contracts.ThemeEntry }
		if code != 0 || json.Unmarshal([]byte(out), &reply) != nil || len(reply.Themes) != 10 {
			t.Fatalf("list exit=%d out=%s err=%s", code, out, stderr)
		}
		if reply.Themes[0].Name != palette.Default().Name || reply.Themes[0].Source != "built-in" || reply.Themes[9].Name != "evening" || reply.Themes[9].Source != "user" {
			t.Fatalf("themes=%+v", reply.Themes)
		}
		for _, entry := range reply.Themes {
			if entry.Current != (entry.Name == current) {
				t.Fatalf("current=%s themes=%+v", current, reply.Themes)
			}
		}
	}
	t.Run("theme list builtin", func(t *testing.T) { list(t, palette.Default().Name) })
	for _, tc := range []struct {
		name  string
		args  []string
		key   string
		value any
	}{
		{"theme set", []string{"theme", "set", "evening"}, "theme.name", "evening"},
		{"settings set", []string{"settings", "set", "plugins.acme.git.show_hidden", "true"}, "plugins.acme.git.show_hidden", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, stderr := cliRun(h, append(tc.args, "--format", "json")...)
			var reply contracts.SettingSetResult
			if code != 0 || json.Unmarshal([]byte(out), &reply) != nil || reply.Key != tc.key || reply.Value != tc.value || reply.Source != "user" || len(reply.Reports) != 5 {
				t.Fatalf("exit=%d out=%s err=%s", code, out, stderr)
			}
			code, out, stderr = cliRun(h, "settings", "get", tc.key, "--format", "json")
			var serving contracts.SettingValue
			if code != 0 || json.Unmarshal([]byte(out), &serving) != nil || serving.Value != tc.value || serving.Source != "user" {
				t.Fatalf("get exit=%d out=%s err=%s", code, out, stderr)
			}
		})
	}
	t.Run("theme list selected", func(t *testing.T) { list(t, "evening") })
	t.Run("write errors exit one", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(h.root.store.Paths().Config, "config.json"), []byte(`{"theme":`), 0600); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			args   []string
			code   protocol.Code
			reason string
		}{
			{[]string{"settings", "set", "theme.name", "42"}, protocol.CodeInvalidParams, "invalid_value"},
			{[]string{"theme", "set", "evening"}, protocol.CodeUnavailable, "store_error"},
		} {
			t.Run(tc.args[0], func(t *testing.T) {
				code, out, stderr := cliRun(h, append(tc.args, "--format", "json")...)
				var reply struct {
					Error struct {
						Code protocol.Code
						Data struct{ Reason string }
					}
				}
				if code != output.ExitRemote || out != "" || json.Unmarshal([]byte(stderr), &reply) != nil || reply.Error.Code != tc.code || reply.Error.Data.Reason != tc.reason {
					t.Fatalf("exit=%d out=%s err=%s", code, out, stderr)
				}
			})
		}
	})
}
