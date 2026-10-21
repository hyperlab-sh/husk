package manifest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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

func TestRelativeExecutableResolvedOnce(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(sub, "tool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Relative path with ./ prefix, like --plugin ./plugins/foo/plugin.sh
	// run from the parent directory.
	t.Chdir(dir)
	p, err := Load("./sub/tool")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := p.ResolvedCommand()[0]
	if !filepath.IsAbs(got) {
		t.Errorf("ResolvedCommand[0] = %q, want absolute", got)
	}
	if got != bin {
		t.Errorf("ResolvedCommand[0] = %q, want %q", got, bin)
	}
}

func TestBareCommandUsesPath(t *testing.T) {
	// Bare argv[0] in a manifest must NOT be resolved against the dir.
	path := writeManifest(t, t.TempDir(),
		`{"id": "i", "name": "n", "version": "v", "command": ["sh", "-c", "true"]}`)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Command[0] != "sh" {
		t.Errorf("Command[0] = %q, want %q (PATH lookup)", p.Command[0], "sh")
	}
}

func TestInterpreterWithRelativeScriptArg(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "plugin.py")
	if err := os.WriteFile(script, []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := writeManifest(t, dir,
		`{"id": "i", "name": "n", "version": "v", "command": ["python3", "./plugin.py"]}`)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	argv := p.ResolvedCommand()
	if argv[0] != "python3" {
		t.Errorf("argv[0] = %q, want python3 (PATH lookup)", argv[0])
	}
	if argv[1] != script {
		t.Errorf("argv[1] = %q, want %q", argv[1], script)
	}
}

func TestOptionArgumentsUntouched(t *testing.T) {
	path := writeManifest(t, t.TempDir(),
		`{"id": "i", "name": "n", "version": "v", "command": ["./run.sh", "--mode", "sh", "--flag=./x"]}`)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	argv := p.ResolvedCommand()
	for i, want := range []string{"--mode", "sh", "--flag=./x"} {
		if argv[1+i] != want {
			t.Errorf("argv[%d] = %q, want %q", 1+i, argv[1+i], want)
		}
	}
}

// TestExecutesFromOtherCwd runs a manifest-launched plugin from a working
// directory unrelated to the manifest, as the workbench would.
func TestExecutesFromOtherCwd(t *testing.T) {
	pluginDir := t.TempDir()
	script := filepath.Join(pluginDir, "plugin.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat ./data.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "data.txt"), []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := writeManifest(t, pluginDir, validManifest)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Run from an unrelated cwd using WorkDir, like spawnStdioCommand does.
	other := t.TempDir()
	argv := p.ResolvedCommand()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = p.WorkDir()
	// Make ./plugin.sh (cwd-dependent) resolve: copy script expectations by
	// chdir-independent check — the script reads ./data.txt relative to cmd.Dir.
	_ = other
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(out) != "payload\n" {
		t.Errorf("output = %q, want %q", out, "payload\n")
	}
}

func TestRelativePluginDirResolvesOnce(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, validManifest)
	// LoadDir with a relative dir, like --plugin-dir ./plugins.
	t.Chdir(root)
	plugins, err := LoadDir(".")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("got %d plugins, want 1", len(plugins))
	}
	argv := plugins[0].ResolvedCommand()
	if !filepath.IsAbs(argv[0]) {
		t.Errorf("ResolvedCommand[0] = %q, want absolute", argv[0])
	}
	if argv[0] != filepath.Join(dir, "plugin.sh") {
		t.Errorf("ResolvedCommand[0] = %q, want %q", argv[0], filepath.Join(dir, "plugin.sh"))
	}
	if !filepath.IsAbs(plugins[0].WorkDir()) {
		t.Errorf("WorkDir = %q, want absolute", plugins[0].WorkDir())
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in      string
		want    []string
		wantErr string
	}{
		{"a b c", []string{"a", "b", "c"}, ""},
		{"  a \t b  ", []string{"a", "b"}, ""},
		{"", nil, ""},
		{`python3 "my plugin.py" --flag`, []string{"python3", "my plugin.py", "--flag"}, ""},
		{`'/opt/my dir/bin' x`, []string{"/opt/my dir/bin", "x"}, ""},
		{`/opt/my\ dir/bin x`, []string{"/opt/my dir/bin", "x"}, ""},
		{`a "" b`, []string{"a", "", "b"}, ""},
		{`--name="two words"`, []string{"--name=two words"}, ""},
		{`"say \"hi\""`, []string{`say "hi"`}, ""},
		{`"back\\slash"`, []string{`back\slash`}, ""},
		{`"keep\nescape"`, []string{`keep\nescape`}, ""},
		{`'no \escape here'`, []string{`no \escape here`}, ""},
		{`a"b c"d`, []string{"ab cd"}, ""},
		{`"unterminated x`, nil, "unterminated \" quote"},
		{`'unterminated x`, nil, "unterminated ' quote"},
		{`trailing\`, nil, "trailing backslash"},
	}
	for _, c := range cases {
		got, err := splitCommand(c.in)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("splitCommand(%q) err = %v, want %q", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitCommand(%q) unexpected error %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCommand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoadNonexistentCommandLine(t *testing.T) {
	p, err := Load(`python3 "my plugin.py"`)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"python3", "my plugin.py"}; !reflect.DeepEqual(p.Command, want) {
		t.Fatalf("Command = %q, want %q", p.Command, want)
	}
	if p.Path != "" {
		t.Fatalf("implicit plugin has Path %q", p.Path)
	}
	if _, err := Load(`python3 "oops`); err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("unterminated quote error = %v", err)
	} else if !strings.Contains(err.Error(), `plugin path "python3 \"oops" not found`) {
		t.Fatalf("path is not quoted in %q", err)
	}
	if _, err := Load("/definitely/missing/plugin"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("single missing token error = %v", err)
	}
}

func TestCheckHello(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, `{"id":"a.b","name":"n","version":"1","command":["x"],"slots":["main-stage","side-panel"]}`)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != path {
		t.Fatalf("Path = %q, want %q", p.Path, path)
	}
	cases := []struct {
		id      string
		slots   []string
		wantErr string
	}{
		{"a.b", []string{"side-panel", "main-stage"}, ""},
		{"a.b", []string{"main-stage", "main-stage", "side-panel"}, ""},
		{"a.c", []string{"main-stage", "side-panel"}, `plugin_id "a.c" does not match manifest id "a.b"`},
		{"a.b", []string{"main-stage"}, "omits declared slots [side-panel]"},
		{"a.b", []string{"main-stage", "side-panel", "main-header"}, "claims undeclared slots [main-header]"},
		{"a.b", []string{"main-header"}, "claims undeclared slots [main-header]; omits declared slots [main-stage side-panel]"},
		{"a.b", nil, "omits declared slots [main-stage side-panel]"},
	}
	for _, c := range cases {
		err := p.CheckHello(c.id, c.slots)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("CheckHello(%q,%v) = %v, want nil", c.id, c.slots, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("CheckHello(%q,%v) = %v, want %q", c.id, c.slots, err, c.wantErr)
		}
	}
	// A manifest with no slots forbids claiming any.
	pn := &Plugin{ID: "z", Path: "/z/" + FileName}
	if err := pn.CheckHello("z", []string{"main-stage"}); err == nil {
		t.Error("slotless manifest accepted a slot claim")
	}
	if err := pn.CheckHello("z", nil); err != nil {
		t.Errorf("slotless manifest rejected empty claim: %v", err)
	}
	// Implicit plugins are never checked.
	if err := (&Plugin{ID: "z"}).CheckHello("other", []string{"main-stage"}); err != nil {
		t.Errorf("implicit plugin checked: %v", err)
	}
}

