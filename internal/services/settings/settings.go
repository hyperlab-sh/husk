// Package settings validates configuration atomically against registered schemas.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
)

// Type is a schema value kind.
type Type = string

const (
	// TypeString requires a string.
	TypeString = "string"
	// TypeInt requires an integral JSON number.
	TypeInt = "int"
	// TypeBool requires a boolean.
	TypeBool = "bool"
	// TypeDuration is milliseconds in JSON and time.Duration to readers.
	TypeDuration = "duration"
	// TypeEnum requires one of Enum.
	TypeEnum = "enum"
	// TypeStrings requires an array of strings.
	TypeStrings = "strings"
)

// Entry is the Store/manifest schema contract.
type Entry = c.SettingDecl

// Snapshot is the immutable reader interface shared through contracts.
type Snapshot = c.SettingsSnapshot

// Options supplies the distribution schema and the warning sink.
type Options struct {
	Schema []Entry
	// Warn receives `settings: unregistered key <key>` once per key per
	// process. It is not a Problem and appears in no report.
	Warn func(msg string)
}

// Service publishes immutable snapshots through a single atomic pointer.
type Service struct {
	current atomic.Pointer[snapshot]
	opts    Options
	base    map[string]Entry // validated host and distribution entries
	unknown atomic.Pointer[map[string]bool]
}
type snapshot struct {
	gen         c.Generation
	values, raw map[string]any
	sources     map[string]string
	schema      map[string]Entry
	warn        func(string)
}

