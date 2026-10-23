package theme

import (
	"regexp"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
)

var themeName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

type definition struct {
	roles       map[string]string
	indices     map[string]int
	layer, file string
}

func resolveThemes(layers []store.LayerDoc, problems *[]c.Problem) map[string]definition {
	out := map[string]definition{}
	known := palette.Roles(palette.Default())
	layers = append([]store.LayerDoc{{Layer: layerBuiltIn, Doc: palette.BuiltIn()}}, layers...)
	for _, l := range layers {
		themes, _ := l.Doc["themes"].(map[string]any)
		for i, name := range l.OrderedMembers("/themes", themes) {
			issue := func(code, message string) {
				*problems = append(*problems, c.Problem{Severity: "warning", Code: code, Layer: string(l.Layer), Key: "themes." + name, Message: message, Source: &protocol.Source{Layer: string(l.Layer), File: l.File}})
			}
			if i == 64 {
				issue("too_many_themes", "more than 64 themes; the rest ignored")
				break
			}
			roles, indices, reason := themeRoles(l, name, themes[name], known)
			if reason != "" {
				issue("invalid_theme", "theme "+name+": "+reason)
				continue
			}
			out[name] = definition{roles: roles, indices: indices, layer: string(l.Layer), file: l.File}
		}
	}
	return out
}

const layerBuiltIn = "built-in"

func themeRoles(l store.LayerDoc, name string, raw any, known map[string]string) (map[string]string, map[string]int, string) {
	if !themeName.MatchString(name) {
		return nil, nil, "invalid name"
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, "want object"
	}
	values, ok := obj["palette"].(map[string]any)
	if !ok {
		return nil, nil, "palette: want object"
	}
	roles := map[string]string{}
	indices := map[string]int{}
	// OrderedMembers gives deterministic diagnostics for programmatic maps too.
	for _, role := range l.OrderedMembers("/themes/"+name+"/palette", values) {
		if _, exists := known[role]; !exists || role == "name" {
			return nil, nil, "unknown role " + role
		}
		hex, index, err := palette.ResolveValue(values[role])
		if err != nil {
			return nil, nil, "role " + role + ": " + err.Error()
		}
		roles[role] = hex
		if index >= 0 {
			indices[role] = index
		}
	}
	return roles, indices, ""
}
