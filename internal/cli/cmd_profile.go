package cli

import (
	"encoding/json"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func profileCommand() command {
	return command{usage: "husk profile", verbs: map[string]command{
		"list":    serviceMethod("husk profile list", protocol.MethodProfileList, nil, nil),
		"current": serviceMethod("husk profile current", protocol.MethodProfileList, nil, renderProfileCurrent),
		"use":     serviceMethod("husk profile use <name>", protocol.MethodProfileUse, []string{"name"}, renderReports),
	}}
}

func renderProfileCurrent(inv *Invocation, raw json.RawMessage) error {
	var res struct {
		Profiles []json.RawMessage `json:"profiles"`
	}
	if err := decodeService(raw, &res); err != nil {
		return err
	}
	for _, entry := range res.Profiles {
		var p struct {
			Active bool `json:"active"`
		}
		if err := decodeService(entry, &p); err != nil {
			return err
		}
		if p.Active {
			return inv.out.Result(entry)
		}
	}
	if inv.out.Format == output.JSON {
		return inv.out.WriteJSON([]byte("null"))
	}
	return nil
}
