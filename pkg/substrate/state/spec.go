package state

import (
	"fmt"
	"strings"
	"time"
)

const (
	// CoreMaxBytes is the default cap for husk-owned, non-workspace slices.
	CoreMaxBytes = 64 * 1024
	// PluginMaxBytes is the default cap for plugin-owned slices.
	PluginMaxBytes = 256 * 1024
	// WorkspaceMaxBytes is the cap for workspace-scope slices.
	WorkspaceMaxBytes = 1024 * 1024
	// HardMaxBytes is the absolute ceiling for any slice.
	HardMaxBytes = 1024 * 1024
	// MaxSlices bounds the number of simultaneously registered slices.
	MaxSlices = 512
	// MaxNamespaceBytes bounds namespace length in bytes.
	MaxNamespaceBytes = 128
)

// ValidNamespace reports whether namespace is <owner>.<name>, split at the
// final dot. Dotted owners are allowed, but empty components, non-ASCII name
// characters and names over MaxNamespaceBytes are not.
func ValidNamespace(namespace string) bool {
	if len(namespace) > MaxNamespaceBytes || !validID(namespace) {
		return false
	}
	return strings.LastIndexByte(namespace, '.') > 0
}

func validID(id string) bool {
	if id == "" || id[0] == '.' || id[len(id)-1] == '.' || strings.Contains(id, "..") {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func validateSpec(spec Spec, workspaceOwner string) (Spec, error) {
	invalid := func(field string) (Spec, error) {
		return Spec{}, fmt.Errorf("%w: %s", ErrInvalidSpec, field)
	}
	if !ValidNamespace(spec.Namespace) {
		return invalid("namespace")
	}
	separator := strings.LastIndexByte(spec.Namespace, '.')
	if spec.Owner != spec.Namespace[:separator] {
		return invalid("owner")
	}
	classCap := PluginMaxBytes
	if spec.Owner == OwnerHusk {
		classCap = CoreMaxBytes
	}
	switch spec.Scope {
	case ScopeUser, ScopeInstance:
	case ScopeWorkspace:
		if spec.Owner != OwnerHusk && (workspaceOwner == "" || spec.Owner != workspaceOwner) {
			return invalid("scope: workspace owner")
		}
		classCap = WorkspaceMaxBytes
	default:
		return invalid("scope")
	}
	if !spec.Persistent {
		if spec.Retention != (Retention{}) {
			return invalid("retention: non-persistent slice")
		}
	} else {
		switch spec.Retention.Policy {
		case KeepLatest, Manual:
			if spec.Retention.N != 0 || spec.Retention.TTL != 0 {
				return invalid("retention: unexpected N or TTL")
			}
		case KeepLastN:
			if spec.Retention.N < 2 || spec.Retention.N > 20 || spec.Retention.TTL != 0 {
				return invalid("retention: N must be 2..20 and TTL zero")
			}
		case TTL:
			if spec.Retention.TTL < time.Minute || spec.Retention.TTL > 8760*time.Hour || spec.Retention.N != 0 {
				return invalid("retention: TTL must be 1m..8760h and N zero")
			}
		default:
			return invalid("retention: policy")
		}
	}
	if spec.MaxBytes < 0 || spec.MaxBytes > classCap || spec.MaxBytes > HardMaxBytes {
		return invalid("max_bytes")
	}
	if spec.MaxBytes == 0 {
		spec.MaxBytes = classCap
	}
	if spec.Initial == nil {
		spec.Initial = []byte("{}")
	}
	initial, err := normalizeValue(spec.Initial, spec.MaxBytes)
	if err != nil {
		return Spec{}, fmt.Errorf("%w: initial: %w", ErrInvalidSpec, err)
	}
	spec.Initial = initial
	return spec, nil
}
