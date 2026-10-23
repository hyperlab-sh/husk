package theme

import (
	"fmt"
	"regexp"
	"strings"

	c "github.com/hyperlab-sh/husk/internal/services/contracts"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	palette "github.com/hyperlab-sh/husk/pkg/theme"
)

var themeName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

type definition struct {
	roles       map[string]string
	layer, file string
}

func resolveThemes(layers []store.LayerDoc, problems *[]c.Problem) map[string]definition {
	out := map[string]definition{}
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
			if name == palette.Default().Name {
				issue("reserved_theme_name", "theme "+name+": built-in name is reserved")
				continue
			}
			roles, reason := themeRoles(l, name, themes[name])
			if reason != "" {
				issue("invalid_theme", "theme "+name+": "+reason)
				continue
			}
			out[name] = definition{roles: roles, layer: string(l.Layer), file: l.File}
		}
	}
	return out
}

func themeRoles(l store.LayerDoc, name string, raw any) (map[string]string, string) {
	if !themeName.MatchString(name) {
		return nil, "invalid name"
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, "want object"
	}
	values, ok := obj["palette"].(map[string]any)
	if !ok {
		return nil, "palette: want object"
	}
	roles := map[string]string{}
	known := palette.Roles(palette.Default())
	// OrderedMembers gives deterministic diagnostics for programmatic maps too.
	for _, role := range l.OrderedMembers("/themes/"+name+"/palette", values) {
		hex, ok := values[role].(string)
		if _, exists := known[role]; !exists || role == "name" {
			return nil, "unknown role " + role
		}
		if !ok {
			return nil, fmt.Sprintf("role %s: want #RRGGBB", role)
		}
		if _, err := palette.RelativeLuminance(hex); err != nil {
			return nil, "role " + role + ": want #RRGGBB"
		}
		roles[role] = strings.ToLower(hex)
	}
	return roles, ""
}
