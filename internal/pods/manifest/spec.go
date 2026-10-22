// Package manifest converts plugin packages into process configuration and
// plain declaration data. It imports no Stem, Services or Substrate code.
package manifest

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Spec is the converted process configuration and plugin declaration catalog.
// Command is resolved argv; WorkDir is the absolute manifest directory.
type Spec struct {
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	Version           string             `json:"version"`
	Description       string             `json:"description"`
	Protocol          int                `json:"protocol"`
	Command           []string           `json:"command"`
	WorkDir           string             `json:"work_dir"`
	Actions           []ActionDecl       `json:"actions,omitempty"`
	Surfaces          []SurfaceDecl      `json:"surfaces,omitempty"`
	FocusBindings     []FocusBinding     `json:"focus_bindings,omitempty"`
	Subscriptions     []string           `json:"subscriptions,omitempty"`
	EventsDeclared    []EventDecl        `json:"events_declared,omitempty"`
	HooksDeclared     []HookDecl         `json:"hooks_declared,omitempty"`
	HookRegistrations []HookRegistration `json:"hook_registrations,omitempty"`
	PublishedSlots    []SlotDecl         `json:"published_slots,omitempty"`
	Slice             *SliceDecl         `json:"slice,omitempty"`
	SecretRequests    []SecretRequest    `json:"secret_requests,omitempty"`
	SettingsDeclared  []SettingDecl      `json:"settings_declared,omitempty"`
	BindingProblems   []BindingProblem   `json:"binding_problems,omitempty"`
	Suggestions       []Suggestion       `json:"suggestions,omitempty"`
}

// Clone detaches every mutable slice, raw JSON buffer and declaration pointer.
func (s Spec) Clone() Spec { return cloneSpec(s) }

// Validate checks only fields needed to launch a process.
func (s Spec) Validate() error {
	if !ValidID(s.ID) {
		return fmt.Errorf("invalid plugin id %q", s.ID)
	}
	if s.Protocol != protocol.Major {
		return fmt.Errorf("plugin speaks protocol %d, host speaks %d", s.Protocol, protocol.Major)
	}
	if len(s.Command) == 0 || s.Command[0] == "" {
		return errors.New("empty command")
	}
	if s.WorkDir != "" && !filepath.IsAbs(s.WorkDir) {
		return errors.New("plugin work directory must be absolute")
	}
	return nil
}

// ValidID implements the manifest's 64-character ASCII identifier rule.
func ValidID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i, c := range []byte(id) {
		alpha := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if !alpha && (i == 0 || c != '.' && c != '_' && c != '-') {
			return false
		}
	}
	return true
}

// Argv returns detached argv, resolving explicit relative executable paths
// against WorkDir. Later relative arguments resolve naturally through cmd.Dir;
// bare executable names retain PATH lookup and option arguments are untouched.
func (s Spec) Argv() []string {
	out := slices.Clone(s.Command)
	if len(out) > 0 && !filepath.IsAbs(out[0]) && strings.ContainsAny(out[0], `/\`) && s.WorkDir != "" {
		out[0] = filepath.Join(s.WorkDir, out[0])
	}
	return out
}
