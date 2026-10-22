package launch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/internal/host/contracts/fakes"
	"github.com/hyperlab-sh/husk/internal/host/ports/launch"
	"github.com/hyperlab-sh/husk/internal/pods"
	"github.com/hyperlab-sh/husk/internal/pods/manifest"
	"github.com/hyperlab-sh/husk/internal/pods/wrapper"
	"github.com/hyperlab-sh/husk/internal/services"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// fakeClock is deterministic time for Store, Services and the registry: it
// never fires timers, so no test depends on wall time.
type fakeClock struct{ now time.Time }

func (k fakeClock) Now() time.Time { return k.now }

// fakeTimer never delivers.
type fakeTimer struct{ c chan time.Time }

func (t fakeTimer) C() <-chan time.Time { return t.c }
func (fakeTimer) Stop() bool            { return true }

func (k fakeClock) NewTimer(time.Duration) store.Timer {
	return fakeTimer{c: make(chan time.Time)}
}

func spec(id string) manifest.Spec {
	return manifest.Spec{ID: id, Protocol: protocol.Major, Command: []string{"plugin"}}
}

func specWithSlice(id string) manifest.Spec {
	s := spec(id)
	s.Slice = &manifest.SliceDecl{Scope: "instance"}
	s.SettingsDeclared = []manifest.SettingDecl{{Key: "plugins." + id + ".enabled", Type: "bool", Default: json.RawMessage(`false`)}}
	s.SecretRequests = []manifest.SecretRequest{{Name: "git.token", Purpose: "Push", Required: true}}
	return s
}

// recordedLoop is a launch.Loop test double that also records posts and logs.
type recordedLoop struct {
	launch.Loop
	posted []tea.Msg
	logs   []pods.LogMsg
}

func newRecordedLoop(ctx context.Context, registry contracts.PodRegistry, preparer launch.Preparer, starter contracts.WrapperStarter, opening []protocol.Message) (*recordedLoop, *pods.Host) {
	l := &recordedLoop{}
	l.Loop = launch.Loop{
		Context:   ctx,
		Registry:  registry,
		Preparer:  preparer,
		PostFn:    func(m tea.Msg) { l.posted = append(l.posted, m) },
		OpeningFn: func(string) []protocol.Message { return opening },
		LogFn:     func(m pods.LogMsg) { l.logs = append(l.logs, m) },
	}
	h, err := pods.New(l, pods.Options{Registry: registry, Starter: starter})
	if err != nil {
		panic(err)
	}
	return l, h
}

