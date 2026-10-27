package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func bundleConfig(value any) Doc { return Doc{"husk": Doc{"bundle": value}} }
func writeBootstrap(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}
func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
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
		out = append(out, rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReadBootstrapPrecedencePathsAndNoWrites(t *testing.T) {
	for _, tc := range []struct {
		name, user, instance, flag, profile, want, layer, problem string
		anonymous                                                 bool
	}{
		{name: "fresh"},
		{name: "duplicate user absent", user: `{"husk.bundle":"~/first","husk":{"bundle":"~/second"}}`},
		{name: "duplicate instance falls through", user: `{"husk":{"bundle":"~/user"}}`, instance: `{"husk.bundle":"~/first","husk":{"bundle":"~/second"}}`, want: "user", layer: "user"},
		{name: "all layers duplicate absent", user: `{"husk.bundle":"~/first","husk":{"bundle":"~/second"}}`, instance: `{"husk.bundle":"~/third","husk":{"bundle":"~/fourth"}}`},
		{name: "user", user: `{"husk":{"bundle":"~/user"}}`, want: "user", layer: "user"},
		{name: "instance", user: `{"husk":{"bundle":"~/user"}}`, instance: `{"husk":{"bundle":"~/instance"}}`, want: "instance", layer: "instance"},
		{name: "flag", user: `{"husk":{"bundle":"~/user"}}`, instance: `{"husk":{"bundle":"~/instance"}}`, flag: "flag", want: "flag", layer: "instance"},
		{name: "empty disables", user: `{"husk":{"bundle":"~/user"}}`, flag: "empty"},
		{name: "null deletes", user: `{"husk":{"bundle":"~/user"}}`, instance: `{"husk":{"bundle":null}}`},
		{name: "relative user", user: `{"husk":{"bundle":"./x"}}`, problem: `husk.bundle in the user layer must be an absolute path: "./x"`},
		{name: "relative instance", instance: `{"husk":{"bundle":"./x"}}`, problem: `husk.bundle in the instance layer must be an absolute path: "./x"`},
		{name: "nonstring user", user: `{"husk":{"bundle":1}}`, problem: "husk.bundle in the user layer must be a string"},
		{name: "nonstring instance", instance: `{"husk":{"bundle":{}}}`, problem: "husk.bundle in the instance layer must be a string"},
		{name: "unparsable user absent", user: `{`, instance: `{"husk":{"bundle":"~/instance"}}`, want: "instance", layer: "instance"},
		{name: "unparsable instance absent", user: `{"husk":{"bundle":"~/user"}}`, instance: `{`, want: "user", layer: "user"},
		{name: "profile ignored", profile: `{"husk":{"bundle":"./profile"}}`},
		{name: "anonymous ignores instance", anonymous: true, instance: `{"husk":{"bundle":"./x"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			env := Env{LookupEnv: func(string) (string, bool) { return "", false }, HomeDir: func() (string, error) { return home, nil }}
			root := filepath.Join(home, ".config", "husk")
			if tc.user != "" {
				writeBootstrap(t, root, "config.json", tc.user)
			}
			if tc.instance != "" {
				writeBootstrap(t, root, "instances/test/config.json", tc.instance)
			}
			if tc.profile != "" {
				writeBootstrap(t, root, "profiles/test/config.json", tc.profile)
			}
			id := "test"
			if tc.anonymous {
				id = "123"
			}
			docs := map[DocName]Doc{}
			if tc.flag != "" {
				value := filepath.Join(home, tc.flag)
				if tc.flag == "empty" {
					value = ""
				}
				docs["config"] = bundleConfig(value)
			}
			before := tree(t, home)
			got, err := ReadBootstrap(env, InstanceRef{ID: id}, docs)
			if tc.problem != "" {
				var e *BootstrapError
				if !errors.As(err, &e) || e.Code != "bundle_path_relative" || err.Error() != "bundle: "+tc.problem {
					t.Fatalf("error=%v", err)
				}
			} else {
				want := tc.want
				if want != "" {
					want = filepath.Join(home, want)
				}
				if err != nil || got.Bundle != want || string(got.BundleLayer) != tc.layer {
					t.Fatalf("bootstrap=%+v %v want %s %s", got, err, want, tc.layer)
				}
			}
			if after := tree(t, home); !slices.Equal(before, after) {
				t.Fatalf("bootstrap wrote: %v -> %v", before, after)
			}
		})
	}
}

func TestBundleDistributionPrecedenceProfilesOriginsAndReload(t *testing.T) {
	for _, embedded := range []bool{false, true} {
		t.Run(fmt.Sprint(embedded), func(t *testing.T) {
			e := fixture(t)
			bundleDir := t.TempDir()
			writeBootstrap(t, bundleDir, "config.json", `{"from_bundle":1,"winner":"bundle","deleted":null,"profile_order":"bundle base"}`)
			writeBootstrap(t, bundleDir, "keymap.json", `{"version":1,"bind":[{"key":"f2","action":"example.open"}]}`)
			writeBootstrap(t, bundleDir, "profiles/work/config.json", `{"profile_order":"bundle profile","from_profile":true,"removed_by_profile":null}`)
			writeBootstrap(t, bundleDir, "profiles/other/config.json", `{}`)
			s := e.open(t, "test", func(o *Options) {
				o.Bundle = os.DirFS(bundleDir)
				if embedded {
					o.Distribution = fstest.MapFS{
						"config.json":                   {Data: []byte(`{"from_embedded":true,"winner":"embedded","deleted":true,"removed_by_profile":true,"profile_order":"embedded base"}`)},
						"keymap.json":                   {Data: []byte(`{"version":1,"bind":[{"key":"f3","action":"example.old"}]}`)},
						"profiles/work/config.json":     {Data: []byte(`{"profile_order":"embedded profile"}`)},
						"profiles/embedded/config.json": {Data: []byte(`{}`)},
					}
				}
			})
			r, err := s.Resolve("config", "")
			if err != nil || r.Doc["winner"] != "bundle" || r.Origins["from_bundle"] != LayerDistribution {
				t.Fatalf("distribution: %+v %v", r, err)
			}
			if _, ok := r.Doc["deleted"]; ok {
				t.Fatal("bundle tombstone lost")
			}
			layers, err := s.Layers("config", "")
			wantFile := "config.json"
			if embedded {
				wantFile = ""
			}
			if err != nil || len(layers) != 1 || layers[0].Layer != LayerDistribution || layers[0].File != wantFile {
				t.Fatalf("layers=%+v %v", layers, err)
			}
			r, err = s.Resolve("config", "work")
			if err != nil || r.Doc["profile_order"] != "bundle profile" || r.Doc["from_profile"] != true {
				t.Fatalf("profile order: %+v %v", r, err)
			}
			if _, ok := r.Doc["removed_by_profile"]; ok {
				t.Fatal("bundle profile tombstone lost")
			}
			sources := s.ProfileSources()
			if sources["work"] != LayerDistribution || sources["other"] != LayerDistribution || embedded && sources["embedded"] != LayerDistribution {
				t.Fatalf("profile sources: %v", sources)
			}
			keymap, err := s.Resolve("keymap", "")
			if err != nil || keymap.Origins["bind"] != LayerDistribution || !strings.Contains(fmt.Sprint(keymap.Doc["bind"]), "example.open") || strings.Contains(fmt.Sprint(keymap.Doc["bind"]), "example.old") {
				t.Fatalf("keymap: %+v %v", keymap, err)
			}
			writeBootstrap(t, s.paths.Config, "config.json", `{"winner":"user"}`)
			writeBootstrap(t, bundleDir, "config.json", `{"from_bundle":2,"winner":"bundle changed"}`)
			s.Invalidate()
			r, err = s.Resolve("config", "")
			if err != nil || r.Doc["winner"] != "user" || r.Doc["from_bundle"] != float64(2) || r.Origins["winner"] != LayerUser {
				t.Fatalf("reload: %+v %v", r, err)
			}
			if err := s.Patch("config", LayerDistribution, "", Doc{"bad": true}); err == nil {
				t.Fatal("distribution writable")
			}
			if !reflect.DeepEqual(sources, s.ProfileSources()) {
				t.Fatal("profile sources changed unexpectedly")
			}
		})
	}
}
