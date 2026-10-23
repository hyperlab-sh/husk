package calls

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestSpecDefaultLimits(t *testing.T) {
	if DefaultTimeout != 10*time.Second || MinTimeout != time.Second || MaxTimeout != 60*time.Second || SweepInterval != 250*time.Millisecond || MaxPending != 1024 || MaxPendingPerPlugin != 64 {
		t.Fatal("specification defaults changed")
	}
	if clock := reflect.TypeFor[Clock](); clock.NumMethod() != 1 || clock.Method(0).Name != "Now" {
		t.Fatal("Clock must only provide Now")
	}
}

func TestCountersArePerInstanceAndNeverResetAfterPluginExit(t *testing.T) {
	first, _, _ := newTestTable(TableOptions{})
	second, _, _ := newTestTable(TableOptions{})
	h1 := mustSend(t, first, "p")
	h2 := mustSend(t, second, "p")
	if h1.ID != 1 || h2.ID != 1 {
		t.Fatal("counter was global")
	}
	first.DropPlugin("p", "exit")
	h3 := mustSend(t, first, "p")
	if h3.ID != 2 {
		t.Fatal("plugin restart reused an id")
	}
}

func TestAllPluginErrorCodesPassThroughUnmodified(t *testing.T) {
	for _, code := range protocol.Codes() {
		t.Run(string(code), func(t *testing.T) {
			table, _, _ := newTestTable(TableOptions{})
			h := mustSend(t, table, "p")
			m := replyFor(h, `{}`)
			m.Result = nil
			m.Error = &Error{Code: code, Message: "plugin's own error"}
			r, ok := table.Resolve("p", m)
			if !ok || r.Err != m.Error || r.Value != nil {
				t.Fatalf("error not passed through: %+v", r)
			}
		})
	}
}

func TestCallsDependencyAndConcurrencyBoundary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			switch path {
			case "github.com/hyperlab-sh/husk/pkg/protocol":
			case "charm.land/bubbletea/v2":
				if name != "sweep.go" {
					t.Errorf("Bubble Tea scheduling escaped the adapter into %s", name)
				}
			case "encoding/json", "errors", "fmt", "slices", "strconv", "time":
			default:
				t.Errorf("unexpected dependency %s in %s", path, name)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node.(type) {
			case *ast.GoStmt, *ast.SendStmt, *ast.SelectStmt:
				t.Errorf("concurrency primitive in %s", name)
			}
			if selector, ok := node.(*ast.SelectorExpr); ok {
				if prefix, ok := selector.X.(*ast.Ident); ok && prefix.Name == "protocol" && strings.HasPrefix(selector.Sel.Name, "Method") {
					t.Errorf("Calls knows a method name in %s", name)
				}
			}
			return true
		})
	}
}
