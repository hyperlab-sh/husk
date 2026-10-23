package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// pluginCommand is the plugin noun: list, log, add and remove, each a named
// wrapper over one socket method (docs/docs/design/pods.md, "plugin.* wire
// shapes"). Who may call them is the instance's rule; a plugin's token comes
// back as denied. Add and remove have longer default timeouts for launch/stop
// and the Services reload.
func pluginCommand() command {
	return command{
		usage: "husk plugin",
		verbs: map[string]command{
			"add":    {usage: "husk plugin add <path> | --dir <dir>", timeout: 10 * time.Second, accepts: []string{"dir"}, run: runPluginAdd},
			"list":   {usage: "husk plugin list", timeout: 5 * time.Second, run: runPluginList},
			"log":    {usage: "husk plugin log <id>", timeout: 5 * time.Second, run: runPluginLog},
			"remove": {usage: "husk plugin remove <id>", timeout: 10 * time.Second, run: runPluginRemove},
		},
	}
}

func runPluginAdd(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) > 1 || (len(args) == 1 && inv.flagSet["dir"]) || (len(args) == 0 && !inv.flagSet["dir"]) {
		return output.Usage("plugin add takes exactly one <path> or --dir <dir>")
	}
	key, path := "path", ""
	if inv.flagSet["dir"] {
		key, path = "dir", inv.pluginDir
	} else {
		path = args[0]
	}
	if path == "" {
		return output.Usage("plugin add needs a nonempty path")
	}
	if !filepath.IsAbs(path) {
		getwd := inv.env.Getwd
		if getwd == nil {
			getwd = os.Getwd
		}
		cwd, err := getwd()
		if err != nil {
			return output.Usage("working directory: %v", err)
		}
		path = filepath.Join(cwd, path)
	}
	res, err := pluginRequest(ctx, inv, protocol.MethodPluginAdd, map[string]string{key: filepath.Clean(path)})
	if err != nil {
		return err
	}
	var result struct {
		Added []struct{ Plugin, Status string }
	}
	if err := json.Unmarshal(res, &result); err != nil {
		return output.Protocol(err, "unreadable plugin.add reply: %v", err)
	}
	if err := inv.out.Result(res); err != nil {
		return err
	}
	var failed []string
	for _, added := range result.Added {
		if added.Status == "failed" {
			failed = append(failed, added.Plugin)
		}
	}
	// A failed admitted launch did not fulfill the request to run that plugin.
	// Refusals and reload reports are not failed launch outcomes.
	if len(failed) > 0 {
		return &output.Error{Exit: output.ExitRemote, Code: string(protocol.CodeFailed), Message: "plugins failed to launch: " + strings.Join(failed, ", ")}
	}
	return nil
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
	res, err := pluginRequest(ctx, inv, method, params)
	if err != nil {
		return err
	}
	return inv.out.Result(res)
}

// pluginRequest sends one request without rendering its outcome.
func pluginRequest(ctx context.Context, inv *Invocation, method string, params map[string]string) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, output.Usage("params: %v", err)
	}
	if err := inv.checkRequestSize(method, raw); err != nil {
		return nil, err
	}
	c, err := inv.Connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.Call(c.Ctx, method, params)
}
