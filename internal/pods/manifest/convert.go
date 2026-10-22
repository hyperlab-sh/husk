package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/tailscale/hujson"
)

const maxManifestBytes = 1 << 20

func readManifest(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxManifestBytes+1))
	if err == nil && len(b) > maxManifestBytes {
		err = errors.New("manifest exceeds 1 MiB")
	}
	return b, err
}
func standardize(b []byte) ([]byte, error) {
	if len(b) > maxManifestBytes {
		return nil, errors.New("manifest exceeds 1 MiB")
	}
	return hujson.Standardize(b)
}
func decodeField(fields map[string]json.RawMessage, key string, out any) error {
	if raw, ok := fields[key]; ok {
		return json.Unmarshal(raw, out)
	}
	return nil
}
func alias(fields map[string]json.RawMessage, a, b string, out any) error {
	if fields[a] != nil && fields[b] != nil {
		return fmt.Errorf("manifest supplies both %s and %s", a, b)
	}
	if fields[a] != nil {
		return decodeField(fields, a, out)
	}
	return decodeField(fields, b, out)
}

func convert(src Source, modern bool) (*Spec, []Warning, error) {
	if src.err != nil {
		return nil, nil, src.err
	}
	if !filepath.IsAbs(src.Dir) {
		return nil, nil, errors.New("manifest directory must be absolute")
	}
	if src.Path == "" {
		if modern || len(src.Command) == 0 {
			return nil, nil, errors.New("implicit plugin requires argv")
		}
		id := filepath.Base(src.Command[0])
		s := &Spec{ID: id, Name: id, Version: "0.0.0", Protocol: protocol.Major, Command: append([]string(nil), src.Command...), WorkDir: src.Dir}
		if err := s.Validate(); err != nil {
			return nil, nil, err
		}
		if s.ID == "husk" {
			return nil, nil, errors.New("plugin id husk is reserved")
		}
		s.Command = s.Argv()
		return s, nil, nil
	}
	raw, err := standardize(src.Bytes)
	if err != nil {
		return nil, nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, nil, err
	}
	if fields == nil {
		return nil, nil, errors.New("manifest must be an object")
	}
	s := &Spec{WorkDir: src.Dir, Protocol: protocol.Major}
	for _, f := range []struct {
		key  string
		dest any
	}{{"id", &s.ID}, {"name", &s.Name}, {"version", &s.Version}, {"description", &s.Description}, {"command", &s.Command}, {"published_slots", &s.PublishedSlots}} {
		if err := decodeField(fields, f.key, f.dest); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", f.key, err)
		}
	}
	if s.Name == "" || s.Version == "" {
		return nil, nil, errors.New("manifest requires name and version")
	}
	if s.ID == "husk" {
		return nil, nil, errors.New("plugin id husk is reserved")
	}
	var warnings []Warning
	var slots []string
	if err := decodeField(fields, "slots", &slots); err != nil {
		return nil, nil, err
	}
	for _, slot := range slots {
		s.Surfaces = append(s.Surfaces, SurfaceDecl{ID: protocol.SurfaceID(slot)})
	}
	if modern {
		var version int
		if err := decodeField(fields, "manifest_version", &version); err != nil || version != 2 {
			return nil, nil, errors.New("v2 manifest requires manifest_version 2")
		}
		if fields["protocol"] == nil {
			return nil, nil, errors.New("v2 manifest requires protocol")
		}
		if err := decodeField(fields, "protocol", &s.Protocol); err != nil {
			return nil, nil, err
		}
		for _, f := range []struct {
			key  string
			dest any
		}{{"actions", &s.Actions}, {"surfaces", &s.Surfaces}, {"subscriptions", &s.Subscriptions}, {"events_declared", &s.EventsDeclared}, {"hooks_declared", &s.HooksDeclared}, {"hook_registrations", &s.HookRegistrations}} {
			if err := decodeField(fields, f.key, f.dest); err != nil {
				return nil, nil, err
			}
		}
		if err := alias(fields, "slice", "state", &s.Slice); err != nil {
			return nil, nil, err
		}
		if err := alias(fields, "secret_requests", "secrets", &s.SecretRequests); err != nil {
			return nil, nil, err
		}
		if err := alias(fields, "settings_declared", "settings", &s.SettingsDeclared); err != nil {
			return nil, nil, err
		}
		if b := fields["events"]; b != nil {
			var events struct {
				Subscribe []string    `json:"subscribe"`
				Publish   []EventDecl `json:"publish"`
			}
			if err := json.Unmarshal(b, &events); err != nil {
				return nil, nil, err
			}
			s.Subscriptions = append(s.Subscriptions, events.Subscribe...)
			s.EventsDeclared = append(s.EventsDeclared, events.Publish...)
		}
		if b := fields["hooks"]; b != nil {
			var hooks struct {
				Subscribe []string   `json:"subscribe"`
				Publish   []HookDecl `json:"publish"`
			}
			if err := json.Unmarshal(b, &hooks); err != nil {
				return nil, nil, err
			}
			s.HooksDeclared = append(s.HooksDeclared, hooks.Publish...)
			for _, id := range hooks.Subscribe {
				s.HookRegistrations = append(s.HookRegistrations, HookRegistration{ID: id})
			}
		}
		var bindings []struct {
			FocusBinding
			Plugin  json.RawMessage `json:"plugin"`
			Disable json.RawMessage `json:"disable"`
		}
		if err := alias(fields, "focus_bindings", "keybindings", &bindings); err != nil {
			return nil, nil, err
		}
		actions := make(map[string]bool)
		for _, a := range s.Actions {
			actions[a.ID] = true
		}
		surfaces := make(map[string]bool)
		for _, surface := range s.Surfaces {
			surfaces[string(surface.ID)] = true
		}
		for i, b := range bindings {
			code, message := "", ""
			if b.Focus == "" || !surfaces[b.Focus] || b.Plugin != nil || b.Disable != nil {
				code, message = "global_not_allowed", "focus must be a surface owned by this plugin; plugin/disable fields are not allowed"
			} else if !actions[b.Action] && !(strings.HasPrefix(b.Action, "husk.") && len(b.Action) > 5) {
				code, message = "foreign_action", "focus binding must target an own action or husk.*"
			}
			if code != "" {
				s.BindingProblems = append(s.BindingProblems, BindingProblem{i, code, message})
				warnings = append(warnings, Warning{Code: code, Field: "keybindings", Index: i, Message: message})
				continue
			}
			s.FocusBindings = append(s.FocusBindings, b.FocusBinding)
		}
	} else {
		for _, key := range []string{"transport", "language"} {
			if fields[key] != nil {
				warnings = append(warnings, Warning{Code: "ignored_field", Field: key, Message: "legacy field " + key + " is ignored"})
			}
		}
		if err := decodeField(fields, "hooks", &s.HooksDeclared); err != nil {
			return nil, warnings, err
		}
		var events []struct{ ID, Title, Description string }
		if err := decodeField(fields, "events", &events); err != nil {
			return nil, warnings, err
		}
		for _, e := range events {
			s.EventsDeclared = append(s.EventsDeclared, EventDecl{Name: e.ID, Title: e.Title, Description: e.Description})
		}
	}
	if err := s.Validate(); err != nil {
		return nil, warnings, err
	}
	if err := validateDeclarations(s); err != nil {
		return nil, warnings, err
	}
	s.Command = s.Argv()
	return s, warnings, nil
}

