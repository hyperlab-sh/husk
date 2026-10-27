package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
)

func TestCLIProblem(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"launch", "{\"error\":{\"code\":\"failed\",\"message\":\"plugins failed to launch: example\"}}\n", "failed: plugins failed to launch: example"},
		{"reason", `{"error":{"code":"conflict","message":"already loaded","data":{"reason":"duplicate_plugin_id","plugin":"example"}}}`, "conflict: already loaded (duplicate_plugin_id)"},
		{"controls", `{"error":{"code":"failed","message":"first\nsecond\tline","data":{"reason":"some\nreason"}}}`, "failed: first second line (some reason)"},
		{"exec fallback", "exec failed\n  permission denied\n", "exec failed permission denied"},
		{"invalid json", "{bad\njson", "{bad json"},
		{"wrong shape", `{"warning":{"message":"ignored"}}`, `{"warning":{"message":"ignored"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cliProblem([]byte(tc.raw)); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestConversionProblemSelectsFirstProblemNotNotes(t *testing.T) {
	raw := "{\"level\":\"info\",\"message\":\"build not run\"}\n" +
		"{\"code\":\"manifest_invalid\",\"message\":\"first\\nproblem\"}\n" +
		"{\"code\":\"other\",\"message\":\"second problem\"}\n"
	if got := conversionProblem([]byte(raw)); got != "manifest_invalid: first problem" {
		t.Fatalf("diagnostics=%q", got)
	}
	for _, raw := range []string{"", "{\"level\":\"info\",\"message\":\"build not run\"}\n"} {
		if got := conversionProblem([]byte(raw)); got != "conversion failed" {
			t.Fatalf("notes without a problem: %q", got)
		}
	}
}

func TestDoctorSyncabilityAndListProblems(t *testing.T) {
	sockets, err := os.MkdirTemp("/tmp", "hr-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	fakeUpstream(t, filepath.Join(sockets, "u.sock"), fakeherdr.Options{})
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := guardUpstream(t, captureEnvironment(exe, runtime.GOOS, os.Getenv, os.UserHomeDir))
	if err := os.MkdirAll(filepath.Dir(env.configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.configPath, []byte(fmt.Sprintf("herdr_bin=%q\n", exe)), 0600); err != nil {
		t.Fatal(err)
	}
	valid := syncFixture(t, root, "manifest:current", true)
	broken := syncFixture(t, root, "broken", true)
	if err := os.WriteFile(filepath.Join(broken, "herdr-plugin.toml"), []byte("id='broken'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	conversion := syncFixture(t, root, "conversion", false)
	records := []map[string]any{
		{"plugin_id": "registry:stale", "plugin_root": valid, "enabled": true},
		{"plugin_id": "broken", "plugin_root": broken, "enabled": true},
		{"plugin_id": "relative", "plugin_root": "relative-plugin", "enabled": true},
		{"plugin_id": "conversion", "plugin_root": conversion, "enabled": true},
	}
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(env.registryPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.registryPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	list := "[]"
	listError, listExit := "", 0
	env.runCLI = func(args []string) ([]byte, []byte, int) {
		if args[0] != "plugin" || args[1] != "list" {
			t.Fatalf("unexpected CLI command %v", args)
		}
		return []byte(list), []byte(listError), listExit
	}
	var out, diagnostic bytes.Buffer
	if code := execute([]string{"doctor"}, &out, &diagnostic, env); code != 0 {
		t.Fatalf("doctor=%d: %s", code, &diagnostic)
	}
	if !strings.Contains(out.String(), "plugin registry:stale absent from instance; run husk-herdr sync") || strings.Count(out.String(), "absent from instance") != 1 || !strings.Contains(out.String(), "registry_warning:") || !strings.Contains(out.String(), "plugin_root must be an absolute") || !strings.Contains(out.String(), "plugin conversion: duplicate_action:") {
		t.Fatalf("syncability: %s", &out)
	}
	list = `[{"id":"manifest-current"}]`
	out.Reset()
	if code := execute([]string{"doctor"}, &out, &diagnostic, env); code != 0 || strings.Contains(out.String(), "absent from instance") {
		t.Fatalf("converted manifest id not respected: %d %s", code, &out)
	}
	listError, listExit = `{"error":{"code":"no_instance","message":"instance is missing","data":{"reason":"not_running"}}}`, 3
	out.Reset()
	if code := execute([]string{"doctor"}, &out, &diagnostic, env); code != 0 || !strings.Contains(out.String(), "instance: unavailable (no_instance: instance is missing (not_running))") {
		t.Fatalf("doctor list error: %d %s", code, &out)
	}
	out.Reset()
	diagnostic.Reset()
	if code := execute([]string{"sync"}, &out, &diagnostic, env); code != 3 || out.Len() != 0 || !strings.HasSuffix(diagnostic.String(), "no_instance: instance is missing (not_running)\n") || strings.Contains(diagnostic.String(), `"error"`) {
		t.Fatalf("sync list error: %d %s %s", code, &out, &diagnostic)
	}
}

func TestSyncConversionRefusalLabels(t *testing.T) {
	sockets, err := os.MkdirTemp("/tmp", "hl-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	fakeUpstream(t, filepath.Join(sockets, "u.sock"), fakeherdr.Options{})
	for _, registryID := range []string{"registry:label", "!unmappable"} {
		t.Run(registryID, func(t *testing.T) {
			root := t.TempDir()
			dir := syncFixture(t, root, "invalid", false)
			raw, err := json.Marshal([]map[string]any{{"plugin_id": registryID, "plugin_root": dir, "enabled": true}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "plugins.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			env := guardUpstream(t, environment{executable: "/adapter", platform: runtime.GOOS, registryPath: path})
			env.runCLI = func(args []string) ([]byte, []byte, int) {
				if args[1] != "list" {
					t.Fatalf("conversion refusal called add: %v", args)
				}
				return []byte("[]"), nil, 0
			}
			label := "registry-label"
			if registryID == "!unmappable" {
				label = dir
			}
			var out, diagnostic bytes.Buffer
			if code := execute([]string{"sync"}, &out, &diagnostic, env); code != 1 || out.String() != "refused "+label+": duplicate_action: duplicate mapped action \"go\"\n" {
				t.Fatalf("label: %d %s %s", code, &out, &diagnostic)
			}
		})
	}
}
