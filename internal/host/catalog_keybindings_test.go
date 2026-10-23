package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// The declared plugins document of a launched plugin carries its focus-local
// bindings under "keybindings", the manifest's own spelling, and never under
// the retired "focus_bindings".
func TestDeclaredPluginsDocumentSpellsBindingsKeybindings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"manifest_version": 2, "protocol": 2, "id": "alpha", "name": "alpha", "version": "1",
		"command":  []string{"./plugin"},
		"actions":  []any{map[string]any{"id": "alpha.go"}},
		"surfaces": []any{map[string]any{"id": "sidebar-list"}},
		"keybindings": []any{map[string]any{
			"key": "n", "action": "alpha.go", "focus": "sidebar-list",
		}},
	})
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	hook, ready := readyHook("")
	f := newFakes()
	h, err := boot(t, func(c *contracts.Config) { c.Plugins = []string{dir} }, withHook(hook), WithFactory(f.factory))
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ready, h)
	<-f.started
	h.loop(func() {
		doc := h.root.store.Declared(store.DeclaredPlugins)["alpha"]
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		binds, ok := got["keybindings"].([]any)
		if !ok || len(binds) != 1 {
			t.Fatalf("keybindings = %#v", got["keybindings"])
		}
		if b, _ := binds[0].(map[string]any); b["key"] != "n" || b["action"] != "alpha.go" || b["focus"] != "sidebar-list" {
			t.Fatalf("binding = %#v", binds[0])
		}
		if _, ok := got["focus_bindings"]; ok {
			t.Fatal("catalog still carries focus_bindings")
		}
	})
}
