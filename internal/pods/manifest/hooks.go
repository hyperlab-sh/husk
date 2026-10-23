package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const maxHookIDLen = 256

var integerLiteral = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// wellFormedHookID is the one id rule of hooks.md (Terms): at least two
// non-empty dot-separated segments, no whitespace, ';', '=', backslash or '*',
// at most 256 bytes.
func wellFormedHookID(id string) bool {
	if id == "" || len(id) > maxHookIDLen {
		return false
	}
	if strings.ContainsFunc(id, unicode.IsSpace) || strings.ContainsAny(id, ";=\\*\x00\x1b") {
		return false
	}
	parts := strings.Split(id, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}

// parseHooks decodes the v2 "hooks" member and writes the defaults: timeout_ms
// 10000 and priority 10. It checks shapes only; validateHooks checks values.
func parseHooks(raw json.RawMessage) ([]HookDecl, []HookRegistration, error) {
	var block struct {
		Publish   []json.RawMessage `json:"publish"`
		Subscribe []json.RawMessage `json:"subscribe"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil, nil, fmt.Errorf("hooks: %w", err)
	}
	var decls []HookDecl
	for i, entry := range block.Publish {
		var d struct {
			ID          string          `json:"id"`
			Kind        string          `json:"kind"`
			Title       string          `json:"title"`
			Description string          `json:"description"`
			TimeoutMs   json.RawMessage `json:"timeout_ms"`
		}
		if err := json.Unmarshal(entry, &d); err != nil {
			return nil, nil, fmt.Errorf("hooks.publish[%d]: %w", i, err)
		}
		timeout := DefaultHookTimeoutMs
		if len(d.TimeoutMs) > 0 {
			n, err := jsonInt(d.TimeoutMs)
			if err != nil {
				return nil, nil, fmt.Errorf("hooks.publish[%d].timeout_ms: %w", i, err)
			}
			if n != 0 {
				timeout = n
			}
		}
		decls = append(decls, HookDecl{ID: d.ID, Kind: HookKind(d.Kind), Title: d.Title, Description: d.Description, TimeoutMs: timeout})
	}
	var regs []HookRegistration
	for i, entry := range block.Subscribe {
		reg := HookRegistration{Priority: DefaultHookPriority}
		if trimmed := bytes.TrimSpace(entry); len(trimmed) > 0 && trimmed[0] == '"' {
			if err := json.Unmarshal(trimmed, &reg.ID); err != nil {
				return nil, nil, fmt.Errorf("hooks.subscribe[%d]: %w", i, err)
			}
		} else {
			var o struct {
				ID       string          `json:"id"`
				Priority json.RawMessage `json:"priority"`
			}
			if err := json.Unmarshal(entry, &o); err != nil {
				return nil, nil, fmt.Errorf("hooks.subscribe[%d]: want a hook id or an {id, priority} object: %w", i, err)
			}
			reg.ID = o.ID
			if len(o.Priority) > 0 {
				n, err := jsonInt(o.Priority)
				if err != nil {
					return nil, nil, fmt.Errorf("hooks.subscribe[%d].priority: %w", i, err)
				}
				reg.Priority = n
			}
		}
		regs = append(regs, reg)
	}
	return decls, regs, nil
}

// jsonInt accepts only a plain JSON integer literal (no fraction, exponent or string).
func jsonInt(raw json.RawMessage) (int, error) {
	lit := string(bytes.TrimSpace(raw))
	if !integerLiteral.MatchString(lit) {
		return 0, fmt.Errorf("must be a JSON integer, got %s", lit)
	}
	var n int
	if err := json.Unmarshal([]byte(lit), &n); err != nil {
		return 0, errors.New("integer out of range")
	}
	return n, nil
}

// validateHooks checks the hook declarations of a converted Spec. Each error
// names the offending field.
func validateHooks(s *Spec) error {
	seen := make(map[string]bool)
	for i, h := range s.HooksDeclared {
		field := fmt.Sprintf("hooks.publish[%d]", i)
		switch {
		case !wellFormedHookID(h.ID):
			return fmt.Errorf("%s.id: %q is not a well-formed hook id", field, h.ID)
		case h.ID == "husk" || strings.HasPrefix(h.ID, "husk."):
			return fmt.Errorf("%s.id: %q is in the reserved husk namespace", field, h.ID)
		case !strings.HasPrefix(h.ID, s.ID+".") || len(h.ID) == len(s.ID)+1:
			return fmt.Errorf("%s.id: %q must be %q followed by a name", field, h.ID, s.ID+".")
		case seen[h.ID]:
			return fmt.Errorf("%s.id: duplicate hook id %q", field, h.ID)
		case !h.Kind.Valid():
			return fmt.Errorf("%s.kind: %q must be transform, collect or gate", field, h.Kind)
		case h.TimeoutMs < MinHookTimeoutMs || h.TimeoutMs > MaxHookTimeoutMs:
			return fmt.Errorf("%s.timeout_ms: %d outside %d..%d", field, h.TimeoutMs, MinHookTimeoutMs, MaxHookTimeoutMs)
		}
		seen[h.ID] = true
	}
	subs := make(map[string]bool)
	for i, r := range s.HookRegistrations {
		field := fmt.Sprintf("hooks.subscribe[%d]", i)
		switch {
		case !wellFormedHookID(r.ID):
			return fmt.Errorf("%s.id: %q is not a well-formed hook id", field, r.ID)
		case subs[r.ID]:
			return fmt.Errorf("%s.id: duplicate subscription to %q", field, r.ID)
		case r.Priority < MinHookPriority || r.Priority > MaxHookPriority:
			return fmt.Errorf("%s.priority: %d outside %d..%d", field, r.Priority, MinHookPriority, MaxHookPriority)
		}
		subs[r.ID] = true
	}
	return nil
}
