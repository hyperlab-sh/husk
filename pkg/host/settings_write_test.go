package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	ports "github.com/hyperlab-sh/husk/pkg/host/ports/services"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
)

func hostSet(t *testing.T, h *harness, key string, value any) contracts.SettingSetResult {
	t.Helper()
	p := ports.Settings{Svc: h.root.rt.Settings, Inputs: h.root.rt.Coordinator}
	r, err := p.Set(t.Context(), contracts.Caller{Kind: protocol.CallerHost}, key, value)
	if err != nil {
		t.Fatal(err)
	}
	if r.Key != key || len(r.Reports) != 5 {
		t.Fatalf("%+v", r)
	}
	return r
}
func TestSettingsSetPatchTouchesOneKeyAndKeepsComments(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(h.root.store.Paths().Config, "config.json")
	if err := os.WriteFile(file, []byte("{\n // keep this comment\n \"unregistered\": {\"keep\": 7}\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := hostSet(t, h, "theme.name", "evening")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "// keep this comment") || !strings.Contains(string(b), `"keep": 7`) || !strings.Contains(string(b), `"evening"`) || r.Source != "user" || r.Value != "evening" {
		t.Fatalf("reply=%+v file=%s", r, b)
	}
}
func TestSettingsSetRestartWrittenAndReported(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := hostSet(t, h, "husk.plugins.paths", []string{"next-start"})
	found := false
	for _, rep := range r.Reports {
		for _, p := range rep.Problems {
			if p.Code == "restart_required" && p.Key == "husk.plugins.paths" {
				found = true
			}
		}
	}
	b, err := os.ReadFile(filepath.Join(h.root.store.Paths().Config, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !found || !strings.Contains(string(b), "next-start") || len(h.root.rt.Settings.Snapshot().Strings("husk.plugins.paths")) != 0 {
		t.Fatalf("reply=%+v file=%s", r, b)
	}
}
func TestSettingsSetThemeChangedOnceAndNoOpDoesNotPush(t *testing.T) {
	h, f, _ := bootFrontend(t, nil)
	file := filepath.Join(h.root.store.Paths().Config, "themes.json")
	if err := os.WriteFile(file, []byte(`{"version":1,"themes":{"evening":{"palette":{"accent":"#f1effe"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r := hostSet(t, h, "theme.name", "evening")
		h.loop(func() {})
		var theme contracts.ReloadReport
		for _, rep := range r.Reports {
			if rep.Service == "theme" {
				theme = rep
			}
		}
		pushes := f.proc("alpha").themePushes(t)
		if len(pushes) != 1 || pushes[0].Name != "evening" || theme.Changed != (i == 0) || !theme.Applied {
			t.Fatalf("iteration=%d report=%+v pushes=%+v", i, theme, pushes)
		}
	}
	if h.root.rt.Theme.Palette().Accent == palette.Default().Accent {
		t.Fatal("palette did not change")
	}
}
func TestSettingsSetPortErrorsPreserveCodesAndKey(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := ports.Settings{Svc: h.root.rt.Settings, Inputs: h.root.rt.Coordinator}
	for _, tc := range []struct {
		key          string
		value        any
		code, reason string
	}{
		{"theme.name", 42, "invalid_params", "invalid_value"},
		{"theme.name", nil, "invalid_params", "null_value"},
		{"nope.key", 1, "not_found", "unknown_setting"},
	} {
		_, err := p.Set(context.Background(), contracts.Caller{Kind: "host"}, tc.key, tc.value)
		data, ok := err.(contracts.DataError)
		if contracts.CodeOf(err) != tc.code || !ok || data.Data()["reason"] != tc.reason {
			t.Fatalf("%s: %v", tc.key, err)
		}
		if tc.reason == "invalid_value" && data.Data()["key"] != tc.key {
			t.Fatalf("key lost: %v", data.Data())
		}
	}
	// A broken existing config makes Patch fail before any flight can run.
	file := filepath.Join(h.root.store.Paths().Config, "config.json")
	if err := os.WriteFile(file, []byte(`{"theme":`), 0600); err != nil {
		t.Fatal(err)
	}
	gen := h.root.rt.Settings.Generation()
	_, err = p.Set(t.Context(), contracts.Caller{Kind: "host"}, "theme.name", "evening")
	if contracts.CodeOf(err) != "unavailable" || err.(contracts.DataError).Data()["reason"] != "store_error" || h.root.rt.Settings.Generation() != gen {
		t.Fatalf("%v gen=%d", err, h.root.rt.Settings.Generation())
	}
}
