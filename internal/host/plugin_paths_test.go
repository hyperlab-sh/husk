package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/services"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func writeUserPluginDirs(t *testing.T, dirs ...string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "husk")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"husk": map[string]any{"plugins": map[string]any{"dirs": dirs}}})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPluginSettingsLaunchLayers(t *testing.T) {
	for _, mode := range []string{"user", "replace", "independent paths"} {
		t.Run(mode, func(t *testing.T) {
			alpha := writePlugin(t, "alpha", false)
			beta := writePlugin(t, "beta", false)
			userDir := filepath.Dir(alpha)
			f := newFakes()
			hook, ready := readyHook("")
			h, err := boot(t, func(c *contracts.Config) {
				c.Clock = frozenClock{time.Unix(1000, 0)}
				writeUserPluginDirs(t, userDir)
				switch mode {
				case "replace":
					pluginSettings(c)["dirs"] = []string{filepath.Dir(beta)}
				case "independent paths":
					pluginSettings(c)["paths"] = []string{beta}
				}
			}, withHook(hook), WithFactory(f.factory))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"alpha"}
			source := "user"
			if mode == "replace" {
				want = []string{"beta"}
				source = "instance"
			}
			if mode == "independent paths" {
				want = []string{"alpha", "beta"}
			}
			if h.root.Planned() != len(want) {
				t.Fatalf("planned=%d want %v", h.root.Planned(), want)
			}
			runHost(t, ready, h)
			got := []string{}
			for range want {
				got = append(got, <-f.started)
			}
			slices.Sort(got)
			h.loop(func() {
				if !slices.Equal(got, want) || h.root.Planned() != len(want) {
					t.Errorf("plugins=%v planned=%d want=%v", got, h.root.Planned(), want)
				}
			})
			if got := h.root.rt.Settings.Snapshot().Source("husk.plugins.dirs"); got != source {
				t.Fatalf("source=%s want=%s", got, source)
			}
			if mode != "user" {
				return
			}
			writeUserPluginDirs(t, filepath.Dir(beta))
			reply := rpc(t, h, "", protocol.MethodConfigReload, map[string]any{})
			if reply.Error != nil {
				t.Fatalf("reload: %+v", reply.Error)
			}
			var result struct {
				Reports []contracts.ReloadReport `json:"reports"`
			}
			if err := json.Unmarshal(reply.Result, &result); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, r := range result.Reports {
				for _, p := range r.Problems {
					if r.Service == "settings" && r.Applied && p.Code == "restart_required" && p.Key == "husk.plugins.dirs" {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("no applied restart report: %s", reply.Result)
			}
			if got := h.root.rt.Settings.Snapshot().Strings("husk.plugins.dirs"); !slices.Equal(got, []string{userDir}) {
				t.Fatalf("in-force dirs=%v", got)
			}
			h.loop(func() {
				if _, ok := h.root.pods.Pod("alpha"); !ok {
					t.Error("alpha disappeared")
				}
				if _, ok := h.root.pods.Pod("beta"); ok {
					t.Error("beta launched on reload")
				}
			})
		})
	}
}

func TestStartupSnapshotPrecedesDeclarations(t *testing.T) {
	var partial *Root
	stop := errors.New("inspect startup")
	_, err := boot(t, func(c *contracts.Config) {
		c.Clock = frozenClock{time.Unix(1000, 0)}
		pluginSettings(c)["paths"] = []string{writePlugin(t, "alpha", false)}
	}, withHook(func(stage string) error {
		if stage == "startup" {
			return stop
		}
		return nil
	}), func(o *options) { o.afterFail = func(r *Root) { partial = r } })
	if !errors.Is(err, stop) || partial == nil {
		t.Fatalf("startup stage: %v", err)
	}
	if len(partial.specs) != 0 {
		t.Fatal("manifests were converted before startup")
	}
	if partial.rt.Settings.Generation() != 1 {
		t.Fatal("startup did not publish Settings")
	}
	if reports := partial.Frontend().StartupReports(); len(reports) != 5 {
		t.Fatalf("startup reports: %+v", reports)
	}
	if !storeStopped(partial) {
		t.Fatal("startup failure leaked Store")
	}
}

