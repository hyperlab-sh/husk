package cli

import (
	"context"
	"encoding/json"
	"io"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// runRPC is the escape hatch: any method name of the right shape, params as a
// JSON object (`-` reads it from stdin; `{}` when absent), the raw result
// printed. Whether the method exists is the instance's answer.
func runRPC(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return output.Usage("rpc takes <method> [<params-json>]")
	}
	method := args[0]
	if err := protocol.ValidateMethod(method); err != nil {
		return output.Usage("invalid method %q: %v", method, err)
	}
	params := json.RawMessage(`{}`)
	if len(args) == 2 {
		var err error
		if params, err = inv.objectArg(args[1], "params-json"); err != nil {
			return err
		}
	}
	if err := inv.checkRequestSize(method, params); err != nil {
		return err
	}
	c, err := inv.Connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	raw, err := c.Call(c.Ctx, method, params)
	if err != nil {
		return err
	}
	return inv.out.Result(raw)
}

// objectArg reads a JSON argument: the literal "-" reads stdin. It must be a
// JSON object, else exit 2 before connecting.
func (inv *Invocation) objectArg(arg, what string) (json.RawMessage, error) {
	text := []byte(arg)
	if arg == "-" {
		b, err := io.ReadAll(io.LimitReader(inv.env.Stdin, protocol.MaxLineBytes+1))
		if err != nil {
			return nil, output.Usage("read %s from stdin: %v", what, err)
		}
		if len(b) > protocol.MaxLineBytes {
			return nil, output.Usage("request exceeds the 4 MiB line limit")
		}
		text = b
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(text, &obj); err != nil || obj == nil {
		return nil, output.Usage("%s must be a JSON object", what)
	}
	return json.RawMessage(text), nil
}

// checkRequestSize is exit 2 for a request the CLI can see is over the
// protocol's line limit, before anything is sent.
func (inv *Invocation) checkRequestSize(method string, params json.RawMessage) error {
	id, err := protocol.NewIntID(2) // the command's request follows the ping
	if err != nil {
		return output.Protocol(err, "request id: %v", err)
	}
	b, err := json.Marshal(protocol.Message{ID: &id, Method: method, Params: params, Token: inv.env.Getenv(EnvPluginToken)})
	if err != nil {
		return output.Usage("params: %v", err)
	}
	if len(b) > protocol.MaxLineBytes {
		return output.Usage("request exceeds the 4 MiB line limit")
	}
	return nil
}
