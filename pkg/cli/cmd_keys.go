package cli

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func keysCommand() command {
	return command{usage: "husk keys [--action <id>] [--focus <surface>] [--shadowed] [--problems]", timeout: 5 * time.Second, accepts: []string{"action", "focus", "shadowed", "problems"}, run: runKeys}
}

func runKeys(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 0 {
		return output.Usage("keys takes no arguments")
	}
	if inv.problems && (inv.flagSet["action"] || inv.flagSet["focus"]) {
		return output.Usage("--problems cannot be combined with --action or --focus")
	}
	raw, err := inv.call(ctx, protocol.MethodKeysList, struct{}{})
	if err != nil {
		return err
	}
	var res struct {
		Entries  []json.RawMessage  `json:"entries"`
		Problems []protocol.Problem `json:"problems"`
	}
	if err := decodeService(raw, &res); err != nil {
		return err
	}
	if res.Problems == nil {
		res.Problems = []protocol.Problem{}
	}
	if inv.problems {
		if inv.out.Format == output.JSON {
			return projected(inv, map[string]any{"problems": res.Problems})
		}
		return problemTable(inv, res.Problems, true)
	}
	kept := make([]json.RawMessage, 0, len(res.Entries))
	rows := make([][]string, 0, len(res.Entries))
	for _, entry := range res.Entries {
		var e struct {
			Keys       []string         `json:"keys"`
			Action     string           `json:"action"`
			Focus      string           `json:"focus"`
			Plugin     string           `json:"plugin"`
			Disable    bool             `json:"disable"`
			State      string           `json:"state"`
			Source     protocol.Source  `json:"source"`
			ShadowedBy *protocol.Source `json:"shadowed_by"`
		}
		if err := decodeService(entry, &e); err != nil {
			return err
		}
		if (!inv.shadowed && e.State == "shadowed") || (inv.flagSet["action"] && e.Action != inv.action) || (inv.flagSet["focus"] && (e.Focus == "" || e.Focus != inv.focus)) {
			continue
		}
		kept = append(kept, entry)
		scope := "global"
		if e.Focus != "" {
			scope = e.Focus
			if e.Plugin != "" {
				scope += " (" + e.Plugin + ")"
			}
		}
		action := e.Action
		if e.Disable {
			action = "(disabled)"
		}
		state := e.State
		if e.ShadowedBy != nil {
			state = "shadowed by " + e.ShadowedBy.Layer + " " + bindingSource(*e.ShadowedBy)
		}
		rows = append(rows, []string{strings.Join(e.Keys, " "), scope, action, e.Source.Layer, bindingSource(e.Source), state})
	}
	if inv.out.Format == output.JSON {
		return projected(inv, map[string]any{"entries": kept, "problems": res.Problems})
	}
	if len(rows) == 0 {
		return nil
	}
	return serviceTable(inv, []string{"KEY", "SCOPE", "ACTION", "LAYER", "SOURCE", "STATE"}, rows)
}
