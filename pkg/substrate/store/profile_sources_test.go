package store

import (
	"testing"
	"testing/fstest"
)

func TestProfileSourcesUserOverridesDistributionAndIsDetached(t *testing.T) {
	e := fixture(t)
	s := e.open(t, "notes", func(o *Options) {
		o.Distribution = fstest.MapFS{"profiles/dist/config.json": {Data: []byte(`{}`)}, "profiles/shared/config.json": {Data: []byte(`{}`)}}
	})
	writeConfig(t, s, []string{"profiles", "user", "config.json"}, `{}`)
	writeConfig(t, s, []string{"profiles", "shared", "config.json"}, `{}`)
	sources := s.ProfileSources()
	if sources["dist"] != LayerDistribution || sources["user"] != LayerUser || sources["shared"] != LayerUser || len(sources) != 3 {
		t.Fatal(sources)
	}
	sources["dist"] = LayerUser
	if s.ProfileSources()["dist"] != LayerDistribution {
		t.Fatal("mutable provenance")
	}
	names := s.Profiles()
	if len(names) != 3 || names[0] != "dist" || names[1] != "shared" || names[2] != "user" {
		t.Fatal(names)
	}
}
