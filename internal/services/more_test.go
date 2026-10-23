package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/services/profile"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	stheme "github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSettingsTypesBoundsSnapshotIsolationAndWarnings(t *testing.T) {
	min, max := int64(1), int64(3)
	warnings := 0
	s := newSet(settings.Options{Schema: []settings.Entry{{Key: "dist.n", Type: "int", Default: 2, Min: &min, Max: &max, Owner: "dist", Apply: "live"}, {Key: "dist.mode", Type: "enum", Default: "one", Enum: []string{"one", "two"}, Owner: "dist", Apply: "live"}, {Key: "dist.names", Type: "strings", Default: []string{"a"}, Owner: "dist", Apply: "live"}}, Warn: func(string) { warnings++ }})
	apply(t, s, input(`{}`))
	snap := s.Snapshot()
	names := snap.Strings("dist.names")
	names[0] = "bad"
	if snap.Strings("dist.names")[0] != "a" {
		t.Fatal("mutable snapshot")
	}
	snap.String("missing")
	snap.Int("missing")
	if warnings != 1 {
		t.Fatal("warning not once")
	}
	for _, raw := range []string{`{"dist":{"n":0}}`, `{"dist":{"n":4}}`, `{"dist":{"n":1.5}}`, `{"dist":{"mode":"other"}}`, `{"dist":{"names":[1]}}`} {
		t.Run(raw, func(t *testing.T) {
			candidate, r, err := s.Rebuild(context.Background(), input(raw))
			if err == nil || candidate != nil || len(r.Problems) == 0 || s.Snapshot() != snap {
				t.Fatal("invalid settings published", r)
			}
		})
	}
	apply(t, s, input(`{"dist":{"n":3,"mode":"two","names":["x","y"]}}`))
	if snap.Int("dist.n") != 2 || s.Snapshot().Int("dist.n") != 3 {
		t.Fatal("old snapshot changed")
	}
	r, err := s.Validate(context.Background(), Caller{Kind: "cli"}, input(`{"dist":{"n":1}}`))
	if err != nil || !r.OK || s.Snapshot().Int("dist.n") != 3 {
		t.Fatal("dry validation applied")
	}
}
func TestThemeOverlayDerivationEveryFormatAndHiddenDistributionHatch(t *testing.T) {
	base := palette.Default()
	p, err := palette.Overlay(base, map[string]string{"canvas": "#101018"})
	if err != nil || p.Slot != palette.SlotBackground(p.Canvas) {
		t.Fatal("slot not derived", err)
	}
	p, err = palette.Overlay(base, map[string]string{"canvas": "#101018", "slot_background": "#ABCDEF"})
	if err != nil || p.Slot != "#abcdef" {
		t.Fatal("explicit slot overwritten")
	}
	for _, pair := range palette.ContrastPairs() {
		roles := palette.Roles(base)
		roles[pair.Foreground] = "#000000"
		roles[pair.Background] = "#000000"
		b, _ := json.Marshal(roles)
		var changed palette.Palette
		json.Unmarshal(b, &changed)
		found := false
		for _, v := range palette.CheckContrast(changed) {
			found = found || v.Foreground == pair.Foreground && v.Background == pair.Background
		}
		if !found {
			t.Fatal("missing pair", pair)
		}
	}
	s := newSet(settings.Options{})
	in := input(`{"theme":{"palette":{"ink":"#123","accent":"index"}}}`)
	apply(t, s, in)
	in.Settings = s.Snapshot()
	th := stheme.New()
	_, report, err := th.Rebuild(context.Background(), in)
	if err == nil || len(report.Problems) != 2 {
		t.Fatal("format did not collect all", report)
	}
	in = input(`{"theme":{"allow_low_contrast":true}}`)
	in.Doc.Origins = map[string]store.Layer{"theme.allow_low_contrast": store.LayerUser}
	apply(t, s, in)
	in.Settings = s.Snapshot()
	in.ConfigLayers = []store.LayerDoc{{Layer: store.LayerDistribution, Doc: doc(`{"theme":{"allow_low_contrast":false}}`)}}
	if _, _, err = th.Rebuild(context.Background(), in); err == nil {
		t.Fatal("masked distribution escape hatch allowed")
	}
}
func TestKeybindingAllInvalidRowsAndSequences(t *testing.T) {
	invalid := []string{`{"action":"husk.home"}`, `{"key":"a","keys":["a","b"],"action":"husk.home"}`, `{"keys":["a"],"action":"husk.home"}`, `{"key":"a"}`, `{"key":"a","disable":true,"action":"husk.home"}`, `{"key":"a","plugin":"files","action":"husk.home"}`, `{"key":"a","hint":1,"action":"husk.home"}`, `{"key":"a","disable":"yes","action":"husk.home"}`, `{"key":"a","args":{"nested":{}},"action":"husk.home"}`, `{"key":"a","hint":"` + strings.Repeat("x", 41) + `","action":"husk.home"}`, `{"key":"a","args":{"huge":"` + strings.Repeat("x", 1024) + `"},"action":"husk.home"}`}
	kb := keybinding.New()
	for _, entry := range invalid {
		in := input(`{}`)
		in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1,"bind":[` + entry + `]}`)}}
		r := apply(t, kb, in)
		if len(r.Problems) == 0 {
			t.Fatal("silent invalid entry", entry)
		}
	}
	for _, version := range []string{`2`, `"1"`, `null`} {
		in := input(`{}`)
		in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":` + version + `,"bind":[]}`)}}
		apply(t, kb, in)
		if kb.Listing().Problems[0].Code != "unsupported_version" {
			t.Fatal(kb.Listing().Problems)
		}
	}
	in := input(`{}`)
	in.Layers = []store.LayerDoc{{Layer: store.LayerDistribution, Doc: doc(`{"version":1,"builtin":false,"bind":[{"key":"ctrl+k","action":"husk.home"},{"keys":["ctrl+k","ctrl+s"],"action":"husk.quit"}]}`)}}
	apply(t, kb, in)
	codes := map[string]bool{}
	for _, p := range kb.Listing().Problems {
		codes[p.Code] = true
	}
	if !codes["unreachable"] || !codes["no_exit_binding"] {
		t.Fatal(codes)
	}
	if kb.Resolve(keybinding.KeyContext{}, []string{"q"}).Outcome != keybinding.NoMatch {
		t.Fatal("builtin was not dropped")
	}
}
func TestKeybindingCapsAndScopedDisable(t *testing.T) {
	kb := keybinding.New()
	in := catalog("files")
	entries := []any{}
	for i := 0; i < 130; i++ {
		entries = append(entries, map[string]any{"key": string(rune(0x4e00 + i)), "focus": "stage", "action": "files.run"})
	}
	in.Declared[store.DeclaredKeymap] = map[string]store.Doc{"files": {"version": 1, "bind": entries}}
	apply(t, kb, in)
	tooMany := 0
	for _, p := range kb.Listing().Problems {
		if p.Code == "too_many" {
			tooMany++
		}
	}
	// The 128-entry manifest cap belongs to conversion; the service only
	// applies the per-document limit.
	if tooMany != 0 {
		t.Fatal("service re-applied the manifest cap", tooMany)
	}
	in = input(`{}`)
	for layerIndex, layer := range []store.Layer{store.LayerDistribution, store.LayerUser, store.LayerProfile} {
		bind := []any{}
		for i := 0; i < 2049; i++ {
			bind = append(bind, map[string]any{"keys": []string{"ctrl+k", string(rune(0x4e00 + layerIndex*2049 + i))}, "action": "husk.home"})
		}
		in.Layers = append(in.Layers, store.LayerDoc{Layer: layer, Doc: store.Doc{"version": 1, "bind": bind}})
	}
	apply(t, kb, in)
	effective := 0
	for _, entry := range kb.Listing().Entries {
		if entry.State == "effective" || entry.State == "disabled" {
			effective++
		}
	}
	if effective != 4096 {
		t.Fatal("resolved cap", effective)
	}
	in = input(`{}`)
	in.Layers = []store.LayerDoc{{Layer: store.LayerDistribution, Doc: doc(`{"version":1,"bind":[{"key":"r","focus":"stage","plugin":"files","action":"files.run"}]}`)}, {Layer: store.LayerUser, Doc: doc(`{"version":1,"bind":[{"key":"r","disable":true}]}`)}}
	apply(t, kb, in)
	found := false
	for _, entry := range kb.Listing().Entries {
		if entry.Focus == "stage" {
			found = entry.State == "effective"
		}
	}
	if !found {
		t.Fatal("global disable removed a different triple")
	}
}

