package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFatalDiagnosticIsFirst(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "registered"
		if missing {
			name = "missing_registry"
		}
		t.Run(name, func(t *testing.T) {
			env, dir := testEnv(t)
			data, err := os.ReadFile(filepath.Join(dir, "herdr-plugin.toml"))
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("[[actions]]\nid='show'\ntitle='Show'\ncommand=['true']\ndefault_key='prefix+s'\n[[panes]]\nid='view'\ntitle='View'\ncommand=['true']\nwidth='oops'\n")...)
			if err := os.WriteFile(filepath.Join(dir, "herdr-plugin.toml"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if missing {
				env.registryPath = filepath.Join(dir, "missing.json")
			}
			var out, diagnostics bytes.Buffer
			if code := execute([]string{"convert", dir}, &out, &diagnostics, env); code != 1 || out.Len() != 0 {
				t.Fatalf("refusal=%d out=%s", code, &out)
			}
			first, _, _ := bytes.Cut(diagnostics.Bytes(), []byte("\n"))
			var problem struct{ Code, Field string }
			if err := json.Unmarshal(first, &problem); err != nil {
				t.Fatal(err)
			}
			if problem.Code != "manifest_invalid" || problem.Field != "panes[0]" {
				t.Fatalf("first stderr line hides refusal: %s", first)
			}
		})
	}
}

func TestInvalidConfigDoesNotHideDiscovery(t *testing.T) {
	env, dir := testEnv(t)
	records := []map[string]any{{"plugin_id": "sample:tool", "plugin_root": dir, "enabled": true}}
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.registryPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.configPath, []byte("hooks=["), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if code := execute([]string{"discover"}, &out, &diagnostics, env); code != 0 || out.String() != dir+"\n" || diagnostics.Len() != 0 {
		t.Fatalf("discovery hidden: code=%d out=%s err=%s", code, &out, &diagnostics)
	}
	out.Reset()
	diagnostics.Reset()
	if code := execute([]string{"convert", dir}, &out, &diagnostics, env); code != 1 || out.Len() != 0 || !bytes.Contains(diagnostics.Bytes(), []byte(`"code":"adapter_config_invalid"`)) {
		t.Fatalf("config refusal lost: code=%d out=%s err=%s", code, &out, &diagnostics)
	}
}
