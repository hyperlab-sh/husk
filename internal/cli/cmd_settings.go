package cli

import (
	"context"
	"encoding/json"
	"time"

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
		"set":      setSettingCommand("husk settings set <key> <value>", ""),
	}}
}

// setSettingCommand shares the write path with theme set. A fixed key means
// the sole argument is always a string; general settings values prefer JSON.
func setSettingCommand(usage, fixedKey string) command {
	return command{usage: usage, timeout: 5 * time.Second, run: func(ctx context.Context, inv *Invocation, args []string) error {
		want := 1
		if fixedKey == "" {
			want = 2
		}
		if len(args) != want {
			return output.Usage("usage: %s", usage)
		}
		key := fixedKey
		var value any = args[len(args)-1]
		if key == "" {
			key = args[0]
			if json.Valid([]byte(args[1])) {
				value = json.RawMessage(args[1])
			}
		}
		raw, err := inv.call(ctx, protocol.MethodSettingsSet, map[string]any{"key": key, "value": value})
		if err != nil {
			return err
		}
		return renderReports(inv, raw)
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
		if len(e.Value) > 0 && e.Value[0] == '"' && json.Unmarshal(e.Value, &text) == nil {
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
