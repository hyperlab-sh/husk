package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/pkg/bundle"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

func putBundleFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}
func hostBundle(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	putBundleFile(t, dir, "husk-bundle.json", fmt.Sprintf(`{"manifest_version":1,"id":%q}`, id))
	target := writePlugin(t, id, false)
	plugins := filepath.Join(dir, "plugins")
	if err := os.Mkdir(plugins, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(plugins, id)); err != nil {
		t.Fatal(err)
	}
	return dir
}
func bundleFlagDoc(c *contracts.Config, dir string) {
	if c.InstanceDocs == nil {
		c.InstanceDocs = map[string]map[string]any{}
	}
	if c.InstanceDocs["config"] == nil {
		c.InstanceDocs["config"] = map[string]any{}
	}
	husk, _ := c.InstanceDocs["config"]["husk"].(map[string]any)
	if husk == nil {
		husk = map[string]any{}
		c.InstanceDocs["config"]["husk"] = husk
	}
	husk["bundle"] = dir
}
func filesystemTree(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if !d.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel += ":" + string(raw)
		}
		entries = append(entries, rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestBundleFatalErrorsBeforeAnyDirectoryCreation(t *testing.T) {
	for _, tc := range []struct{ name, raw, code string }{
		{"not found", "", "bundle_not_found"},
		{"not a directory", "", "bundle_not_found"},
		{"missing manifest", "", "bundle_manifest_missing"},
		{"invalid manifest", `[]`, "bundle_manifest_invalid"},
		{"unsupported version", `{"manifest_version":2}`, "bundle_manifest_version"},
		{"invalid id", `{"manifest_version":1,"id":"My Bundle"}`, "bundle_id_invalid"},
		{"plugins file", `{"manifest_version":1,"id":"example"}`, "bundle_plugins_not_a_directory"},
		{"relative user", "", "bundle_path_relative"},
		{"nonstring user", "", "bundle_path_relative"},
		{"unreadable directory", `{"manifest_version":1,"id":"example"}`, "bundle_unreadable"},
		{"unreadable manifest", `{"manifest_version":1,"id":"example"}`, "bundle_unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.HasPrefix(tc.name, "unreadable") && os.Geteuid() == 0 {
				t.Skip("root bypasses permissions")
			}
			home := t.TempDir()
			for key, dir := range map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"), "XDG_STATE_HOME": filepath.Join(home, "state"), "XDG_DATA_HOME": filepath.Join(home, "data"), "XDG_RUNTIME_DIR": filepath.Join(home, "runtime")} {
				t.Setenv(key, dir)
			}
			dir := t.TempDir()
			if tc.raw != "" {
				putBundleFile(t, dir, "husk-bundle.json", tc.raw)
			}
			switch tc.name {
			case "not found":
				dir = filepath.Join(dir, "missing")
			case "not a directory":
				putBundleFile(t, dir, "file", "")
				dir = filepath.Join(dir, "file")
			case "plugins file":
				putBundleFile(t, dir, "plugins", "")
			case "unreadable directory", "unreadable manifest":
				path := dir
				if tc.name == "unreadable manifest" {
					path = filepath.Join(dir, "husk-bundle.json")
				}
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0700) })
			}
			cfg := contracts.Config{InstanceID: "test", RuntimeDir: filepath.Join(home, "runtime", "husk"), Post: func(any) bool { return false }}
			bundleFlagDoc(&cfg, dir)
			if tc.name == "relative user" || tc.name == "nonstring user" {
				cfg.InstanceDocs = nil
				value := `"./x"`
				if tc.name == "nonstring user" {
					value = `1`
				}
				putBundleFile(t, filepath.Join(home, "config", "husk"), "config.json", `{"husk":{"bundle":`+value+`}}`)
			}
			before := filesystemTree(t, home)
			h, err := New(cfg)
			var be *bundle.Error
			var boot *store.BootstrapError
			code := ""
			if errors.As(err, &be) {
				code = be.Code
			}
			if errors.As(err, &boot) {
				code = boot.Code
			}
			if h != nil || err == nil || code != tc.code || strings.Contains(err.Error(), "host:") {
				t.Fatalf("New=%v %v code=%s want=%s", h, err, code, tc.code)
			}
			prefix := "bundle " + dir + ": "
			if boot != nil {
				prefix = "bundle: "
			}
			if !strings.HasPrefix(err.Error(), prefix) {
				t.Fatalf("error prefix: %v", err)
			}
			if after := filesystemTree(t, home); !reflect.DeepEqual(before, after) {
				t.Fatalf("fatal startup wrote files: %v -> %v", before, after)
			}
		})
	}
}

