package settings

import (
	"strings"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

// resolveSpellings diagnoses collisions before the merged document can hide
// them. Only ambiguous keys need re-resolution; ordinary Store merge results
// (including object/scalar replacement and tombstones) remain authoritative.
func resolveSpellings(layers []store.LayerDoc, schema map[string]Entry, flat map[string]any, ambiguous map[string]bool, problems *[]c.Problem) ([]store.LayerDoc, map[string]string) {
	values := make([]map[string]any, len(layers))
	duplicates := make([]map[string]bool, len(layers))
	clean := append([]store.LayerDoc(nil), layers...)
	for i, layer := range layers {
		values[i] = map[string]any{}
		duplicates[i] = flatten("", layer.Doc, schema, values[i])
		for key := range duplicates[i] {
			ambiguous[key] = true
			*problems = append(*problems, c.Problem{
				Severity: "error", Code: "duplicate_key", Layer: string(layer.Layer), Key: key,
				Message: string(layer.Layer) + " layer: " + key + ": multiple spellings; this layer's value is ignored",
				Source:  &c.Source{Layer: string(layer.Layer), File: layer.File},
			})
		}
		if len(duplicates[i]) != 0 {
			clean[i].Doc = c.Clone(layer.Doc)
			for key := range duplicates[i] {
				removeSpellings("", clean[i].Doc, key)
			}
		}
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
			if duplicates[i][key] {
				continue
			}
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

// removeSpellings deletes only the colliding leaf paths from a detached layer,
// so the bundle and additive-launch readers cannot reintroduce a refused value.
func removeSpellings(prefix string, doc map[string]any, target string) {
	for name, value := range doc {
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		if key == target {
			delete(doc, name)
		} else if object, ok := value.(map[string]any); ok && strings.HasPrefix(target, key+".") {
			removeSpellings(key, object, target)
		}
	}
}
