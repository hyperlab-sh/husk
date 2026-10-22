// Package manifest defines the plugin configuration shared by Pods. The
// converter task extends Spec with declarations and supplies manifest parsing;
// this process-side slice deliberately implements no file-format conversion.
package manifest

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Spec is the minimal process configuration. Command is resolved argv; WorkDir
// is the absolute manifest directory. The converter task extends this struct
// with actions, surfaces, bindings and other declarations from the Pods spec.
type Spec struct {
	ID          string
	Name        string
	Version     string
	Description string
	Protocol    int
	Command     []string
	WorkDir     string
}

// Clone detaches mutable command arguments. Extend it alongside future fields.
func (s Spec) Clone() Spec { s.Command = slices.Clone(s.Command); return s }

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
