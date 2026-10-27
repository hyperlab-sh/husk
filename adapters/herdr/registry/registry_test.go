package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const source = "id='sample'\nname='Sample'\nversion='1'\nmin_herdr_version='0.9.0'\n"

func TestRegistryRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugins.json")
	for _, tc := range []struct {
		name, data string
		want       int
		warning    bool
	}{{"array", `[{"plugin_id":"sample","enabled":true}]`, 1, false}, {"corrupt", "{", 0, true}, {"null", "null", 0, true}, {"missing", "", 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			p := path
			if tc.name == "missing" {
				p = filepath.Join(dir, "missing")
			} else if err := os.WriteFile(p, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			records, problems := Read(p)
			if len(records) != tc.want || (len(problems) > 0) != tc.warning {
				t.Fatalf("records=%v problems=%v", records, problems)
			}
			if tc.warning && problems[0].Code != "registry_warning" {
				t.Fatalf("problem=%+v", problems[0])
			}
		})
	}
}

func TestDiscovery(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good")
	bad := filepath.Join(root, "bad")
	for _, d := range []string{good, bad} {
		if err := os.Mkdir(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range []struct{ dir, data string }{{good, source}, {bad, "invalid=["}} {
		if err := os.WriteFile(filepath.Join(pair.dir, "herdr-plugin.toml"), []byte(pair.data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	records := []Record{
		{ID: "disabled", Root: good, Enabled: false}, {ID: "disabled", Root: good, Enabled: false},
		{ID: "foreign", Root: good, Enabled: true, Platforms: []string{"windows"}},
		{ID: "bad", Root: bad, Enabled: true, Warnings: []string{"cached manifest warning"}},
		{ID: "missing", Root: filepath.Join(root, "missing"), Enabled: true},
		{ID: "good", Root: good, Enabled: true, Warnings: []string{"support unknown"}},
		{ID: "repeat", Root: good, Enabled: true},
	}
	dirs, problems := Discover(records, "linux")
	if !reflect.DeepEqual(dirs, []string{good}) {
		t.Fatalf("directories=%v", dirs)
	}
	var codes []string
	for _, p := range problems {
		codes = append(codes, p.Code)
	}
	if !reflect.DeepEqual(codes, []string{"disabled_in_registry", "registry_warning", "registry_warning", "registry_warning"}) {
		t.Fatalf("problems=%+v", problems)
	}
	if !strings.Contains(problems[1].Message, "cached manifest warning") {
		t.Fatal("lost cached warning")
	}
	// Reading/discovery never rewrites manifests or the registry.
	path := filepath.Join(root, "plugins.json")
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, p := Read(path)
	if len(p) != 0 || len(loaded) != len(records) {
		t.Fatal("registry changed")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatal("registry was modified")
	}
}

func TestCollisionScope(t *testing.T) {
	records := []Record{{ID: "sample:tool", Root: "/plugins/a", Enabled: true}, {ID: "sample-tool", Root: "/plugins/b", Enabled: true, Platforms: []string{"windows"}}, {ID: "sample:tool", Root: "/plugins/off", Enabled: false}}
	if got := IDsFor(records, "/plugins/a"); !reflect.DeepEqual(got, []string{"sample:tool", "sample-tool"}) {
		t.Fatalf("collision IDs=%v", got)
	}
	if got := IDsFor(records, "/plugins/direct"); got != nil {
		t.Fatalf("unregistered directory gained collision scope: %v", got)
	}
}
