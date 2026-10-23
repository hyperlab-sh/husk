package cli

import (
	"encoding/json"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func settingsCommand() command {
	list := serviceMethod("husk settings list [--source]", protocol.MethodSettingsList, nil, renderSettingsList)
	list.accepts = []string{"source"}
	schema := serviceMethod("husk settings schema [--owner <owner>]", protocol.MethodSettingsSchema, nil, nil)
	schema.accepts = []string{"owner"}
	return command{usage: "husk settings", verbs: map[string]command{
		"get":  serviceMethod("husk settings get <key>", protocol.MethodSettingsGet, []string{"key"}, nil),
		"list": list, "schema": schema,
		"validate": serviceMethod("husk settings validate", protocol.MethodSettingsValidate, nil, renderValidation),
	}}
}

func renderSettingsList(inv *Invocation, raw json.RawMessage) error {
	if inv.out.Format == output.JSON {
		return inv.out.Result(raw)
	}
	var res struct {
		Entries []struct {
			Key    string          `json:"key"`
			Value  json.RawMessage `json:"value"`
			Source string          `json:"source"`
		} `json:"entries"`
	}
	if err := decodeService(raw, &res); err != nil {
		return err
	}
	rows := make([][]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		value := string(e.Value)
		var text string
		if json.Unmarshal(e.Value, &text) == nil {
			value = text
		}
		row := []string{e.Key, value}
		if inv.source {
			row = append(row, e.Source)
		}
		rows = append(rows, row)
	}
	headers := []string{"KEY", "VALUE"}
	if inv.source {
		headers = append(headers, "SOURCE")
	}
	return serviceTable(inv, headers, rows)
}
