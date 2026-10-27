package settings

import (
	"strings"

	c "github.com/hyperlab-sh/husk/pkg/services/contracts"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

// resolveSpellings diagnoses collisions before the merged document can hide
// them. Only ambiguous keys need re-resolution; ordinary Store merge results
// (including object/scalar replacement and tombstones) remain authoritative.
func resolveSpellings(layers []store.LayerDoc, schema map[string]Entry, flat map[string]any, ambiguous map[string]bool, problems *[]c.Problem) ([]store.LayerDoc, map[string]string) {
	clean, diagnostics := c.CleanLayers(layers, schema)
	*problems = append(*problems, diagnostics...)
	values := make([]map[string]any, len(layers))
	for i, layer := range clean {
		values[i] = map[string]any{}
		c.FlattenSettings(layer.Doc, schema, values[i])
	}
	for _, problem := range diagnostics {
		ambiguous[problem.Key] = true
	}
	sources := map[string]string{}
	for key := range ambiguous {
		// Without layer inputs there is no way to distinguish a duplicate
		// within one layer from two valid spellings across layers.
		if len(layers) == 0 {
			continue
		}
		delete(flat, key)
		for i, layer := range layers {
			for ancestor, raw := range values[i] {
				if strings.HasPrefix(key, ancestor+".") {
					if _, object := raw.(map[string]any); !object {
						delete(flat, key)
						delete(sources, key)
					}
				}
			}
			if raw, present := values[i][key]; present {
				if raw == nil {
					delete(flat, key)
					delete(sources, key)
				} else {
					flat[key] = raw
					sources[key] = string(layer.Layer)
				}
			}
		}
	}
	return clean, sources
}
