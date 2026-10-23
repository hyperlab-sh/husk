package settings

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// StoreProblem converts a Store diagnostic to a warning with config-relative
// provenance. files supplies known layer files; configDir is Store's actual
// config root for absolute file names. Files outside that root use their base
// name only. Consumers that do not interpret merge clashes should ignore them.
func StoreProblem(p store.Problem, files map[string]string, configDir string) protocol.Problem {
	layer := string(p.Layer)
	file := files[layer]
	if file == "" && p.File != "" {
		file = filepath.ToSlash(p.File)
		if filepath.IsAbs(p.File) {
			file = filepath.Base(p.File)
			if filepath.IsAbs(configDir) {
				if relative, err := filepath.Rel(configDir, p.File); err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					file = filepath.ToSlash(relative)
				}
			}
		}
	}
	out := protocol.Problem{Severity: "warning", Code: "store_error", Layer: layer, Key: p.Path, Source: &protocol.Source{Layer: layer, File: file, Line: p.Line}}
	if p.Code == "type_clash" {
		out.Code = "type_clash"
		out.Message = fmt.Sprintf("%s: the %s layer replaces a value of a different type from the %s layer", p.Path, p.Layer, p.LowerLayer)
	} else {
		where := file
		if layer != "" {
			where += " (" + layer + ")"
		}
		out.Message = where + ": " + p.Message
	}
	return out
}
