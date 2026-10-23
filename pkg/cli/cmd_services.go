package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// serviceMethod checks only positional syntax. Semantic validation belongs to
// the instance; each invocation makes one method call after the version ping.
func serviceMethod(usage, method string, names []string, render func(*Invocation, json.RawMessage) error) command {
	return command{usage: usage, timeout: 5 * time.Second, run: func(ctx context.Context, inv *Invocation, args []string) error {
		if len(args) != len(names) {
			return output.Usage("usage: %s", usage)
		}
		params := map[string]string{}
		for i, name := range names {
			params[name] = args[i]
		}
		if method == protocol.MethodSettingsSchema && inv.flagSet["owner"] {
			params["owner"] = inv.owner
		}
		raw, err := inv.call(ctx, method, params)
		if err != nil {
			return err
		}
		if render != nil {
			return render(inv, raw)
		}
		return inv.out.Result(raw)
	}}
}

func decodeService(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return output.Protocol(err, "unreadable reply: service result")
	}
	return nil
}

func projected(inv *Invocation, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return output.Protocol(err, "unreadable reply: projection")
	}
	return inv.out.Result(raw)
}

// serviceTable is the single route for custom cells through the shared escaper.
func serviceTable(inv *Invocation, headers []string, rows [][]string) error {
	for _, row := range rows {
		for i := range row {
			row[i] = output.Text(row[i])
		}
	}
	return output.WriteTable(inv.out.Stdout, headers, rows)
}

func problemTable(inv *Invocation, problems []protocol.Problem, source bool) error {
	if len(problems) == 0 {
		return nil
	}
	headers := []string{"SEVERITY", "CODE", "LAYER", "KEY"}
	if source {
		headers = append(headers, "SOURCE")
	}
	headers = append(headers, "MESSAGE")
	rows := make([][]string, 0, len(problems))
	for _, p := range problems {
		row := []string{p.Severity, p.Code, p.Layer, p.Key}
		if source {
			s := ""
			if p.Source != nil {
				s = bindingSource(*p.Source)
			}
			row = append(row, s)
		}
		rows = append(rows, append(row, p.Message))
	}
	return serviceTable(inv, headers, rows)
}

func renderValidation(inv *Invocation, raw json.RawMessage) error {
	if inv.out.Format == output.JSON {
		return inv.out.Result(raw)
	}
	var res struct {
		OK       bool               `json:"ok"`
		Problems []protocol.Problem `json:"problems"`
	}
	if err := decodeService(raw, &res); err != nil {
		return err
	}
	if err := output.WriteRows(inv.out.Stdout, [][]string{{"ok", strconv.FormatBool(res.OK)}}); err != nil {
		return err
	}
	return problemTable(inv, res.Problems, false)
}

func renderReports(inv *Invocation, raw json.RawMessage) error {
	if inv.out.Format == output.JSON {
		return inv.out.Result(raw)
	}
	var res struct {
		Reports []struct {
			Service  string             `json:"service"`
			Gen      uint64             `json:"gen"`
			Applied  bool               `json:"applied"`
			Changed  bool               `json:"changed"`
			Problems []protocol.Problem `json:"problems"`
		} `json:"reports"`
	}
	if err := decodeService(raw, &res); err != nil {
		return err
	}
	for _, rep := range res.Reports {
		if err := serviceTable(inv, []string{"SERVICE", "GEN", "APPLIED", "CHANGED"}, [][]string{{rep.Service, fmt.Sprint(rep.Gen), strconv.FormatBool(rep.Applied), strconv.FormatBool(rep.Changed)}}); err != nil {
			return err
		}
		if err := problemTable(inv, rep.Problems, false); err != nil {
			return err
		}
	}
	return nil
}

func bindingSource(s protocol.Source) string {
	if s.Plugin != "" {
		return fmt.Sprintf("plugin %s #%d", s.Plugin, s.Order)
	}
	file := s.File
	if s.Layer == "husk" && file == "" {
		file = "builtin"
	}
	return fmt.Sprintf("%s#%d", file, s.Order)
}
