package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// Output formats of --format.
const (
	formatTable = "table"
	formatJSON  = "json"
)

// IsTTY reports whether f is a terminal; the default output format keys on it.
func IsTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// writeTable prints rows under headers in aligned columns.
func writeTable(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}

// writeJSON prints one JSON document and a newline: compact when piped,
// indented on a terminal.
func (inv *Invocation) writeJSON(raw []byte) error {
	var out bytes.Buffer
	if inv.env.StdoutIsTTY {
		if err := json.Indent(&out, raw, "", "  "); err != nil {
			return protocolErr(err, "reply is not valid JSON: %v", err)
		}
	} else if err := json.Compact(&out, raw); err != nil {
		return protocolErr(err, "reply is not valid JSON: %v", err)
	}
	out.WriteByte('\n')
	_, err := inv.env.Stdout.Write(out.Bytes())
	return err
}

// printResult prints a raw result in the chosen format. In JSON mode the
// result is the document. In table mode a scalar prints bare, an array of
// objects prints one row per record, an object one "key  value" row per
// member; nested values print as compact JSON.
func (inv *Invocation) printResult(raw json.RawMessage) error {
	if inv.format == formatJSON {
		return inv.writeJSON(raw)
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return protocolErr(err, "reply is not valid JSON: %v", err)
	}
	switch t := v.(type) {
	case map[string]any:
		keys := sortedKeys(t)
		rows := make([][]string, 0, len(keys))
		for _, k := range keys {
			rows = append(rows, []string{k, cell(t[k])})
		}
		return writeRows(inv.env.Stdout, rows)
	case []any:
		if headers, rows, ok := recordRows(t); ok {
			return writeTable(inv.env.Stdout, headers, rows)
		}
		for _, e := range t {
			fmt.Fprintln(inv.env.Stdout, cell(e))
		}
		return nil
	default:
		_, err := fmt.Fprintln(inv.env.Stdout, cell(v))
		return err
	}
}

// writeRows prints key/value rows without a header.
func writeRows(w io.Writer, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}

// recordRows turns an array of objects into columns (the union of keys,
// sorted) and rows; ok is false when the array is empty or holds anything
// but objects.
func recordRows(items []any) (headers []string, rows [][]string, ok bool) {
	if len(items) == 0 {
		return nil, nil, false
	}
	seen := map[string]bool{}
	for _, it := range items {
		obj, isObj := it.(map[string]any)
		if !isObj {
			return nil, nil, false
		}
		for k := range obj {
			seen[k] = true
		}
	}
	for k := range seen {
		headers = append(headers, k)
	}
	sort.Strings(headers)
	for _, it := range items {
		obj := it.(map[string]any)
		row := make([]string, len(headers))
		for i, h := range headers {
			if v, has := obj[h]; has {
				row[i] = cell(v)
			}
		}
		rows = append(rows, row)
	}
	return headers, rows, true
}

// cell renders one value for a table: strings bare, null empty, everything
// else as compact JSON.
func cell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func itoa(n int) string { return fmt.Sprint(n) }

func jsonMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, &Error{Exit: ExitRemote, Code: "error", Message: err.Error(), Err: err}
	}
	return b, nil
}