// Builtins returns the complete host/Theme/Profile schema. Frame-dump/demo flags
// are commands, not settings; layout preferences live in State. No entry is
// public: plugins reach the palette through theme.get.
func Builtins() []Entry {
	out := []Entry{
		{Key: "profile.default", Type: TypeString, Default: "", Owner: "husk", Apply: "live"},
		{Key: "theme.name", Type: TypeString, Default: palette.Default().Name, Owner: "husk", Apply: "live"},
		{Key: "theme.allow_low_contrast", Type: TypeBool, Default: false, Owner: "husk", Apply: "live"},
		{Key: "husk.plugins.paths", Type: TypeStrings, Default: []string{}, Owner: "husk", Apply: "restart", Doc: "Plugin arguments loaded once at start, interpreted like --plugin; --plugin is the instance layer on top."},
		{Key: "husk.plugins.dirs", Type: TypeStrings, Default: []string{}, Owner: "husk", Apply: "restart", Doc: "Plugin directories loaded once at start, interpreted like --plugin-dir; --plugin-dir is the instance layer on top."},
	}
	data, _ := json.Marshal(palette.Default())
	var roles map[string]string
	json.Unmarshal(data, &roles)
	for role, value := range roles {
		if role != "name" {
			out = append(out, Entry{Key: "theme.palette." + role, Type: TypeString, Default: value, Owner: "husk", Apply: "live"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// New initializes the built-in snapshot before the first reload. It performs
// no I/O. A duplicate, foreign, invalid-default or invalid built-in or
// distribution entry is a programming error: New returns an error naming the
// entry and the process does not start.
func New(opts Options) (*Service, error) {
	s := &Service{opts: opts, base: map[string]Entry{}}
	s.opts.Schema = c.Clone(opts.Schema)
	m := map[string]bool{}
	s.unknown.Store(&m)
	add := func(e Entry, source string) error {
		if e.Owner == "" && source == "dist" {
			e.Owner = "dist"
		}
		if e.Owner != source {
			return fmt.Errorf("settings: %s schema entry %q: foreign_schema_key: owner %q", source, e.Key, e.Owner)
		}
		if _, dup := s.base[e.Key]; dup {
			return fmt.Errorf("settings: %s schema entry %q: duplicate_schema_key", source, e.Key)
		}
		if code := entryProblem(e, false); code != "" {
			return fmt.Errorf("settings: %s schema entry %q: %s", source, e.Key, code)
		}
		if e.Apply == "" {
			e.Apply = "live"
		}
		s.base[e.Key] = e
		return nil
	}
	for _, e := range Builtins() {
		if err := add(e, "husk"); err != nil {
			return nil, err
		}
	}
	for _, e := range s.opts.Schema {
		if err := add(e, "dist"); err != nil {
			return nil, err
		}
	}
	s.current.Store(defaults(s.base, s.warning))
	return s, nil
}

// entryProblem returns the schema problem code of one entry, or "". Key and
// owner agreement is checked first, then the entry's own shape, then its
// default.
func entryProblem(e Entry, plugin bool) string {
	if !validSchemaKey(e.Key, e.Owner) {
		return "foreign_schema_key"
	}
	switch e.Type {
	case TypeString, TypeInt, TypeBool, TypeDuration, TypeEnum, TypeStrings:
	default:
		return "invalid_schema_entry"
	}
	switch e.Apply {
	case "", "live", "restart":
	default:
		return "invalid_schema_entry"
	}
	if e.Type == TypeEnum && len(e.Enum) == 0 || plugin && e.Public {
		return "invalid_schema_entry"
	}
	if _, err := validate(e, e.Default); err != nil {
		return "invalid_schema_default"
	}
	return ""
}
func defaults(schema map[string]Entry, warn func(string)) *snapshot {
	n := &snapshot{values: map[string]any{}, raw: map[string]any{}, sources: map[string]string{}, schema: c.Clone(schema), warn: warn}
	for k, e := range schema {
		v, err := validate(e, e.Default)
		if err == nil {
			n.values[k] = v
			n.raw[k] = c.Clone(e.Default)
			n.sources[k] = "default"
		}
	}
	return n
}
func (s *Service) warning(key string) {
	for {
		old := s.unknown.Load()
		if (*old)[key] {
			return
		}
		next := maps.Clone(*old)
		next[key] = true
		if s.unknown.CompareAndSwap(old, &next) {
			if s.opts.Warn != nil {
				s.opts.Warn("settings: unregistered key " + key)
			}
			return
		}
	}
}

// Snapshot loads an immutable reader without locking.
func (s *Service) Snapshot() Snapshot { return s.current.Load() }

// visible reports whether a plugin may read the entry: its own declaration,
// or a host or distribution entry marked public. A key prefix is never
// authorization.
func visible(caller c.Caller, e Entry) bool {
	return e.Owner == "plugin:"+caller.PluginID || e.Public && (e.Owner == "husk" || e.Owner == "dist")
}

// SnapshotFor filters access: a user gets the full snapshot, a plugin exactly
// its own declared entries plus public host and distribution entries. The
// full Snapshot is trusted Stem access and must not be handed to plugins.
func (s *Service) SnapshotFor(caller c.Caller) Snapshot {
	if c.IsUser(caller) {
		return s.Snapshot()
	}
	old := s.current.Load()
	out := defaults(map[string]Entry{}, func(string) {})
	out.gen = old.gen
	if !c.ValidCaller(caller) {
		return out
	}
	for key, entry := range old.schema {
		if visible(caller, entry) {
			out.schema[key] = c.Clone(entry)
			out.values[key] = cloneTyped(old.values[key])
			out.raw[key] = c.Clone(old.raw[key])
			out.sources[key] = old.sources[key]
		}
	}
	return out
}

// Generation is the currently serving snapshot generation.
func (s *Service) Generation() c.Generation { return s.current.Load().gen }

// Schema returns a detached, key-sorted schema inventory.
func (s *Service) Schema() []Entry {
	m := s.current.Load().schema
	out := make([]Entry, 0, len(m))
	for _, e := range m {
		out = append(out, c.Clone(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Get answers settings.get. A user sees every known key and every retained
// unknown key; a plugin sees only what visible allows. A key unknown to this
// caller is not_found/unknown_setting for everyone, never denied, so a plugin
// cannot learn whether another owner's key exists.
func (s *Service) Get(caller c.Caller, key string) (any, string, error) {
	if err := c.CheckCaller(caller); err != nil {
		return nil, "", err
	}
	snap := s.current.Load()
	if !c.IsUser(caller) {
		if e, ok := snap.schema[key]; !ok || !visible(caller, e) {
			return nil, "", c.Failure("not_found", "unknown_setting")
		}
	}
	v, ok := snap.Raw(key)
	if !ok {
		return nil, "", c.Failure("not_found", "unknown_setting")
	}
	return v, snap.Source(key), nil
}

// ListEntry is one settings.list row.
type ListEntry struct {
	Key    string
	Value  any
	Source string
}

// List is the user-only settings.list view: every known key and every
// retained unknown key, sorted by key.
func (s *Service) List(caller c.Caller) ([]ListEntry, error) {
	if err := c.RequireUser(caller); err != nil {
		return nil, err
	}
	snap := s.current.Load()
	out := make([]ListEntry, 0, len(snap.raw))
	for key, v := range snap.raw {
		out = append(out, ListEntry{Key: key, Value: c.Clone(v), Source: snap.Source(key)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// SchemaFor is the user-only settings.schema view, optionally filtered by owner.
func (s *Service) SchemaFor(caller c.Caller, owner string) ([]Entry, error) {
	if err := c.RequireUser(caller); err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range s.Schema() {
		if owner == "" || owner == e.Owner {
			out = append(out, e)
		}
	}
	return out, nil
}

// Check is the outcome of a dry run: OK is whether a reload now would publish
// this Service's candidate, Problems is every problem found.
type Check struct {
	OK       bool
	Problems []c.Problem
}

// Validate prepares a dry candidate and never publishes it. An invalid input
// is OK=false with its problems, not an error.
func (s *Service) Validate(ctx context.Context, caller c.Caller, in c.ReloadInput) (Check, error) {
	if err := c.RequireUser(caller); err != nil {
		return Check{}, err
	}
	_, r, err := s.Rebuild(ctx, in)
	if err != nil && !errors.Is(err, c.ErrInvalid) {
		return Check{}, err
	}
	return Check{OK: err == nil, Problems: r.Problems}, nil
}

// Preview validates fresh input against the same restart baseline but builds
// only an isolated snapshot, never the live one. theme.check uses the typed
// snapshot as the one Theme would see; it is nil when the input is invalid.
func (s *Service) Preview(ctx context.Context, caller c.Caller, in c.ReloadInput) (Snapshot, Check, error) {
	if err := c.RequireUser(caller); err != nil {
		return nil, Check{}, err
	}
	shadow := &Service{opts: s.opts, base: s.base}
	shadow.opts.Warn = nil
	shadow.current.Store(s.current.Load())
	m := map[string]bool{}
	shadow.unknown.Store(&m)
	candidate, report, err := shadow.Rebuild(ctx, in)
	if errors.Is(err, c.ErrInvalid) {
		return nil, Check{Problems: report.Problems}, nil
	}
	if err != nil {
		return nil, Check{}, err
	}
	if !candidate.Apply().Applied {
		return nil, Check{}, c.SafeError("unavailable")
	}
	return shadow.Snapshot(), Check{OK: true, Problems: report.Problems}, nil
}

// Rebuild checks all schemas and values, returning an unpublished candidate.
func (s *Service) Rebuild(ctx context.Context, in c.ReloadInput) (c.Candidate, c.Report, error) {
	old := s.current.Load()
	r := c.Report{Service: "settings", Gen: old.gen}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	schema := maps.Clone(s.base)
	catalog, ps := c.Catalogs(in)
	r.Problems = append(r.Problems, ps...)
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		registerPlugin(schema, id, catalog[id], &r.Problems)
	}
	next := defaults(schema, s.warning)
	next.gen = old.gen + 1
	layerFiles := map[string]string{}
	for _, l := range in.ConfigLayers {
		layerFiles[string(l.Layer)] = l.File
	}
	flat := map[string]any{}
	flatten("", in.Doc.Doc, schema, flat)
	// Store reports a clash at an object node as often as at a leaf; the Key is
	// set only when settings.get accepts it.
	for _, p := range in.Doc.Problems {
		if p.Code != "type_clash" {
			continue
		}
		_, known := schema[p.Path]
		_, kept := flat[p.Path]
		key := ""
		if known || kept {
			key = p.Path
		}
		r.Problems = append(r.Problems, c.Problem{Severity: "warning", Code: "type_clash", Layer: string(p.Layer), Key: key, Message: fmt.Sprintf("%s: the %s layer replaces a value of a different type from the %s layer", p.Path, p.Layer, p.LowerLayer), Source: sourceOf(string(p.Layer), layerFiles)})
	}
	for key, raw := range flat {
		layer := originOf(in.Doc.Origins, key)
		e, known := schema[key]
		if !known {
			r.Problems = append(r.Problems, c.Problem{Severity: "warning", Code: "unknown_key", Layer: layer, Key: key, Message: key + ": no schema entry; the value is kept and no plugin sees it", Source: sourceOf(layer, layerFiles)})
			next.raw[key] = c.Clone(raw)
			next.sources[key] = layer
			continue
		}
		v, err := validate(e, raw)
		if err != nil {
			r.Problems = append(r.Problems, c.Problem{Severity: "error", Code: "invalid_value", Layer: layer, Key: key, Message: fmt.Sprintf("%s layer: %s: want %s", layer, key, wantOf(e)), Source: sourceOf(layer, layerFiles)})
			continue
		}
		next.raw[key] = c.Clone(raw)
		next.sources[key] = layer
		next.values[key] = v
	}
	// The value in force for a restart key is what the first successful load
	// produced; later loads keep it and say what is pending. Removing the key
	// counts as a change back to the default.
	if old.gen > 0 {
		for key, e := range schema {
			prev, initialized := old.values[key]
			if e.Apply != "restart" || !initialized || reflect.DeepEqual(next.values[key], prev) {
				continue
			}
			wanted := next.raw[key]
			r.Problems = append(r.Problems, c.Problem{Severity: "warning", Code: "restart_required", Layer: next.sources[key], Key: key, Message: fmt.Sprintf("%s: restart required; in force %s, configured %s", key, jsonText(old.raw[key]), jsonText(wanted)), Source: sourceOf(next.sources[key], layerFiles)})
			next.values[key] = cloneTyped(prev)
			next.raw[key] = c.Clone(old.raw[key])
			next.sources[key] = old.sources[key]
		}
	}
	sort.SliceStable(r.Problems, func(i, j int) bool { return r.Problems[i].Key < r.Problems[j].Key })
	if err := c.Invalid(r.Problems); err != nil {
		return nil, r, err
	}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	return c.Prepare(ctx, r, func() bool { return s.current.CompareAndSwap(old, next) }, s.Generation), r, nil
}

// registerPlugin adds one plugin's declared entries to schema, all or nothing:
// any problem is a warning against layer plugin:<id> and none of its entries
// is registered.
func registerPlugin(schema map[string]Entry, id string, cat c.Catalog, ps *[]c.Problem) {
	owner := "plugin:" + id
	secrets := map[string]bool{}
	for _, req := range cat.SecretRequests {
		secrets[req.Name] = true
	}
	prefix := "plugins." + id + "."
	seen := map[string]bool{}
	var accepted []Entry
	bad := false
	for _, e := range cat.SettingsDeclared {
		e.Owner = owner
		code := entryProblem(e, true)
		switch {
		case code == "foreign_schema_key":
		case seen[e.Key] || schema[e.Key].Key != "":
			code = "duplicate_schema_key"
		case code != "":
		case secrets[strings.TrimPrefix(e.Key, prefix)]:
			code = "secret_as_setting"
		}
		if code != "" {
			bad = true
			*ps = append(*ps, c.Problem{Severity: "warning", Code: code, Layer: owner, Key: e.Key, Message: schemaMessage(code), Source: &c.Source{Layer: owner, Plugin: id}})
			continue
		}
		seen[e.Key] = true
		if e.Apply == "" {
			e.Apply = "live"
		}
		accepted = append(accepted, e)
	}
	if bad {
		return
	}
	for _, e := range accepted {
		schema[e.Key] = e
	}
}
func schemaMessage(code string) string {
	switch code {
	case "foreign_schema_key":
		return "schema key is not in this plugin's namespace; its settings are not registered"
	case "duplicate_schema_key":
		return "duplicate schema key; the plugin's settings are not registered"
	case "invalid_schema_default":
		return "schema default does not match its type; the plugin's settings are not registered"
	case "secret_as_setting":
		return "setting is named like one of the plugin's secrets; the plugin's settings are not registered"
	}
	return "invalid schema entry; the plugin's settings are not registered"
}

// sourceOf names the layer and its config-relative file, as Store reports it.
func sourceOf(layer string, files map[string]string) *c.Source {
	if layer == "" {
		return nil
	}
	return &c.Source{Layer: layer, File: files[layer]}
}
func wantOf(e Entry) string {
	if e.Type == TypeEnum {
		return "one of " + strings.Join(e.Enum, ", ")
	}
	return e.Type
}
func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return string(b)
}

// originOf is the layer that won key. A value that is an object has origins on
// its leaves only; the first of those, in key order, speaks for it.
func originOf(origins map[string]store.Layer, key string) string {
	if l, ok := origins[key]; ok {
		return string(l)
	}
	best := ""
	for k := range origins {
		if strings.HasPrefix(k, key+".") && (best == "" || k < best) {
			best = k
		}
	}
	return string(origins[best])
}

// flatten turns a document into dotted keys. It stops at a schema key, so a
// value of the wrong shape there (an object for a scalar) reaches validation
// whole instead of becoming unknown leaves. An empty object at a schema
// prefix says nothing and is dropped.
func flatten(prefix string, m map[string]any, schema map[string]Entry, out map[string]any) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		obj, isObj := v.(map[string]any)
		if _, known := schema[key]; known || !isObj {
			out[key] = v
			continue
		}
		if len(obj) == 0 {
			if !isSchemaPrefix(schema, key) {
				out[key] = v
			}
			continue
		}
		flatten(key, obj, schema, out)
	}
}
func isSchemaPrefix(schema map[string]Entry, key string) bool {
	for k := range schema {
		if strings.HasPrefix(k, key+".") {
			return true
		}
	}
	return false
}
func cloneTyped(v any) any {
	if ss, ok := v.([]string); ok {
		return append([]string{}, ss...)
	}
	return v
}
func validSchemaKey(key, owner string) bool {
	if strings.HasPrefix(owner, "plugin:") {
		prefix := "plugins." + strings.TrimPrefix(owner, "plugin:") + "."
		return strings.HasPrefix(key, prefix) && validLocalKey(strings.TrimPrefix(key, prefix))
	}
	if !validKey(key) {
		return false
	}
	if owner == "dist" {
		return strings.HasPrefix(key, "dist.")
	}
	return owner == "husk" && (strings.HasPrefix(key, "husk.") || strings.HasPrefix(key, "theme.") || strings.HasPrefix(key, "profile."))
}
func validLocalKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func validKey(k string) bool {
	if k == "" || strings.HasPrefix(k, ".") || strings.HasSuffix(k, ".") || strings.Contains(k, "..") {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-", r)) {
			return false
		}
	}
	return true
}
func integer(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, e := n.Int64()
		return i, e == nil
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		i := int64(n)
		return i, float64(i) == n
	}
	return 0, false
}
func validate(e Entry, v any) (any, error) {
	bad := fmt.Errorf("want valid %s within schema bounds", e.Type)
	switch e.Type {
	case TypeString:
		if x, ok := v.(string); ok {
			return x, nil
		}
	case TypeBool:
		if x, ok := v.(bool); ok {
			return x, nil
		}
	case TypeEnum:
		if x, ok := v.(string); ok && slices.Contains(e.Enum, x) {
			return x, nil
		}
	case TypeInt, TypeDuration:
		if n, ok := integer(v); ok && (e.Min == nil || n >= *e.Min) && (e.Max == nil || n <= *e.Max) {
			if e.Type == TypeDuration {
				if n > int64((1<<63-1)/time.Millisecond) || n < int64((-1<<63)/time.Millisecond) {
					return nil, bad
				}
				return time.Duration(n) * time.Millisecond, nil
			}
			return n, nil
		}
	case TypeStrings:
		switch x := v.(type) {
		case []string:
			return append([]string{}, x...), nil
		case []any:
			out := []string{}
			for _, item := range x {
				str, ok := item.(string)
				if !ok {
					return nil, bad
				}
				out = append(out, str)
			}
			return out, nil
		}
	}
	return nil, bad
}
func (s *snapshot) Generation() c.Generation { return s.gen }
func (s *snapshot) get(key string) any {
	v, ok := s.values[key]
	if !ok {
		s.warn(key)
	}
	return v
}
func (s *snapshot) String(k string) string          { v, _ := s.get(k).(string); return v }
func (s *snapshot) Int(k string) int64              { v, _ := s.get(k).(int64); return v }
func (s *snapshot) Bool(k string) bool              { v, _ := s.get(k).(bool); return v }
func (s *snapshot) Duration(k string) time.Duration { v, _ := s.get(k).(time.Duration); return v }
func (s *snapshot) Strings(k string) []string {
	v, _ := s.get(k).([]string)
	return append([]string{}, v...)
}
func (s *snapshot) Raw(k string) (any, bool) { v, ok := s.raw[k]; return c.Clone(v), ok }
func (s *snapshot) Source(k string) string {
	if v, ok := s.sources[k]; ok {
		return v
	}
	return "default"
}

// StoreReader is the minimal input adapter used by a standalone settings loader.
type StoreReader interface {
	Resolve(store.DocName, string) (store.Resolved, error)
}
