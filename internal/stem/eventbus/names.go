package eventbus

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Source values the bus itself attributes.
const (
	// SourceHost is the source of core publishers.
	SourceHost = "husk"
	// SourceUser is the source of a bare-shell CLI emit.
	SourceUser = "user"
)

// UserPrefix is the only namespace a bare-shell CLI caller may emit under.
const UserPrefix = "user."

var coreNames = map[string]struct{}{
	"state.changed":       {},
	"pods.plugin-exited":  {},
	"pods.plugin-removed": {},
	"services.reloaded":   {},
	"secrets.read":        {},
}

var corePrefixes = []string{"state.", "surface.", "input.", "pods.", "services.", "secrets.", UserPrefix, "husk."}

// IsCoreName reports whether name is reserved for core publishers: one of the
// named core events or anything under a core prefix (user. included; the CLI
// has its own rule in Authorize).
func IsCoreName(name string) bool {
	if _, ok := coreNames[name]; ok {
		return true
	}
	for _, p := range corePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func validComponent(c string) bool {
	if c == "" {
		return false
	}
	for _, r := range c {
		if r == '*' || r == '.' || unicode.IsSpace(r) || unicode.IsControl(r) || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// ValidateName reports whether name is a well-formed event name: one or more
// dot-separated, non-empty components without wildcards, spaces or control
// characters.
func ValidateName(name string) error {
	for _, c := range strings.Split(name, ".") {
		if !validComponent(c) {
			return invalid("malformed event name %q", name)
		}
	}
	return nil
}

// ValidatePattern reports whether pattern is an exact event name or a prefix
// ending in ".*". No other wildcard is accepted, so "*foo", "a.*.b", "*" and
// ".*" are refused.
func ValidatePattern(pattern string) error {
	if prefix, ok := strings.CutSuffix(pattern, ".*"); ok {
		if ValidateName(prefix) != nil {
			return invalid("malformed event pattern %q", pattern)
		}
		return nil
	}
	if ValidateName(pattern) != nil {
		return invalid("malformed event pattern %q", pattern)
	}
	return nil
}

// Matches reports whether a valid pattern matches name. "notes.*" matches any
// name that continues "notes." with at least one more component, never
// "notes" itself.
func Matches(pattern, name string) bool {
	if prefix, ok := strings.CutSuffix(pattern, ".*"); ok {
		return len(name) > len(prefix)+1 && strings.HasPrefix(name, prefix) && name[len(prefix)] == '.'
	}
	return pattern == name
}

// Authorize applies the publishing rules of event.emit and returns the source
// to stamp on the event. A plugin may emit only under "<plugin_id>." and never
// under a core name or prefix; the user (CLI or Stem) only under "user.". A
// refusal is a denied error; a malformed name is invalid_params.
func Authorize(kind contracts.CallerKind, pluginID, name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	switch {
	case kind == contracts.CallerPlugin:
		if pluginID == "" || !strings.HasPrefix(name, pluginID+".") || len(name) == len(pluginID)+1 || IsCoreName(name) {
			return "", contracts.NewError(protocol.CodeDenied, "event "+name+" is outside the caller's namespace", nil)
		}
		return pluginID, nil
	case kind.User():
		if !strings.HasPrefix(name, UserPrefix) || len(name) == len(UserPrefix) {
			return "", contracts.NewError(protocol.CodeDenied, "a user may emit only under \"user.\"", nil)
		}
		return SourceUser, nil
	}
	return "", contracts.NewError(protocol.CodeDenied, "unknown caller kind", nil)
}

func invalid(format string, args ...any) error {
	return contracts.NewError(protocol.CodeInvalidParams, fmt.Sprintf(format, args...), nil)
}