func TestPluginSettingsFatalPaths(t *testing.T) {
	for _, mode := range []string{"missing path", "directory is file", "unreadable directory", "user directory is file"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "unreadable directory" && os.Geteuid() == 0 {
				t.Skip("root can read mode-000 directories")
			}
			value := filepath.Join(t.TempDir(), "missing")
			key := "paths"
			if mode == "directory is file" || mode == "user directory is file" {
				key = "dirs"
				if err := os.WriteFile(value, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unreadable directory" {
				key = "dirs"
				if err := os.Mkdir(value, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(value, 0700) })
			}
			var failed *Root
			layer := "instance"
			h, err := boot(t, func(c *contracts.Config) {
				c.Clock = frozenClock{time.Unix(1000, 0)}
				if mode == "user directory is file" {
					layer = "user"
					writeUserPluginDirs(t, value)
				} else {
					pluginSettings(c)[key] = []string{value}
				}
			}, func(o *options) { o.afterFail = func(r *Root) { failed = r } })
			for _, text := range []string{fmt.Sprintf("husk.plugins.%s[0]", key), fmt.Sprintf("%q", value), "(layer " + layer + ")"} {
				if err == nil || !strings.Contains(err.Error(), text) {
					t.Fatalf("error=%v missing %q", err, text)
				}
			}
			if failed == nil || failed.rt == nil || !storeStopped(failed) || len(h.leftovers()) != 0 {
				t.Fatal("failed launch did not clean built resources")
			}
		})
	}
}

func TestStartupFatal(t *testing.T) {
	problem := func(code string) services.Report {
		return services.Report{Service: "theme", Problems: []services.Problem{{Severity: "error", Code: code}}}
	}
	invalid := func(service, key string) services.Report {
		return services.Report{Service: service, Problems: []services.Problem{{Severity: "error", Code: "invalid_value", Key: key}}}
	}
	incomplete := errors.New("service reload incomplete")
	for _, tc := range []struct {
		name    string
		reports []services.Report
		err     error
		want    bool
	}{
		{"store error", []services.Report{problem("store_error")}, incomplete, true},
		{"timeout after settings applied", []services.Report{{Service: "settings", Applied: true}, problem("timed_out")}, incomplete, true},
		{"cancelled", []services.Report{problem("cancelled")}, incomplete, true},
		{"no reports with error", nil, incomplete, true},
		{"invalid plugin dirs", []services.Report{invalid("settings", "husk.plugins.dirs")}, incomplete, true},
		{"invalid plugin paths", []services.Report{invalid("settings", "husk.plugins.paths")}, incomplete, true},
		{"invalid theme name stays nonfatal", []services.Report{invalid("settings", "theme.name")}, incomplete, false},
		{"keyless settings validation", []services.Report{invalid("settings", "")}, incomplete, false},
		{"other service plugin key stays nonfatal", []services.Report{invalid("theme", "husk.plugins.dirs")}, incomplete, false},
		{"nil error", nil, nil, false},
		{"applied with nil error", []services.Report{{Service: "settings", Applied: true}}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := startupFatal(tc.reports, tc.err, nil); got != tc.want {
				t.Fatalf("startupFatal(%+v, %v)=%v want %v", tc.reports, tc.err, got, tc.want)
			}
		})
	}
}

