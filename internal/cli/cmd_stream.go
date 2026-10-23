package cli

import (
	"context"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
)

// openingBound is the streaming opening bound (instances.md, "Timeouts"):
// session.ping and the opening reply together. It is not a flag; it is a
// variable only so tests can shorten it.
var openingBound = 5 * time.Second

// watch is the body of a streaming command: it sends the opening request, prints
// the opening reply (the generic rendering in table mode, one compact line in
// JSON mode), then one notification's params per line until the connection ends
// (exit 5 "instance went away") or a signal kills the process. Nothing here
// handles signals, and the stream is never bounded by --timeout.
func (inv *Invocation) watch(ctx context.Context, method string, params any, notify string) error {
	c, err := inv.Connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	st, err := c.Stream(c.Ctx, method, params) // c.Ctx carries the opening bound
	if err != nil {
		return err
	}
	st.Only(notify)
	if inv.out.Format == output.JSON {
		err = inv.out.WriteLine(st.Result)
	} else {
		err = inv.out.Result(st.Result)
	}
	if err != nil {
		return err
	}
	for {
		m, err := st.Next(ctx)
		if err != nil {
			return err
		}
		line := m.Params
		if len(line) == 0 {
			line = []byte("{}")
		}
		if err := inv.out.WriteLine(line); err != nil {
			return err
		}
	}
}
