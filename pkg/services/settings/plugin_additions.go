package settings

import (
	"fmt"
	"path/filepath"
	"strings"

	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

// addPlugins materializes the additive flags as instance values. Resolve only
// the target leaf across config layers, including the named-instance file:
// arrays replace, null deletes, and a scalar ancestor removes the leaf. Invalid
// instance-file arrays retain the base below them. No input document is mutated.
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
			node := l.Doc
			for i, part := range strings.Split(key, ".") {
				value, present := node[part]
				if !present {
					break
				}
				if i == 2 {
					if l.Layer == store.LayerInstance && value != nil {
						if _, err := validate(next.schema[key], value); err != nil {
							*problems = append(*problems, c.Problem{Severity: "error", Code: "invalid_value", Layer: string(l.Layer), Key: key, Message: fmt.Sprintf("%s layer: %s: want %s", l.Layer, key, wantOf(next.schema[key])), Source: &c.Source{Layer: string(l.Layer), File: pluginConfigFile(l)}})
							break
						}
					}
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
				*problems = append(*problems, c.Problem{Severity: "error", Code: "invalid_value", Layer: "instance", Key: key, Message: key + ": cannot resolve absolute path", Source: &c.Source{Layer: "instance"}})
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

// pluginConfigFile identifies the saved value even when an unrelated flag
// document was folded into the instance layer. Matching replace/add flags are
// rejected by the CLI, so the additive key cannot have come from that document.
func pluginConfigFile(layer store.LayerDoc) string {
	if layer.File != "" {
		return layer.File
	}
	return layer.InstanceFile
}