type declarationStore struct {
	fakeStore
	mu       sync.Mutex
	declared map[string]store.Doc
}

func (s *declarationStore) Declared(name store.DocName) map[string]store.Doc {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != store.DeclaredPlugins {
		return nil
	}
	return c.Clone(s.declared)
}

type inputService struct {
	fakeService
	observe func(ReloadInput)
}

func (s *inputService) Rebuild(ctx context.Context, in ReloadInput) (Candidate, Report, error) {
	s.observe(in)
	return s.fakeService.Rebuild(ctx, in)
}
func TestLateRequestsShareOneFreshFollowupFlight(t *testing.T) {
	services := fakeServices()
	entered := make(chan struct{})
	gate := make(chan struct{})
	profile := services["profile"].(*fakeService)
	profile.run = func(context.Context) {
		if profile.calls.Load() == 1 {
			close(entered)
			<-gate
		}
	}
	source := &declarationStore{declared: map[string]store.Doc{}}
	observed := make(chan bool, 4)
	services["settings"] = &inputService{fakeService: fakeService{name: "settings"}, observe: func(in ReloadInput) { _, exists := in.Declared[store.DeclaredPlugins]["new"]; observed <- exists }}
	events := make(chan any, 4)
	co, _ := NewCoordinator(Options{Store: source, Services: services, Clock: clock(), Publisher: c.PublishFunc(func(_ string, data any) { events <- data })})
	defer co.Close()
	done := make(chan []Report, 3)
	go func() { r, _ := co.Reload(context.Background(), ReasonCLI); done <- r }()
	<-entered
	source.mu.Lock()
	source.declared["new"] = doc(`{"id":"new"}`)
	source.mu.Unlock()
	watched, parked := parkedCtx(context.Background())
	go func() { r, _ := co.Reload(watched, ReasonPlugin); done <- r }()
	<-parked
	watched, parked = parkedCtx(context.Background())
	go func() { r, _ := co.Reload(watched, ReasonCLI); done <- r }()
	<-parked
	close(gate)
	gens := map[Generation]int{}
	for i := 0; i < 3; i++ {
		r := <-done
		gens[r[0].Gen]++
	}
	if gens[1] != 1 || gens[2] != 2 {
		t.Fatal("late callers did not share one followup", gens)
	}
	if <-observed || !<-observed {
		t.Fatal("follow-up did not capture latest declarations")
	}
	if len(events) != 2 {
		t.Fatal("event per flight", len(events))
	}
}
func TestStoreInputDeadlineAndNoLatePublication(t *testing.T) {
	gate := make(chan struct{})
	source := &fakeStore{block: gate}
	clk := clock()
	services := fakeServices()
	co, _ := NewCoordinator(Options{Store: source, Services: services, Clock: clk})
	defer co.Close()
	done := make(chan struct{})
	go func() {
		_, err := co.Reload(context.Background(), ReasonCLI)
		if err == nil {
			t.Error("no timeout")
		}
		close(done)
	}()
	<-clk.created
	clk.advance(2 * time.Second)
	<-done
	close(gate)
	for _, service := range services {
		if service.Generation() != 0 {
			t.Fatal("input timeout published")
		}
	}
}
func TestRuntimeDefaultsListenerAndSettingsFreeze(t *testing.T) {
	tree := state.New(state.Options{})
	posts := make(chan func(), 8)
	events := make(chan any, 8)
	source := &fakeStore{profiles: []string{"work"}, resolved: store.Resolved{Doc: doc(`{}`)}}
	runtime, err := New(RuntimeOptions{Store: source, State: tree, Post: func(f func()) bool { posts <- f; return true }, Clock: clock(), Crypto: &memoryCrypto{data: map[string][]byte{}}, Publisher: c.PublishFunc(func(_ string, data any) { events <- data })})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	tree.Flush()
	if _, err = runtime.Coordinator.Reload(context.Background(), ReasonCLI); err != nil {
		t.Fatal(err)
	}
	<-events
	used := make(chan error, 1)
	go func() {
		_, _, err := runtime.Profile.Use(context.Background(), Caller{Kind: "cli"}, "work")
		used <- err
	}()
	(<-posts)()
	tree.Flush()
	runtime.Coordinator.Tick()
	if err = <-used; err != nil {
		t.Fatal(err)
	}
	<-events
	if runtime.Profile.Active() != "work" {
		t.Fatal("listener did not reload")
	}
	before := runtime.Theme.Generation()
	source.resolved = store.Resolved{Doc: doc(`{"theme":{"name":5}}`)}
	reports, err := runtime.Coordinator.Reload(context.Background(), ReasonCLI)
	if err == nil || reports[1].Applied || reports[2].Applied || runtime.Theme.Generation() != before || !reports[3].Applied || !reports[4].Applied {
		t.Fatal("partial failure semantics", reports, err)
	}
	newSettings := newSet(settings.Options{})
	_, _, err = newSettings.Rebuild(context.Background(), input(`{"theme":{"name":5}}`))
	if err == nil || newSettings.Snapshot() == nil || newSettings.Snapshot().String("theme.name") == "" {
		t.Fatal("first-load defaults missing")
	}
}
func TestSecretsMissingBrokenMetadataCapsAndPendingDigest(t *testing.T) {
	tree := state.New(state.Options{})
	posts := make(chan func(), 8)
	answers := make(chan bool, 1)
	prompted := make(chan struct{}, 8)
	crypto := &memoryCrypto{data: map[string][]byte{}}
	sec, _ := secrets.New(secrets.Options{State: tree, Clock: clock(), Post: func(f func()) bool { posts <- f; return true }, Crypto: crypto, Prompt: func(secrets.Prompt) <-chan bool { prompted <- struct{}{}; return answers }})
	defer sec.Close()
	in := catalog("files")
	apply(t, sec, in)
	plugin := Caller{Kind: "plugin", PluginID: "files"}
	sec.Read(plugin, "git.token")
	<-prompted
	in.Declared[store.DeclaredPlugins]["files"]["secret_requests"] = []any{map[string]any{"name": "git.token", "purpose": "new purpose"}}
	apply(t, sec, in)
	answers <- true
	(<-posts)()
	if grants, _ := sec.Grants(Caller{Kind: "cli"}); len(grants) != 0 {
		t.Fatal("stale dialog grant applied")
	}
	if err := sec.Set(Caller{Kind: "cli"}, "git.token", make([]byte, 64*1024+1)); err == nil {
		t.Fatal("secret cap")
	}
	if err := sec.Set(Caller{Kind: "cli"}, "Bad Name", nil); err == nil {
		t.Fatal("secret name")
	}
	if _, err := sec.Read(Caller{Kind: "cli"}, "git.token"); err == nil {
		t.Fatal("unset secret")
	}
	crypto.err = errors.New("sensitive failure payload")
	infos, err := sec.Names(Caller{Kind: "cli"})
	if err == nil || len(infos) != 1 || !infos[0].Broken || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("broken metadata", infos, err)
	}
	if _, err = sec.Grants(plugin); err == nil {
		t.Fatal("plugin grants inventory")
	}
}