func TestBundlePrecedenceActuallyLaunchesSelectedPlugins(t *testing.T) {
	for _, mode := range []string{"user", "instance", "flag", "empty flag"} {
		t.Run(mode, func(t *testing.T) {
			user, inst, flag := hostBundle(t, "user"), hostBundle(t, "instance"), hostBundle(t, "flag")
			f := newFakes()
			hook, ready := readyHook("")
			h, err := boot(t, func(c *contracts.Config) {
				c.Clock = frozenClock{time.Unix(1000, 0)}
				config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "husk")
				raw, _ := json.Marshal(map[string]any{"husk": map[string]any{"bundle": user}})
				putBundleFile(t, config, "config.json", string(raw))
				if mode != "user" {
					raw, _ = json.Marshal(map[string]any{"husk": map[string]any{"bundle": inst}})
					putBundleFile(t, config, filepath.Join("instances", c.InstanceID, "config.json"), string(raw))
				}
				if mode == "flag" {
					bundleFlagDoc(c, flag)
				}
				if mode == "empty flag" {
					bundleFlagDoc(c, "")
				}
			}, withHook(hook), WithFactory(f.factory))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "empty flag" {
				if h.root.Planned() != 0 || h.root.bundle != nil {
					t.Fatal("empty flag did not disable")
				}
				return
			}
			runHost(t, ready, h)
			if got := <-f.started; got != mode {
				t.Fatalf("launched=%s want=%s", got, mode)
			}
			if h.root.Planned() != 1 || h.root.bundle.Manifest.ID != mode {
				t.Fatalf("selected bundle=%+v", h.root.bundle)
			}
		})
	}
}

func TestBundlePluginRefusalAndExplicitPriority(t *testing.T) {
	dir := hostBundle(t, "alpha")
	putBundleFile(t, dir, "plugins/bad/husk-plugin.json", `{`)
	if err := os.Symlink(writePlugin(t, "beta", false), filepath.Join(dir, "plugins", "beta")); err != nil {
		t.Fatal(err)
	}
	explicit := writePlugin(t, "alpha", false)
	f := newFakes()
	hook, ready := readyHook("")
	var logs []string
	h, err := boot(t, func(c *contracts.Config) {
		c.Clock = frozenClock{time.Unix(1000, 0)}
		bundleFlagDoc(c, dir)
		pluginSettings(c)["paths"] = []string{explicit}
	}, withHook(hook), WithFactory(f.factory), WithLogger(func(_, msg string) { logs = append(logs, msg) }))
	if err != nil {
		t.Fatal(err)
	}
	if h.root.Planned() != 2 || h.root.specs[0].WorkDir != explicit {
		t.Fatalf("explicit did not win: %+v", h.root.specs)
	}
	refusals := h.root.Frontend().Refusals()
	if len(refusals) != 2 {
		t.Fatalf("refusals=%+v", refusals)
	}
	duplicate, bad := false, false
	for _, r := range refusals {
		duplicate = duplicate || r.Plugin == "alpha" && strings.Contains(r.Reason, "duplicate")
		bad = bad || strings.Contains(r.Reason, "bad")
	}
	if !duplicate || !bad || !strings.Contains(strings.Join(logs, "\n"), "bad") {
		t.Fatalf("refusals=%+v logs=%v", refusals, logs)
	}
	// Do not retain a loop-written log slice once Run begins.
	h.root.opts.logger = nil
	runHost(t, ready, h)
	started := map[string]bool{}
	for range 2 {
		started[<-f.started] = true
	}
	if !started["alpha"] || !started["beta"] {
		t.Fatalf("good plugins not live: %v", started)
	}
}

func TestBundleKeymapReachesKeysList(t *testing.T) {
	dir := hostBundle(t, "alpha")
	putBundleFile(t, dir, "keymap.json", `{"version":1,"bind":[{"key":"f2","action":"husk.home","hint":"bundle"}]}`)
	f := newFakes()
	hook, ready := readyHook("")
	h, err := boot(t, func(c *contracts.Config) { bundleFlagDoc(c, dir) }, withHook(hook), WithFactory(f.factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	reply := rpc(t, h, "", protocol.MethodKeysList, map[string]any{})
	if reply.Error != nil {
		t.Fatalf("keys.list: %+v", reply.Error)
	}
	var listing struct {
		Entries []struct {
			Keys   []string        `json:"keys"`
			Action string          `json:"action"`
			Source protocol.Source `json:"source"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(reply.Result, &listing); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range listing.Entries {
		if e.Action == "husk.home" && e.Source.Layer == "distribution" && len(e.Keys) == 1 && e.Keys[0] == "f2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bundle key missing: %s", reply.Result)
	}
}

func TestBundleWithoutPluginsLogsWarningWithoutRefusal(t *testing.T) {
	dir := t.TempDir()
	putBundleFile(t, dir, "husk-bundle.json", `{"manifest_version":1,"id":"config-only"}`)
	var logs []string
	h, err := boot(t, func(c *contracts.Config) { bundleFlagDoc(c, dir) }, WithLogger(func(_, msg string) { logs = append(logs, msg) }))
	if err != nil {
		t.Fatal(err)
	}
	if h.root.Planned() != 0 || len(h.root.Frontend().Refusals()) != 0 || !strings.Contains(strings.Join(logs, "\n"), "bundle config-only: no plugins") {
		t.Fatalf("planned=%d logs=%v refusals=%v", h.root.Planned(), logs, h.root.Frontend().Refusals())
	}
	h.root.opts.logger = nil
}
