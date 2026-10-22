package main

import (
	"slices"
	"strings"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// keySource is the part of contracts.Frontend that answers key questions. The
// model reads it from the attached host; without a host (the demo frames and
// the hermetic golden tests) defaultKeys stands in.
type keySource interface {
	Resolve(ctx contracts.KeyContext, keys []string) contracts.KeyResolution
	Hints(ctx contracts.KeyContext) []contracts.KeyHint
	KeysFor(action string) []string
}

// defaultKeys mirrors the distribution keymap for a model with no host. It
// resolves single keys only and advertises no hints; TestDefaultKeysMatchTheDistributionKeymap
// keeps it in step with the embedded keymap.
type defaultKeys struct {
	// Extra adds bindings after the distribution ones; tests use it to stand in
	// for plugin-declared keymap documents.
	Extra []defaultBinding
}

type defaultBinding struct{ key, action, focus string }

var defaultBindings = []defaultBinding{
	{"q", "husk.quit", ""}, {"ctrl+c", "husk.quit", ""},
	{"f1", "husk.home", ""}, {"esc", "husk.home", ""}, {"alt+1", "husk.home", ""},
	{"ctrl+p", "husk.palette.open", ""}, {":", "husk.palette.open", ""}, {"alt+p", "husk.palette.open", ""},
	{"v", "husk.hotspots.toggle_stage", ""}, {"ctrl+i", "husk.hotspots.toggle_stage", ""}, {"alt+v", "husk.hotspots.toggle_stage", ""},
	{"h", "husk.hotspots.badges", ""},
	{"[", "husk.sidebar.toggle", ""}, {"]", "husk.sidepanel.toggle", ""}, {"/", "husk.docks.cycle", ""},
	{"=", "husk.layout.reset", ""},
	{"<", "husk.sidebar.narrower", ""}, {",", "husk.sidebar.narrower", ""},
	{">", "husk.sidebar.wider", ""}, {".", "husk.sidebar.wider", ""},
	{"tab", "husk.focus.next", ""}, {"?", "husk.modifiers.toggle_alt_latch", ""},
	{"esc", "husk.modal.close", string(protocol.SurfaceModal)},
}

func (d defaultKeys) Resolve(ctx contracts.KeyContext, keys []string) contracts.KeyResolution {
	if len(keys) != 1 {
		return contracts.KeyResolution{}
	}
	var hit *defaultBinding
	all := append(slices.Clone(defaultBindings), d.Extra...)
	for i := range all {
		b := &all[i]
		if b.key != keys[0] || b.focus != "" && b.focus != ctx.FocusSurface {
			continue
		}
		if ctx.Frozen && b.action != "husk.quit" {
			continue
		}
		if ctx.ModalOpen && b.focus == "" && b.action != "husk.modal.close" && (ctx.ModalAction == "" || b.action != ctx.ModalAction) {
			continue
		}
		if hit == nil || hit.focus == "" && b.focus != "" {
			hit = b
		}
	}
	if hit == nil {
		return contracts.KeyResolution{}
	}
	return contracts.KeyResolution{Outcome: contracts.KeyMatch, Binding: &contracts.KeyEntry{Keys: []string{hit.key}, Action: hit.action, Focus: hit.focus}}
}

func (defaultKeys) Hints(contracts.KeyContext) []contracts.KeyHint { return nil }

func (d defaultKeys) KeysFor(action string) []string {
	var out []string
	for _, b := range append(slices.Clone(defaultBindings), d.Extra...) {
		if b.action == action && b.focus == "" {
			out = append(out, b.key)
		}
	}
	return out
}

// firstKey is the strongest spelling of action, or "" when none is bound.
func (m *WorkbenchModel) firstKey(action string) string {
	if ks := m.keys.KeysFor(action); len(ks) > 0 {
		return ks[0]
	}
	return ""
}

// keyLabel is a key spelling as the footer and the rail show it.
func keyLabel(spelling string) string {
	return railShortcutLabel(spelling)
}

// keyHintText joins up to two spellings the way the palette's hint column
// shows them ("f1 / esc").
func keyHintText(spellings []string) string {
	if len(spellings) > 2 {
		spellings = spellings[:2]
	}
	return strings.Join(spellings, " / ")
}