type sourcedProfiles struct{ fakeStore }

func (*sourcedProfiles) ProfileSources() map[string]store.Layer {
	return map[string]store.Layer{"app": store.LayerDistribution, "work": store.LayerUser}
}
func TestProfileProvenanceAndStartupFlagOverHydration(t *testing.T) {
	source := &sourcedProfiles{fakeStore{profiles: []string{"app", "work"}}}
	flag := "work"
	tree := state.New(state.Options{Hydrator: profileHydrator{}})
	p, err := profile.New(profile.Options{Store: source, State: tree, Post: func(func()) bool { return true }, Flag: &flag})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, p, input(`{}`))
	if name, problems := p.Select(true, nil, []string{"app", "work"}); name != "work" || len(problems) != 0 {
		t.Fatal("flag lost to hydration", name, problems)
	}
	if name, _ := p.Select(false, nil, []string{"app", "work"}); name != "app" {
		t.Fatal("flag applied outside the startup flight", name)
	}
	list := p.List()
	if list[0].Source != "distribution" || list[1].Source != "user" {
		t.Fatal(list)
	}
}

type profileHydrator struct{}

func (profileHydrator) Hydrate(state.Spec) (json.RawMessage, bool) {
	return json.RawMessage(`{"active":"app"}`), true
}
func TestCandidatesDoNotPublishUntilApplyAndDiscardCancellation(t *testing.T) {
	s := newSet(settings.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	candidate, report, err := s.Rebuild(ctx, input(`{}`))
	if err != nil || report.Applied || s.Generation() != 0 {
		t.Fatal(report, err)
	}
	cancel()
	if report = candidate.Apply(); report.Applied || s.Generation() != 0 {
		t.Fatal("cancelled candidate applied")
	}
	one, _, _ := s.Rebuild(context.Background(), input(`{}`))
	two, _, _ := s.Rebuild(context.Background(), input(`{}`))
	if !one.Apply().Applied || two.Apply().Applied {
		t.Fatal("stale candidate overwrote snapshot")
	}
}

var _ = fmt.Sprint

// Past the resolved cap the weakest layers are dropped: the profile layer, the
// strongest, keeps every entry.
func TestKeybindingResolvedCapKeepsStrongestLayers(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	for layerIndex, layer := range []store.Layer{store.LayerDistribution, store.LayerUser, store.LayerProfile} {
		bind := []any{}
		for i := 0; i < 2049; i++ {
			bind = append(bind, map[string]any{"keys": []string{"ctrl+k", string(rune(0x4e00 + layerIndex*2049 + i))}, "action": "husk.home"})
		}
		in.Layers = append(in.Layers, store.LayerDoc{Layer: layer, Doc: store.Doc{"version": 1, "bind": bind}})
	}
	apply(t, kb, in)
	kept := map[keybinding.Layer]int{}
	for _, entry := range kb.Listing().Entries {
		if entry.State == "effective" {
			kept[entry.Source.Layer]++
		}
	}
	// The cap order is scope first, so the built-in surface-scoped modal-close
	// entry outranks every global one; among the globals the weakest go first.
	if kept[keybinding.LayerProfile] != 2048 || kept[keybinding.LayerUser] != 2047 || kept[keybinding.LayerDistribution] != 0 || kept[keybinding.LayerHusk] != 1 {
		t.Errorf("kept per layer = %v, want profile whole, the surface-scoped built-in entry, then the strongest globals", kept)
	}
	dropped := 0
	for _, p := range kb.Listing().Problems {
		if p.Code == "too_many" {
			dropped++
		}
	}
	// One too_many per layer past its 2048 limit, plus one per entry past the cap.
	if total := len(kb.Listing().Entries); dropped != 3+total-4096 {
		t.Errorf("too_many problems = %d, want %d", dropped, 3+total-4096)
	}
}

// A key problem names the config-relative file.
func TestKeybindingProblemsKeepLayerFile(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{
		{Layer: store.LayerUser, File: `C:\cfg\keymap.json`, Doc: doc(`{"version":1,"bind":[{"key":"ctrl+","action":"husk.home"}]}`)},
		{Layer: store.LayerProfile, File: "/cfg/profiles/work/keymap.json", Doc: doc(`{"version":1,"bind":[{"action":"husk.home"}]}`)},
		{Layer: store.LayerDistribution, Doc: doc(`{"version":1,"bind":[{"action":"husk.home"}]}`)},
	}
	apply(t, kb, in)
	files := map[keybinding.Layer]string{}
	for _, p := range kb.Listing().Problems {
		files[p.Source.Layer] = p.Source.File
	}
	want := map[keybinding.Layer]string{
		keybinding.LayerUser:         "keymap.json",
		keybinding.LayerProfile:      "profiles/work/keymap.json",
		keybinding.LayerDistribution: "keymap.json",
	}
	for layer, file := range want {
		if files[layer] != file {
			t.Errorf("%s problem file = %q, want %q (all %v)", layer, files[layer], file, files)
		}
	}
}

// Palette overrides are published in one hex casing whatever the user typed.
func TestThemeOverridesNormalizeHexCasing(t *testing.T) {
	s := newSet(settings.Options{})
	in := input(`{"theme":{"allow_low_contrast":true,"palette":{"accent":"#AaBbCc"}}}`)
	in.Doc.Origins = map[string]store.Layer{"theme.allow_low_contrast": store.LayerUser, "theme.palette.accent": store.LayerUser}
	apply(t, s, in)
	in.Settings = s.Snapshot()
	th := stheme.New()
	apply(t, th, in)
	if got := th.Overrides()["accent"]; got != "#aabbcc" {
		t.Errorf("override = %q, want #aabbcc", got)
	}
}
