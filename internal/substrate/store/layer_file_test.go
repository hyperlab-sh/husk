package store

import (
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLayerFileNames(t *testing.T) {
	for _, tc := range []struct {
		name, instance, profile string
		doc                     DocName
		local                   string
		distribution            []string
		flags                   bool
		layer                   Layer
		file                    string
	}{
		{name: "user", doc: "config", local: "config.json", layer: LayerUser, file: "config.json"},
		{name: "nested user", doc: "a/b", local: "a/b.json", layer: LayerUser, file: "a/b.json"},
		{name: "profile", doc: "config", profile: "work", local: "profiles/work/config.json", layer: LayerProfile, file: "profiles/work/config.json"},
		{name: "instance file", doc: "config", local: "instances/t1/config.json", layer: LayerInstance, file: "instances/t1/config.json"},
		{name: "instance file and flags", doc: "config", local: "instances/t1/config.json", flags: true, layer: LayerInstance},
		{name: "pid flags", instance: "123", doc: "config", flags: true, layer: LayerInstance},
		{name: "distribution base", doc: "config", distribution: []string{"config.json"}, layer: LayerDistribution, file: "config.json"},
		{name: "distribution profile", doc: "config", profile: "work", distribution: []string{"profiles/work/config.json"}, layer: LayerDistribution, file: "profiles/work/config.json"},
		{name: "distribution folded profile", doc: "config", profile: "work", distribution: []string{"config.json", "profiles/work/config.json"}, layer: LayerDistribution, file: "config.json"},
		{name: "missing", doc: "config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fixture(t)
			instance := tc.instance
			if instance == "" {
				instance = "t1"
			}
			s := e.open(t, instance, func(o *Options) {
				if tc.flags {
					o.InstanceDocs = map[DocName]Doc{tc.doc: {"flag": true}}
				}
				if tc.distribution != nil {
					dist := fstest.MapFS{}
					for _, file := range tc.distribution {
						dist[file] = &fstest.MapFile{Data: []byte(`{"value":true}`)}
					}
					o.Distribution = dist
				}
			})
			if tc.local != "" {
				writeConfig(t, s, strings.Split(tc.local, "/"), `{"value":true}`)
			}
			layers, err := s.Layers(tc.doc, tc.profile)
			if err != nil {
				t.Fatal(err)
			}
			wantLen := 1
			if tc.layer == "" {
				wantLen = 0
			}
			if len(layers) != wantLen {
				t.Fatalf("layers = %+v, want %d", layers, wantLen)
			}
			for _, l := range layers {
				if l.Layer != tc.layer || l.File != tc.file {
					t.Errorf("layer/file = %s %q, want %s %q", l.Layer, l.File, tc.layer, tc.file)
				}
				if filepath.IsAbs(l.File) || strings.Contains(l.File, "..") || strings.Contains(l.File, "\\") || strings.Contains(l.File, e.root) || strings.HasPrefix(l.File, "/") || strings.HasPrefix(l.File, "./") {
					t.Errorf("unsafe layer file %q", l.File)
				}
			}
		})
	}
}
