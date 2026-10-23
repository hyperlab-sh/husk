package createcmd

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestImportsStayWithinAllowList keeps scaffolding independent of loading,
// configuration and the socket client.
func TestImportsStayWithinAllowList(t *testing.T) {
	const module = "github.com/hyperlab-sh/husk/"
	allowed := map[string]bool{
		"pkg/cli/output": true,
		"pkg/protocol":   true,
		"plugins/guide":  true,
	}
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
			if strings.Contains(strings.SplitN(path, "/", 2)[0], ".") && !strings.HasPrefix(path, module) {
				t.Errorf("%s imports %s: standard library and Husk packages only", f, path)
			}
			if rel, ok := strings.CutPrefix(path, module); ok && !allowed[rel] {
				t.Errorf("%s imports %s", f, path)
			}
		}
	}
}
