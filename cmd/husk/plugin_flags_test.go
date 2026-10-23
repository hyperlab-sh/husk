package main

import (
	"flag"
	"reflect"
	"testing"
)

func pluginFlagDocs(t *testing.T, args ...string) map[string]map[string]any {
	t.Helper()
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	var paths, dirs stringFlag
	fs.Var(&paths, "plugin", "")
	fs.Var(&dirs, "plugin-dir", "")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return pluginInstanceDocs(fs)
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
