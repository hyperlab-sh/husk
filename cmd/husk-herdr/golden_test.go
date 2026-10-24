package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteConversionGoldens(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("cannot find module root")
		}
		root = parent
	}
	paths, err := filepath.Glob(filepath.Join(root, "adapters", "herdr", "testdata", "manifests", "*", "herdr-plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures")
	}
	for _, path := range paths {
		dir := filepath.Dir(path)
		for _, platform := range []string{"linux", "darwin", "windows"} {
			t.Run(filepath.Base(dir)+"/"+platform, func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				env := environment{executable: "/opt/husk-herdr", platform: platform}
				if code := execute([]string{"convert", dir}, &out, &diagnostics, env); code != 0 {
					t.Fatalf("conversion=%d: %s", code, &diagnostics)
				}
				expected, err := os.ReadFile(filepath.Join(dir, platform+".golden.json"))
				if err != nil {
					t.Fatal(err)
				}
				var compact bytes.Buffer
				if err := json.Compact(&compact, expected); err != nil {
					t.Fatal(err)
				}
				compact.WriteByte('\n')
				actualDir, _ := json.Marshal(dir)
				stableDir, _ := json.Marshal("/plugins/" + filepath.Base(dir))
				actual := bytes.ReplaceAll(out.Bytes(), actualDir, stableDir)
				if !bytes.Equal(actual, compact.Bytes()) {
					t.Fatalf("stdout differs:\n%s", actual)
				}
				expected, err = os.ReadFile(filepath.Join(dir, platform+".problems.golden"))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(diagnostics.Bytes(), expected) {
					t.Fatalf("stderr differs:\n%s", &diagnostics)
				}
			})
		}
	}
}
