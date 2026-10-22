package cli

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestImportsStayWithinAllowList pins the spec's dependency rule: the CLI
// imports the instance package and the wire package from Husk, and no Stem,
// Services, Substrate or Pods package.
func TestImportsStayWithinAllowList(t *testing.T) {
	const module = "github.com/hyperlab-sh/husk/"
	allowed := map[string]bool{"internal/instance": true, "pkg/protocol": true}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range src.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if rel, ok := strings.CutPrefix(path, module); ok && !allowed[rel] {
				t.Errorf("%s imports %s", f, path)
			}
		}
	}
}
