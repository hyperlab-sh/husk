// Package keybinding resolves immutable layered keymaps without reading State.
package keybinding

import (
	"context"
	_ "embed"
	"encoding/json"
	"github.com/hyperlab-sh/husk/internal/keyspelling"
	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// Layer is one fixed keymap precedence tier.
type Layer string

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

// Source locates one original declaration.
type Source struct {
	Layer  Layer  `json:"layer"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Plugin string `json:"plugin,omitempty"`
	Order  int    `json:"order"`
}

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
	Frozen       bool
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

// Problem retains machine-readable loader/conversion codes.
type Problem struct {
	Code    string `json:"code"`
	Layer   string `json:"layer"`
	Key     string `json:"key,omitempty"`
	Message string `json:"message"`
	Source  Source `json:"source"`
}

// Entry includes a binding's static precedence state.
type Entry struct {
	Binding
	State      string  `json:"state"`
	ShadowedBy *Source `json:"shadowed_by,omitempty"`
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

// maxLayerEntries is the per-document entry limit.
const maxLayerEntries = 2048

//go:embed keymap.json
var builtin []byte

type snapshot struct {
	gen          c.Generation
	listing      Listing
	index        map[string][]Binding
	documents    map[string]store.Doc
	actionOwners map[string]string
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
	return b.Focus == "" || b.Focus == ctx.FocusSurface && (b.Plugin == "" || b.Plugin == ctx.FocusOwner)
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
	if len(keys) == 0 {
		return Result{}
	}
	for _, b := range s.current.Load().index[keys[0]] {
		if !begins(b.Keys, keys) || !scope(b, ctx) || ctx.Frozen && b.Action != "husk.quit" {
			continue
		}
		// A tombstone unbinds exactly the sequence it names, never a prefix of it.
		if b.Disable && len(b.Keys) != len(keys) {
			continue
		}
		if ctx.ModalOpen && b.Focus != "modal-surface" && !(b.Focus == "" && (b.Action == "husk.modal.close" || ctx.ModalAction != "" && b.Action == ctx.ModalAction)) {
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
	snap := s.current.Load()
	var all []Binding
	for _, bindings := range snap.index {
		all = append(all, bindings...)
	}
	sort.SliceStable(all, func(i, j int) bool { return stronger(all[i], all[j]) })
	for _, b := range all {
		if b.Hint == "" || seen[triple(b)] {
			continue
		}
		r := s.Resolve(ctx, b.Keys)
		if r.Outcome == Match && r.Binding.Action == b.Action && r.Binding.Source == b.Source {
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

// ListFor filters keys.list to a plugin's own actions/declarations when needed.
func (s *Service) ListFor(caller c.Caller) Listing {
	snap := s.current.Load()
	out := c.Clone(snap.listing)
	if caller.User() {
		return out
	}
	if caller.PluginID == "" {
		return Listing{}
	}
	var entries []Entry
	for _, e := range out.Entries {
		if e.Plugin == caller.PluginID || snap.actionOwners[e.Action] == caller.PluginID {
			entries = append(entries, e)
		}
	}
	out.Entries = entries
	var ps []Problem
	for _, p := range out.Problems {
		if p.Source.Plugin == caller.PluginID {
			ps = append(ps, p)
		}
	}
	out.Problems = ps
	return out
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

// layerProblem is the file-level code for doc and the key the problem names.
func layerProblem(doc store.Doc) (code, key string) {
	if !validVersion(doc["version"]) {
		return "unsupported_version", ""
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return "invalid_entry", ""
	}
	if len(encoded) > 256*1024 {
		return "too_large", ""
	}
	if doc["bind"] == nil {
		return "invalid_entry", "bind"
	}
	var entries []map[string]any
	if c.Decode(doc["bind"], &entries) != nil {
		return "invalid_entry", "bind"
	}
	return "", ""
}

func parse(raw map[string]any, src Source) (Binding, string) {
	b := Binding{Source: src}
	for _, field := range []string{"action", "focus", "plugin", "hint"} {
		if value, exists := raw[field]; exists {
			if _, ok := value.(string); !ok {
				return b, "invalid_entry"
			}
		}
	}
	if value, exists := raw["disable"]; exists {
		if _, ok := value.(bool); !ok {
			return b, "invalid_entry"
		}
	}
	key, hasKey := raw["key"]
	keys, hasKeys := raw["keys"]
	if hasKey == hasKeys {
		return b, "invalid_entry"
	}
	var sequence []string
	if hasKey {
		str, ok := key.(string)
		if !ok {
			return b, "invalid_entry"
		}
		sequence = []string{str}
	} else {
		if c.Decode(keys, &sequence) != nil || len(sequence) < 2 || len(sequence) > 4 {
			return b, "invalid_entry"
		}
	}
	for _, key := range sequence {
		n, err := keyspelling.Normalize(key)
		if err != nil {
			return b, "invalid_key"
		}
		b.Keys = append(b.Keys, n)
	}
	b.Action, _ = raw["action"].(string)
	b.Focus, _ = raw["focus"].(string)
	b.Plugin, _ = raw["plugin"].(string)
	b.Hint, _ = raw["hint"].(string)
	b.Disable, _ = raw["disable"].(bool)
	if b.Plugin != "" && b.Focus == "" || utf8.RuneCountInString(b.Hint) > 40 {
		return b, "invalid_entry"
	}
	if b.Disable {
		if raw["action"] != nil || raw["args"] != nil || raw["hint"] != nil {
			return b, "invalid_entry"
		}
	} else if b.Action == "" || !strings.Contains(b.Action, ".") {
		return b, "invalid_entry"
	}
	if args, ok := raw["args"]; ok {
		m, ok := args.(map[string]any)
		if !ok {
			return b, "invalid_entry"
		}
		for _, v := range m {
			switch v.(type) {
			case string, bool, json.Number, float64, int, int64:
			default:
				return b, "invalid_entry"
			}
		}
		encoded, err := json.Marshal(m)
		if err != nil || len(encoded) > 1024 {
			return b, "invalid_entry"
		}
		b.Args = c.Clone(m)
	}
	return b, ""
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
	next := &snapshot{gen: old.gen + 1, index: map[string][]Binding{}, documents: map[string]store.Doc{}, actionOwners: map[string]string{}}
	issue := func(code string, src Source, key string) {
		layer := string(src.Layer)
		if src.Plugin != "" {
			layer = "plugin:" + src.Plugin
		}
		next.listing.Problems = append(next.listing.Problems, Problem{code, layer, key, code, src})
		c.Issue(&r.Problems, "warning", layer, key, code)
	}
	catalog, catalogProblems := c.Catalogs(in)
	r.Problems = append(r.Problems, catalogProblems...)
	for owner, plugin := range catalog {
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
	for _, layer := range in.Layers {
		if layer.Layer == store.LayerDistribution {
			doc := layer.Doc
			if code, _ := layerProblem(doc); code != "" {
				doc = old.documents[string(LayerDistribution)]
			}
			if doc["builtin"] == false {
				useBuiltin = false
			}
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
		// Problems name the file relative to the config directory, never the
		// Store's absolute path: keymap.json for the user layer and
		// profiles/<name>/keymap.json for a profile. A layer without a file
		// (injected documents) gets the same conventional name.
		file := "keymap.json"
		if l.Layer == store.LayerProfile {
			name := ""
			if in.Profile != nil {
				name = *in.Profile
			}
			file = "profiles/" + name + "/" + file
			if path := strings.ReplaceAll(l.File, "\\", "/"); path != "" {
				if at := strings.LastIndex(path, "/profiles/"); at >= 0 {
					file = path[at+1:]
				}
			}
		}
		layers = append(layers, struct {
			doc store.Doc
			src Source
		}{l.Doc, Source{Layer: Layer(l.Layer), File: file}})
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
		if code, key := layerProblem(doc); code != "" {
			issue(code, src, key)
			doc = old.documents[cacheKey]
			if doc == nil {
				continue
			}
		}
		next.documents[cacheKey] = c.Clone(doc)
		if _, exists := doc["builtin"]; exists && src.Layer != LayerDistribution {
			issue("invalid_entry", src, "builtin")
		}
		if src.Plugin != "" {
			var imported []struct {
				Index   int    `json:"index"`
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			c.Decode(doc["problems"], &imported)
			for _, p := range imported {
				problemSource := src
				problemSource.Order = p.Index
				issue(p.Code, problemSource, "")
				if p.Message != "" {
					next.listing.Problems[len(next.listing.Problems)-1].Message = p.Message
					r.Problems[len(r.Problems)-1].Message = p.Message
				}
			}
			if suggestions, ok := doc["suggestions"].([]any); ok {
				for range suggestions {
					issue("suggestion_ignored", src, "")
				}
			}
		}
		var entries []map[string]any
		if doc["bind"] == nil || c.Decode(doc["bind"], &entries) != nil {
			issue("invalid_entry", src, "bind")
			continue
		}
		for i, raw := range entries {
			src.Order = i
			if i >= maxLayerEntries {
				issue("too_many", src, "")
				continue
			}
			b, code := parse(raw, src)
			if code != "" {
				if src.Layer == LayerFocusLocal && code == "invalid_key" {
					code = "unparsable_key"
				}
				issue(code, src, "")
				if code == "invalid_key" || code == "unparsable_key" {
					key, message := invalidKeyDetail(raw)
					if message != "" {
						last := len(next.listing.Problems) - 1
						next.listing.Problems[last].Key = key
						next.listing.Problems[last].Message = message
						last = len(r.Problems) - 1
						r.Problems[last].Key = key
						r.Problems[last].Message = message
					}
				}
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
				entry.State = "shadowed"
				entry.ShadowedBy = &first
				issue("duplicate", src, sequence(b.Keys))
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
	if len(effective) > 4096 {
		sort.SliceStable(effective, func(a, b int) bool { return stronger(es[effective[a]].Binding, es[effective[b]].Binding) })
		for _, i := range effective[4096:] {
			es[i].State = "shadowed"
		}
		dropped := effective[4096:]
		sort.Ints(dropped)
		for _, i := range dropped {
			issue("too_many", es[i].Source, sequence(es[i].Keys))
		}
	}
	quit, palette := false, false
	for i := range es {
		if es[i].State != "effective" {
			continue
		}
		b := es[i].Binding
		if b.Disable {
			es[i].State = "disabled"
		}
		next.index[b.Keys[0]] = append(next.index[b.Keys[0]], b)
		quit = quit || b.Action == "husk.quit"
		palette = palette || b.Action == "husk.palette.open"
	}
	for key := range next.index {
		bs := next.index[key]
		sort.SliceStable(bs, func(i, j int) bool { return stronger(bs[i], bs[j]) })
	}
	if !quit || !palette {
		issue("no_exit_binding", Source{Layer: LayerHusk}, "")
	}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	return c.Prepare(ctx, r, func() bool { return s.current.CompareAndSwap(old, next) }, s.Generation), r, nil
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
	by := make([]int, len(es))
	for j, b := range es {
		by[j] = -1
		if b.State == "shadowed" {
			continue
		}
		candidates := extends[sequence(b.Keys)]
		for n := 1; n <= len(b.Keys); n++ {
			candidates = append(candidates[:len(candidates):len(candidates)], exact[sequence(b.Keys[:n])]...)
		}
		for _, i := range candidates {
			if i != j && shadows(es[i].Binding, b.Binding) && (by[j] < 0 || stronger(es[i].Binding, es[by[j]].Binding)) {
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
// effective sequence that has an effective strict prefix: the shorter binding
// (or tombstone) always resolves first.
func unreachable(es []Entry, issue func(code string, src Source, key string)) {
	prefixes := map[string]bool{}
	group := func(b Binding) string { return string(b.Source.Layer) + "\x00" + b.Focus + "\x00" + b.Plugin + "\x00" }
	for _, e := range es {
		if e.State == "effective" {
			prefixes[group(e.Binding)+sequence(e.Keys)] = true
		}
	}
	for i := range es {
		if es[i].State != "effective" {
			continue
		}
		for n := 1; n < len(es[i].Keys); n++ {
			if prefixes[group(es[i].Binding)+sequence(es[i].Keys[:n])] {
				es[i].State = "shadowed"
				issue("unreachable", es[i].Source, sequence(es[i].Keys))
				break
			}
		}
	}
}
