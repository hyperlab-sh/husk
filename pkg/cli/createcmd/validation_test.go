package createcmd

import (
	"testing"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
)

func TestGoDirective(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"go1.26.6", "1.26"}, {"go1.27rc1", "1.27"},
		{"devel go1.28-abc123 linux/amd64", "1.28"},
		{"go1.26.0 X:rangefunc", "1.26"}, {"go1.26.0-custom", "1.26"},
		{"", ""}, {"unknown", ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := goDirective(tc.raw); got != tc.want {
				t.Fatalf("goDirective(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestModulePathsBeforeFilesystem(t *testing.T) {
	for _, module := range []string{"", "example.test/a b", "example.test/日本", "a@b", "a\\b", "/app", "app/", "app//pkg", "app/.hidden", "app/pkg.", "-app", "app/NUL.txt", "app/com1", "app/pkg~1"} {
		t.Run(module, func(t *testing.T) {
			cmd := command("", fakeGuide, func() (string, error) { t.Fatal("invalid module reached filesystem"); return "", nil }, writeFile)
			err := cmd.Run(t.Context(), []string{"tui", "app", module}, output.Options{})
			e := output.AsError(err)
			if e.Exit != 2 || e.Code != output.CodeUsage {
				t.Fatalf("module %q: %#v", module, e)
			}
		})
	}
	for _, module := range []string{"app", "example.test/pkg", "example.test/Upper-case_~pkg/v2"} {
		if _, err := invoke(t, t.TempDir(), []string{"tui", "app", module}, output.JSON, writeFile); err != nil {
			t.Fatalf("valid module %q: %v", module, err)
		}
	}
}
