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
// imports the output package, the instance package and the wire package from
// Husk, no Stem, Services, Substrate or Pods package, and golang.org/x/term as
// its only third-party module.
func TestImportsStayWithinAllowList(t *testing.T) {
	const module = "github.com/hyperlab-sh/husk/"
	allowed := map[string]bool{"pkg/cli/output": true, "pkg/instance": true, "pkg/protocol": true}
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
			if path == "os/signal" {
				t.Errorf("%s imports os/signal: the CLI installs no signal handler; a signal ends it by the signal", f)
			}
			if strings.Contains(strings.SplitN(path, "/", 2)[0], ".") && !strings.HasPrefix(path, module) && path != "golang.org/x/term" {
				t.Errorf("%s imports %s: the CLI imports the standard library, Husk packages and golang.org/x/term only", f, path)
			}
			if rel, ok := strings.CutPrefix(path, module); ok && !allowed[rel] {
				t.Errorf("%s imports %s", f, path)
			}
		}
	}
}
