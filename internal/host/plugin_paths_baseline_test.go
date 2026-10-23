package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/services"
)

func TestPluginPathsStartupBaseline(t *testing.T) {
	for _, startupInvalid := range []bool{true, false} {
		for _, tc := range []struct {
			name       string
			paths      bool
			dirs       bool
			fixInvalid bool
		}{
			{"paths", true, false, false},
			{"dirs", false, true, false},
			{"neither", false, false, false},
			{"paths and fix", true, false, true},
			{"dirs and fix", false, true, true},
			{"neither and fix", false, false, true},
		} {
			name := "valid/" + tc.name
			if startupInvalid {
				name = "nonfatal/" + tc.name
			}
			t.Run(name, func(t *testing.T) {
				plugin := writePlugin(t, "alpha", false)
				initialPaths, initialDirs := []string{plugin}, []string{filepath.Dir(plugin)}
				writeConfig := func(file string, paths, dirs []string, invalid bool) {
					t.Helper()
					var themeName any = "charm"
					if invalid {
						themeName = true
					}
					raw, err := json.Marshal(map[string]any{
						"theme": map[string]any{"name": themeName},
						"husk":  map[string]any{"plugins": map[string]any{"paths": paths, "dirs": dirs}},
					})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				h, err := boot(t, func(c *contracts.Config) {
					c.Clock = frozenClock{time.Unix(1000, 0)}
					writeUserPluginDirs(t)
					writeConfig(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "husk", "config.json"), initialPaths, initialDirs, startupInvalid)
				})
				if err != nil {
					t.Fatal(err)
				}
				baseline := h.root.rt.Settings.Snapshot()
				wantPaths, wantDirs := initialPaths, initialDirs
				if !slices.Equal(baseline.Strings("husk.plugins.paths"), wantPaths) || !slices.Equal(baseline.Strings("husk.plugins.dirs"), wantDirs) {
					t.Fatal("startup did not serve the expected arrays")
				}
				planned := h.root.Planned()
				paths, dirs := slices.Clone(wantPaths), slices.Clone(wantDirs)
				if tc.paths {
					paths = []string{"next-plugin"}
				}
				if tc.dirs {
					dirs = []string{"next-plugins"}
				}
				writeConfig(filepath.Join(h.root.store.Paths().Config, "config.json"), paths, dirs, startupInvalid && !tc.fixInvalid)
				// Repeat to pin the baseline even after the first successful recovery.
				for i := 0; i < 2; i++ {
					reports, err := h.root.rt.Coordinator.Reload(t.Context(), services.ReasonCLI)
					if err != nil {
						t.Fatal(err)
					}
					var report services.Report
					for _, r := range reports {
						if r.Service == "settings" {
							report = r
						}
					}
					if report.Applied != (!startupInvalid || tc.fixInvalid) {
						t.Errorf("applied=%v problems=%+v", report.Applied, report.Problems)
					}
					for key, changed := range map[string]bool{"husk.plugins.paths": tc.paths, "husk.plugins.dirs": tc.dirs} {
						count := 0
						for _, p := range report.Problems {
							if p.Key == key && p.Code == "restart_required" {
								count++
								if p.Severity != "warning" || p.Layer != "user" {
									t.Errorf("restart problem=%+v", p)
								}
							}
						}
						if (count == 1) != changed || count > 1 {
							t.Errorf("%s restart warnings=%d want change=%v; problems=%+v", key, count, changed, report.Problems)
						}
					}
					snap := h.root.rt.Settings.Snapshot()
					if !slices.Equal(snap.Strings("husk.plugins.paths"), wantPaths) || !slices.Equal(snap.Strings("husk.plugins.dirs"), wantDirs) || h.root.Planned() != planned {
						t.Errorf("running baseline changed: paths=%v dirs=%v planned=%d", snap.Strings("husk.plugins.paths"), snap.Strings("husk.plugins.dirs"), h.root.Planned())
					}
				}
			})
		}
	}
}
