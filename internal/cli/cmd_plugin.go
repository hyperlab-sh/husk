package cli

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// pluginCommand is the plugin noun: list, log and remove, each a named
// wrapper over one socket method (docs/docs/design/pods.md, "plugin.* wire
// shapes"). Who may call them is the instance's rule; a plugin's token comes
// back as denied. Remove has the longer default timeout because it waits for
// the stop policy and a Services reload.
func pluginCommand() command {
	return command{
		usage: "husk plugin",
		verbs: map[string]command{
			"list":   {usage: "husk plugin list", timeout: 5 * time.Second, run: runPluginList},
			"log":    {usage: "husk plugin log <id>", timeout: 5 * time.Second, run: runPluginLog},
			"remove": {usage: "husk plugin remove <id>", timeout: 10 * time.Second, run: runPluginRemove},
		},
	}
}

func runPluginList(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 0 {
		return output.Usage("plugin list takes no arguments")
	}
	return pluginCall(ctx, inv, protocol.MethodPluginList, map[string]string{})
}

func runPluginLog(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 1 || args[0] == "" {
		return output.Usage("plugin log takes one <id>")
	}
	return pluginCall(ctx, inv, protocol.MethodPluginLog, map[string]string{"plugin": args[0]})
}

// runPluginRemove is husk plugin remove: the reply ({plugin, status, log,
// reports}) is printed whatever the reports say; only an error reply is a
// failure.
func runPluginRemove(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 1 || args[0] == "" {
		return output.Usage("plugin remove takes one <id>")
	}
	return pluginCall(ctx, inv, protocol.MethodPluginRemove, map[string]string{"plugin": args[0]})
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
