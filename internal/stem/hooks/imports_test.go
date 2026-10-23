package hooks_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestImportsStayWithinAllowList keeps the Hook runner free of the Dispatcher,
// Store, Pods, Services and the Server.
func TestImportsStayWithinAllowList(t *testing.T) {
	allowed := map[string]bool{
		"github.com/hyperlab-sh/husk/internal/stem/calls": true,
		"github.com/hyperlab-sh/husk/pkg/protocol":        true,
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(p, ".") && !allowed[p] {
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
}