func TestStartupFatalAdditions(t *testing.T) {
	for _, tc := range []struct {
		name, key, layer string
		additions        map[string][]string
		source           *protocol.Source
		want             bool
	}{
		{"matching lower paths", "husk.plugins.paths", "user", map[string][]string{"paths": {"added"}}, nil, false},
		{"cross array stays fatal", "husk.plugins.paths", "user", map[string][]string{"dirs": {"added"}}, nil, true},
		{"instance error stays fatal", "husk.plugins.paths", "instance", map[string][]string{"paths": {"added"}}, nil, true},
		{"instance file error is not fatal", "husk.plugins.paths", "instance", map[string][]string{"paths": {"added"}}, &protocol.Source{Layer: "instance", File: "instances/x/config.json"}, false},
		{"bundle error stays fatal", "husk.bundle", "user", map[string][]string{"paths": {"added"}}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reports := []services.Report{{Service: "settings", Problems: []services.Problem{{Severity: "error", Code: "invalid_value", Key: tc.key, Layer: tc.layer, Source: tc.source}}}}
			if got := startupFatal(reports, errors.New("service reload incomplete"), tc.additions); got != tc.want {
				t.Fatalf("startupFatal(%+v, additions=%v)=%v want %v", reports, tc.additions, got, tc.want)
			}
		})
	}
}

func TestStartupError(t *testing.T) {
	storeMessage := "parse_error (layer user, file config.json): invalid JSON"
	storeErr := errors.New(storeMessage)
	incomplete := errors.New("service reload incomplete")
	storeReport := services.Report{Problems: []services.Problem{{Code: "store_error", Message: storeMessage}}}
	locatedMessage := "invalid_document: configuration file config.json (layer user) line 1, column 10 could not be loaded"
	locatedErr := errors.New(locatedMessage)
	located := services.Report{Problems: []services.Problem{{Code: "store_error", Message: locatedMessage, Source: &protocol.Source{Layer: "user", File: "config.json", Line: 1}}}}
	for _, tc := range []struct {
		name    string
		reports []services.Report
		cause   error
		want    string
	}{
		{"nil cause", []services.Report{{Problems: []services.Problem{{Code: "timed_out", Message: "deadline exceeded"}}}}, nil, "startup reload failed: timed_out: deadline exceeded"},
		{"repeated store problem", []services.Report{storeReport, storeReport}, storeErr, "store_error: " + storeMessage},
		{"separate diagnostic", []services.Report{{Problems: []services.Problem{{Code: "cancelled", Message: "shutdown", Source: &protocol.Source{Layer: "user", File: "config.json"}}}}}, incomplete, "service reload incomplete: cancelled: shutdown (layer user, file config.json)"},
		{"explicit key independent of prose", []services.Report{{Service: "settings", Problems: []services.Problem{{Code: "invalid_value", Key: "husk.plugins.dirs", Message: "want strings", Source: &protocol.Source{Layer: "user", File: "config.json"}}}}}, incomplete, "service reload incomplete: invalid_value: want strings (key husk.plugins.dirs) (layer user, file config.json)"},
		{"shared cause retains key", []services.Report{{Service: "settings", Problems: []services.Problem{{Code: "invalid_value", Key: "husk.plugins.paths", Message: incomplete.Error()}}}}, incomplete, "service reload incomplete: invalid_value: service reload incomplete (key husk.plugins.paths)"},
		{"source already in message", []services.Report{located, located}, locatedErr, "store_error: " + locatedMessage},
		{"no reports", nil, incomplete, "service reload incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := startupError(tc.reports, tc.cause)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error=%v want %q", err, tc.want)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("error does not wrap cause: %v", err)
			}
		})
	}
}

