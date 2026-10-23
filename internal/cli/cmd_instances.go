package cli

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/internal/instance"
)

// instanceRow is one element of the JSON form of `husk instances`: the
// registration entry as the spec spells it, plus its liveness.
type instanceRow struct {
	ID              string    `json:"id"`
	PID             int       `json:"pid"`
	Socket          string    `json:"socket"`
	StartedAt       time.Time `json:"started_at"`
	ProtocolVersion string    `json:"protocol_version"`
	HostVersion     string    `json:"host_version"`
	Status          string    `json:"status"`
}

// runInstances lists the registry. It never resolves and takes no target
// flag; stale entries are removed by the survey and do not appear. An absent
// runtime directory is an empty list: nothing is running.
func runInstances(_ context.Context, inv *Invocation, args []string) error {
	if len(args) != 0 {
		return output.Usage("instances takes no arguments")
	}
	dir, found, err := inv.env.RuntimeDir()
	if err != nil {
		return output.Environment(err, "%v", err)
	}
	var statuses []instance.Status
	if found {
		if statuses, err = instance.Survey(dir, inv.env.Deps); err != nil {
			return output.Transport(err, "read runtime dir: %v", err)
		}
	}
	rows := make([]instanceRow, 0, len(statuses))
	for _, st := range statuses {
		if st.Skipped() {
			inv.out.Warn(output.WarningUnresponsive, skipWarning(dir, st))
			continue
		}
		if st.Locked {
			inv.out.Warn(output.WarningUnresponsive, skipWarning(dir, st))
		}
		e := st.Entry
		rows = append(rows, instanceRow{
			ID: e.ID, PID: e.PID, Socket: e.Socket, StartedAt: e.StartedAt,
			ProtocolVersion: e.ProtocolVersion, HostVersion: e.HostVersion, Status: st.Verdict.String(),
		})
	}
	if inv.out.Format == output.JSON {
		raw, err := jsonMarshal(rows)
		if err != nil {
			return err
		}
		return inv.out.WriteJSON(raw)
	}
	cells := make([][]string, 0, len(rows))
	for _, r := range rows {
		cells = append(cells, []string{
			r.ID, strconv.Itoa(r.PID), r.StartedAt.UTC().Format(time.RFC3339), r.ProtocolVersion, r.Status,
		})
	}
	return output.WriteTable(inv.out.Stdout, []string{"ID", "PID", "STARTED", "PROTO", "STATUS"}, cells)
}

func jsonMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, output.Protocol(err, "%v", err)
	}
	return b, nil
}
