package cli

import (
	"context"
	"strconv"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
)

// runPing prints the session.ping reply of the connection's own version
// check: that request is the command, so nothing more is sent.
func runPing(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 0 {
		return output.Usage("ping takes no arguments")
	}
	c, err := inv.Connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if inv.out.Format == output.JSON {
		return inv.out.WriteJSON(c.RawPing)
	}
	p := c.Ping
	uptime := (time.Duration(p.UptimeMS) * time.Millisecond).Round(time.Second)
	return output.WriteTable(inv.out.Stdout,
		[]string{"ID", "PID", "PROTO", "HOST", "UPTIME"},
		[][]string{{p.ID, strconv.Itoa(p.PID), p.ProtocolVersion, p.HostVersion, uptime.String()}})
}
