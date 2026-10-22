package cli

import (
	"context"
	"time"

	"github.com/hyperlab-sh/husk/internal/instance"
)

// instanceRow is one element of the JSON form of `husk instances`: the
// registration entry plus its liveness.
type instanceRow struct {
	instance.Entry
	Status string `json:"status"`
}

// runInstances lists the runtime directory. It never resolves, so the
// target flags do not apply; stale entries are removed by the survey.
func runInstances(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 0 {
		return usageErr("instances takes no arguments")
	}
	dir, err := inv.env.RuntimeDir()
	if err != nil {
		return runtimeDirErr(err)
	}
	ctx, cancel := inv.withTimeout(ctx)
	defer cancel()
	type surveyed struct {
		st  []instance.Status
		err error
	}
	ch := make(chan surveyed, 1)
	go func() {
		st, err := instance.Survey(dir, inv.env.Deps)
		ch <- surveyed{st, err}
	}()
	var s surveyed
	select {
	case s = <-ch:
	case <-ctx.Done():
		return transportErr(ctx.Err(), "listing instances: %v", ctx.Err())
	}
	if s.err != nil {
		return transportErr(s.err, "read runtime dir: %v", s.err)
	}

	rows := make([]instanceRow, 0, len(s.st))
	for _, st := range s.st {
		rows = append(rows, instanceRow{Entry: st.Entry, Status: st.Verdict.String()})
	}
	if inv.format == formatJSON {
		raw, err := jsonMarshal(rows)
		if err != nil {
			return err
		}
		return inv.writeJSON(raw)
	}
	table := make([][]string, 0, len(rows))
	for _, r := range rows {
		table = append(table, []string{
			r.ID, r.Name, itoa(r.PID), r.StartedAt.UTC().Format(time.RFC3339), r.ProtocolVersion, r.Status,
		})
	}
	return writeTable(inv.env.Stdout, []string{"ID", "NAME", "PID", "STARTED", "PROTO", "STATUS"}, table)
}
