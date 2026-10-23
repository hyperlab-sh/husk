package output

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestImportsStayWithinAllowList pins the output package as a leaf: standard
// library, pkg/protocol and github.com/mattn/go-runewidth only, so the
// command packages that share it never import each other.
func TestImportsStayWithinAllowList(t *testing.T) {
	const module = "github.com/hyperlab-sh/husk/"
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
			if strings.Contains(strings.SplitN(path, "/", 2)[0], ".") && path != module+"pkg/protocol" && path != "github.com/mattn/go-runewidth" {
				t.Errorf("%s imports %s", f, path)
			}
		}
	}
}
