package contracts

import (
	"maps"
	"slices"
	"strings"

	"github.com/hyperlab-sh/husk/pkg/substrate/store"
)

// CleanLayers removes all spellings of duplicated settings from detached layer
// documents and returns one diagnostic per key and layer. schema identifies
// scalar settings at which flattening must stop, even for an invalid object.
// Readers with a narrower purpose may pass only the keys they consume.
func CleanLayers(layers []store.LayerDoc, schema map[string]SettingDecl) ([]store.LayerDoc, []Problem) {
	clean := append([]store.LayerDoc(nil), layers...)
	var problems []Problem
	for i, layer := range layers {
		values := map[string]any{}
		duplicates := FlattenSettings(layer.Doc, schema, values)
		for _, key := range slices.Sorted(maps.Keys(duplicates)) {
			clean[i].Doc, _ = store.WithoutDuplicateSetting(clean[i].Doc, key)
			problems = append(problems, Problem{
				Severity: "error", Code: "duplicate_key", Layer: string(layer.Layer), Key: key,
				Message: string(layer.Layer) + " layer: " + key + ": multiple spellings; this layer's value is ignored",
				Source:  &Source{Layer: string(layer.Layer), File: layer.File},
			})
		}
	}
	return clean, problems
}

// FlattenSettings flattens setting spellings and returns colliding keys. It
// stops at schema keys so invalid objects reach validation whole. Empty schema
// prefixes contribute nothing. Lexical traversal makes Doc-only input stable.
func FlattenSettings(doc store.Doc, schema map[string]SettingDecl, out map[string]any) map[string]bool {
	duplicates := map[string]bool{}
	var walk func(string, map[string]any)
	walk = func(prefix string, node map[string]any) {
		for _, name := range slices.Sorted(maps.Keys(node)) {
			value := node[name]
			key := prefix + name
			object, isObject := value.(map[string]any)
			_, known := schema[key]
			if !known && isObject && len(object) != 0 {
				walk(key+".", object)
				continue
			}
			if !known && isObject {
				prefix := false
				for name := range schema {
					prefix = prefix || strings.HasPrefix(name, key+".")
				}
				if prefix {
					continue
				}
			}
			if _, exists := out[key]; exists {
				duplicates[key] = true
			}
			out[key] = value
		}
	}
	walk("", doc)
	return duplicates
}
