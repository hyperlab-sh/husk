package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// runPing sends session.ping to the resolved instance, checks the protocol
// major and prints the reply.
func runPing(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 0 {
		return usageErr("ping takes no arguments")
	}
	c, ctx, _, err := inv.Dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	raw, err := c.Call(ctx, protocol.MethodSessionPing, protocol.SessionPingParams{})
	if err != nil {
		return err
	}
	var res protocol.SessionPingResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return protocolErr(err, "unreadable session.ping reply: %v", err)
	}
	if err := checkMajor(res.ProtocolVersion); err != nil {
		return err
	}
	if inv.format == formatJSON {
		return inv.writeJSON(raw)
	}
	uptime := (time.Duration(res.UptimeMS) * time.Millisecond).Round(time.Millisecond)
	return writeTable(inv.env.Stdout,
		[]string{"ID", "PID", "PROTO", "HOST", "UPTIME"},
		[][]string{{res.ID, strconv.Itoa(res.PID), res.ProtocolVersion, res.HostVersion, fmt.Sprint(uptime)}})
}
