package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
)

func TestHostExecDiscoveryRemembersRefusedDirectories(t *testing.T) {
	for _, mode := range []string{"directory", "discovered", "duplicate-id"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			plugins := filepath.Join(root, "plugins")
			target := filepath.Join(plugins, "bad")
			if mode == "duplicate-id" {
				target = filepath.Join(plugins, "duplicate")
			}
			if err := os.MkdirAll(target, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "format"), []byte(filepath.Base(exe)), 0600); err != nil {
				t.Fatal(err)
			}
			var paths []string
			if mode == "duplicate-id" {
				alpha := filepath.Join(plugins, "alpha")
				if err := os.Mkdir(alpha, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(alpha, "format"), []byte(filepath.Base(exe)), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "plugin-id"), []byte("alpha"), 0600); err != nil {
					t.Fatal(err)
				}
				paths = []string{alpha, target}
			}
			trace := filepath.Join(root, "trace")
			t.Setenv("CONVERTER_TRACE", trace)
			t.Setenv("CONVERTER_DISCOVER", target+"\n"+target+"\n")
			h, err := boot(t, func(c *contracts.Config) {
				settings := pluginSettings(c)
				settings["paths"] = paths
				if mode == "directory" {
					settings["dirs"] = []string{plugins}
				}
			}, WithConverters([]string{exe}))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(raw), filepath.Base(exe)+":convert "+target+"\n") != 1 {
				t.Fatalf("trace=%s", raw)
			}
			refusals := h.root.Frontend().Refusals()
			if len(refusals) != 1 {
				t.Fatalf("refusals=%+v", refusals)
			}
			reason := "converter_failed"
			if mode == "duplicate-id" {
				reason = "duplicate plugin id"
			}
			if !strings.Contains(refusals[0].Reason, reason) {
				t.Fatalf("refusals=%+v", refusals)
			}
		})
	}
}
