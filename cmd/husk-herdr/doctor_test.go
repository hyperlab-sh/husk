package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/adapters/herdr/fakeherdr"
)

func TestDoctorUsesFakeServerAndReportsHookDecision(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	sockets, err := os.MkdirTemp("/tmp", "hd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	fakeUpstream(t, filepath.Join(sockets, "u.sock"), fakeherdr.Options{Plugins: []map[string]any{{"plugin_id": "example", "enabled": true}}})
	t.Setenv("HUSK_BIN_PATH", filepath.Join(root, "missing-cli"))
	exe, _ := os.Executable()
	dir := filepath.Join(root, "husk-herdr")
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "config.toml"), []byte(fmt.Sprintf("herdr_bin=%q\n", exe)), 0600)
	_ = os.MkdirAll(filepath.Join(root, "herdr"), 0700)
	_ = os.WriteFile(filepath.Join(root, "herdr", "plugins.json"), []byte(`[{"plugin_id":"example","enabled":true}]`), 0600)
	var out, errout bytes.Buffer
	env := guardUpstream(t, captureEnvironment(exe, runtime.GOOS, os.Getenv, os.UserHomeDir))
	if code := execute([]string{"doctor", "--instance", "unit"}, &out, &errout, env); code != 0 {
		t.Fatalf("doctor=%d: %s", code, &errout)
	}
	text := out.String()
	for _, want := range []string{"version: 0.9.5", "registry_enabled=true server_enabled=true hooks=server", "manual check:", "instance: unavailable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
	t.Setenv("HUSK_BIN_PATH", exe)
	t.Setenv("HUSK_INSTANCE", "inherited-instance")
	t.Setenv("HUSK_PLUGIN_TOKEN", "private-fixture")
	out.Reset()
	errout.Reset()
	if code := execute([]string{"doctor"}, &out, &errout, env); code != 0 || strings.Contains(out.String(), "instance: unavailable") || strings.Contains(out.String(), "absent from instance") {
		t.Fatalf("doctor did not preserve user targeting while dropping credentials: %d %s %s", code, &out, &errout)
	}
	for _, args := range [][]string{{"run"}, {"run", "--bad"}, {"run", "dir", "--instance", "unit"}, {"doctor", "--bad"}} {
		out.Reset()
		errout.Reset()
		if code := execute(args, &out, &errout, env); code != 2 {
			t.Fatalf("accepted role flags: %v code=%d", args, code)
		}
	}
}
