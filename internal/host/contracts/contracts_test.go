package contracts_test

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
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

var portTypes = map[string]reflect.Type{
	"SessionPort":    reflect.TypeFor[contracts.SessionPort](),
	"CallsPort":      reflect.TypeFor[contracts.CallsPort](),
	"EventPort":      reflect.TypeFor[contracts.EventPort](),
	"HookPort":       reflect.TypeFor[contracts.HookPort](),
	"StatePort":      reflect.TypeFor[contracts.StatePort](),
	"SurfacePort":    reflect.TypeFor[contracts.SurfacePort](),
	"PluginPort":     reflect.TypeFor[contracts.PluginPort](),
	"SettingsPort":   reflect.TypeFor[contracts.SettingsPort](),
	"SecretsPort":    reflect.TypeFor[contracts.SecretsPort](),
	"ProfilePort":    reflect.TypeFor[contracts.ProfilePort](),
	"ThemePort":      reflect.TypeFor[contracts.ThemePort](),
	"KeybindingPort": reflect.TypeFor[contracts.KeybindingPort](),
	"ConfigPort":     reflect.TypeFor[contracts.ConfigPort](),
}

func TestEveryWireMethodHasExactlyOneHome(t *testing.T) {
	homes := contracts.MethodHomes()
	for _, m := range protocol.Methods() {
		if _, ok := homes[m]; !ok {
			t.Errorf("method %s has no home", m)
		}
	}
	if len(homes) != len(protocol.Methods()) {
		t.Errorf("%d homes for %d methods", len(homes), len(protocol.Methods()))
	}
}

func TestHomesNameRealPortMethodsAndKinds(t *testing.T) {
	used := map[string]bool{}
	for m, h := range contracts.MethodHomes() {
		switch h.Port {
		case contracts.Outbound, contracts.Unrouted:
			if h.Method != "" || len(h.Kinds) != 0 {
				t.Errorf("%s: %s must have no method or kinds", m, h.Port)
			}
			continue
		}
		typ, ok := portTypes[h.Port]
		if !ok {
			t.Errorf("%s: unknown port %q", m, h.Port)
			continue
		}
		used[h.Port+"."+h.Method] = true
		if _, ok := typ.MethodByName(h.Method); !ok {
			t.Errorf("%s: %s has no method %q", m, h.Port, h.Method)
		}
		if len(h.Kinds) == 0 {
			t.Errorf("%s: empty caller kinds", m)
		}
	}
	// Every port method is the home of some wire method.
	for name, typ := range portTypes {
		for i := 0; i < typ.NumMethod(); i++ {
			if !used[name+"."+typ.Method(i).Name] {
				t.Errorf("%s.%s serves no wire method", name, typ.Method(i).Name)
			}
		}
	}
}

func TestCLIOnlyMethodsListOnlyCLI(t *testing.T) {
	homes := contracts.MethodHomes()
	for _, m := range []string{
		protocol.MethodProfileList, protocol.MethodProfileUse, protocol.MethodPluginList,
		protocol.MethodPluginLog, protocol.MethodConfigReload, protocol.MethodStateClear,
		protocol.MethodSecretsSet, protocol.MethodSecretsGrant, protocol.MethodSecretsRevoke,
		protocol.MethodSecretsReveal, protocol.MethodThemeCheck,
		protocol.MethodSettingsList, protocol.MethodSettingsSchema, protocol.MethodSettingsValidate,
	} {
		k := homes[m].Kinds
		if len(k) != 1 || k[0] != contracts.CallerCLI {
			t.Errorf("%s kinds = %v, want [CLI]", m, k)
		}
	}
}
