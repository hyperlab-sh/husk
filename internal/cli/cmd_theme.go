package cli

import (
	"encoding/json"
	"sort"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func themeCommand() command {
	return command{usage: "husk theme", verbs: map[string]command{
		"show":  serviceMethod("husk theme show", protocol.MethodThemeGet, nil, renderThemeShow),
		"roles": serviceMethod("husk theme roles", protocol.MethodThemeGet, nil, renderThemeRoles),
		"check": serviceMethod("husk theme check", protocol.MethodThemeCheck, nil, renderValidation),
		"list":  serviceMethod("husk theme list", protocol.MethodThemeList, nil, renderThemeList),
		"set":   setSettingCommand("husk theme set <name>", "theme.name"),
	}}
}

func renderThemeList(inv *Invocation, raw json.RawMessage) error {
	if inv.out.Format == output.JSON {
		return inv.out.Result(raw)
	}
	var res struct {
		Themes []struct {
			Name, Source string
			Current      bool
		} `json:"themes"`
	}
	if err := decodeService(raw, &res); err != nil {
		return err
	}
	rows := make([][]string, 0, len(res.Themes))
	for _, theme := range res.Themes {
		marker := ""
		if theme.Current {
			marker = "*"
		}
		rows = append(rows, []string{theme.Name, theme.Source, marker})
	}
	return serviceTable(inv, []string{"NAME", "SOURCE", "CURRENT"}, rows)
}

func themePalette(raw json.RawMessage) (map[string]string, []string, error) {
	var res struct {
		Palette map[string]string `json:"palette"`
	}
	if err := decodeService(raw, &res); err != nil {
		return nil, nil, err
	}
	roles := make([]string, 0, len(res.Palette))
	for role := range res.Palette {
		if role != "name" {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	return res.Palette, roles, nil
}

func renderThemeShow(inv *Invocation, raw json.RawMessage) error {
	if inv.out.Format == output.JSON {
		return inv.out.Result(raw)
	}
	palette, roles, err := themePalette(raw)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(roles))
	for _, role := range roles {
		rows = append(rows, []string{role, palette[role]})
	}
	return serviceTable(inv, []string{"ROLE", "COLOR"}, rows)
}

func renderThemeRoles(inv *Invocation, raw json.RawMessage) error {
	_, roles, err := themePalette(raw)
	if err != nil {
		return err
	}
	return projected(inv, roles)
}
