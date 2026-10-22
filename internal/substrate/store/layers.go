package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tailscale/hujson"
)

// ParseDoc parses bounded HuJSON into a JSON object. It is the shared parser
// Services use, rather than maintaining separate comment/trailing-comma rules.
func ParseDoc(b []byte) (Doc, error) {
	if len(b) > MaxDocBytes {
		return nil, fmt.Errorf("store: document exceeds %d bytes", MaxDocBytes)
	}
	// Standardize may reuse its input buffer; retain the original HuJSON so
	// Patch can preserve comments and callers never observe parser mutations.
	standardized, err := hujson.Standardize(bytes.Clone(b))
	if err != nil {
		return nil, err
	}
	var doc Doc
	if err = json.Unmarshal(standardized, &doc); err != nil {
		return nil, err
	}
	if doc == nil || len(bytes.TrimSpace(standardized)) == 0 || bytes.TrimSpace(standardized)[0] != '{' {
		return nil, fmt.Errorf("store: document must be an object")
	}
	if !boundedDepth(doc, 0) {
		return nil, fmt.Errorf("store: document exceeds depth %d", MaxDepth)
	}
	return doc, nil
}
func boundedDepth(v any, depth int) bool {
	if depth > MaxDepth {
		return false
	}
	switch x := v.(type) {
	case map[string]any:
		for _, v := range x {
			if !boundedDepth(v, depth+1) {
				return false
			}
		}
	case []any:
		for _, v := range x {
			if !boundedDepth(v, depth+1) {
				return false
			}
		}
	}
	return true
}
func copyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		r := make(Doc, len(x))
		for k, v := range x {
			r[k] = copyValue(v)
		}
		return r
	case []any:
		r := make([]any, len(x))
		for i, v := range x {
			r[i] = copyValue(v)
		}
		return r
	default:
		return v
	}
}
func copyDoc(d Doc) Doc {
	if d == nil {
		return Doc{}
	}
	return copyValue(d).(Doc)
}
func copyResolved(r Resolved) Resolved {
	c := Resolved{Doc: copyDoc(r.Doc), Origins: make(map[string]Layer, len(r.Origins)), Problems: append([]Problem(nil), r.Problems...)}
	for k, v := range r.Origins {
		c.Origins[k] = v
	}
	return c
}
func validDoc(n DocName) bool {
	for _, s := range strings.Split(string(n), "/") {
		if s == "" {
			return false
		}
		for _, c := range s {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}
func validProfile(p string) bool { return p == "" || validDoc(DocName(p)) && !strings.Contains(p, "/") }
func clearOrigins(origins map[string]Layer, path string) {
	for p := range origins {
		if p == path || strings.HasPrefix(p, path+".") {
			delete(origins, p)
		}
	}
}
func setOrigins(v any, path string, layer Layer, origins map[string]Layer) {
	if obj, ok := v.(Doc); ok && len(obj) > 0 {
		for k, v := range obj {
			setOrigins(v, path+"."+k, layer, origins)
		}
		return
	}
	origins[path] = layer
}
func mergeDoc(dst, src Doc, layer Layer, origins map[string]Layer, problems *[]Problem, prefix string) {
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		higher := src[key]
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if higher == nil {
			delete(dst, key)
			clearOrigins(origins, path)
			continue
		}
		lower, exists := dst[key]
		lo, loObj := lower.(Doc)
		hi, hiObj := higher.(Doc)
		if exists && loObj != hiObj {
			lowerLayer := origins[path]
			if lowerLayer == "" {
				for p, l := range origins {
					if strings.HasPrefix(p, path+".") {
						lowerLayer = l
						break
					}
				}
			}
			*problems = append(*problems, Problem{Code: "type_clash", Path: path, Layer: layer, LowerLayer: lowerLayer, Message: "object/non-object type changed"})
		}
		if hiObj {
			if !loObj {
				lo = Doc{}
				clearOrigins(origins, path)
			} else {
				delete(origins, path)
			}
			mergeDoc(lo, hi, layer, origins, problems, path)
			dst[key] = lo
			if len(lo) == 0 {
				origins[path] = layer
			}
		} else {
			clearOrigins(origins, path)
			dst[key] = copyValue(higher)
			setOrigins(higher, path, layer, origins)
		}
	}
}

// overlayDoc folds flag input into the instance layer without consuming null
// tombstones that must still delete values from lower configuration layers.
func overlayDoc(dst, src Doc) {
	for k, v := range src {
		hi, ok := v.(Doc)
		lo, loOK := dst[k].(Doc)
		if ok && loOK {
			overlayDoc(lo, hi)
		} else {
			dst[k] = copyValue(v)
		}
	}
}

type layerLoad struct {
	layers   []LayerDoc
	problems []Problem
}
type docKey struct {
	name    DocName
	profile string
}

func (s *Store) loadLayers(name DocName, profile string) (layerLoad, error) {
	if !validDoc(name) || !validProfile(profile) {
		return layerLoad{}, ErrInvalidPath
	}
	var out layerLoad
	appendDoc := func(layer Layer, file string, b []byte, readErr error) {
		if errors.Is(readErr, fs.ErrNotExist) {
			return
		}
		var d Doc
		err := readErr
		if err == nil {
			d, err = ParseDoc(b)
		}
		if err != nil {
			p := Problem{Code: "invalid_document", Layer: layer, File: file, Message: err.Error()}
			_, _ = fmt.Sscanf(err.Error(), "hujson: line %d, column %d:", &p.Line, &p.Column)
			var syntax *json.SyntaxError
			if errors.As(err, &syntax) {
				offset := min(int(syntax.Offset)-1, len(b))
				if offset >= 0 {
					p.Line = bytes.Count(b[:offset], []byte("\n")) + 1
					p.Column = offset - bytes.LastIndexByte(b[:offset], '\n')
				}
			}
			out.problems = append(out.problems, p)
			return
		}
		out.layers = append(out.layers, LayerDoc{Layer: layer, File: file, Doc: d})
	}
	if s.opts.Distribution != nil {
		b, err := fs.ReadFile(s.opts.Distribution, string(name)+".json")
		appendDoc(LayerDistribution, "", b, err)
		// Distribution profiles are defaults inside the same single layer.
		if profile != "" {
			b, err = fs.ReadFile(s.opts.Distribution, "profiles/"+profile+"/"+string(name)+".json")
			if !errors.Is(err, fs.ErrNotExist) {
				var previous Doc = Doc{}
				if len(out.layers) > 0 {
					previous = out.layers[0].Doc
					out.layers = out.layers[:0]
				}
				appendDoc(LayerDistribution, "", b, err)
				if len(out.layers) > 0 {
					var ps []Problem
					mergeDoc(previous, out.layers[0].Doc, LayerDistribution, map[string]Layer{}, &ps, "")
					out.layers[0].Doc = previous
				} else if len(previous) > 0 {
					out.layers = append(out.layers, LayerDoc{Layer: LayerDistribution, Doc: previous})
				}
			}
		}
	}
	read := func(layer Layer, parts []string) {
		p, err := pathInside(s.paths.Config, append(parts, strings.Split(string(name)+".json", "/")...), false)
		var b []byte
		if err == nil {
			b, _, err = readFile(p, MaxDocBytes, false)
		}
		appendDoc(layer, p, b, err)
	}
	read(LayerUser, nil)
	if profile != "" {
		read(LayerProfile, []string{"profiles", profile})
	}
	if s.opts.Instance.Named {
		read(LayerInstance, []string{"instances", s.opts.Instance.ID})
	}
	if d, ok := s.opts.InstanceDocs[name]; ok {
		var previous Doc
		for i := range out.layers {
			if out.layers[i].Layer == LayerInstance {
				previous = out.layers[i].Doc
				out.layers = append(out.layers[:i], out.layers[i+1:]...)
				break
			}
		}
		if previous == nil {
			previous = Doc{}
		}
		overlayDoc(previous, d)
		out.layers = append(out.layers, LayerDoc{Layer: LayerInstance, Doc: previous})
	}
	return out, nil
}

// Layers returns independent unmerged documents in fixed precedence order.
// A distribution profile is folded into its single distribution layer; flags
// are folded into the single instance layer. Parse failures are reported by Resolve.
func (s *Store) Layers(name DocName, profile string) ([]LayerDoc, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	load, err := s.cachedLayers(name, profile)
	if err != nil {
		return nil, err
	}
	out := make([]LayerDoc, len(load.layers))
	for i, l := range load.layers {
		out[i] = l
		out[i].Doc = copyDoc(l.Doc)
	}
	return out, nil
}
func (s *Store) cachedLayers(name DocName, profile string) (layerLoad, error) {
	k := docKey{name, profile}
	if l, ok := s.layerCache[k]; ok {
		return l, nil
	}
	l, err := s.loadLayers(name, profile)
	if err == nil {
		s.layerCache[k] = l
	}
	return l, err
}

// Resolve deep-merges the fixed layers, returning independent data and origins.
// On a bad reload, the previous good result remains serving with new problems.
func (s *Store) Resolve(name DocName, profile string) (Resolved, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	k := docKey{name, profile}
	if r, ok := s.resolved[k]; ok {
		return copyResolved(r), nil
	}
	load, err := s.cachedLayers(name, profile)
	if err != nil {
		return Resolved{}, err
	}
	r := Resolved{Doc: Doc{}, Origins: map[string]Layer{}, Problems: append([]Problem(nil), load.problems...)}
	if len(load.problems) > 0 {
		if old, ok := s.lastGood[k]; ok {
			r = copyResolved(old)
			r.Problems = append(r.Problems, load.problems...)
			s.resolved[k] = r
			return copyResolved(r), nil
		}
	}
	for _, l := range load.layers {
		mergeDoc(r.Doc, l.Doc, l.Layer, r.Origins, &r.Problems, "")
	}
	s.resolved[k] = r
	if len(load.problems) == 0 {
		s.lastGood[k] = copyResolved(r)
	}
	return copyResolved(r), nil
}

// Invalidate discards caches, preserving last-good results for malformed reloads.
func (s *Store) Invalidate() {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.layerCache = map[docKey]layerLoad{}
	s.resolved = map[docKey]Resolved{}
}

// Profiles lists validated profile directory names in distribution and user layers.
func (s *Store) Profiles() []string {
	set := map[string]bool{}
	if s.opts.Distribution != nil {
		entries, _ := fs.ReadDir(s.opts.Distribution, "profiles")
		for _, e := range entries {
			if e.IsDir() && validProfile(e.Name()) {
				set[e.Name()] = true
			}
		}
	}
	path := filepath.Join(s.paths.Config, "profiles")
	if privateExistingDir(path) == nil {
		entries, _ := os.ReadDir(path)
		for _, e := range entries {
			if e.IsDir() && e.Type()&os.ModeSymlink == 0 && validProfile(e.Name()) {
				set[e.Name()] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Declare atomically replaces a plugin's non-layered document. Invalid declared
// names are ignored; supported names are plugins and focus-keymap only.
func (s *Store) Declare(name DocName, source string, d Doc) {
	if name != DeclaredPlugins && name != DeclaredFocusKeymap {
		return
	}
	if !validID(source) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.declared[name] == nil {
		s.declared[name] = map[string]Doc{}
	}
	s.declared[name][source] = copyDoc(d)
}

// Undeclare removes all declared documents belonging to source.
func (s *Store) Undeclare(source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.declared {
		delete(d, source)
	}
}

// Declared returns independent snapshots, never merged into layered documents.
func (s *Store) Declared(name DocName) map[string]Doc {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Doc{}
	for k, d := range s.declared[name] {
		out[k] = copyDoc(d)
	}
	return out
}

// Patch applies JSON Merge Patch semantics to a writable HuJSON layer while
// retaining comments and formatting of untouched nodes. It never repairs bad files.
func (s *Store) Patch(name DocName, layer Layer, profile string, patch Doc) error {
	if !validDoc(name) || !validProfile(profile) || !boundedDepth(patch, 0) {
		return ErrInvalidPath
	}
	parts := []string{}
	switch layer {
	case LayerUser:
	case LayerProfile:
		if profile == "" {
			return ErrInvalidPath
		}
		parts = []string{"profiles", profile}
	case LayerInstance:
		if !s.opts.Instance.Named {
			return ErrInvalidPath
		}
		parts = []string{"instances", s.opts.Instance.ID}
	default:
		return fmt.Errorf("store: layer is read-only")
	}
	parts = append(parts, strings.Split(string(name)+".json", "/")...)
	s.configMu.Lock()
	defer s.configMu.Unlock()
	path, err := pathInside(s.paths.Config, parts, true)
	if err != nil {
		return err
	}
	// configMu protects this Store's cache; the file lock protects the shared
	// read-modify-write transaction against every other Store process.
	lockPath := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".lock")
	return withFileLock(lockPath, func() error {
		raw, _, err := readFile(path, MaxDocBytes, false)
		if errors.Is(err, fs.ErrNotExist) {
			raw = []byte("{}")
		} else if err != nil {
			return err
		}
		doc, err := ParseDoc(raw)
		if err != nil {
			return err
		}
		value, err := hujson.Parse(raw)
		if err != nil {
			return err
		}
		ops := []map[string]any{}
		patchOps(doc, patch, "", &ops)
		encoded, err := json.Marshal(ops)
		if err != nil {
			return err
		}
		if err = value.Patch(encoded); err != nil {
			return err
		}
		packed := value.Pack()
		if _, err = ParseDoc(packed); err != nil {
			return err
		}
		if err = atomicWrite(context.Background(), path, packed); err != nil {
			return err
		}
		s.layerCache = map[docKey]layerLoad{}
		s.resolved = map[docKey]Resolved{}
		return nil
	})
}
func patchOps(target, patch Doc, prefix string, ops *[]map[string]any) {
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := patch[k]
		old, exists := target[k]
		p := prefix + "/" + strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
		if v == nil {
			if exists {
				*ops = append(*ops, map[string]any{"op": "remove", "path": p})
			}
			continue
		}
		if obj, ok := v.(Doc); ok {
			prev, ok := old.(Doc)
			if !ok {
				prev = Doc{}
				*ops = append(*ops, map[string]any{"op": "add", "path": p, "value": Doc{}})
			}
			patchOps(prev, obj, p, ops)
		} else {
			*ops = append(*ops, map[string]any{"op": "add", "path": p, "value": v})
		}
	}
}
