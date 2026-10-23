package services

import (
	"context"
	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/services/secrets"
	"github.com/hyperlab-sh/husk/internal/services/settings"
	"github.com/hyperlab-sh/husk/internal/substrate/state"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"strings"
	"testing"
)

func TestSchemaAuthorityDefaultsAndNewRestartPlugin(t *testing.T) {
	for _, entry := range []settings.Entry{{Key: "theme.name", Type: "string", Default: "overwrite", Owner: "husk", Apply: "live"}, {Key: "dist.bad", Type: "string", Default: "x", Owner: "unknown", Apply: "live"}, {Key: "plugins.foreign.key", Type: "bool", Default: true, Owner: "dist", Apply: "live"}} {
		s, err := settings.New(settings.Options{Schema: []settings.Entry{entry}})
		if err == nil || s != nil || !strings.Contains(err.Error(), entry.Key) {
			t.Fatal("schema authority accepted or entry not named", entry, err)
		}
	}
	s := newSet(settings.Options{})
	apply(t, s, input(`{}`))
	in := catalog("new")
	in.Declared[store.DeclaredPlugins]["new"]["settings_declared"] = []any{map[string]any{"key": "plugins.new.enabled", "type": "bool", "default": false, "apply": "restart"}}
	in.Doc.Doc = doc(`{"plugins":{"new":{"enabled":true}}}`)
	apply(t, s, in)
	if !s.Snapshot().Bool("plugins.new.enabled") {
		t.Fatal("new plugin treated as previously initialized restart key")
	}
}
func TestKeymapKeepsLastGoodSemanticDocument(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1,"bind":[{"key":"g","action":"husk.home"}]}`)}}
	apply(t, kb, in)
	in.Layers[0].Doc = doc(`{"version":2,"bind":[]}`)
	report := apply(t, kb, in)
	if len(report.Problems) == 0 {
		t.Fatal("no unsupported-version problem")
	}
	match := kb.Resolve(keybinding.KeyContext{}, []string{"g"})
	if match.Outcome != keybinding.Match || match.Binding.Action != "husk.home" {
		t.Fatal("last good keymap lost")
	}
}
func TestAcceptedDialogRemainsPendingUntilLoopRuns(t *testing.T) {
	tree := state.New(state.Options{})
	posts := make(chan func(), 8)
	answers := make(chan bool, 1)
	prompts := make(chan struct{}, 8)
	sec, _ := secrets.New(secrets.Options{State: tree, Clock: clock(), Crypto: &memoryCrypto{data: map[string][]byte{}}, Post: func(f func()) bool { posts <- f; return true }, Prompt: func(secrets.Prompt) <-chan bool { prompts <- struct{}{}; return answers }})
	defer sec.Close()
	apply(t, sec, catalog("files"))
	plugin := Caller{Kind: "plugin", PluginID: "files"}
	sec.Read(plugin, "git.token")
	<-prompts
	answers <- true
	queued := <-posts
	sec.Read(plugin, "git.token")
	if len(prompts) != 0 {
		t.Fatal("duplicate dialog while accepted grant awaits loop")
	}
	queued()
	if grants, _ := sec.Grants(Caller{Kind: "cli"}); len(grants) != 1 {
		t.Fatal("queued grant lost")
	}
}
func TestUserExposureDryPreviewAndNoGenerationChange(t *testing.T) {
	source := &fakeStore{resolved: store.Resolved{Doc: doc(`{"theme":{"name":"Preview"}}`)}}
	co, _ := NewCoordinator(Options{Store: source, Services: fakeServices(), Clock: clock()})
	defer co.Close()
	if _, err := co.Inputs(context.Background(), Caller{PluginID: "plugin"}); err == nil {
		t.Fatal("plugin input/dry validation")
	}
	if _, err := co.ReloadFor(context.Background(), Caller{Kind: "plugin"}); err == nil {
		t.Fatal("plugin reload")
	}
	in, err := co.Inputs(context.Background(), Caller{Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	s := newSet(settings.Options{})
	preview, r, err := s.Preview(context.Background(), Caller{Kind: "cli"}, in)
	if err != nil || !r.OK || s.Generation() != 0 || preview.String("theme.name") != "Preview" || s.Snapshot().String("theme.name") == "Preview" {
		t.Fatal("preview mutated live cache", r, err)
	}
}
