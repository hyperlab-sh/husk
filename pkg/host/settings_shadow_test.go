package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	ports "github.com/hyperlab-sh/husk/pkg/host/ports/services"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func TestSettingsSetUserWriteShadowedByProfileReportsWinningSource(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := h.root.store.Paths().Config
	profile := filepath.Join(dir, "profiles", "work")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	for file, text := range map[string]string{
		filepath.Join(dir, "config.json"):     `{"profile":{"default":"work"}}`,
		filepath.Join(profile, "config.json"): `{"theme":{"name":"zulu"}}`,
		filepath.Join(dir, "themes.json"):     `{"version":1,"themes":{"alpha":{"palette":{}},"zulu":{"palette":{}}}}`,
	} {
		if err := os.WriteFile(file, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r := hostSet(t, h, "theme.name", "alpha")
	rows, err := (ports.Theme{Svc: h.root.rt.Theme}).List(t.Context(), contracts.Caller{Kind: protocol.CallerHost})
	if err != nil || r.Source != "profile" || len(rows) != 11 || rows[9].Name != "alpha" || rows[9].Current || rows[10].Name != "zulu" || !rows[10].Current {
		t.Fatalf("reply=%+v rows=%+v err=%v", r, rows, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct{ Theme struct{ Name string } }
	if err := json.Unmarshal(b, &raw); err != nil || raw.Theme.Name != "alpha" {
		t.Fatalf("user write missing: %s %v", b, err)
	}
}

func TestSettingsSetRestartKeyShadowedByProfileNamesProfileInProblem(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := h.root.store.Paths().Config
	profile := filepath.Join(dir, "profiles", "work")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	for file, text := range map[string]string{
		filepath.Join(dir, "config.json"):     `{"profile":{"default":"work"}}`,
		filepath.Join(profile, "config.json"): `{"husk":{"plugins":{"paths":["from-profile"]}}}`,
	} {
		if err := os.WriteFile(file, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r := hostSet(t, h, "husk.plugins.paths", []string{"from-user"})
	want := h.root.rt.Settings.Snapshot().Source("husk.plugins.paths")
	var layer string
	for _, rep := range r.Reports {
		for _, p := range rep.Problems {
			if p.Code == "restart_required" && p.Key == "husk.plugins.paths" {
				layer = p.Layer
			}
		}
	}
	if r.Source != want || r.Source == "profile" || r.Source == "user" || layer != "profile" {
		t.Fatalf("source=%q in-force=%q problem layer=%q reply=%+v", r.Source, want, layer, r)
	}
}
