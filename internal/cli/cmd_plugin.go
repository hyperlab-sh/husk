package cli

import (
	"context"
	"encoding/json"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// runPlugin dispatches `husk plugin <verb>`: list and log, each a named
// wrapper over one socket method (docs/docs/design/pods.md, "plugin.* wire
// shapes"). Who may call them is the instance's rule; a plugin's token comes
// back as denied.
func runPlugin(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) == 0 {
		return output.Usage("plugin takes a verb: list or log <id>")
	}
	switch verb, rest := args[0], args[1:]; verb {
	case "list":
		if len(rest) != 0 {
			return output.Usage("plugin list takes no arguments")
		}
		return pluginCall(ctx, inv, protocol.MethodPluginList, map[string]string{})
	case "log":
		if len(rest) != 1 || rest[0] == "" {
			return output.Usage("plugin log takes one <id>")
		}
		return pluginCall(ctx, inv, protocol.MethodPluginLog, map[string]string{"plugin": rest[0]})
	default:
		return output.Usage("unknown plugin verb %q: use list or log <id>", verb)
	}
}

// pluginCall sends one request and prints the reply's result: the document in
// JSON, the generic rendering in table mode.
func pluginCall(ctx context.Context, inv *Invocation, method string, params map[string]string) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return output.Usage("params: %v", err)
	}
	if err := inv.checkRequestSize(method, raw); err != nil {
		return err
	}
	c, err := inv.Connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	res, err := c.Call(c.Ctx, method, params)
	if err != nil {
		return err
	}
	return inv.out.Result(res)
}