// Criterion 1: the six launch steps in pods.md order, asserted through one
// shared Recorder: Reserve and starting, per-plugin Declare and slice
// registration, exactly one Reload for the batch, then Wrapper starts with
// live writes.
func TestLaunchSequenceOrder(t *testing.T) {
	rec := fakes.NewRecorder()
	registry := fakes.NewPods(rec)
	catalog := fakes.NewCatalog(rec)
	slices := fakes.NewSlices(rec)
	reload := fakes.NewReloader(rec)
	reload.Applied = true
	starter := fakes.NewWrapper(rec)

	opening := []protocol.Message{{Method: protocol.MethodThemeChanged}, {Method: protocol.MethodSurfaceResized}}
	_, h := newRecordedLoop(t.Context(), registry, launch.Preparer{Catalog: catalog, Slices: slices, Reload: reload}, starter, opening)

	if _, err := h.Add(specWithSlice("files")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Add(spec("notes")); err != nil {
		t.Fatal(err)
	}
	cmd := h.Prepare()
	if cmd == nil {
		t.Fatal("missing preparation command")
	}
	if !h.Handle(cmd()) {
		t.Fatal("unhandled preparation")
	}

	want := []string{
		"Pods.Reserve",         // step 1: files
		"Pods.SetStatus",       // step 2: files starting
		"Pods.Reserve",         // step 1: notes
		"Pods.SetStatus",       // step 2: notes starting
		"Catalog.Declare",      // step 3: files (worker)
		"Catalog.Declare",      // step 3: notes (worker)
		"Reloader.Reload",      // step 3/5: one reload for the batch (worker)
		"Slices.RegisterSlice", // step 4: marshaled back, applied on the loop
		"Wrapper.Start",        // step 6: files after the gate
		"Pods.SetStatus",       // live
		"Wrapper.Start",        // step 6: notes
		"Pods.SetStatus",       // live
	}
	if got := rec.Methods(); !reflect.DeepEqual(got, want) {
		t.Fatalf("launch order\n got %v\nwant %v", got, want)
	}
	// Only the slice-declaring plugin registers a slice, under its own id.
	for _, call := range rec.Calls() {
		if call.Method == "Slices.RegisterSlice" {
			if len(call.Args) != 1 || call.Args[0] != "files.state" {
				t.Fatalf("slice registration %v", call.Args)
			}
		}
	}
	// Each starter call carried the opening queue.
	for _, call := range rec.Calls() {
		if call.Method == "Wrapper.Start" && call.Args[1] != len(opening) {
			t.Fatalf("start without opening: %v", call.Args)
		}
	}
}

// D48: a completed-but-not-applied reload still opens the gate. The problems
// are surfaced as a logged, non-blocking reload error and the Wrapper starts.
func TestFailedReloadStillOpensGate(t *testing.T) {
	rec := fakes.NewRecorder()
	reload := fakes.NewReloader(rec)
	reload.Applied = false
	reload.Problems = []contracts.Problem{{Severity: "error", Layer: "theme", Message: "contrast below floor"}}
	starter := fakes.NewWrapper(rec)

	loop, h := newRecordedLoop(t.Context(), fakes.NewPods(rec), launch.Preparer{Reload: reload}, starter, nil)
	added, err := h.Add(spec("files"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := h.Prepare()
	prepared := cmd().(pods.PreparedMsg)
	if prepared.Preparation.ReloadError == nil || !strings.Contains(prepared.Preparation.ReloadError.Error(), "contrast below floor") {
		t.Fatal("reload problems lost", prepared.Preparation.ReloadError)
	}
	if len(prepared.Preparation.Failed) != 0 {
		t.Fatal("reload error marked the plugin failed")
	}
	if !h.Handle(prepared) {
		t.Fatal("unhandled preparation")
	}
	if added.Info().Status != wrapper.Live {
		t.Fatal("plugin not live after a failed reload")
	}
	if len(loop.logs) != 1 || loop.logs[0].Level != "warn" {
		t.Fatal("reload error not logged", loop.logs)
	}
	var started int
	for _, m := range rec.Methods() {
		if m == "Wrapper.Start" {
			started++
		}
	}
	if started != 1 {
		t.Fatal("wrapper starts", started)
	}
}

// A slice-registration failure is discovered on the loop, when the worker's
// marshaled write is applied: that plugin fails with no Wrapper start, and
// the batch continues (step 4 failure behaves like a declaration failure).
func TestRegisterFailureFailsOnlyThatPlugin(t *testing.T) {
	rec := fakes.NewRecorder()
	slices := fakes.NewSlices(rec)
	slices.Err = contracts.NewError(protocol.CodeConflict, "slice already registered", nil)
	reload := fakes.NewReloader(rec)
	reload.Applied = true
	starter := fakes.NewWrapper(rec)

	_, h := newRecordedLoop(t.Context(), fakes.NewPods(rec), launch.Preparer{Slices: slices, Reload: reload}, starter, nil)
	declared, err := h.Add(specWithSlice("files"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Add(spec("notes")); err != nil {
		t.Fatal(err)
	}
	if !h.Handle(h.Prepare()()) {
		t.Fatal("unhandled preparation")
	}
	if declared.Info().Status != wrapper.Failed {
		t.Fatal("registration failure did not fail the plugin", declared.Info().Status)
	}
	var started []string
	for _, call := range rec.Calls() {
		if call.Method == "Wrapper.Start" {
			started = append(started, fmt.Sprint(call.Args[0]))
		}
	}
	if len(started) != 1 || started[0] != "notes" {
		t.Fatal("batch did not continue past the failed registration", started)
	}
	if !strings.Contains(declared.Info().LastError, "slice already registered") {
		t.Fatal("registration error not surfaced", declared.Info().LastError)
	}
}

// catalogFunc adapts a function to the CatalogDeclarer seam for tests that
// need per-plugin outcomes.
type catalogFunc func(pluginID string) error

func (f catalogFunc) Declare(pluginID string, _, _ json.RawMessage) error { return f(pluginID) }
func (catalogFunc) Undeclare(string)                                      {}

// A declaration failure fails only that plugin: no Wrapper start, failed
// liveness; the rest of the batch launches.
func TestDeclareFailureFailsOnlyThatPlugin(t *testing.T) {
	rec := fakes.NewRecorder()
	catalog := catalogFunc(func(pluginID string) error {
		if pluginID == "broken" {
			return errors.New("store refused")
		}
		return nil
	})
	reload := fakes.NewReloader(rec)
	reload.Applied = true
	starter := fakes.NewWrapper(rec)

	_, h := newRecordedLoop(t.Context(), fakes.NewPods(rec), launch.Preparer{Catalog: catalog, Reload: reload}, starter, nil)
	broken, err := h.Add(spec("broken"))
	if err != nil {
		t.Fatal(err)
	}
	fine, err := h.Add(spec("fine"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := h.Prepare()
	prepared := cmd().(pods.PreparedMsg)
	if err := prepared.Preparation.Failed["broken"]; err == nil {
		t.Fatal("declare failure not recorded")
	}
	if !h.Handle(prepared) {
		t.Fatal("unhandled preparation")
	}
	if broken.Info().Status != wrapper.Failed || broken.Info().LastError == "" {
		t.Fatalf("failed pod %+v", broken.Info())
	}
	if fine.Info().Status != wrapper.Live {
		t.Fatal("declare failure blocked the batch")
	}
	var started int
	for _, m := range rec.Methods() {
		if m == "Wrapper.Start" {
			started++
		}
	}
	if started != 1 {
		t.Fatal("wrapper starts", started)
	}
}

// The real PodRegistry over State: reservations, merge-patched entries,
// not_found for unreserved ids, conflict for duplicates, and Release.
func TestPodRegistryOverState(t *testing.T) {
	tree := state.New(state.Options{})
	registry, err := launch.NewPodRegistry(tree)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := registry.Reserve(ctx, "files"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reserve(ctx, "files"); contracts.CodeOf(err) != string(protocol.CodeConflict) {
		t.Fatal("duplicate reserve", err)
	}
	if err := registry.SetStatus(ctx, "nope", contracts.PodStatusUpdate{Status: contracts.PodStarting}); contracts.CodeOf(err) != string(protocol.CodeNotFound) {
		t.Fatal("unreserved set", err)
	}

	if err := registry.SetStatus(ctx, "files", contracts.PodStatusUpdate{Status: contracts.PodStarting}); err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1700000000, 0).UTC()
	pid := 4242
	log := "/runtime/logs/files/plugin.log"
	errText := "boom"
	if err := registry.SetStatus(ctx, "files", contracts.PodStatusUpdate{
		Status:    contracts.PodLive,
		PID:       &pid,
		StartedAt: &started,
		LogPath:   &log,
		Detail:    &errText,
	}); err != nil {
		t.Fatal(err)
	}
	empty := ""
	if err := registry.SetStatus(ctx, "files", contracts.PodStatusUpdate{Detail: &empty}); err != nil {
		t.Fatal(err)
	}

	var value struct {
		Plugins map[string]map[string]any `json:"plugins"`
	}
	if err := json.Unmarshal(tree.Snapshot().Slices[state.PodsNamespace].Value, &value); err != nil {
		t.Fatal(err)
	}
	entry := value.Plugins["files"]
	want := map[string]any{"status": "live", "pid": float64(pid), "started_at": started.Format(time.RFC3339Nano), "log": log}
	if !reflect.DeepEqual(entry, want) {
		t.Fatalf("husk.pods entry %v, want %v", entry, want)
	}

	registry.Release(ctx, "files")
	value.Plugins = nil // unmarshal merges into a non-nil map
	if err := json.Unmarshal(tree.Snapshot().Slices[state.PodsNamespace].Value, &value); err != nil {
		t.Fatal(err)
	}
	if _, ok := value.Plugins["files"]; ok {
		t.Fatal("release kept the entry")
	}
	if err := registry.Reserve(ctx, "files"); err != nil {
		t.Fatal("released id not reservable again")
	}
}

// A duplicate NewPodRegistry fails: husk.pods has exactly one owner.
func TestPodRegistryRegistersOnce(t *testing.T) {
	tree := state.New(state.Options{})
	if _, err := launch.NewPodRegistry(tree); err != nil {
		t.Fatal(err)
	}
	if _, err := launch.NewPodRegistry(tree); !errors.Is(err, state.ErrDuplicate) {
		t.Fatal("second registry accepted", err)
	}
}

// The real CatalogDeclarer over Store: the manifest Spec's catalog and
// focus-keymap documents become the plugin's declared documents, atomically
// replacing earlier ones, and Undeclare removes both.
func TestCatalogDeclarerOverStore(t *testing.T) {
	s := openStore(t)
	declarer := launch.CatalogDeclarer{Store: s}
	spec := specWithSlice("files")

	catalog, err := spec.CatalogJSON()
	if err != nil {
		t.Fatal(err)
	}
	focus, err := spec.FocusKeymapJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := declarer.Declare("files", catalog, focus); err != nil {
		t.Fatal(err)
	}
	assertDocEqual(t, "catalog", catalog, s.Declared(store.DeclaredPlugins)["files"])
	assertDocEqual(t, "focus-keymap", focus, s.Declared(store.DeclaredFocusKeymap)["files"])

	// Replacing a declaration leaves no residue from the earlier one.
	replaced := specWithSlice("files")
	replaced.SettingsDeclared = nil
	catalog2, _ := replaced.CatalogJSON()
	if err := declarer.Declare("files", catalog2, focus); err != nil {
		t.Fatal(err)
	}
	assertDocEqual(t, "replaced catalog", catalog2, s.Declared(store.DeclaredPlugins)["files"])

	declarer.Undeclare("files")
	if docs := s.Declared(store.DeclaredPlugins); len(docs) != 0 {
		t.Fatal("undeclare left plugins", docs)
	}
	if docs := s.Declared(store.DeclaredFocusKeymap); len(docs) != 0 {
		t.Fatal("undeclare left focus-keymap", docs)
	}

	if err := declarer.Declare("files", json.RawMessage("not json"), focus); err == nil {
		t.Fatal("invalid catalog accepted")
	}
}

// The wrapper-to-husk.pods status vocabulary: the four liveness values are the
// four PodStatus values.
func TestLivenessVocabularyPinned(t *testing.T) {
	pin := map[wrapper.Liveness]contracts.PodStatus{
		wrapper.Starting: contracts.PodStarting,
		wrapper.Live:     contracts.PodLive,
		wrapper.Exited:   contracts.PodExited,
		wrapper.Failed:   contracts.PodFailed,
	}
	if len(pin) != 4 {
		t.Fatal("liveness vocabulary changed", pin)
	}
	for w, p := range pin {
		if string(w) != string(p) {
			t.Fatalf("status mismatch %q != %q", w, p)
		}
	}
}

// Criterion 4: the whole launch with real State, Store and Services
// coordinator seams and a fake WrapperStarter. Two plugins launch through one
// reload; declarations are visible to Services, the slice is registered with
// the Pod as owner, and husk.pods ends live.
func TestLaunchIntegrationRealSeams(t *testing.T) {
	st := openStore(t)
	tree := state.New(state.Options{})
	clk := fakeClock{now: time.Unix(0, 0)}
	svc := newServiceStack(t, st, tree, clk)
	co := svc.runtime.Coordinator

	registry, err := launch.NewPodRegistry(tree)
	if err != nil {
		t.Fatal(err)
	}
	rec := fakes.NewRecorder()
	starter := fakes.NewWrapper(rec)
	opening := []protocol.Message{{Method: protocol.MethodThemeChanged}}
	_, h := newRecordedLoop(t.Context(), registry, launch.Preparer{
		Catalog: launch.CatalogDeclarer{Store: st},
		Slices:  launch.SliceRegistrar{State: tree},
		Reload:  launch.Reloader{Coordinator: co},
	}, starter, opening)

	settingsBefore := svc.runtime.Settings.Generation()
	secretsBefore := svc.runtime.Secrets.Generation()

	if _, err := h.Add(specWithSlice("files")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Add(spec("notes")); err != nil {
		t.Fatal(err)
	}
	cmd := h.Prepare()
	prepared := cmd().(pods.PreparedMsg)
	if len(prepared.Preparation.Failed) != 0 || prepared.Preparation.ReloadError != nil {
		t.Fatalf("preparation problems: %+v / %v", prepared.Preparation.Failed, prepared.Preparation.ReloadError)
	}
	if !h.Handle(prepared) {
		t.Fatal("unhandled preparation")
	}
	svc.drainPosts()

	// Declared documents serve the manifest catalogs.
	files := specWithSlice("files")
	catalog, _ := files.CatalogJSON()
	focus, _ := files.FocusKeymapJSON()
	assertDocEqual(t, "catalog", catalog, st.Declared(store.DeclaredPlugins)["files"])
	assertDocEqual(t, "focus-keymap", focus, st.Declared(store.DeclaredFocusKeymap)["files"])

	// The plugin slice is registered with the plugin as owner, value {}.
	entry, ok := tree.Snapshot().Slices["files.state"]
	if !ok {
		t.Fatal("plugin slice missing")
	}
	if entry.Owner != "files" || string(entry.Value) != "{}" {
		t.Fatalf("slice entry owner=%q value=%s", entry.Owner, entry.Value)
	}

	// The reload served the declarations: settings and secrets rebuilt.
	if svc.runtime.Settings.Generation() == settingsBefore || svc.runtime.Secrets.Generation() == secretsBefore {
		t.Fatal("reload did not rebuild settings and secrets")
	}
	value, layer, err := svc.runtime.Settings.Get(c.Caller{Kind: "plugin", PluginID: "files"}, "plugins.files.enabled")
	if err != nil || value != false {
		t.Fatalf("declared plugin setting: %v %v %v", value, layer, err)
	}

	// husk.pods holds live entries for both plugins.
	var podsValue struct {
		Plugins map[string]map[string]any `json:"plugins"`
	}
	if err := json.Unmarshal(tree.Snapshot().Slices[state.PodsNamespace].Value, &podsValue); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"files", "notes"} {
		if podsValue.Plugins[id]["status"] != "live" {
			t.Fatalf("%s entry %v", id, podsValue.Plugins[id])
		}
	}

	// The fake starter started both plugins with the opening queue.
	var startedCount int
	for _, call := range rec.Calls() {
		if call.Method == "Wrapper.Start" {
			startedCount++
			if call.Args[1] != len(opening) {
				t.Fatalf("start without opening: %v", call.Args)
			}
		}
	}
	if startedCount != 2 {
		t.Fatal("starter calls", startedCount)
	}
	// Secret requests live in the declared catalog: the launch path hands the
	// Wrapper nothing but the opening queue; grants flow through the
	// reload-driven services, never through process state.
	if catalogDoc := st.Declared(store.DeclaredPlugins)["files"]; catalogDoc["secret_requests"] == nil {
		t.Fatal("secret requests not declared")
	}
}

// Criterion 3 (relaunch half in this unit; occupant cleanup belongs to the
// host root): after a crash only State is lost. The same Store re-declares
// atomically (no residue), a fresh State re-registers the slice, and the
// plugin ends live again with its new declaration served.
func TestRelaunchRedeclaresAndReregisters(t *testing.T) {
	st := openStore(t)

	first := state.New(state.Options{})
	svc1 := newServiceStack(t, st, first, fakeClock{now: time.Unix(0, 0)})
	registry1, err := launch.NewPodRegistry(first)
	if err != nil {
		t.Fatal(err)
	}
	rec := fakes.NewRecorder()
	_, h1 := newRecordedLoop(t.Context(), registry1, launch.Preparer{
		Catalog: launch.CatalogDeclarer{Store: st},
		Slices:  launch.SliceRegistrar{State: first},
		Reload:  launch.Reloader{Coordinator: svc1.runtime.Coordinator},
	}, fakes.NewWrapper(rec), nil)
	if _, err := h1.Add(specWithSlice("files")); err != nil {
		t.Fatal(err)
	}
	if !h1.Handle(h1.Prepare()()) {
		t.Fatal("first launch failed")
	}
	if len(st.Declared(store.DeclaredPlugins)) != 1 {
		t.Fatal("first launch declared", st.Declared(store.DeclaredPlugins))
	}

	// Crash: State (and everything on it) is gone; the Store survives.
	svc1.runtime.Close()

	second := state.New(state.Options{})
	svc2 := newServiceStack(t, st, second, fakeClock{now: time.Unix(1, 0)})
	registry2, err := launch.NewPodRegistry(second)
	if err != nil {
		t.Fatal(err)
	}
	_, h2 := newRecordedLoop(t.Context(), registry2, launch.Preparer{
		Catalog: launch.CatalogDeclarer{Store: st},
		Slices:  launch.SliceRegistrar{State: second},
		Reload:  launch.Reloader{Coordinator: svc2.runtime.Coordinator},
	}, fakes.NewWrapper(rec), nil)
	redeclared := specWithSlice("files")
	redeclared.SettingsDeclared = append(redeclared.SettingsDeclared,
		manifest.SettingDecl{Key: "plugins.files.verbose", Type: "bool", Default: json.RawMessage(`true`)})
	if _, err := h2.Add(redeclared); err != nil {
		t.Fatal(err)
	}
	if !h2.Handle(h2.Prepare()()) {
		t.Fatal("relaunch failed")
	}
	svc2.drainPosts()

	// One declared document, carrying the new declaration; the slice is
	// registered on the fresh State with the Pod as owner; live again.
	docs := st.Declared(store.DeclaredPlugins)
	if len(docs) != 1 {
		t.Fatal("relaunch declared", docs)
	}
	assertDocEqual(t, "redeclared catalog", mustCatalog(t, redeclared), docs["files"])
	entry, ok := second.Snapshot().Slices["files.state"]
	if !ok || entry.Owner != "files" {
		t.Fatalf("slice after relaunch %+v", entry)
	}
	var podsValue struct {
		Plugins map[string]map[string]any `json:"plugins"`
	}
	if err := json.Unmarshal(second.Snapshot().Slices[state.PodsNamespace].Value, &podsValue); err != nil {
		t.Fatal(err)
	}
	if podsValue.Plugins["files"]["status"] != "live" {
		t.Fatal("relaunch not live", podsValue.Plugins)
	}
	value, _, err := svc2.runtime.Settings.Get(c.Caller{Kind: "plugin", PluginID: "files"}, "plugins.files.verbose")
	if err != nil || value != true {
		t.Fatalf("redeclared setting: %v %v", value, err)
	}
}

// Criterion 4: a secret request is a declaration — name, purpose, required —
// with no value field anywhere, so the launch path cannot carry secret
// material into the Wrapper, the spec, or the environment. The wrapper env is
// pinned in the wrapper's own tests to inherited-minus-HUSK_* plus HUSK_*
// runtime variables only.
func TestSecretsNeverReachTheProcessEnvironment(t *testing.T) {
	for _, req := range specWithSlice("files").SecretRequests {
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		for key := range fields {
			switch key {
			case "name", "purpose", "required":
			default:
				t.Fatalf("secret request carries %q; declarations only", key)
			}
		}
	}
}

func mustCatalog(t *testing.T, spec manifest.Spec) json.RawMessage {
	t.Helper()
	catalog, err := spec.CatalogJSON()
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// TestImportsStayWithinAllowList keeps the package on the launch seam
// surfaces only: no Stem engines, no Server, no composition root.
func TestImportsStayWithinAllowList(t *testing.T) {
	allowed := func(p string) bool {
		if !strings.Contains(p, ".") { // standard library
			return true
		}
		for _, ok := range []string{
			"charm.land/bubbletea/v2", // PostFn delivers tea.Msg workers
			"github.com/hyperlab-sh/husk/pkg/protocol",
			"github.com/hyperlab-sh/husk/internal/host/contracts",
			"github.com/hyperlab-sh/husk/internal/host/contracts/fakes",
			"github.com/hyperlab-sh/husk/internal/host/ports/launch",
			"github.com/hyperlab-sh/husk/internal/pods",
			"github.com/hyperlab-sh/husk/internal/pods/manifest",
			"github.com/hyperlab-sh/husk/internal/pods/wrapper",
			"github.com/hyperlab-sh/husk/internal/services",
			"github.com/hyperlab-sh/husk/internal/services/contracts",
			"github.com/hyperlab-sh/husk/internal/services/keybinding",
			"github.com/hyperlab-sh/husk/internal/services/profile",
			"github.com/hyperlab-sh/husk/internal/services/secrets",
			"github.com/hyperlab-sh/husk/internal/services/settings",
			"github.com/hyperlab-sh/husk/internal/services/theme",
			"github.com/hyperlab-sh/husk/internal/substrate/state",
			"github.com/hyperlab-sh/husk/internal/substrate/store",
		} {
			if p == ok {
				return true
			}
		}
		return false
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !allowed(p) {
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
}

// serviceStack assembles the real five Services and coordinator over the
// Store reader and a State tree via services.New, the way the composition
// root does, plus a drainable posts queue standing in for the host loop.
type serviceStack struct {
	runtime *services.Runtime
	posts   chan func()
}

func newServiceStack(t *testing.T, st *store.Store, tree *state.State, clk fakeClock) *serviceStack {
	t.Helper()
	posts := make(chan func(), 64)
	post := func(f func()) bool {
		select {
		case posts <- f:
			return true
		default:
			return false
		}
	}
	crypto, err := st.Secrets("husk-secrets")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := services.New(services.RuntimeOptions{
		Store:   st,
		State:   tree,
		Post:    post,
		Clock:   clk,
		Crypto:  crypto,
		Context: t.Context(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return &serviceStack{runtime: rt, posts: posts}
}

// drainPosts runs everything the services posted, as the host loop would.
func (s *serviceStack) drainPosts() {
	for {
		select {
		case f := <-s.posts:
			f()
		default:
			return
		}
	}
}

// openStore builds a real Store over private temporary XDG directories with a
// frozen clock; nothing touches the user's home or runtime.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	root := t.TempDir()
	runtime := filepath.Join(root, "runtime")
	if err := os.MkdirAll(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	posts := make(chan func(), 64)
	s, err := store.Open(store.Options{
		Instance: store.InstanceRef{ID: "husk-test", Named: true},
		Clock:    fakeClock{now: time.Unix(0, 0)},
		Env: store.Env{
			LookupEnv: func(k string) (string, bool) {
				switch k {
				case "XDG_CONFIG_HOME":
					return filepath.Join(root, "config"), true
				case "XDG_STATE_HOME":
					return filepath.Join(root, "state"), true
				case "XDG_DATA_HOME":
					return filepath.Join(root, "data"), true
				}
				return "", false
			},
			HomeDir:    func() (string, error) { return root, nil },
			RuntimeDir: func() (string, error) { return runtime, nil },
		},
		Post: func(f func()) bool {
			select {
			case posts <- f:
				return true
			default:
				return false
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func assertDocEqual(t *testing.T, what string, raw json.RawMessage, doc store.Doc) {
	t.Helper()
	var want store.Doc
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&want); err != nil {
		t.Fatal(what, err)
	}
	if !reflect.DeepEqual(want, doc) {
		t.Fatalf("%s mismatch\n want %v\n got  %v", what, want, doc)
	}
}
