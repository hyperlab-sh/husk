package workbench

import (
	"slices"
	"strings"

	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/keyspelling"
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
	// Extra stands in for a layer above the built-in one (tests use it for
	// plugin-declared or distribution bindings); within a scope it outranks
	// defaultBindings.
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
	{"tab", "husk.focus.next", ""}, {"shift+tab", "husk.focus.next", ""}, {"?", "husk.modifiers.toggle_alt_latch", ""},
	{"esc", "husk.modal.close", string(protocol.SurfaceModal)},
	{"ctrl+]", "husk.capture.release", ""},
}

// ordered lists the bindings strongest first, as the keybinding service ranks
// them: scope first (surface before global), then layer (Extra stands in for
// the plugin-declared layer, which sits above the built-in one), then file
// order.
func (d defaultKeys) ordered() []defaultBinding {
	out := append(slices.Clone(d.Extra), defaultBindings...)
	slices.SortStableFunc(out, func(a, b defaultBinding) int { return scopeOrder(b) - scopeOrder(a) })
	return out
}

// scopeOrder is 1 for a surface-scoped binding and 0 for a global one.
func scopeOrder(b defaultBinding) int {
	if b.focus != "" {
		return 1
	}
	return 0
}

func (d defaultKeys) Resolve(ctx contracts.KeyContext, keys []string) contracts.KeyResolution {
	if ctx.Frozen {
		ctx.Capture = protocol.CaptureNone
		ctx.ModalOpen = false
	}
	if len(keys) != 1 {
		return contracts.KeyResolution{}
	}
	for _, b := range d.ordered() {
		if b.key != keys[0] || b.focus != "" && b.focus != ctx.FocusSurface {
			continue
		}
		if ctx.Frozen && b.action != "husk.quit" {
			continue
		}
		modalClose := b.action == "husk.modal.close" || ctx.ModalAction != "" && b.action == ctx.ModalAction
		release := ctx.Capture != protocol.CaptureNone && b.action == "husk.capture.release"
		if ctx.Capture == protocol.CaptureAll && (b.action == "husk.modal.close" || b.focus == "" && b.action != "husk.capture.release") {
			continue
		}
		if ctx.Capture == protocol.CaptureText && !release && b.focus == "" && keyspelling.IsTextKey(b.key) && !(ctx.ModalOpen && modalClose) {
			continue
		}
		if ctx.ModalOpen && !release && b.focus == "" && b.action != "husk.modal.close" && (ctx.ModalAction == "" || b.action != ctx.ModalAction) {
			continue
		}
		return contracts.KeyResolution{Outcome: contracts.KeyMatch, Binding: &contracts.KeyEntry{Keys: []string{b.key}, Action: b.action, Focus: b.focus}}
	}
	return contracts.KeyResolution{}
}

func (defaultKeys) Hints(contracts.KeyContext) []contracts.KeyHint { return nil }

// KeysFor lists global spellings first, then surface-scoped ones, each group
// strongest layer first.
func (d defaultKeys) KeysFor(action string) []string {
	var out []string
	for _, scoped := range []bool{false, true} {
		for _, b := range d.ordered() {
			if b.action == action && (b.focus != "") == scoped {
				out = append(out, b.key)
			}
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
