package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/services"
)

func TestStartConsumedPluginFlagSurvivesUnrelatedProblem(t *testing.T) {
	for _, invalid := range []bool{true, false} {
		t.Run(map[bool]string{true: "nonfatal", false: "plain"}[invalid], func(t *testing.T) {
			for _, key := range []string{"paths", "dirs"} {
				t.Run(key, func(t *testing.T) {
					plugin := writePlugin(t, "alpha", false)
					f := newFakes()
					hook, ready := readyHook("")
					h, err := boot(t, func(c *contracts.Config) {
						c.Clock = frozenClock{time.Unix(1000, 0)}
						var name any = "charm"
						if invalid {
							name = true
						}
						raw, _ := json.Marshal(map[string]any{"theme": map[string]any{"name": name, "allow_low_contrast": true}})
						putBundleFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "husk"), "config.json", string(raw))
						value := plugin
						if key == "dirs" {
							value = filepath.Dir(plugin)
						}
						pluginSettings(c)[key] = []string{value}
					}, withHook(hook), WithFactory(f.factory))
					if err != nil {
						t.Fatal(err)
					}
					if h.root.Planned() != 1 {
						t.Fatalf("planned=%d want=1", h.root.Planned())
					}
					if snap := h.root.rt.Settings.Snapshot(); snap.Source("husk.plugins."+key) != "instance" || !snap.Bool("theme.allow_low_contrast") || snap.String("theme.name") != "charm" {
						t.Fatal("startup reset a valid key or retained an invalid one")
					}
					if invalid {
						reports := h.root.Frontend().StartupReports()
						if len(reports) != 5 {
							t.Fatalf("startup reports=%+v", reports)
						}
						settingsReport, themeReport := reports[1], reports[2]
						if settingsReport.Service != "settings" || settingsReport.Applied || settingsReport.Changed || settingsReport.Gen != 0 {
							t.Fatalf("startup settings=%+v", settingsReport)
						}
						if themeReport.Service != "theme" || themeReport.Applied || themeReport.Changed || len(themeReport.Problems) != 1 || themeReport.Problems[0].Code != "settings_unchanged" {
							t.Fatalf("startup theme=%+v", themeReport)
						}
					}
					runHost(t, ready, h)
					if id := <-f.started; id != "alpha" {
						t.Fatalf("started=%s", id)
					}
					result := rpc(t, h, "", protocol.MethodPluginList, map[string]any{})
					if result.Error != nil {
						t.Fatal(result.Error)
					}
					var listed []struct {
						ID string `json:"id"`
					}
					if err := json.Unmarshal(result.Result, &listed); err != nil {
						t.Fatal(err)
					}
					if len(listed) != 1 || listed[0].ID != "alpha" {
						t.Fatalf("listed=%s", result.Result)
					}
				})
			}
		})
	}
}

func TestStartConsumedBundleReloadAfterNonfatalStartup(t *testing.T) {
	for _, invalid := range []bool{true, false} {
		t.Run(map[bool]string{true: "nonfatal", false: "plain"}[invalid], func(t *testing.T) {
			initial, pending := hostBundle(t, "initial"), hostBundle(t, "pending")
			write := func(dir string, bad bool) string {
				var name any = "charm"
				if bad {
					name = true
				}
				raw, _ := json.Marshal(map[string]any{"theme": map[string]any{"name": name}, "husk": map[string]any{"bundle": dir}})
				return string(raw)
			}
			h, err := boot(t, func(c *contracts.Config) {
				c.Clock = frozenClock{time.Unix(1000, 0)}
				putBundleFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "husk"), "config.json", write(initial, invalid))
			})
			if err != nil {
				t.Fatal(err)
			}
			if snap := h.root.rt.Settings.Snapshot(); snap.String("husk.bundle") != initial || snap.Source("husk.bundle") != "user" {
				t.Fatal("startup bundle baseline missing")
			}
			putBundleFile(t, h.root.store.Paths().Config, "config.json", write(pending, false))
			for range 2 {
				reports, err := h.root.rt.Coordinator.Reload(t.Context(), services.ReasonCLI)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, r := range reports {
					if r.Service == "settings" {
						if !r.Applied {
							t.Fatalf("settings=%+v", r)
						}
						for _, p := range r.Problems {
							if p.Code == "restart_required" && p.Key == "husk.bundle" {
								count++
							}
						}
					}
				}
				if count != 1 {
					t.Fatalf("bundle restart warnings=%d reports=%+v", count, reports)
				}
				if h.root.rt.Settings.Snapshot().String("husk.bundle") != initial || h.root.bundle.Dir != initial || h.root.Planned() != 1 || h.root.specs[0].ID != "initial" {
					t.Fatal("reload replaced bundle in force")
				}
			}
		})
	}
}
