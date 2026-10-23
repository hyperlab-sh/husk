// Package settings validates configuration atomically against registered schemas.
package settings

import (
	"context"
	"encoding/json"
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

// Options supplies trusted schemas and a value-free unknown-key warning callback.
type Options struct {
	Schema []Entry
	Warn   func(c.Problem)
}

// Service publishes immutable snapshots through a single atomic pointer.
type Service struct {
	current atomic.Pointer[snapshot]
	opts    Options
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
// are commands, not settings; layout preferences live in State.
func Builtins() []Entry {
	out := []Entry{{Key: "profile.default", Type: TypeString, Default: "", Owner: "husk", Apply: "restart"}, {Key: "theme.name", Type: TypeString, Default: palette.Default().Name, Owner: "husk", Apply: "live"}, {Key: "theme.allow_low_contrast", Type: TypeBool, Default: false, Owner: "husk", Apply: "live"}, {Key: "husk.plugins.paths", Type: TypeStrings, Default: []string{}, Owner: "husk", Apply: "restart"}, {Key: "husk.plugins.dirs", Type: TypeStrings, Default: []string{}, Owner: "husk", Apply: "restart"}, {Key: "husk.layout.rail", Type: TypeEnum, Default: "", Enum: []string{"", "modal", "full-canvas", "stage"}, Owner: "husk", Apply: "live"}, {Key: "husk.input.hold_alt", Type: TypeBool, Default: false, Owner: "husk", Apply: "live"}}
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

// New initializes built-in defaults before the first reload. It performs no I/O.
func New(opts Options) *Service {
	s := &Service{opts: opts}
	s.opts.Schema = c.Clone(opts.Schema)
	m := map[string]bool{}
	s.unknown.Store(&m)
	schema := map[string]Entry{}
	for _, e := range Builtins() {
		schema[e.Key] = e
	}
	for _, e := range opts.Schema {
		if e.Apply == "" {
			e.Apply = "live"
		}
		if _, exists := schema[e.Key]; exists || !validSchemaKey(e.Key, e.Owner) {
			continue
		}
		if _, err := validate(e, e.Default); err == nil {
			schema[e.Key] = e
		}
	}
	s.current.Store(defaults(schema, s.warning))
	return s
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
				s.opts.Warn(c.Problem{Severity: "warning", Code: "unknown_key", Key: key, Message: "unregistered key"})
			}
			return
		}
	}
}

// Snapshot loads an immutable reader without locking.
func (s *Service) Snapshot() Snapshot { return s.current.Load() }