func TestParseDeclarations(t *testing.T) {
	path := writeManifest(t, t.TempDir(), `{
  "id": "acme.p", "name": "n", "version": "1", "command": ["x"],
  "slots": ["main-stage"],
  "published_slots": [
    {"id": "acme.p.rows", "title": "Rows", "description": "Row actions", "kind": "inline"},
    {"id": "acme.p.bar", "kind": "region", "rect": {"surface": "main-stage", "x": 1, "y": 2, "width": 30, "height": 3}}
  ],
  "hooks": [{"id": "acme.p.before", "title": "Before", "description": "d"}],
  "events": [{"id": "acme.p.changed"}]
}`)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.PublishedSlots) != 2 || p.PublishedSlots[0].Title != "Rows" || p.PublishedSlots[0].Kind != "inline" {
		t.Fatalf("PublishedSlots = %+v", p.PublishedSlots)
	}
	r := p.PublishedSlots[1].Rect
	if r == nil || r.Surface != "main-stage" || r.X != 1 || r.Y != 2 || r.Width != 30 || r.Height != 3 {
		t.Fatalf("rect = %+v", r)
	}
	if len(p.Hooks) != 1 || p.Hooks[0].ID != "acme.p.before" || p.Hooks[0].Description != "d" {
		t.Fatalf("Hooks = %+v", p.Hooks)
	}
	if len(p.Events) != 1 || p.Events[0].ID != "acme.p.changed" {
		t.Fatalf("Events = %+v", p.Events)
	}
}

func TestManifestWithoutDeclarationsStillLoads(t *testing.T) {
	p, err := Load(writeManifest(t, t.TempDir(), validManifest))
	if err != nil {
		t.Fatal(err)
	}
	if p.PublishedSlots != nil || p.Hooks != nil || p.Events != nil {
		t.Fatalf("unexpected declarations: %+v %+v %+v", p.PublishedSlots, p.Hooks, p.Events)
	}
}