func TestStartupFlightFailures(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		fatal        bool
		key          string
	}{
		{"malformed user config", `{"theme":`, true, ""},
		{"invalid plugin dirs", `{"husk":{"plugins":{"dirs":"./plugins"}}}`, true, "husk.plugins.dirs"},
		{"invalid plugin paths", `{"husk":{"plugins":{"paths":"./plugin"}}}`, true, "husk.plugins.paths"},
		{"invalid theme name stays nonfatal", `{"theme":{"name":true}}`, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var failed *Root
			logs := make(chan string, 32)
			h, err := boot(t, func(c *contracts.Config) {
				c.Clock = frozenClock{time.Unix(1000, 0)}
				writeUserPluginDirs(t)
				if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "husk", "config.json"), []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}, WithLogger(func(level, msg string) {
				if level == "warn" {
					logs <- msg
				}
			}), func(o *options) { o.afterFail = func(r *Root) { failed = r } })
			if tc.fatal {
				want := []string{"host: startup:", "config.json", "layer user"}
				if tc.key != "" {
					want = append(want, "invalid_value", "(key "+tc.key+")")
				} else {
					want = append(want, "store_error", "line 1, column ")
				}
				for _, text := range want {
					if err == nil || !strings.Contains(err.Error(), text) {
						t.Fatalf("error=%v missing %q", err, text)
					}
				}
				if strings.Count(err.Error(), "config.json") != 1 {
					t.Fatalf("duplicated startup source: %v", err)
				}
				if failed == nil || !storeStopped(failed) || len(h.leftovers()) != 0 {
					t.Fatal("fatal flight leaked resources")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case log := <-logs:
				if log != "services: startup reload: 1 problems" {
					t.Fatalf("warning=%s", log)
				}
			default:
				t.Fatal("missing startup warning")
			}
			if h.root.rt.Settings.Generation() != 0 || h.root.Planned() != 0 {
				t.Fatal("invalid settings were applied")
			}
			fe := h.root.Frontend()
			reports := fe.StartupReports()
			if len(reports) != 5 {
				t.Fatalf("startup reports=%+v", reports)
			}
			var settingsIndex int
			for i, r := range reports {
				if r.Service == "settings" {
					settingsIndex = i
				}
			}
			problem := reports[settingsIndex].Problems
			if len(problem) != 1 || problem[0].Code != "invalid_value" || problem[0].Key != "theme.name" || problem[0].Source == nil {
				t.Fatalf("frontend startup errors=%+v", reports)
			}
			reports[settingsIndex].Problems[0].Code = "mutated"
			reports[settingsIndex].Problems[0].Source.File = "mutated"
			if next := fe.StartupReports()[settingsIndex].Problems[0]; next.Code != "invalid_value" || next.Source.File != "config.json" {
				t.Fatalf("mutable startup reports: %+v", next)
			}
			if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "husk", "config.json"), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := h.root.rt.Coordinator.Reload(t.Context(), services.ReasonCLI); err != nil {
				t.Fatal(err)
			}
			if next := fe.StartupReports()[settingsIndex]; next.Applied || len(next.Problems) != 1 || next.Problems[0].Code != "invalid_value" {
				t.Fatalf("later flight replaced startup reports: %+v", next)
			}
		})
	}
}

func TestPluginDirectoryRefusalKeepsGoodPlugin(t *testing.T) {
	good := writePlugin(t, "alpha", false)
	dir := filepath.Dir(good)
	bad := filepath.Join(dir, "bad")
	if err := os.Mkdir(bad, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "husk-plugin.json"), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	logs := make(chan string, 32)
	f := newFakes()
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) { c.Clock = frozenClock{time.Unix(1000, 0)}; writeUserPluginDirs(t, dir) }, withHook(hook), WithFactory(f.factory), WithLogger(func(level, msg string) {
		if level == "warn" {
			logs <- msg
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if h.root.Planned() != 1 {
		t.Fatalf("planned=%d", h.root.Planned())
	}
	refused := h.root.Frontend().Refusals()
	if len(refused) != 1 || !strings.Contains(refused[0].Reason, "bad") {
		t.Fatalf("refusals=%+v", refused)
	}
	select {
	case log := <-logs:
		if !strings.Contains(log, fmt.Sprintf("husk.plugins.dirs %q", dir)) || !strings.Contains(log, "bad") {
			t.Fatalf("warning=%s", log)
		}
	default:
		t.Fatal("missing directory refusal warning")
	}
	runHost(t, ready, h)
	if id := <-f.started; id != "alpha" {
		t.Fatalf("started=%s", id)
	}
	h.loop(func() {
		if _, ok := h.root.pods.Pod("alpha"); !ok {
			t.Error("good plugin was refused")
		}
	})
}
