package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv(t *testing.T) (environment, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "plugin")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "herdr-plugin.toml"), []byte("id='sample:tool'\nname='Sample'\nversion='1'\nmin_herdr_version='0.9.0'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env := environment{executable: "/opt/husk-herdr", platform: "linux", configPath: filepath.Join(root, "config.toml"), registryPath: filepath.Join(root, "plugins.json")}
	if err := os.WriteFile(env.registryPath, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	return env, dir
}

func TestConverterRoles(t *testing.T) {
	env, dir := testEnv(t)
	for _, args := range [][]string{nil, {"run"}, {"sync"}, {"run", dir, "--bad"}, {"detect"}, {"discover", "--instance", "x"}, {"convert", "--bad"}} {
		var out, err bytes.Buffer
		if code := execute(args, &out, &err, env); code != 2 || out.Len() != 0 || !strings.Contains(err.String(), "usage:") {
			t.Fatalf("args=%v code=%d out=%s err=%s", args, code, &out, &err)
		}
	}
	var out, diagnostics bytes.Buffer
	if code := execute([]string{"detect", dir}, &out, &diagnostics, env); code != 0 || out.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("detect=%d", code)
	}
	if code := execute([]string{"detect", filepath.Join(dir, "absent")}, &out, &diagnostics, env); code != 1 {
		t.Fatalf("decline=%d", code)
	}
	if code := execute([]string{"convert", dir}, &out, &diagnostics, env); code != 0 {
		t.Fatalf("convert=%d: %s", code, &diagnostics)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["id"] != "sample-tool" || doc["state"] != nil || len(doc["command"].([]any)) != 3 || doc["command"].([]any)[2] != dir {
		t.Fatalf("document=%v", doc)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected problems: %s", &diagnostics)
	}
	// Missing registries warn on discovery only, not direct conversion.
	env.registryPath = filepath.Join(dir, "absent.json")
	out.Reset()
	diagnostics.Reset()
	if code := execute([]string{"discover"}, &out, &diagnostics, env); code != 0 || out.Len() != 0 || !strings.Contains(diagnostics.String(), `"code":"registry_warning"`) {
		t.Fatalf("missing registry code=%d out=%s err=%s", code, &out, &diagnostics)
	}
	out.Reset()
	diagnostics.Reset()
	if code := execute([]string{"convert", dir}, &out, &diagnostics, env); code != 0 || out.Len() == 0 || diagnostics.Len() != 0 {
		t.Fatalf("direct conversion needs no registry or registry warning: code=%d err=%s", code, &diagnostics)
	}
}

func TestCLIRegistryCollisions(t *testing.T) {
	env, dir := testEnv(t)
	records := []map[string]any{{"plugin_id": "sample:tool", "plugin_root": dir, "enabled": true}, {"plugin_id": "sample-tool", "plugin_root": "/plugins/other", "enabled": true}}
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.registryPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if code := execute([]string{"convert", dir}, &out, &diagnostics, env); code != 1 || out.Len() != 0 || !strings.Contains(diagnostics.String(), `"code":"id_unmappable"`) {
		t.Fatalf("collision=%d out=%s err=%s", code, &out, &diagnostics)
	}
	out.Reset()
	diagnostics.Reset()
	if code := execute([]string{"discover"}, &out, &diagnostics, env); code != 0 || strings.TrimSpace(out.String()) != dir {
		t.Fatalf("discover=%d out=%s err=%s", code, &out, &diagnostics)
	}
	records[0]["plugin_root"] = "/plugins/unrelated"
	data, err = json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.registryPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diagnostics.Reset()
	if code := execute([]string{"convert", dir}, &out, &diagnostics, env); code != 0 || out.Len() == 0 {
		t.Fatalf("unregistered directory refused: code=%d err=%s", code, &diagnostics)
	}
}

func TestBuildIsOnlyANote(t *testing.T) {
	env, dir := testEnv(t)
	path := filepath.Join(dir, "herdr-plugin.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("[[build]]\ncommand=['not-an-executable']\n")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if code := execute([]string{"convert", dir}, &out, &diagnostics, env); code != 0 || !json.Valid(out.Bytes()) {
		t.Fatalf("code=%d err=%s", code, &diagnostics)
	}
	var note map[string]string
	if err := json.Unmarshal(diagnostics.Bytes(), &note); err != nil {
		t.Fatal(err)
	}
	if note["code"] != "" || note["level"] != "info" || !strings.HasPrefix(note["message"], "build_not_run:") {
		t.Fatalf("build became a problem: %v", note)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, data) {
		t.Fatal("conversion modified the manifest")
	}
}

func TestAdapterConfig(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{{"missing", "", true}, {"valid", "server='attach'\nhooks='auto'\n[plugins.'sample:tool']\nhooks='never'", true}, {"syntax", "server=[", false}, {"type", "hooks=1", false}, {"server", "server='stop'", false}, {"hooks", "hooks='sometimes'", false}, {"plugin", "[plugins.sample]\nhooks='sometimes'", false}, {"binary", "herdr_bin='relative'", false}} {
		t.Run(tc.name, func(t *testing.T) {
			env, dir := testEnv(t)
			if tc.name != "missing" {
				if err := os.WriteFile(env.configPath, []byte(tc.data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, diagnostics bytes.Buffer
			code := execute([]string{"convert", dir}, &out, &diagnostics, env)
			if tc.valid {
				if code != 0 || out.Len() == 0 {
					t.Fatalf("config rejected: code=%d err=%s", code, &diagnostics)
				}
			} else if code != 1 || out.Len() != 0 || !strings.Contains(diagnostics.String(), `"code":"adapter_config_invalid"`) || !strings.Contains(diagnostics.String(), "line ") {
				t.Fatalf("bad config: code=%d out=%s err=%s", code, &out, &diagnostics)
			}
		})
	}
}
