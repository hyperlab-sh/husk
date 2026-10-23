package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// hookCommand is the hook noun. It has list only: a hook is fired by its
// owner inside its own operation (docs/docs/design/hooks.md, "CLI surface"),
// so the CLI offers no way to fire one.
func hookCommand() command {
	return command{
		usage: "husk hook",
		verbs: map[string]command{
			"list": {usage: "husk hook list [--owner <id>]", timeout: 5 * time.Second, accepts: []string{"owner"}, run: runHookList},
		},
	}
}

// hookEntry is the part of a hook.list entry the table shows.
type hookEntry struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Kind        string `json:"kind"`
	TimeoutMS   int64  `json:"timeout_ms"`
	Subscribers []struct {
		Plugin   string `json:"plugin_id"`
		Priority int    `json:"priority"`
		Live     bool   `json:"live"`
	} `json:"subscribers"`
}

// runHookList is husk hook list: the catalog sorted by id, filtered by --owner
// in the CLI because the method takes no parameters.
func runHookList(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 0 {
		return output.Usage("hook list takes no arguments")
	}
	raw, err := inv.call(ctx, protocol.MethodHookList, struct{}{})
	if err != nil {
		return err
	}
	var res struct {
		Hooks []json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return output.Protocol(err, "unreadable reply: hook.list result: %v", err)
	}
	type row struct {
		raw  json.RawMessage
		info hookEntry
	}
	rows := make([]row, 0, len(res.Hooks))
	for _, h := range res.Hooks {
		var e hookEntry
		if err := json.Unmarshal(h, &e); err != nil {
			return output.Protocol(err, "unreadable reply: hook.list entry: %v", err)
		}
		if inv.flagSet["owner"] && e.Owner != inv.owner {
			continue
		}
		rows = append(rows, row{h, e})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].info.ID < rows[j].info.ID })

	if inv.out.Format == output.JSON {
		kept := make([]json.RawMessage, len(rows))
		for i, r := range rows {
			kept[i] = r.raw
		}
		out, err := json.Marshal(map[string]any{"hooks": kept})
		if err != nil {
			return output.Protocol(err, "unreadable reply: %v", err)
		}
		return inv.out.WriteJSON(out)
	}
	table := make([][]string, 0, len(rows))
	for _, r := range rows {
		subs := make([]string, 0, len(r.info.Subscribers))
		for _, s := range r.info.Subscribers {
			state := "live"
			if !s.Live {
				state = "not live"
			}
			subs = append(subs, fmt.Sprintf("%s (priority %d, %s)", output.Text(s.Plugin), s.Priority, state))
		}
		table = append(table, []string{output.Text(r.info.ID), output.Text(r.info.Owner), output.Text(r.info.Kind),
			(time.Duration(r.info.TimeoutMS) * time.Millisecond).String(), strings.Join(subs, ", ")})
	}
	return output.WriteTable(inv.out.Stdout, []string{"HOOK", "OWNER", "KIND", "TIMEOUT", "SUBSCRIBERS"}, table)
}
