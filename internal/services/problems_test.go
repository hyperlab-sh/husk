package services

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/services/profile"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	stheme "github.com/hyperlab-sh/husk/internal/services/theme"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func codes(ps []protocol.Problem) map[string]protocol.Problem {
	out := map[string]protocol.Problem{}
	for _, p := range ps {
		out[p.Code] = p
	}
	return out
}

// keys.list carries the shared Problem unchanged: severity, code and source
// all serialize, and the source keeps its file, line-less shape.
func TestKeysListProblemIsTheSharedProblem(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1,"bind":[{"key":"shift+1","action":"husk.home"}]}`)}}
	apply(t, kb, in)
	var got *protocol.Problem
	for _, p := range kb.Listing().Problems {
		if p.Code == "invalid_key" {
			p := p
			got = &p
		}
	}
	if got == nil || got.Severity != "warning" || got.Layer != "user" || got.Source == nil || got.Source.Layer != "user" {
		t.Fatalf("problem = %+v", got)
	}
	b, _ := json.Marshal(got)
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"severity", "code", "layer", "key", "message", "source"} {
		if _, ok := wire[k]; !ok {
			t.Errorf("wire problem lacks %q: %s", k, b)
		}
	}
}

// Every problem a Service raises has a code.
func TestServiceProblemsCarryCodes(t *testing.T) {
	// settings: invalid value, unknown key.
	s := newSet(settings.Options{})
	in := input(`{"theme":{"allow_low_contrast":"yes"},"nothing":{"here":1}}`)
	_, rep, _ := s.Rebuild(context.Background(), in)
	got := codes(rep.Problems)
	if got["invalid_value"].Severity != "error" {
		t.Fatalf("no invalid_value: %+v", rep.Problems)
	}
	if got["unknown_key"].Code == "" {
		t.Fatalf("no unknown_key: %+v", rep.Problems)
	}
	// settings: restart_required on a second load.
	s = newSet(settings.Options{})
	in = catalog("new")
	in.Declared[store.DeclaredPlugins]["new"]["settings_declared"] = []any{map[string]any{"key": "plugins.new.enabled", "type": "bool", "default": false, "apply": "restart"}}
	apply(t, s, in)
	in.Doc.Doc = doc(`{"plugins":{"new":{"enabled":true}}}`)
	if _, rep, _ := s.Rebuild(context.Background(), in); codes(rep.Problems)["restart_required"].Severity != "warning" {
		t.Fatalf("no restart_required: %+v", rep.Problems)
	}
	// profile: unknown_profile.
	p, _ := profile.New(profile.Options{Store: &fakeStore{}, State: state.New(state.Options{}), Post: func(f func()) bool { f(); return true }})
	ghost := "ghost"
	in = input(`{}`)
	in.Profile = &ghost
	if _, rep, _ := p.Rebuild(context.Background(), in); codes(rep.Problems)["unknown_profile"].Severity != "warning" {
		t.Fatalf("no unknown_profile: %+v", rep.Problems)
	}
}

// A malformed plugin catalog is invalid_plugin_catalog.
func TestInvalidPluginCatalogCode(t *testing.T) {
	in := input(`{}`)
	in.Declared[store.DeclaredPlugins] = map[string]store.Doc{"a": doc(`{"id":"b"}`)}
	_, rep, _ := keybinding.New().Rebuild(context.Background(), in)
	if p := codes(rep.Problems)["invalid_plugin_catalog"]; p.Layer != "plugin:a" || p.Severity != "warning" {
		t.Fatalf("problems = %+v", rep.Problems)
	}
}

type failingService struct{ *fakeService }

func (s failingService) Rebuild(ctx context.Context, in ReloadInput) (Candidate, Report, error) {
	return nil, Report{Service: s.name, Gen: s.Generation(), Problems: []Problem{{Severity: "error", Code: "invalid_value", Message: "bad"}}}, errors.New("invalid")
}

// Theme is skipped with the settings_unchanged warning, which is the only
// applied:false report with no error.
func TestThemeSkippedCarriesSettingsUnchangedWarning(t *testing.T) {
	services := fakeServices()
	services["settings"] = failingService{services["settings"].(*fakeService)}
	co, err := NewCoordinator(Options{Store: &fakeStore{}, Services: services, Clock: clock()})
	if err != nil {
		t.Fatal(err)
	}
	defer co.Close()
	reports, _ := co.Reload(context.Background(), ReasonCLI)
	for _, r := range reports {
		if r.Service != "theme" {
			continue
		}
		if r.Applied || len(r.Problems) != 1 || r.Problems[0].Code != "settings_unchanged" || r.Problems[0].Severity != "warning" || r.Problems[0].Message != "settings unchanged; palette unchanged" {
			t.Fatalf("theme report = %+v", r)
		}
		return
	}
	t.Fatal("no theme report")
}

// Bad secret declarations are dropped with warnings; the rebuild still
// produces a candidate and the good declaration stands.
func TestSecretDeclarationProblemsAreWarnings(t *testing.T) {
	s, _ := secrets.New(secrets.Options{State: state.New(state.Options{}), Post: func(f func()) bool { f(); return true }, Clock: clock(), Crypto: &memoryCrypto{data: map[string][]byte{}}})
	defer s.Close()
	in := input(`{}`)
	in.Declared[store.DeclaredPlugins] = map[string]store.Doc{"a": doc(`{"id":"a","name":"A","secret_requests":[{"name":"Bad Name","purpose":"p"},{"name":"git.token","purpose":"p"},{"name":"git.token","purpose":"p"}]}`)}
	rep := apply(t, s, in)
	got := codes(rep.Problems)
	for _, code := range []string{"invalid_secret_name", "duplicate_secret"} {
		if got[code].Severity != "warning" || got[code].Layer != "plugin:a" {
			t.Errorf("%s = %+v", code, got[code])
		}
	}
	if names, _ := s.Names(Caller{Kind: protocol.CallerCLI}); len(names) != 1 {
		t.Fatalf("names = %+v", names)
	}
}

// A bad color override is invalid_color, an error that blocks the palette.
func TestThemeBadColorIsInvalidColor(t *testing.T) {
	in := input(`{"theme":{"palette":{"ink":"red"}}}`)
	st := newSet(settings.Options{})
	apply(t, st, in)
	in.Settings = st.Snapshot()
	_, rep, err := stheme.New().Rebuild(context.Background(), in)
	if err == nil || codes(rep.Problems)["invalid_color"].Severity != "error" {
		t.Fatalf("err=%v problems=%+v", err, rep.Problems)
	}
}
