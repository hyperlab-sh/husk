package settings

import (
	"fmt"
	"path/filepath"
	"strings"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// addPlugins materializes the additive flags as instance values. Resolve only
// the target leaf below instance: arrays replace, null deletes, and replacing
// an ancestor with a scalar also removes the leaf. No input document is mutated.
func (s *Service) addPlugins(next *snapshot, layers []store.LayerDoc, files map[string]string, problems *[]c.Problem) {
	for _, name := range []string{"paths", "dirs"} {
		additions, enabled := s.opts.PluginAdditions[name]
		if !enabled {
			continue
		}
		key := "husk.plugins." + name
		var raw any
		layer := "default"
		for _, l := range layers {
			if l.Layer == store.LayerInstance {
				continue
			}
			node := l.Doc
			for i, part := range strings.Split(key, ".") {
				value, present := node[part]
				if !present {
					break
				}
				if i == 2 {
					raw, layer = value, string(l.Layer)
					break
				}
				object, ok := value.(map[string]any)
				if !ok {
					raw, layer = nil, "default"
					break
				}
				node = object
			}
		}
		base := []string{}
		if raw != nil {
			value, err := validate(next.schema[key], raw)
			if err != nil {
				*problems = append(*problems, c.Problem{Severity: "error", Code: "invalid_value", Layer: layer, Key: key, Message: fmt.Sprintf("%s layer: %s: want %s", layer, key, wantOf(next.schema[key])), Source: sourceOf(layer, files)})
			} else {
				base = value.([]string)
			}
		}
		values := []string{}
		seen := map[string]bool{}
		for _, path := range append(base, additions...) {
			abs, err := filepath.Abs(path)
			if err != nil {
				*problems = append(*problems, c.Problem{Severity: "error", Code: "invalid_value", Layer: "instance", Key: key, Message: key + ": cannot resolve absolute path", Source: sourceOf("instance", files)})
				continue
			}
			if !seen[abs] {
				seen[abs] = true
				values = append(values, path)
			}
		}
		next.values[key] = values
		next.raw[key] = c.Clone(values)
		next.sources[key] = "instance"
	}
}
