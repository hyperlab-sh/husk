package store

import "strings"

// WithoutDuplicateSetting returns doc unchanged when key has at most one
// spelling. Otherwise it returns a detached copy with every spelling of that
// key removed. Dots in object members and nesting denote the same setting key.
// It interprets only the requested key, not general Store merge semantics.
func WithoutDuplicateSetting(doc Doc, key string) (Doc, bool) {
	count := 0
	var walk func(Doc, string, bool)
	walk = func(node Doc, remaining string, remove bool) {
		for member, value := range node {
			if member == remaining {
				count++
				if remove {
					delete(node, member)
				}
			} else if rest, ok := strings.CutPrefix(remaining, member+"."); ok {
				if object, ok := value.(map[string]any); ok {
					walk(object, rest, remove)
				}
			}
		}
	}
	walk(doc, key, false)
	if count < 2 {
		return doc, false
	}
	clean := copyDoc(doc)
	walk(clean, key, true)
	return clean, true
}
