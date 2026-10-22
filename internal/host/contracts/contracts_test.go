package contracts_test

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestCodeOf(t *testing.T) {
	coded := contracts.NewError(protocol.CodeBusy, "slow down", map[string]any{"reason": "rate_limited"})
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain", errors.New("boom"), "internal"},
		{"coded", coded, "busy"},
		{"wrapped", fmt.Errorf("ctx: %w", coded), "busy"},
	}
	for _, tt := range tests {
		if got := contracts.CodeOf(tt.err); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	var d contracts.DataError
	if !errors.As(coded, &d) || d.Data()["reason"] != "rate_limited" {
		t.Fatal("data lost")
	}
}

func TestCallerKindUser(t *testing.T) {
	if !contracts.CallerCLI.User() || !contracts.CallerStem.User() || contracts.CallerPlugin.User() {
		t.Fatal("only plugins are not the user")
	}
}

// TestImportsStayWithinAllowList keeps the package free of Services and of the
// Server, which still live on other branches.
func TestImportsStayWithinAllowList(t *testing.T) {
	allowed := func(p string) bool {
		if !strings.Contains(p, ".") { // standard library
			return true
		}
		for _, ok := range []string{
			"github.com/hyperlab-sh/husk/pkg/protocol",
			"github.com/hyperlab-sh/husk/internal/substrate/state",
			"github.com/hyperlab-sh/husk/internal/instance",
			"github.com/hyperlab-sh/husk/internal/host/contracts",
		} {
			if p == ok {
				return true
			}
		}
		return false
	}
	files, _ := filepath.Glob("*.go")
	more, _ := filepath.Glob("fakes/*.go")
	for _, f := range append(files, more...) {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !allowed(p) {
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
}

func TestEveryWireMethodHasExactlyOneHome(t *testing.T) {
	homes := contracts.MethodHomes()
	for _, m := range protocol.Methods() {
		if homes[m] == "" {
			t.Errorf("method %s has no home", m)
		}
	}
	if len(homes) != len(protocol.Methods()) {
		t.Errorf("%d homes for %d methods", len(homes), len(protocol.Methods()))
	}
}
