package settings

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// StoreProblem converts a Store diagnostic to a warning with config-relative
// provenance. files supplies the known layer files, when available. Consumers
// that do not interpret merge clashes should ignore type_clash diagnostics.
func StoreProblem(p store.Problem, files map[string]string) protocol.Problem {
	layer := string(p.Layer)
	file := files[layer]
	if file == "" && p.File != "" {
		file = filepath.ToSlash(p.File)
		if filepath.IsAbs(p.File) {
			file = filepath.Base(p.File)
			for _, prefix := range []string{"/profiles/", "/instances/"} {
				if i := strings.LastIndex(filepath.ToSlash(p.File), prefix); i >= 0 {
					file = filepath.ToSlash(p.File)[i+1:]
					break
				}
			}
		}
	}
	out := protocol.Problem{Severity: "warning", Code: "store_error", Layer: layer, Key: p.Path, Source: &protocol.Source{Layer: layer, File: file, Line: p.Line}}
	if p.Code == "type_clash" {
		out.Code = "type_clash"
		out.Message = fmt.Sprintf("%s: the %s layer replaces a value of a different type from the %s layer", p.Path, p.Layer, p.LowerLayer)
	} else {
		out.Message = fmt.Sprintf("%s (%s): %s", file, layer, p.Message)
	}
	return out
}
