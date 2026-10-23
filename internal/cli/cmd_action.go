package cli

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// actionCommand is the action noun.
func actionCommand() command {
	return command{
		usage: "husk action",
		verbs: map[string]command{
			"list":   {usage: "husk action list [--owner <id>]", timeout: 5 * time.Second, accepts: []string{"owner"}, run: runActionList},
			"invoke": {usage: "husk action invoke <action-id> [--arg <k>=<v>]... [--args-json <json>]", timeout: 30 * time.Second, accepts: []string{"arg", "args-json"}, run: runActionInvoke},
		},
	}
}

// runActionList is husk action list: the catalog, filtered by --owner in the
// CLI because the method takes no parameters.
func runActionList(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 0 {
		return output.Usage("action list takes no arguments")
	}
	raw, err := inv.call(ctx, protocol.MethodActionList, struct{}{})
	if err != nil {
		return err
	}
	if inv.flagSet["owner"] {
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return output.Protocol(err, "unreadable reply: action.list result: %v", err)
		}
		kept := []json.RawMessage{}
		for _, e := range entries {
			var o struct {
				Owner string `json:"owner"`
			}
			if json.Unmarshal(e, &o) == nil && o.Owner == inv.owner {
				kept = append(kept, e)
			}
		}
		if raw, err = json.Marshal(kept); err != nil {
			return output.Protocol(err, "unreadable reply: %v", err)
		}
	}
	return inv.out.Result(raw)
}

// runActionInvoke is husk action invoke: the action id, with args from the
// --arg pairs (an object of strings) or --args-json, not both.
func runActionInvoke(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 1 {
		return output.Usage("action invoke takes <action-id>")
	}
	if len(inv.args) > 0 && inv.flagSet["args-json"] {
		return output.Usage("--arg and --args-json are mutually exclusive")
	}
	params := map[string]any{"action": args[0]}
	switch {
	case len(inv.args) > 0:
		pairs := map[string]string{}
		for _, kv := range inv.args {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return output.Usage("--arg %q: use <k>=<v> with a non-empty key", kv)
			}
			pairs[k] = v
		}
		params["args"] = pairs
	case inv.flagSet["args-json"]:
		obj, err := inv.objectArg(inv.argsJSON, "args-json")
		if err != nil {
			return err
		}
		params["args"] = obj
	}
	raw, err := inv.call(ctx, protocol.MethodActionInvoke, params)
	if err != nil {
		return err
	}
	return inv.out.Result(raw)
}
