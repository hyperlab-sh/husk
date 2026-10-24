package herdr_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("cannot find module root")
		}
		root = parent
	}
}

func checkCore(root string) ([]string, error) {
	var violations []string
	for _, dir := range []string{"cmd/husk", "pkg", "docs/docs/design"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			ext := filepath.Ext(path)
			if ext != ".go" && ext != ".md" {
				return nil
			}
			if entry.Name() == "herdr-adapter.md" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			// Navigation to the exempt spec is not application-specific prose.
			text := string(data)
			if ext == ".md" {
				text = strings.ReplaceAll(text, "(herdr-adapter.md)", "(adapter-spec)")
			}
			// Existing core tests name forbidden identifiers in their blacklists;
			// production source and design prose may not name them.
			if !strings.HasSuffix(path, "_test.go") && strings.Contains(strings.ToLower(text), "herdr") {
				violations = append(violations, path+": application name")
			}
			if ext == ".go" {
				f, err := parser.ParseFile(token.NewFileSet(), path, data, parser.ImportsOnly)
				if err != nil {
					return err
				}
				for _, imp := range f.Imports {
					name, err := strconv.Unquote(imp.Path.Value)
					if err != nil {
						return err
					}
					if strings.Contains(name, "github.com/hyperlab-sh/husk/adapters/") {
						violations = append(violations, path+": adapter import")
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return violations, nil
}

func TestAdapterIsNotImportedByCore(t *testing.T) {
	violations, err := checkCore(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("core boundary violations:\n%s", strings.Join(violations, "\n"))
	}
	// Pin both checks against synthetic trees, including the sole doc exemption.
	for _, tc := range []struct {
		name, path, content string
		count               int
	}{
		{"import", "pkg/example.go", "package example\nimport _ \"github.com/hyperlab-sh/husk/adapters/example\"", 1},
		{"test_import", "pkg/example_test.go", "package example\nimport _ \"github.com/hyperlab-sh/husk/adapters/example\"", 1},
		{"source", "cmd/husk/example.go", "package main\n// herdr coupling", 1},
		{"doc", "docs/docs/design/example.md", "# " + strings.ToUpper("herdr") + " coupling", 1},
		{"navigation", "docs/docs/design/example.md", "[Adapter](herdr-adapter.md)", 0},
		{"exemption", "docs/docs/design/herdr-adapter.md", "# herdr adapter", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"cmd/husk", "pkg", "docs/docs/design"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, tc.path), []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := checkCore(root)
			if err != nil || len(got) != tc.count {
				t.Fatalf("violations=%v err=%v want=%d", got, err, tc.count)
			}
		})
	}
}

func checkManifestImports(path string) error {
	return filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if strings.Contains(name, ".") && name != "github.com/pelletier/go-toml/v2" {
				return fmt.Errorf("manifest imports %s", name)
			}
		}
		return nil
	})
}

func TestManifestImportsNoHost(t *testing.T) {
	if err := checkManifestImports(filepath.Join(repositoryRoot(t), "adapters/herdr/manifest")); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "example.go"), []byte("package example\nimport _ \"github.com/hyperlab-sh/husk/pkg/protocol\""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkManifestImports(root); err == nil {
		t.Fatal("manifest dependency guard accepted a host package")
	}
}
