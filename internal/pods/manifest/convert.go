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

// MaxFocusBindings is the manifest's document-order focus-binding limit.
const MaxFocusBindings = 128

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
	s := &Spec{WorkDir: src.Dir, Dir: src.Dir, Protocol: protocol.Major}
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
		if fields["protocol"] == nil || bytes.Equal(bytes.TrimSpace(fields["protocol"]), []byte("null")) {
			return nil, nil, errors.New("v2 manifest requires protocol")
		}
		if err := decodeField(fields, "protocol", &s.Protocol); err != nil {
			return nil, nil, err
		}
		for _, f := range []struct {
			key  string
			dest any
		}{{"actions", &s.Actions}, {"surfaces", &s.Surfaces}, {"subscriptions", &s.Subscriptions}, {"events_declared", &s.EventsDeclared}} {
			if err := decodeField(fields, f.key, f.dest); err != nil {
				return nil, nil, err
			}
		}
		s.Subscriptions = dropCLIOnly(s, "subscriptions", s.Subscriptions)
		for i := range s.Surfaces {
			if s.Surfaces[i].ID == protocol.SurfaceAppOverlay && s.Surfaces[i].Frame != nil {
				s.Surfaces[i].Frame = nil
				warnings = append(warnings, Warning{Code: "ignored_field", Field: "surfaces", Index: i, Message: "app-overlay has no frame; draw decorations in its content"})
			}
		}
		// The catalog-form members take the same entry parsers as "hooks".
		var rawDecls, rawRegs []json.RawMessage
		if err := decodeField(fields, "hooks_declared", &rawDecls); err != nil {
			return nil, nil, fmt.Errorf("hooks_declared: %w", err)
		}
		if err := decodeField(fields, "hook_registrations", &rawRegs); err != nil {
			return nil, nil, fmt.Errorf("hook_registrations: %w", err)
		}
		decls, err := parsePublish("hooks_declared", rawDecls)
		if err != nil {
			return nil, nil, err
		}
		regs, err := parseSubscribe("hook_registrations", rawRegs)
		if err != nil {
			return nil, nil, err
		}
		s.HooksDeclared = append(s.HooksDeclared, decls...)
		s.HookRegistrations = append(s.HookRegistrations, regs...)
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
			s.Subscriptions = append(s.Subscriptions, dropCLIOnly(s, "events.subscribe", events.Subscribe)...)
			s.EventsDeclared = append(s.EventsDeclared, events.Publish...)
		}
		if b := fields["hooks"]; b != nil {
			decls, regs, err := parseHooks(b)
			if err != nil {
				return nil, nil, err
			}
			s.HooksDeclared = append(s.HooksDeclared, decls...)
			s.HookRegistrations = append(s.HookRegistrations, regs...)
		}
		var rails []struct {
			RailItemDecl
			Shortcut string `json:"shortcut"`
		}
		if err := decodeField(fields, "rail_items", &rails); err != nil {
			return nil, nil, fmt.Errorf("rail_items: %w", err)
		}
		for i, r := range rails {
			s.RailItems = append(s.RailItems, r.RailItemDecl)
			if r.Shortcut != "" {
				s.Suggestions = append(s.Suggestions, Suggestion{Key: r.Shortcut, Action: r.ID})
				warnings = append(warnings, Warning{Code: "suggestion_ignored", Field: "rail_items", Index: i, Message: "rail item shortcut is a suggestion only; bind a key in a keymap layer"})
			}
		}
		var bindings []struct {
			FocusBinding
			Plugin  json.RawMessage `json:"plugin"`
			Disable json.RawMessage `json:"disable"`
		}
		if err := decodeField(fields, "keybindings", &bindings); err != nil {
			return nil, nil, err
		}
		if fields["focus_bindings"] != nil {
			warnings = append(warnings, Warning{Code: "ignored_field", Field: "focus_bindings", Message: "focus_bindings is not a manifest field; declare surface-scoped keys as keybindings"})
		}
		actions := make(map[string]bool)
		for _, a := range s.Actions {
			actions[a.ID] = true
		}
		surfaces := make(map[string]bool)
		for _, surface := range s.Surfaces {
			surfaces[string(surface.ID)] = true
		}
		overLimit := len(bindings) > MaxFocusBindings
		if overLimit {
			bindings = bindings[:MaxFocusBindings]
		}
		for i, b := range bindings {
			code, message := "", ""
			if b.Focus == "" || !surfaces[b.Focus] || b.Plugin != nil || b.Disable != nil {
				code, message = "global_not_allowed", "focus must be a surface owned by this plugin; plugin/disable fields are not allowed"
			} else if !actions[b.Action] && !(strings.HasPrefix(b.Action, "husk.") && len(b.Action) > 5) {
				code, message = "foreign_action", "focus binding must target an own action or husk.*"
			}
			if code != "" {
				s.BindingProblems = append(s.BindingProblems, BindingProblem{Index: i, Code: code, Message: message})
				warnings = append(warnings, Warning{Code: code, Field: "keybindings", Index: i, Message: message})
				continue
			}
			s.Keybindings = append(s.Keybindings, b.FocusBinding)
		}
		if overLimit {
			message := fmt.Sprintf("focus-local bindings exceed the %d-entry limit; remaining entries dropped", MaxFocusBindings)
			s.BindingProblems = append(s.BindingProblems, BindingProblem{Index: MaxFocusBindings, Code: "too_many", Message: message})
			warnings = append(warnings, Warning{Code: "too_many", Field: "keybindings", Index: MaxFocusBindings, Message: message})
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
		// The v1 wait-for-everyone behaviour is the gate kind.
		for i := range s.HooksDeclared {
			s.HooksDeclared[i].Kind = HookGate
			s.HooksDeclared[i].TimeoutMs = DefaultHookTimeoutMs
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

// dropCLIOnly returns the subscriptions a plugin may hold. secrets.* and
// services.* events reach cli watchers only (services.md, Events), so only the
// offending pattern is dropped and the rest stand. Each drop is a problem on
// the declared document, like a bad binding, indexed into its own field.
func dropCLIOnly(s *Spec, field string, patterns []string) []string {
	kept := make([]string, 0, len(patterns))
	for i, pattern := range patterns {
		if protocol.CLIOnlyEventPattern(pattern) {
			s.SubscriptionProblems = append(s.SubscriptionProblems, BindingProblem{Index: i, Code: "cli_only_event", Key: fmt.Sprintf("%s[%d]", field, i), Message: "event " + pattern + " is delivered to cli watchers only; subscription dropped"})
			continue
		}
		kept = append(kept, pattern)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
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
	for _, item := range s.RailItems {
		// A rail item is addressable as an action by its id, so it shares the
		// action id space.
		if err := check("action", item.ID); err != nil {
			return fmt.Errorf("rail item: %w", err)
		}
		if item.Label == "" || item.Icon == "" {
			return fmt.Errorf("rail item %q needs an icon and a label", item.ID)
		}
		if !item.Placement.Valid() {
			return fmt.Errorf("rail item %q: unknown placement %q (want modal|full-canvas|stage)", item.ID, item.Placement)
		}
	}
	for _, e := range s.EventsDeclared {
		if err := check("event", e.Name); err != nil {
			return err
		}
	}
	if err := validateHooks(s); err != nil {
		return err
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
		if strings.Contains(v.Key, ".") || !localKey(v.Key) || v.Default == nil {
			return errors.New("setting needs a single-segment lowercase local key and explicit default; dotted or already-qualified keys are not allowed")
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