// ownedID preserves the legacy final-dot owner rule for dotted plugin ids.
func ownedID(owner, id string) bool {
	at := strings.LastIndexByte(id, '.')
	return at > 0 && id[:at] == owner && at < len(id)-1 && !strings.ContainsAny(id, " \t\r\n\x1b;=\\")
}
func eventPattern(pattern string) bool {
	pattern = strings.TrimSuffix(pattern, ".*")
	if pattern == "" || strings.Contains(pattern, "*") {
		return false
	}
	for _, part := range strings.Split(pattern, ".") {
		if part == "" {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}
func validateDeclarations(s *Spec) error {
	surfaces := make(map[protocol.SurfaceID]bool)
	for _, v := range s.Surfaces {
		if !v.ID.Valid() || surfaces[v.ID] {
			return fmt.Errorf("invalid or duplicate surface %q", v.ID)
		}
		surfaces[v.ID] = true
	}
	seen := make(map[string]bool)
	check := func(kind, id string) error {
		key := kind + ":" + id
		if !ownedID(s.ID, id) || seen[key] {
			return fmt.Errorf("invalid or duplicate %s id %q", kind, id)
		}
		seen[key] = true
		return nil
	}
	for _, a := range s.Actions {
		if err := check("action", a.ID); err != nil {
			return err
		}
		if a.ArgsSchema != nil && !object(a.ArgsSchema) {
			return errors.New("args_schema must be an object")
		}
	}
	for _, e := range s.EventsDeclared {
		if err := check("event", e.Name); err != nil {
			return err
		}
	}
	for _, h := range s.HooksDeclared {
		if err := check("hook", h.ID); err != nil {
			return err
		}
	}
	for _, h := range s.HookRegistrations {
		if !eventPattern(h.ID) || strings.Contains(h.ID, "*") || !strings.Contains(h.ID, ".") {
			return errors.New("invalid hook registration")
		}
	}
	for _, pattern := range s.Subscriptions {
		if !eventPattern(pattern) {
			return fmt.Errorf("invalid event pattern %q", pattern)
		}
	}
	for _, slot := range s.PublishedSlots {
		if err := check("slot", slot.ID); err != nil {
			return err
		}
		switch slot.Kind {
		case "inline":
			if slot.Rect != nil {
				return errors.New("inline slots cannot have rect")
			}
		case "region":
			r := slot.Rect
			if r == nil || r.X < 0 || r.Y < 0 || r.Width <= 0 || r.Height <= 0 || !surfaces[r.Surface] {
				return errors.New("region requires a positive rect on a claimed surface")
			}
		default:
			return errors.New("slot kind must be inline or region")
		}
	}
	for _, secret := range s.SecretRequests {
		if len(secret.Name) == 0 || len(secret.Name) > 64 || !localKey(secret.Name) {
			return errors.New("invalid secret name")
		}
	}
	seenSettings := make(map[string]bool)
	for i := range s.SettingsDeclared {
		v := &s.SettingsDeclared[i]
		prefix := "plugins." + s.ID + "."
		if strings.HasPrefix(v.Key, "plugins.") {
			if !strings.HasPrefix(v.Key, prefix) {
				return errors.New("foreign plugin setting prefix")
			}
			v.Key = strings.TrimPrefix(v.Key, prefix)
		}
		if !localKey(v.Key) || v.Default == nil {
			return errors.New("setting needs a lowercase local key and explicit default")
		}
		v.Key = prefix + v.Key
		if seenSettings[v.Key] {
			return errors.New("duplicate setting key")
		}
		seenSettings[v.Key] = true
		switch v.Type {
		case "string", "int", "bool", "duration", "enum", "strings":
		default:
			return errors.New("invalid setting type")
		}
		if v.Apply == "" {
			v.Apply = "live"
		}
		if v.Apply != "live" && v.Apply != "restart" {
			return errors.New("invalid setting apply policy")
		}
		if v.Min != nil && v.Max != nil && *v.Min > *v.Max {
			return errors.New("setting min exceeds max")
		}
	}
	if v := s.Slice; v != nil {
		if v.Scope == "" {
			v.Scope = "instance"
		}
		if v.Scope != "instance" && v.Scope != "user" && v.Scope != "workspace" {
			return errors.New("invalid slice scope")
		}
		cap := 256 << 10
		if v.Scope == "workspace" {
			cap = 1 << 20
		}
		if v.MaxBytes < 0 || v.MaxBytes > cap {
			return errors.New("invalid slice size cap")
		}
		if v.Persistent != (v.Retention != nil) {
			return errors.New("retention is required iff persistent")
		}
		if r := v.Retention; r != nil {
			switch r.Policy {
			case "keep-latest", "manual":
				if r.N != 0 || r.TTL != 0 {
					return errors.New("extraneous retention fields")
				}
			case "keep-last-n":
				if r.N < 2 || r.N > 20 || r.TTL != 0 {
					return errors.New("invalid generation retention")
				}
			case "ttl":
				if r.N != 0 || r.TTL < int64(time.Minute) || r.TTL > int64(8760*time.Hour) {
					return errors.New("invalid TTL retention")
				}
			default:
				return errors.New("unknown retention policy")
			}
		}
	}
	return nil
}
func object(b []byte) bool { b = bytes.TrimSpace(b); return len(b) > 0 && b[0] == '{' && json.Valid(b) }
func localKey(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}