// SnapshotFor filters access to exact declared keys plus public husk.* keys.
// Snapshot itself is trusted Stem access and must not be handed to plugins.
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
		if entry.Owner == "plugin:"+caller.PluginID || strings.HasPrefix(key, "husk.") {
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

// Get authorizes exact declared settings ownership; prefixes are not identities.
func (s *Service) Get(caller c.Caller, key string) (any, string, error) {
	if err := c.CheckCaller(caller); err != nil {
		return nil, "", err
	}
	snap := s.current.Load()
	if !c.IsUser(caller) && (caller.PluginID == "" || !strings.HasPrefix(key, "husk.") && snap.schema[key].Owner != "plugin:"+caller.PluginID) {
		return nil, "", c.Failure("denied", "undeclared_setting")
	}
	v, ok := snap.Raw(key)
	if !ok {
		return nil, "", c.Failure("not_found", "unknown_setting")
	}
	return v, snap.Source(key), nil
}

// List is the user-only settings.list view.
func (s *Service) List(caller c.Caller) (map[string]any, error) {
	if err := c.RequireUser(caller); err != nil {
		return nil, err
	}
	return c.Clone(s.current.Load().raw), nil
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

// Validate prepares a dry candidate and never publishes it.
func (s *Service) Validate(ctx context.Context, caller c.Caller, in c.ReloadInput) (c.Report, error) {
	if err := c.RequireUser(caller); err != nil {
		return c.Report{}, err
	}
	_, r, err := s.Rebuild(ctx, in)
	return r, err
}

// Preview validates fresh input against the same restart baseline but publishes
// only into an isolated service, never into the live snapshot. Theme.check uses
// this typed view; invalid input returns the current live view plus all problems.
func (s *Service) Preview(ctx context.Context, caller c.Caller, in c.ReloadInput) (Snapshot, c.Report, error) {
	if err := c.RequireUser(caller); err != nil {
		return s.Snapshot(), c.Report{}, err
	}
	shadow := &Service{opts: s.opts}
	shadow.current.Store(s.current.Load())
	m := map[string]bool{}
	shadow.unknown.Store(&m)
	candidate, report, err := shadow.Rebuild(ctx, in)
	if err != nil {
		return s.Snapshot(), report, err
	}
	applied := candidate.Apply()
	if !applied.Applied {
		return s.Snapshot(), applied, c.SafeError("unavailable")
	}
	report.Gen = s.Generation()
	report.Applied = false
	return shadow.Snapshot(), report, nil
}

// Rebuild checks all schemas and values, returning an unpublished candidate.
func (s *Service) Rebuild(ctx context.Context, in c.ReloadInput) (c.Candidate, c.Report, error) {
	old := s.current.Load()
	r := c.Report{Service: "settings", Gen: old.gen}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	schema := map[string]Entry{}
	for _, e := range Builtins() {
		schema[e.Key] = e
	}
	for _, e := range s.opts.Schema {
		if e.Apply == "" {
			e.Apply = "live"
		}
		if _, exists := schema[e.Key]; exists {
			c.Issue(&r.Problems, "error", "duplicate_schema_key", e.Owner, e.Key, "duplicate schema key")
			continue
		}
		schema[e.Key] = e
	}
	catalog, ps := c.Catalogs(in)
	r.Problems = append(r.Problems, ps...)
	for id, cat := range catalog {
		secretNames := map[string]bool{}
		for _, req := range cat.SecretRequests {
			secretNames[req.Name] = true
		}
		for _, e := range cat.SettingsDeclared {
			if !strings.HasPrefix(e.Key, "plugins."+id+".") {
				c.Issue(&r.Problems, "error", "foreign_schema_key", "plugin:"+id, e.Key, "foreign schema key")
				continue
			}
			e.Owner = "plugin:" + id
			local := strings.TrimPrefix(e.Key, "plugins."+id+".")
			if secretNames[local] || secretNames[e.Key] {
				c.Issue(&r.Problems, "error", "secret_as_setting", e.Owner, e.Key, "secrets are not settings")
			}
			if e.Apply == "" {
				e.Apply = "live"
			}
			if _, exists := schema[e.Key]; exists {
				c.Issue(&r.Problems, "error", "duplicate_schema_key", e.Owner, e.Key, "duplicate schema key")
				continue
			}
			schema[e.Key] = e
		}
	}
	for key, e := range schema {
		if !validSchemaKey(key, e.Owner) || e.Apply != "live" && e.Apply != "restart" {
			c.Issue(&r.Problems, "error", "invalid_schema_entry", e.Owner, key, "invalid schema key or apply policy")
		}
		if _, err := validate(e, e.Default); err != nil {
			c.Issue(&r.Problems, "error", "invalid_schema_default", e.Owner, key, "invalid schema default")
		}
		if e.Owner == "dist" && !strings.HasPrefix(key, "dist.") {
			c.Issue(&r.Problems, "error", "foreign_schema_key", e.Owner, key, "foreign schema key")
		}
	}
	next := defaults(schema, s.warning)
	next.gen = old.gen + 1
	flat := map[string]any{}
	flatten("", in.Doc.Doc, flat)
	for key, raw := range flat {
		layer := string(in.Doc.Origins[key])
		if layer == "" {
			layer = "user"
		}
		e, known := schema[key]
		if !known {
			owner := ""
			for id := range catalog {
				if strings.HasPrefix(key, "plugins."+id+".") && len(id) > len(owner) {
					owner = id
				}
			}
			untyped := owner != "" && len(catalog[owner].SettingsDeclared) == 0
			if untyped {
				c.Issue(&r.Problems, "warning", "unknown_key", layer, key, "plugin has no schema; raw value retained")
				next.raw[key] = c.Clone(raw)
				next.sources[key] = layer
			} else {
				c.Issue(&r.Problems, "error", "unknown_key", layer, key, "unregistered settings key")
			}
			continue
		}
		v, err := validate(e, raw)
		if err != nil {
			c.Issue(&r.Problems, "error", "invalid_value", layer, key, err.Error())
			continue
		}
		next.raw[key] = c.Clone(raw)
		next.sources[key] = layer
		if _, initialized := old.values[key]; e.Apply == "restart" && initialized && old.gen > 0 && !reflect.DeepEqual(v, old.values[key]) {
			next.values[key] = cloneTyped(old.values[key])
			c.Issue(&r.Problems, "warning", "restart_required", layer, key, "restart required")
		} else {
			next.values[key] = v
		}
	}
	// Removing a restart setting is itself a change back to its schema default.
	if old.gen > 0 {
		for key, e := range schema {
			if _, initialized := old.values[key]; e.Apply == "restart" && initialized && !reflect.DeepEqual(next.values[key], old.values[key]) {
				next.values[key] = cloneTyped(old.values[key])
				c.Issue(&r.Problems, "warning", "restart_required", next.sources[key], key, "restart required")
			}
		}
	}
	if err := c.Invalid(r.Problems); err != nil {
		return nil, r, err
	}
	if ctx.Err() != nil {
		return c.ContextError(ctx, r)
	}
	return c.Prepare(ctx, r, func() bool { return s.current.CompareAndSwap(old, next) }, s.Generation), r, nil
}
func flatten(prefix string, m map[string]any, out map[string]any) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if obj, ok := v.(map[string]any); ok && len(obj) > 0 {
			flatten(key, obj, out)
		} else {
			out[key] = v
		}
	}
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
