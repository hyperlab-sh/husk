package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// stateCommand is the state noun: husk state is the descriptor snapshot, the
// verbs are the other state.* wrappers.
func stateCommand() command {
	const t = 5 * time.Second
	return command{
		usage:   "husk state",
		timeout: t,
		run:     runStateSnapshot,
		verbs: map[string]command{
			"get":   {usage: "husk state get <slice> [<pointer>]", timeout: t, run: runStateGet},
			"dump":  {usage: "husk state dump", timeout: t, run: runStateDump},
			"watch": {usage: "husk state watch [<slice>...]", timeout: openingBound, stream: true, run: runStateWatch},
			"set":   {usage: "husk state set <json>", timeout: t, run: runStateSet},
			"patch": {usage: "husk state patch <json> [--if-revision <n>]", timeout: t, accepts: []string{"if-revision"}, run: runStatePatch},
			"clear": {usage: "husk state clear <namespace> [--force]", timeout: t, accepts: []string{"force"}, run: runStateClear},
		},
	}
}

// snapshotSlice is the part of a state.snapshot entry the table shows.
type snapshotSlice struct {
	Slice      string `json:"slice"`
	Owner      string `json:"owner"`
	Scope      string `json:"scope"`
	Persistent bool   `json:"persistent"`
	Retention  *struct {
		Policy string `json:"policy"`
		N      int    `json:"n"`
	} `json:"retention"`
	Revision uint64 `json:"revision"`
	Bytes    int    `json:"bytes"`
}

