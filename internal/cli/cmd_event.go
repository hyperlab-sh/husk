package cli

import (
	"context"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// eventCommand is the event noun.
func eventCommand() command {
	return command{
		usage: "husk event",
		verbs: map[string]command{
			"emit":  {usage: "husk event emit <name> [--data <json>]", timeout: 5 * time.Second, accepts: []string{"data"}, run: runEventEmit},
			"watch": {usage: "husk event watch <pattern>", timeout: openingBound, stream: true, run: runEventWatch},
		},
	}
}

// runEventEmit is husk event emit: the reply {} says the event is on the bus.
// Which names a caller may emit is the instance's rule.
func runEventEmit(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 1 {
		return output.Usage("event emit takes <name>")
	}
	params := map[string]any{"name": args[0]}
	if inv.flagSet["data"] {
		data, err := inv.objectArg(inv.data, "data")
		if err != nil {
			return err
		}
		params["data"] = data
	}
	raw, err := inv.call(ctx, protocol.MethodEventEmit, params)
	if err != nil {
		return err
	}
	return inv.out.Result(raw)
}

// runEventWatch is husk event watch: one event.deliver params object per line.
func runEventWatch(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 1 {
		return output.Usage("event watch takes <pattern>")
	}
	return inv.watch(ctx, protocol.MethodEventWatch, map[string]any{"pattern": args[0]}, protocol.MethodEventDeliver)
}
