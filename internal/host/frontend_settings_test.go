package host

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
)

func TestFrontendSettingsReads(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	t.Run("Themes", func(t *testing.T) {
		themes := fe.Themes()
		if len(themes) == 0 || themes[0].Name != palette.Default().Name || !themes[0].Current {
			t.Fatalf("themes: %+v", themes)
		}
		themes[0].Name = "changed"
		if fe.Themes()[0].Name != palette.Default().Name {
			t.Fatal("shared themes")
		}
	})
	t.Run("SettingsSchema", func(t *testing.T) {
		found := false
		for _, entry := range fe.SettingsSchema() {
			if entry.Key == "theme.name" && entry.Type == "string" {
				found = true
			}
		}
		if !found {
			t.Fatal("missing theme schema")
		}
	})
	t.Run("Setting", func(t *testing.T) {
		value, source, err := fe.Setting("theme.allow_low_contrast")
		if err != nil || value != false || source != "default" {
			t.Fatalf("%v %q %v", value, source, err)
		}
		if _, _, err := fe.Setting("missing.key"); contracts.CodeOf(err) != "not_found" {
			t.Fatalf("unknown: %v", err)
		}
	})
	t.Run("KeyListing", func(t *testing.T) {
		id := protocol.NewStringID("keys")
		res := h.root.mux.Serve(t.Context(), contracts.Caller{Kind: protocol.CallerCLI}, protocol.Message{ID: &id, Method: protocol.MethodKeysList}, nil)
		got, err := json.Marshal(fe.KeyListing())
		if err != nil || res.Error != nil || !bytes.Equal(got, res.Result) {
			t.Fatalf("seam=%s wire=%s err=%v", got, res.Result, res.Error)
		}
		if len(fe.KeyListing().Entries) == 0 {
			t.Fatal("empty listing fixture")
		}
	})
	t.Run("ConfigDir", func(t *testing.T) {
		if fe.ConfigDir() == "" || fe.ConfigDir() != h.root.store.Paths().Config {
			t.Fatal("config path")
		}
	})
}

func TestFrontendProfiles(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	// New profiles are discovered by the coordinator, not by the read seam.
	dir := filepath.Join(h.root.store.Paths().Config, "profiles", "work")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.root.rt.Coordinator.Reload(t.Context(), "cli"); err != nil {
		t.Fatal(err)
	}
	profiles := h.root.Profiles()
	if len(profiles) != 1 || profiles[0].Name != "work" || profiles[0].Active {
		t.Fatalf("%+v", profiles)
	}
}

// awaitFrontend verifies asynchronous, exactly-once delivery on the host loop.
func awaitFrontend[T any](t *testing.T, h *harness, start func(func(T, error))) (T, error) {
	t.Helper()
	type outcome struct {
		value T
		err   error
	}
	done := make(chan outcome, 2)
	var calls atomic.Int32
	h.loop(func() {
		inCall := true
		start(func(value T, err error) {
			calls.Add(1)
			if !h.root.onLoop() {
				t.Error("reply off-loop")
			}
			if inCall {
				t.Error("synchronous reply")
			}
			done <- outcome{value, err}
		})
		inCall = false
	})
	result := <-done
	// Wait for the tracked worker and its queued reply to finish before counting.
	h.root.wg.Wait()
	h.loop(func() {})
	if calls.Load() != 1 {
		t.Fatalf("reply count %d", calls.Load())
	}
	return result.value, result.err
}

func TestFrontendSetSetting(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := awaitFrontend(t, h, func(reply func(contracts.SettingSetResult, error)) {
		h.root.SetSetting("theme.allow_low_contrast", true, reply)
	})
	if err != nil || res.Key != "theme.allow_low_contrast" || res.Value != true || res.Source != "user" || len(res.Reports) != 5 {
		t.Fatalf("%+v %v", res, err)
	}
	value, source, err := h.root.Setting(res.Key)
	if err != nil || value != true || source != "user" {
		t.Fatalf("%v %s %v", value, source, err)
	}
}

func TestFrontendUseProfile(t *testing.T) {
	h, err := boot(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(h.root.store.Paths().Config, "profiles", "work")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.root.rt.Coordinator.Reload(t.Context(), "cli"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"work", "missing"} {
		t.Run(name, func(t *testing.T) {
			res, err := awaitFrontend(t, h, func(reply func(contracts.ProfileUseResult, error)) { h.root.UseProfile(name, reply) })
			if name == "missing" {
				if contracts.CodeOf(err) != "not_found" {
					t.Fatalf("%v", err)
				}
				return
			}
			if err != nil || res.Active != name || len(res.Reports) != 5 {
				t.Fatalf("%+v %v", res, err)
			}
		})
	}
}

func TestFrontendRemovePlugin(t *testing.T) {
	h, _, fe := bootFrontend(t, nil)
	res, err := awaitFrontend(t, h, func(reply func(contracts.PluginRemoval, error)) { fe.RemovePlugin("alpha", reply) })
	if err != nil || res.Plugin != "alpha" || len(res.Reports) != 5 {
		t.Fatalf("%+v %v", res, err)
	}
	infos, err := h.root.pods.List(t.Context(), contracts.Caller{Kind: protocol.CallerCLI})
	if err != nil || len(infos) != 0 {
		t.Fatalf("%+v %v", infos, err)
	}
}

func TestFrontendPluginDirSurvivesLifecycle(t *testing.T) {
	h, _, _ := bootFrontend(t, nil)
	infos, err := h.root.pods.List(t.Context(), contracts.Caller{Kind: protocol.CallerCLI})
	if err != nil || len(infos) != 1 || infos[0].Dir == "" {
		t.Fatalf("%+v %v", infos, err)
	}
	dir := infos[0].Dir
	for _, status := range []wrapper.Liveness{wrapper.Live, wrapper.Exited} {
		h.loop(func() {
			if status == wrapper.Exited {
				h.root.Update(pods.ExitMsg{Info: wrapper.Info{PluginID: "alpha", Status: status}})
			}
		})
		raw := h.root.Snapshot().Slices["husk.pods"].Value
		var doc struct {
			Plugins map[string]struct {
				Dir    string
				Status string
			}
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Plugins["alpha"].Dir != dir || doc.Plugins["alpha"].Status != string(status) {
			t.Fatalf("%s", raw)
		}
	}
	infos, err = h.root.pods.List(t.Context(), contracts.Caller{Kind: protocol.CallerCLI})
	if err != nil || len(infos) != 1 || infos[0].Dir != dir {
		t.Fatalf("%+v %v", infos, err)
	}
}
