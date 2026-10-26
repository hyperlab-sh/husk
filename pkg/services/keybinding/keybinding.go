// Package keybinding resolves immutable layered keymaps without reading State.
package keybinding

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/hyperlab-sh/husk/pkg/keyspelling"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

// Layer is one fixed keymap precedence tier.
type Layer = string

const (
	// LayerHusk is the weakest built-in default.
	LayerHusk Layer = "husk"
	// LayerFocusLocal contains plugin-owned surface bindings.
	LayerFocusLocal Layer = "focus-local"
	// LayerDistribution outranks all plugins.
	LayerDistribution Layer = "distribution"
	// LayerUser contains explicit user choices.
	LayerUser Layer = "user"
	// LayerProfile is strongest.
	LayerProfile Layer = "profile"
)

// Source locates one original declaration; it is the shared protocol type.
type Source = protocol.Source

// Binding is an immutable parsed declaration; returned copies are detached.
type Binding struct {
	Keys    []string       `json:"keys"`
	Action  string         `json:"action,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	Hint    string         `json:"hint,omitempty"`
	Focus   string         `json:"focus,omitempty"`
	Plugin  string         `json:"plugin,omitempty"`
	Disable bool           `json:"disable,omitempty"`
	Source  Source         `json:"source"`
}

// KeyContext contains plain focus/overlay facts supplied by Input.
type KeyContext struct {
	FocusSurface string
	FocusOwner   string
	ModalOpen    bool
	ModalAction  string
	// Capture is set while the focused occupant takes text input on
	// FocusSurface: global candidates on a text key are removed.
	Capture protocol.CaptureMode
	Frozen  bool
}

// Outcome describes resolution of the current typed sequence.
type Outcome int

const (
	// NoMatch hands the original key to the occupant.
	NoMatch Outcome = iota
	// Prefix means Input should wait for the next key.
	Prefix
	// Match dispatches the selected action.
	Match
)

// Result describes one lookup.
type Result struct {
	Outcome Outcome
	Binding *Binding
}

// Hint is one effective footer advertisement.
type Hint struct {
	Keys   []string `json:"keys"`
	Action string   `json:"action"`
	Text   string   `json:"hint"`
}

// Problem is the one shared diagnostic type, serialized as is by keys.list.
type Problem = protocol.Problem

// Entry includes a binding's static precedence state.
type Entry struct {
	Binding
	State      string  `json:"state"`
	ShadowedBy *Source `json:"shadowed_by,omitempty"`
	// dropped marks a duplicate, unreachable or cap-dropped entry: it is listed
	// but never resolves. A statically shadowed entry still resolves, because
	// the Frozen and modal filters can leave it as the only candidate.
	dropped bool
}

// Listing includes effective and shadowed declarations and every loader problem.
type Listing struct {
	Entries  []Entry   `json:"entries"`
	Problems []Problem `json:"problems"`
}

// Resolver is the lock-free policy view used by Stem Input.
type Resolver interface {
	Resolve(KeyContext, []string) Result
	Hints(KeyContext) []Hint
	KeysFor(string) []string
	Listing() Listing
}

const (
	// maxLayerEntries is the per-document entry limit.
	maxLayerEntries = 2048
	// maxEffective is the limit on effective entries over all layers.
	maxEffective = 4096
)

// layerFile is the config-relative file of a layer: Store names it, and the
// conventional name serves injected documents and parse problems.
func layerFile(file string, layer store.Layer, profile *string) string {
	if file != "" {
		return file
	}
	file = "keymap.json"
	if layer == store.LayerProfile {
		name := ""
		if profile != nil {
			name = *profile
		}
		file = "profiles/" + name + "/" + file
	}
	return file
}

// sourceName renders a Source for a problem message.
func sourceName(s Source) string {
	name := string(s.Layer)
	if s.Plugin != "" {
		name += " " + s.Plugin
	}
	if s.File != "" {
		name += " " + s.File
	}
	return fmt.Sprintf("%s #%d", name, s.Order)
}

// capLayerRank orders layers for the 4096 cap, strongest first. Plugin
// (focus-local) entries rank last, so a flood of them is dropped before any
// configuration layer or built-in binding.
func capLayerRank(l Layer) int {
	if l == LayerFocusLocal {
		return -1
	}
	return rank(l)
}

// capOrder keeps entries layer first, then by the static order of loader step
// 5 within a layer: scope, plugin condition, declaring plugin, file order.
func capOrder(a, b Binding) bool {
	if capLayerRank(a.Source.Layer) != capLayerRank(b.Source.Layer) {
		return capLayerRank(a.Source.Layer) > capLayerRank(b.Source.Layer)
	}
	return stronger(a, b)
}

//go:embed keymap.json
var builtin []byte

type snapshot struct {
	gen          c.Generation
	listing      Listing
	index        map[string][]Binding
	documents    map[string]store.Doc
	actionOwners map[string]string
	railItems    map[string]bool
}

// Service swaps a complete key index at publication.
type Service struct{ current atomic.Pointer[snapshot] }

// New serves the embedded map before first external configuration is attempted.
func New() *Service {
	s := &Service{}
	s.current.Store(&snapshot{index: map[string][]Binding{}})
	candidate, _, _ := s.Rebuild(context.Background(), c.ReloadInput{})
	candidate.Apply()
	initial := *s.current.Load()
	initial.gen = 0
	s.current.Store(&initial)
	return s
}

// Generation is the active keymap generation.
func (s *Service) Generation() c.Generation { return s.current.Load().gen }
func rank(l Layer) int {
	switch l {
	case LayerProfile:
		return 4
	case LayerUser:
		return 3
	case LayerDistribution:
		return 2
	case LayerFocusLocal:
		return 1
	}
	return 0
}
func scopeRank(b Binding) int {
	if b.Focus != "" {
		return 1
	}
	return 0
}
func conditionRank(b Binding) int {
	if b.Plugin != "" {
		return 1
	}
	return 0
}

// outranks is steps 2 to 4 of "Which binding wins": scope, then layer, then
// whether `plugin` is set. File order is deliberately absent; see stronger.
func outranks(a, b Binding) bool {
	if scopeRank(a) != scopeRank(b) {
		return scopeRank(a) > scopeRank(b)
	}
	if rank(a.Source.Layer) != rank(b.Source.Layer) {
		return rank(a.Source.Layer) > rank(b.Source.Layer)
	}
	return conditionRank(a) > conditionRank(b)
}

// stronger is the total order Resolve ranks candidates by: outranks, then file
// order (declaring plugin, then index in its document).
func stronger(a, b Binding) bool {
	if outranks(a, b) != outranks(b, a) {
		return outranks(a, b)
	}
	if a.Source.Plugin != b.Source.Plugin {
		return a.Source.Plugin < b.Source.Plugin
	}
	if a.Source.Order != b.Source.Order {
		return a.Source.Order < b.Source.Order
	}
	return a.Focus+"\x00"+a.Plugin+"\x00"+sequence(a.Keys)+"\x00"+a.Action < b.Focus+"\x00"+b.Plugin+"\x00"+sequence(b.Keys)+"\x00"+b.Action
}
func sequence(keys []string) string { b, _ := json.Marshal(keys); return string(b) }
func triple(b Binding) string       { return sequence(b.Keys) + "\x00" + b.Focus + "\x00" + b.Plugin }
func begins(a, b []string) bool {
	if len(a) < len(b) {
		return false
	}
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func scope(b Binding, ctx KeyContext) bool {
	return b.Focus == "" || (b.Focus == ctx.FocusSurface || ctx.ModalOpen && b.Focus == string(protocol.SurfaceModal)) && (b.Plugin == "" || b.Plugin == ctx.FocusOwner)
}

// shadows is the static rule of "husk keys": a shadows b when their sequences
// are equal or one begins the other, a outranks b, and every context that
// matches b also matches a. A tombstone longer than b is a candidate only for
// its own sequence, so it shadows nothing shorter (D92).
func shadows(a, b Binding) bool {
	if a.Disable && len(a.Keys) > len(b.Keys) {
		return false
	}
	if !begins(a.Keys, b.Keys) && !begins(b.Keys, a.Keys) {
		return false
	}
	return outranks(a, b) && a.Focus == b.Focus && (a.Plugin == "" || a.Plugin == b.Plugin)
}

// Resolve is pure and never locks or calls another module.
func (s *Service) Resolve(ctx KeyContext, keys []string) Result {
	if ctx.Frozen {
		ctx.Capture = protocol.CaptureNone
		ctx.ModalOpen = false
	}
	if len(keys) == 0 {
		return Result{}
	}
	for _, b := range s.current.Load().index[keys[0]] {
		if !begins(b.Keys, keys) || !scope(b, ctx) || ctx.Frozen && !b.Disable && b.Action != "husk.quit" {
			continue
		}
		// A tombstone unbinds exactly the sequence it names, never a prefix of it.
		if b.Disable && len(b.Keys) != len(keys) {
			continue
		}
		release := ctx.Capture != protocol.CaptureNone && b.Action == "husk.capture.release"
		if ctx.Capture == protocol.CaptureAll && !b.Disable && (b.Action == "husk.modal.close" || b.Focus == "" && b.Action != "husk.capture.release") {
			continue
		}
		if ctx.ModalOpen && !release && !b.Disable && b.Focus != "modal-surface" && b.Focus != ctx.FocusSurface && !(b.Focus == "" && (b.Action == "husk.modal.close" || ctx.ModalAction != "" && b.Action == ctx.ModalAction)) {
			continue
		}
		// Capture keeps plain typing: a global text-key candidate is removed
		// in every layer, except the modal's close keys while a modal is open.
		// Tombstones stay, as under the other filters.
		if ctx.Capture == protocol.CaptureText && !release && !b.Disable && b.Focus == "" && keyspelling.IsTextKey(b.Keys[0]) &&
			!(ctx.ModalOpen && (b.Action == "husk.modal.close" || ctx.ModalAction != "" && b.Action == ctx.ModalAction)) {
			continue
		}
		if b.Disable {
			return Result{}
		}
		if len(b.Keys) > len(keys) {
			return Result{Outcome: Prefix}
		}
		copy := c.Clone(b)
		return Result{Match, &copy}
	}
	return Result{}
}

// Hints returns at most four unshadowed, context-effective hints.
func (s *Service) Hints(ctx KeyContext) []Hint {
	var out []Hint
	seen := map[string]bool{}
	var all []Binding
	for _, e := range s.current.Load().listing.Entries {
		if e.State == "effective" && !e.Disable {
			all = append(all, e.Binding)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return stronger(all[i], all[j]) })
	for _, b := range all {
		if b.Hint == "" || seen[triple(b)] {
			continue
		}
		// Every proper prefix must wait for the next key; a match or a tombstone
		// there ends the sequence before this binding.
		reachable := true
		for n := 1; n < len(b.Keys); n++ {
			reachable = reachable && s.Resolve(ctx, b.Keys[:n]).Outcome == Prefix
		}
		r := s.Resolve(ctx, b.Keys)
		if reachable && r.Outcome == Match && r.Binding.Action == b.Action && r.Binding.Source == b.Source {
			out = append(out, Hint{append([]string(nil), b.Keys...), b.Action, b.Hint})
			seen[triple(b)] = true
			if len(out) == 4 {
				break
			}
		}
	}
	return out
}

// KeysFor returns effective spellings, global entries first and then
// surface-scoped; within each group the stronger layer first, then file order
// (sequences joined for display).
func (s *Service) KeysFor(action string) []string {
	var bs []Binding
	for _, e := range s.current.Load().listing.Entries {
		if e.State == "effective" && e.Action == action {
			bs = append(bs, e.Binding)
		}
	}
	sort.SliceStable(bs, func(i, j int) bool {
		a, b := bs[i], bs[j]
		if scopeRank(a) != scopeRank(b) {
			return scopeRank(a) < scopeRank(b)
		}
		if rank(a.Source.Layer) != rank(b.Source.Layer) {
			return rank(a.Source.Layer) > rank(b.Source.Layer)
		}
		if a.Source.Plugin != b.Source.Plugin {
			return a.Source.Plugin < b.Source.Plugin
		}
		return a.Source.Order < b.Source.Order
	})
	var out []string
	for _, b := range bs {
		out = append(out, strings.Join(b.Keys, " "))
	}
	return out
}

// Listing returns a deep copy for CLI adapters to join with liveness/catalog data.
func (s *Service) Listing() Listing { return c.Clone(s.current.Load().listing) }

// ListFor gives plugins their own entries and problems plus effective host and
// declared rail-item bindings. User callers receive the full listing.
func (s *Service) ListFor(caller c.Caller) (Listing, error) {
	if err := c.CheckCaller(caller); err != nil {
		return Listing{}, err
	}
	snap := s.current.Load()
	out := c.Clone(snap.listing)
	if c.IsUser(caller) {
		return out, nil
	}
	var entries []Entry
	for _, e := range out.Entries {
		if e.Plugin == caller.PluginID || snap.actionOwners[e.Action] == caller.PluginID ||
			e.State == "effective" && (strings.HasPrefix(e.Action, "husk.") || snap.railItems[e.Action]) {
			entries = append(entries, e)
		}
	}
	out.Entries = entries
	var ps []Problem
	for _, p := range out.Problems {
		if p.Source != nil && p.Source.Plugin == caller.PluginID {
			ps = append(ps, p)
		}
	}
	out.Problems = ps
	return out, nil
}
func validVersion(value any) bool {
	switch v := value.(type) {
	case json.Number:
		return v.String() == "1"
	case int:
		return v == 1
	case int64:
		return v == 1
	case float64:
		return v == 1
	}
	return false
}

// layerProblem is the file-level code for doc, the key the problem names and
// the cause.
func layerProblem(doc store.Doc) (code, key, message string) {
	if !validVersion(doc["version"]) {
		return "unsupported_version", "", `"version" must be the integer 1`
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return "invalid_entry", "", "the document cannot be serialized: " + err.Error()
	}
	if len(encoded) > 256*1024 {
		return "too_large", "", "the document is larger than 256 KiB"
	}
	if doc["bind"] == nil {
		return "invalid_entry", "bind", `"bind" is required and must be an array of entries`
	}
	var entries []map[string]any
	if c.Decode(doc["bind"], &entries) != nil {
		return "invalid_entry", "bind", `"bind" must be an array of objects`
	}
	return "", "", ""
}

// entryProblem is why parse dropped one entry.
type entryProblem struct{ code, message string }

func invalid(format string, args ...any) *entryProblem {
	return &entryProblem{"invalid_entry", fmt.Sprintf(format, args...)}
}

func parse(raw map[string]any, src Source) (Binding, *entryProblem) {
	b := Binding{Source: src}
	for _, field := range []string{"action", "focus", "plugin", "hint"} {
		if value, exists := raw[field]; exists {
			if _, ok := value.(string); !ok {
				return b, invalid("%q must be a string, got %s", field, jsonType(value))
			}
		}
	}
	if value, exists := raw["disable"]; exists {
		if _, ok := value.(bool); !ok {
			return b, invalid(`"disable" must be a boolean, got %s`, jsonType(value))
		}
	}
	key, hasKey := raw["key"]
	keys, hasKeys := raw["keys"]
	if hasKey == hasKeys {
		return b, invalid(`exactly one of "key" or "keys" is required, got %s`, map[bool]string{true: "both", false: "neither"}[hasKey])
	}
	var sequence []string
	if hasKey {
		str, ok := key.(string)
		if !ok {
			return b, invalid(`"key" must be a string, got %s`, jsonType(key))
		}
		sequence = []string{str}
	} else {
		if c.Decode(keys, &sequence) != nil {
			return b, invalid(`"keys" must be an array of strings, got %s`, jsonType(keys))
		}
		if len(sequence) < 2 || len(sequence) > 4 {
			return b, invalid(`"keys" must hold 2 to 4 keys, got %d`, len(sequence))
		}
	}
	for _, key := range sequence {
		n, err := keyspelling.Normalize(key)
		if err != nil {
			return b, &entryProblem{"invalid_key", err.Error()}
		}
		b.Keys = append(b.Keys, n)
	}
	b.Action, _ = raw["action"].(string)
	b.Focus, _ = raw["focus"].(string)
	b.Plugin, _ = raw["plugin"].(string)
	b.Hint, _ = raw["hint"].(string)
	b.Disable, _ = raw["disable"].(bool)
	if b.Plugin != "" && b.Focus == "" {
		return b, invalid(`"plugin" requires "focus"`)
	}
	if n := utf8.RuneCountInString(b.Hint); n > 40 {
		return b, invalid(`"hint" must be at most 40 runes, got %d`, n)
	}
	if b.Disable {
		for _, field := range []string{"action", "args", "hint"} {
			if raw[field] != nil {
				return b, invalid(`%q must be absent when "disable" is true`, field)
			}
		}
	} else if b.Action == "" {
		return b, invalid(`"action" is required unless "disable" is true`)
	} else if !strings.Contains(b.Action, ".") {
		return b, invalid(`"action" must contain a ".", got %q`, b.Action)
	}
	if args, ok := raw["args"]; ok {
		m, ok := args.(map[string]any)
		if !ok {
			return b, invalid(`"args" must be an object, got %s`, jsonType(args))
		}
		names := make([]string, 0, len(m))
		for name := range m {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			switch m[name].(type) {
			case string, bool, json.Number, float64, int, int64:
			default:
				return b, invalid(`"args" value %q must be a string, number or boolean, got %s`, name, jsonType(m[name]))
			}
		}
		encoded, err := json.Marshal(m)
		if err != nil {
			return b, invalid(`"args" cannot be serialized: %v`, err)
		}
		if len(encoded) > 1024 {
			return b, invalid(`"args" must be at most 1 KiB serialized, got %d bytes`, len(encoded))
		}
		b.Args = c.Clone(m)
	}
	return b, nil
}

// jsonType names the JSON type of a decoded value for a problem message.
func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case json.Number, float64, int, int64:
		return "a number"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

func invalidKeyDetail(raw map[string]any) (string, string) {
	var keys []string
	if key, ok := raw["key"].(string); ok {
		keys = []string{key}
	} else {
		c.Decode(raw["keys"], &keys)
	}
	for _, key := range keys {
		if _, err := keyspelling.Normalize(key); err != nil {
			return key, err.Error()
		}
	}
	return "", ""
}

// Rebuild parses every layer and prepares a complete immutable index.
func (s *Service) Rebuild(ctx context.Context, in c.ReloadInput) (c.Candidate, c.Report, error) {
	old := s.current.Load()
	r := c.Report{Service: "keybinding", Gen: old.gen}
	next := &snapshot{gen: old.gen + 1, index: map[string][]Binding{}, documents: map[string]store.Doc{}, actionOwners: map[string]string{}, railItems: map[string]bool{}}
	issue := func(code string, src Source, key, message string) {
		layer := src.Layer
		if src.Plugin != "" {
			layer = "plugin:" + src.Plugin
		}
		source := src
		next.listing.Problems = append(next.listing.Problems, Problem{Severity: "warning", Code: code, Layer: layer, Key: key, Message: message, Source: &source})
		c.Issue(&r.Problems, "warning", code, layer, key, message)
	}
	catalog, catalogProblems := c.Catalogs(in)
	r.Problems = append(r.Problems, catalogProblems...)
	for owner, plugin := range catalog {
		for _, id := range plugin.RailItems {
			next.railItems[id] = true
		}
		for _, action := range plugin.Actions {
			if previous, exists := next.actionOwners[action.ID]; exists && previous != owner {
				next.actionOwners[action.ID] = ""
			} else {
				next.actionOwners[action.ID] = owner
			}
		}
	}
	var layers []struct {
		doc store.Doc
		src Source
	}
	var built store.Doc
	json.Unmarshal(builtin, &built)
	useBuiltin := true
	// A layer file that failed to parse is absent from in.Layers; its last
	// good document stands in, and the parse problem is reported once.
	present := map[store.Layer]bool{}
	for _, layer := range in.Layers {
		present[layer.Layer] = true
	}
	type missingLayer struct {
		layer store.Layer
		file  string
	}
	var missing []missingLayer
	for _, p := range in.KeymapProblems {
		if p.Code != "invalid_document" || p.Layer == store.LayerInstance {
			continue
		}
		src := Source{Layer: Layer(p.Layer), File: p.File, Line: p.Line}
		if p.Layer != store.LayerDistribution {
			src.File = layerFile("", p.Layer, in.Profile)
		}
		issue("parse_error", src, "", p.Message)
		if !present[p.Layer] {
			present[p.Layer] = true
			missing = append(missing, missingLayer{p.Layer, src.File})
		}
	}
	for _, layer := range in.Layers {
		if layer.Layer == store.LayerDistribution {
			doc := layer.Doc
			if code, _, _ := layerProblem(doc); code != "" {
				doc = old.documents[string(LayerDistribution)]
			}
			if doc["builtin"] == false {
				useBuiltin = false
			}
		}
	}
	for _, m := range missing {
		if m.layer == store.LayerDistribution && old.documents[string(LayerDistribution)]["builtin"] == false {
			useBuiltin = false
		}
	}
	if useBuiltin {
		layers = append(layers, struct {
			doc store.Doc
			src Source
		}{built, Source{Layer: LayerHusk, File: "builtin"}})
	}
	ids := make([]string, 0, len(in.Declared[store.DeclaredKeymap]))
	for id := range in.Declared[store.DeclaredKeymap] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		layers = append(layers, struct {
			doc store.Doc
			src Source
		}{in.Declared[store.DeclaredKeymap][id], Source{Layer: LayerFocusLocal, Plugin: id}})
	}
	for _, l := range in.Layers {
		if l.Layer == store.LayerInstance {
			continue
		}
		layers = append(layers, struct {
			doc store.Doc
			src Source
		}{l.Doc, Source{Layer: Layer(l.Layer), File: layerFile(l.File, l.Layer, in.Profile)}})
	}
	for _, m := range missing {
		layers = append(layers, struct {
			doc store.Doc
			src Source
		}{nil, Source{Layer: Layer(m.layer), File: m.file}})
	}
	seen := map[string]Source{}
	for _, layer := range layers {
		if ctx.Err() != nil {
			return c.ContextError(ctx, r)
		}
		doc, src := layer.doc, layer.src
		cacheKey := string(src.Layer) + src.Plugin
		if src.Layer == LayerProfile {
			identity := src.File
			if in.Profile != nil {
				identity = *in.Profile
			}
			cacheKey += "\x00" + identity
		}
		if doc == nil {
			// The parse problem is already reported above.
			doc = old.documents[cacheKey]
			if doc == nil {
				continue
			}
		} else if code, key, message := layerProblem(doc); code != "" {
			issue(code, src, key, message)
			doc = old.documents[cacheKey]
			if doc == nil {
				continue
			}
		}
		next.documents[cacheKey] = c.Clone(doc)
		if value, exists := doc["builtin"]; exists {
			if src.Layer != LayerDistribution {
				issue("invalid_entry", src, "builtin", `"builtin" is only allowed in the distribution layer`)
			} else if _, ok := value.(bool); !ok {
				issue("invalid_entry", src, "builtin", fmt.Sprintf(`"builtin" must be a boolean, got %s`, jsonType(value)))
			}
		}
		if src.Plugin != "" {
			var imported []struct {
				Index   int    `json:"index"`
				Code    string `json:"code"`
				Message string `json:"message"`
				Key     string `json:"key"`
			}
			c.Decode(doc["problems"], &imported)
			for _, p := range imported {
				problemSource := src
				// A problem that names its member (Key) is not about a
				// binding position, so Order stays unset.
				if p.Key == "" {
					problemSource.Order = p.Index
				}
				message := p.Message
				if message == "" {
					message = "reported by manifest conversion as " + p.Code
				}
				issue(p.Code, problemSource, p.Key, message)
			}
			if suggestions, ok := doc["suggestions"].([]any); ok {
				for range suggestions {
					issue("suggestion_ignored", src, "", "a rail item or command shortcut is a suggestion and binds nothing; bind it in a keymap layer")
				}
			}
		}
		var entries []map[string]any
		if doc["bind"] == nil || c.Decode(doc["bind"], &entries) != nil {
			issue("invalid_entry", src, "bind", `"bind" is required and must be an array of objects`)
			continue
		}
		for i, raw := range entries {
			src.Order = i
			if i >= maxLayerEntries {
				issue("too_many", src, "", fmt.Sprintf("entry %d is past the %d-entry limit of one layer file and is dropped", i, maxLayerEntries))
				continue
			}
			b, problem := parse(raw, src)
			if problem != nil {
				code, key := problem.code, ""
				if src.Layer == LayerFocusLocal && code == "invalid_key" {
					code = "unparsable_key"
				}
				if code == "invalid_key" || code == "unparsable_key" {
					key, _ = invalidKeyDetail(raw)
				}
				issue(code, src, key, problem.message)
				continue
			}
			// Manifest conversion already enforced ownership, scope and the
			// 128-entry cap; the declared document names the plugin.
			if src.Layer == LayerFocusLocal {
				b.Plugin = src.Plugin
			}
			dup := string(src.Layer) + "\x00" + triple(b)
			entry := Entry{Binding: b, State: "effective"}
			if first, exists := seen[dup]; exists {
				entry.State, entry.dropped = "shadowed", true
				entry.ShadowedBy = &first
				issue("duplicate", src, sequence(b.Keys), fmt.Sprintf("same keys, focus and plugin as an earlier entry of this layer (%s); the first stays effective", sourceName(first)))
			} else {
				seen[dup] = src
			}
			next.listing.Entries = append(next.listing.Entries, entry)
		}
	}
	es := next.listing.Entries
	shadowEntries(es)
	unreachable(es, issue)
	// Past the cap the weakest entries go: rank the effective ones and drop
	// the tail, so a flood of low layers cannot push out the profile or
	// instance bindings.
	var effective []int
	for i := range es {
		if es[i].State == "effective" {
			effective = append(effective, i)
		}
	}
	if len(effective) > maxEffective {
		sort.SliceStable(effective, func(a, b int) bool { return capOrder(es[effective[a]].Binding, es[effective[b]].Binding) })
		for _, i := range effective[maxEffective:] {
			es[i].State, es[i].dropped = "shadowed", true
		}
		dropped := effective[maxEffective:]
		sort.Ints(dropped)
		for _, i := range dropped {
			issue("too_many", es[i].Source, sequence(es[i].Keys), fmt.Sprintf("over the %d-entry cap on effective entries; the lowest layers are dropped first", maxEffective))
		}
	}
	quit, palette := false, false
	for i := range es {
		b := es[i].Binding
		if es[i].dropped {
			continue
		}
		next.index[b.Keys[0]] = append(next.index[b.Keys[0]], b)
		if es[i].State != "effective" {
			continue
		}
		if b.Disable {
			es[i].State = "disabled"
		}
		quit = quit || b.Action == "husk.quit"
		palette = palette || b.Action == "husk.palette.open"
	}
	for key := range next.index {
		bs := next.index[key]
		sort.SliceStable(bs, func(i, j int) bool { return stronger(bs[i], bs[j]) })
	}
	if !quit || !palette {
		issue("no_exit_binding", Source{Layer: LayerHusk}, "", "no effective entry binds husk.quit or husk.palette.open; use `husk action invoke husk.quit` or a signal to exit")
	}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	return c.Prepare(ctx, r, !reflect.DeepEqual(old.listing, next.listing), func() bool { return s.current.CompareAndSwap(old, next) }, s.Generation), r, nil
}

// shadowEntries marks every entry that another entry statically shadows and
// names the strongest shadower. A duplicate is already shadowed by the first
// of its pair and takes no part. Candidates are found through two indexes, one
// by exact sequence and one by every prefix, so a large layer stays linear.
func shadowEntries(es []Entry) {
	exact, extends := map[string][]int{}, map[string][]int{}
	for i, e := range es {
		if e.State == "shadowed" {
			continue
		}
		exact[sequence(e.Keys)] = append(exact[sequence(e.Keys)], i)
		for n := 1; n <= len(e.Keys); n++ {
			extends[sequence(e.Keys[:n])] = append(extends[sequence(e.Keys[:n])], i)
		}
	}
	// Decide in rank order: every valid shadower outranks the entry it
	// shadows, so it is already decided, and an entry that is itself shadowed
	// takes no further part (loader step 3).
	order := make([]int, 0, len(es))
	for i, e := range es {
		if e.State != "shadowed" {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return stronger(es[order[a]].Binding, es[order[b]].Binding) })
	by := make([]int, len(es))
	for i := range by {
		by[i] = -1
	}
	for _, j := range order {
		b := es[j]
		candidates := extends[sequence(b.Keys)]
		for n := 1; n <= len(b.Keys); n++ {
			candidates = append(candidates[:len(candidates):len(candidates)], exact[sequence(b.Keys[:n])]...)
		}
		for _, i := range candidates {
			if i != j && by[i] < 0 && shadows(es[i].Binding, b.Binding) && (by[j] < 0 || stronger(es[i].Binding, es[by[j]].Binding)) {
				by[j] = i
			}
		}
	}
	for j, i := range by {
		if i >= 0 {
			source := es[i].Source
			es[j].State, es[j].ShadowedBy = "shadowed", &source
		}
	}
}

// unreachable drops, within one layer, scope and plugin condition, every
// effective sequence that has an effective strict prefix binding: the shorter
// binding always resolves first. A tombstone is not a prefix here.
func unreachable(es []Entry, issue func(code string, src Source, key, message string)) {
	effective, resolving := map[string]bool{}, map[string]bool{}
	group := func(b Binding) string { return string(b.Source.Layer) + "\x00" + b.Focus + "\x00" + b.Plugin + "\x00" }
	for _, e := range es {
		if e.dropped || e.Disable {
			continue
		}
		resolving[group(e.Binding)+sequence(e.Keys)] = true
		if e.State == "effective" {
			effective[group(e.Binding)+sequence(e.Keys)] = true
		}
	}
	for i := range es {
		if es[i].dropped {
			continue
		}
		prefixes := resolving
		if es[i].State == "effective" {
			prefixes = effective
		}
		for n := 1; n < len(es[i].Keys); n++ {
			if !prefixes[group(es[i].Binding)+sequence(es[i].Keys[:n])] {
				continue
			}
			// A statically shadowed entry is already listed and reported; it
			// only leaves the resolution index.
			if es[i].State == "effective" {
				es[i].State = "shadowed"
				issue("unreachable", es[i].Source, sequence(es[i].Keys), fmt.Sprintf("a shorter effective binding in the same layer, scope and condition takes its prefix %s, so this sequence can never be typed", sequence(es[i].Keys[:n])))
			}
			es[i].dropped = true
			break
		}
	}
}
