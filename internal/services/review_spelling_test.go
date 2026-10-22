package services

import (
	"github.com/hyperlab-sh/husk/internal/services/keybinding"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"strings"
	"testing"
)

func TestReviewKeyLoaderPreservesProducedCharacterHint(t *testing.T) {
	kb := keybinding.New()
	in := input(`{}`)
	in.Layers = []store.LayerDoc{{Layer: store.LayerUser, Doc: doc(`{"version":1,"bind":[{"key":"shift+1","action":"husk.home"},{"key":"ctrl+X","action":"husk.home"}]}`)}}
	apply(t, kb, in)
	found := false
	for _, p := range kb.Listing().Problems {
		if p.Code == "invalid_key" && p.Key == "shift+1" && strings.Contains(p.Message, "!") {
			found = true
		}
	}
	if !found {
		t.Fatal("produced character absent from loader diagnostic", kb.Listing().Problems)
	}
	if kb.Resolve(keybinding.KeyContext{}, []string{"ctrl+x"}).Outcome != keybinding.Match {
		t.Fatal("modified letter was not canonicalized")
	}
}