// runStateSnapshot is husk state: the slice descriptors, without values.
func runStateSnapshot(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 0 {
		return output.Usage("state takes no arguments")
	}
	raw, err := inv.call(ctx, protocol.MethodStateSnapshot, map[string]any{"values": false})
	if err != nil {
		return err
	}
	if inv.out.Format == output.JSON {
		return inv.out.WriteJSON(raw)
	}
	var res struct {
		Slices []snapshotSlice `json:"slices"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return output.Protocol(err, "unreadable reply: state.snapshot result: %v", err)
	}
	rows := make([][]string, 0, len(res.Slices))
	for _, s := range res.Slices {
		retention := ""
		if s.Retention != nil {
			retention = s.Retention.Policy
			if s.Retention.N > 0 {
				retention += ":" + strconv.Itoa(s.Retention.N)
			}
		}
		rows = append(rows, []string{s.Slice, s.Owner, s.Scope, strconv.FormatBool(s.Persistent), retention,
			strconv.FormatUint(s.Revision, 10), strconv.Itoa(s.Bytes)})
	}
	return output.WriteTable(inv.out.Stdout,
		[]string{"SLICE", "OWNER", "SCOPE", "PERSISTENT", "RETENTION", "REVISION", "BYTES"}, rows)
}

// runStateGet is husk state get: the reply in JSON mode, the value as JSON in
// table mode.
func runStateGet(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return output.Usage("state get takes <slice> [<pointer>]")
	}
	params := map[string]any{"slice": args[0]}
	if len(args) == 2 && args[1] != "" {
		params["path"] = args[1]
	}
	raw, err := inv.call(ctx, protocol.MethodStateGet, params)
	if err != nil {
		return err
	}
	if inv.out.Format == output.JSON {
		return inv.out.WriteJSON(raw)
	}
	var res struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || len(res.Value) == 0 {
		return output.Protocol(err, "unreadable reply: state.get result has no value")
	}
	return inv.out.WriteJSON(res.Value)
}

// runStateDump is husk state dump: the snapshot with values.
func runStateDump(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 0 {
		return output.Usage("state dump takes no arguments")
	}
	raw, err := inv.call(ctx, protocol.MethodStateSnapshot, map[string]any{"values": true})
	if err != nil {
		return dumpHint(err)
	}
	return inv.out.Result(raw)
}

// runStateWatch is husk state watch: the baseline, then one state.changed
// params object per line.
func runStateWatch(ctx context.Context, inv *Invocation, args []string) error {
	slices := append([]string{}, args...)
	return inv.watch(ctx, protocol.MethodStateWatch, map[string]any{"slices": slices}, protocol.MethodStateChanged)
}

// runStateSet is husk state set: the JSON object is the request's params
// (slice and value). Whose slice it may write is the instance's rule.
func runStateSet(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 1 {
		return output.Usage("state set takes <json>")
	}
	params, err := inv.objectArg(args[0], "json")
	if err != nil {
		return err
	}
	raw, err := inv.call(ctx, protocol.MethodStateSet, params)
	if err != nil {
		return err
	}
	return inv.out.Result(raw)
}

// runStatePatch is husk state patch: the JSON object is the request's params
// (slice and patch); --if-revision adds if_revision to it.
func runStatePatch(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 1 {
		return output.Usage("state patch takes <json> [--if-revision <n>]")
	}
	params, err := inv.objectArg(args[0], "json")
	if err != nil {
		return err
	}
	if inv.flagSet["if-revision"] {
		rev, err := strconv.ParseUint(inv.ifRevision, 10, 64)
		if err != nil {
			return output.Usage("--if-revision must be a non-negative integer")
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(params, &obj); err != nil {
			return output.Usage("json must be a JSON object")
		}
		obj["if_revision"] = json.RawMessage(strconv.FormatUint(rev, 10))
		if params, err = json.Marshal(obj); err != nil {
			return output.Usage("json: %v", err)
		}
	}
	raw, err := inv.call(ctx, protocol.MethodStatePatch, params)
	if err != nil {
		return err
	}
	return inv.out.Result(raw)
}

// runStateClear is husk state clear: reset one namespace to its initial value.
// --force skips the husk.before-state-clear gate; without it a vetoed clear
// is the instance's denied reply, and the table mode lists each veto.
func runStateClear(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 1 {
		return output.Usage("state clear takes <namespace>")
	}
	params := map[string]any{"namespace": args[0]}
	if inv.force {
		params["force"] = true
	}
	raw, err := inv.call(ctx, protocol.MethodStateClear, params)
	if err != nil {
		return listVetoes(inv, err)
	}
	return inv.out.Result(raw)
}

// listVetoes appends each veto of a vetoed clear to the table-mode message as
// a "plugin_id: reason" line. The error's code, data and exit stay, and the
// JSON message is untouched: its data.vetoes already carries them.
func listVetoes(inv *Invocation, err error) error {
	var oe *output.Error
	if inv.out.Format != output.Table || !errors.As(err, &oe) || oe.Exit != output.ExitRemote || oe.Code != string(protocol.CodeDenied) {
		return err
	}
	var data struct {
		Reason string `json:"reason"`
		Vetoes []struct {
			Plugin string `json:"plugin_id"`
			Reason string `json:"reason"`
		} `json:"vetoes"`
	}
	if json.Unmarshal(oe.Data, &data) != nil || data.Reason != "vetoed" || len(data.Vetoes) == 0 {
		return err
	}
	listed := *oe
	for _, v := range data.Vetoes {
		listed.Message += "\n  " + output.Text(v.Plugin) + ": " + output.Text(v.Reason)
	}
	return &listed
}

// dumpHint points a dump the instance refused as too large at state get
// (state.md): an invalid_params error carrying data.limit gets the pointer
// appended; its code, data and exit stay.
func dumpHint(err error) error {
	var oe *output.Error
	if !errors.As(err, &oe) || oe.Exit != output.ExitRemote || oe.Code != string(protocol.CodeInvalidParams) {
		return err
	}
	var data struct {
		Limit *int `json:"limit"`
	}
	if json.Unmarshal(oe.Data, &data) != nil || data.Limit == nil {
		return err
	}
	hinted := *oe
	hinted.Message += "; use husk state get <slice>"
	return &hinted
}
