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
func specificity(b Binding) int {
	if b.Plugin != "" {
		return 2
	}
	if b.Focus != "" {
		return 1
	}
	return 0
}
func stronger(a, b Binding) bool {
	if rank(a.Source.Layer) != rank(b.Source.Layer) {
		return rank(a.Source.Layer) > rank(b.Source.Layer)
	}
	if specificity(a) != specificity(b) {
		return specificity(a) > specificity(b)
	}
	if a.Source.Order != b.Source.Order {
		return a.Source.Order < b.Source.Order
	}
	return a.Source.Plugin+"\x00"+a.Focus+"\x00"+a.Plugin+"\x00"+sequence(a.Keys)+"\x00"+a.Action < b.Source.Plugin+"\x00"+b.Focus+"\x00"+b.Plugin+"\x00"+sequence(b.Keys)+"\x00"+b.Action
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
func dominates(a, b Binding) bool {
	// Callers group by equal keys first; exact disables do not cover other scopes.
	if a.Disable && (a.Focus != b.Focus || a.Plugin != b.Plugin) {
		return false
	}
	return stronger(a, b) && (a.Focus == "" || a.Focus == b.Focus && (a.Plugin == "" || a.Plugin == b.Plugin))
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

// KeysFor returns effective spellings, strongest first (sequences joined for display).
func (s *Service) KeysFor(action string) []string {
	var bs []Binding
	for _, e := range s.current.Load().listing.Entries {
		if e.State == "effective" && e.Action == action {
			bs = append(bs, e.Binding)
		}
	}
	sort.SliceStable(bs, func(i, j int) bool { return stronger(bs[i], bs[j]) })
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
func layerProblem(doc store.Doc) string {
	encoded, err := json.Marshal(doc)
	if err != nil {
		return "invalid_entry"
	}
	if len(encoded) > 256*1024 {
		return "too_many"
	}
	if !validVersion(doc["version"]) {
		return "unsupported_version"
	}
	if doc["bind"] == nil {
		return "invalid_entry"
	}
	var entries []map[string]any
	if c.Decode(doc["bind"], &entries) != nil {
		return "invalid_entry"
	}
	return ""
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
			if layerProblem(doc) != "" {
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
	seen := map[string]bool{}
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
		if code := layerProblem(doc); code != "" {
			issue(code, src, "")
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
		limit := 2048
		if src.Layer == LayerFocusLocal {
			limit = 128
		}
		for i, raw := range entries {
			src.Order = i
			if i >= limit {
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
			if src.Layer == LayerFocusLocal {
				if _, given := raw["plugin"]; given || b.Disable {
					issue("invalid_entry", src, sequence(b.Keys))
					continue
				}
				if b.Focus == "" {
					issue("global_not_allowed", src, sequence(b.Keys))
					continue
				}
				owned := false
				for _, surface := range catalog[src.Plugin].Surfaces {
					owned = owned || surface.ID == b.Focus
				}
				if !owned {
					issue("foreign_surface", src, sequence(b.Keys))
					continue
				}
				ownAction := strings.HasPrefix(b.Action, "husk.")
				for _, action := range catalog[src.Plugin].Actions {
					ownAction = ownAction || action.ID == b.Action
				}
				if !ownAction {
					issue("foreign_action", src, sequence(b.Keys))
					continue
				}
				b.Plugin = src.Plugin
			}
			dup := string(src.Layer) + "\x00" + triple(b)
			entry := Entry{Binding: b, State: "effective"}
			if seen[dup] {
				entry.State = "shadowed"
				issue("duplicate", src, sequence(b.Keys))
			}
			seen[dup] = true
			next.listing.Entries = append(next.listing.Entries, entry)
		}
	}
	// Exact tombstones and broader higher-ranked scopes shadow lower candidates.
	es := next.listing.Entries
	groups := map[string][]int{}
	for i, e := range es {
		groups[sequence(e.Keys)] = append(groups[sequence(e.Keys)], i)
	}
	for _, group := range groups {
		for _, i := range group {
			if es[i].State == "shadowed" {
				continue
			}
			for _, j := range group {
				if i != j && es[j].State != "shadowed" && dominates(es[j].Binding, es[i].Binding) {
					es[i].State = "shadowed"
					source := es[j].Source
					es[i].ShadowedBy = &source
					break
				}
			}
		}
	}
	prefixes := map[string]int{}
	for i, e := range es {
		if e.State == "effective" {
			prefixes[triple(e.Binding)] = i
		}
	}
	for i := range es {
		if es[i].State != "effective" {
			continue
		}
		for n := 1; n < len(es[i].Keys); n++ {
			key := sequence(es[i].Keys[:n]) + "\x00" + es[i].Focus + "\x00" + es[i].Plugin
			if j, exists := prefixes[key]; exists && es[j].State == "effective" && !es[j].Disable {
				es[i].State = "shadowed"
				issue("unreachable", es[i].Source, sequence(es[i].Keys))
				break
			}
		}
	}
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
