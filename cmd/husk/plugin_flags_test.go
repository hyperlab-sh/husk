package main

import (
	"flag"
	"path/filepath"
	"reflect"
	"testing"
)

func pluginFlagDocs(t *testing.T, args ...string) map[string]map[string]any {
	t.Helper()
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	var paths, dirs stringFlag
	fs.Var(&paths, "plugin", "")
	fs.Var(&dirs, "plugin-dir", "")
	var bundle bundleFlag
	fs.Var(&bundle, "bundle", "")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return pluginInstanceDocs(fs)
}

func TestBundleFlagInstanceDocument(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "application")
	relative, err := filepath.Abs("application")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		args   []string
		want   string
		plugin bool
	}{
		{"relative", []string{"--bundle", "application"}, relative, false},
		{"absolute", []string{"--bundle", abs}, abs, false},
		{"last wins", []string{"--bundle", abs, "--bundle", "application"}, relative, false},
		{"empty disables", []string{"--bundle", abs, "--bundle", ""}, "", false},
		{"combined", []string{"--bundle", abs, "--plugin", "example"}, abs, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := pluginFlagDocs(t, tc.args...)
			husk := map[string]any{"bundle": tc.want}
			if tc.plugin {
				husk["plugins"] = map[string]any{"paths": []string{"example"}}
			}
			want := map[string]map[string]any{"config": {"husk": husk}}
			if !reflect.DeepEqual(docs, want) {
				t.Fatalf("got %#v want %#v", docs, want)
			}
		})
	}
}

func TestPluginFlagInstanceDocument(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"absent", nil, nil},
		{"paths", []string{"--plugin", "a", "--plugin", "b"}, map[string]any{"paths": []string{"a", "b"}}},
		{"dirs", []string{"--plugin-dir", "d"}, map[string]any{"dirs": []string{"d"}}},
		{"literal", []string{"--plugin", "~/raw/./plugin", "--plugin-dir", ""}, map[string]any{"paths": []string{"~/raw/./plugin"}, "dirs": []string{""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pluginFlagDocs(t, tc.args...)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("absent flags produced document: %#v", got)
				}
				return
			}
			want := map[string]map[string]any{"config": {"husk": map[string]any{"plugins": tc.want}}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v want %#v", got, want)
			}
		})
	}
}
