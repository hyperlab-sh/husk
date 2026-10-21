package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validManifest = `{
  "id": "bash.project-git",
  "name": "Project Git Stage",
  "version": "0.1.0",
  "language": "Bash",
  "description": "Git stage",
  "command": ["./plugin.sh"],
  "transport": "stdio",
  "slots": ["workspace-stage"]
}`

func TestParseValidManifest(t *testing.T) {
	path := writeManifest(t, t.TempDir(), validManifest)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.ID != "bash.project-git" {
		t.Errorf("ID = %q", p.ID)
	}
	if p.Transport != TransportStdio {
		t.Errorf("Transport = %q, want stdio", p.Transport)
	}
	if len(p.Slots) != 1 || p.Slots[0] != "workspace-stage" {
		t.Errorf("Slots = %v", p.Slots)
	}
}

func TestMissingRequiredField(t *testing.T) {
	for _, missing := range []string{"id", "name", "version"} {
		body := `{"name": "n", "id": "i", "version": "v", "command": ["x"]}`
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		delete(m, missing)
		rebody, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		path := writeManifest(t, t.TempDir(), string(rebody))
		if _, err := Load(path); err == nil {
			t.Errorf("expected error when %q is missing", missing)
		}
	}
	// missing command
	path := writeManifest(t, t.TempDir(), `{"id": "i", "name": "n", "version": "v"}`)
	if _, err := Load(path); err == nil {
		t.Error("expected error when command is missing")
	}
}

func TestRelativeCommandResolution(t *testing.T) {
	dir := t.TempDir()
	p, err := Load(writeManifest(t, dir, validManifest))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := p.ResolvedCommand()
	want := filepath.Join(dir, "plugin.sh")
	if got[0] != want {
		t.Errorf("ResolvedCommand[0] = %q, want %q", got[0], want)
	}
}

func TestTransportDefault(t *testing.T) {
	path := writeManifest(t, t.TempDir(),
		`{"id": "i", "name": "n", "version": "v", "command": ["./run"]}`)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Transport != TransportSocket {
		t.Errorf("Transport = %q, want default %q", p.Transport, TransportSocket)
	}
}

func TestLoadDir(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, filepath.Join(root, "a"), validManifest)
	writeManifest(t, filepath.Join(root, "b"), validManifest)
	// no manifest: skipped
	if err := os.MkdirAll(filepath.Join(root, "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	plugins, err := LoadDir(root)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(plugins) != 2 {
		t.Fatalf("LoadDir returned %d plugins, want 2", len(plugins))
	}
}

func TestLoadExecutable(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sidebar")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := Load(bin)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Transport != TransportSocket {
		t.Errorf("Transport = %q, want socket", p.Transport)
	}
	if p.Command[0] != bin {
		t.Errorf("Command = %v", p.Command)
	}
}
