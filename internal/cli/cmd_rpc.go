package cli

import (
	"context"
	"encoding/json"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// runRPC is the escape hatch: any method, params as a JSON object, the raw
// result printed.
func runRPC(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usageErr("rpc takes <method> [params-json]")
	}
	method := args[0]
	if err := protocol.ValidateMethod(method); err != nil {
		return usageErr("invalid method %q: %v", method, err)
	}
	var params json.RawMessage
	if len(args) == 2 {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(args[1]), &obj); err != nil || obj == nil {
			return usageErr("params-json must be a JSON object")
		}
		params = json.RawMessage(args[1])
	}
	c, ctx, _, err := inv.Dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	var p any
	if params != nil {
		p = params
	}
	raw, err := c.Call(ctx, method, p)
	if err != nil {
		return err
	}
	return inv.printResult(raw)
}
