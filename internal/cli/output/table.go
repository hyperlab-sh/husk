package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
)

// WriteTable prints a header row and rows in aligned columns: columns are
// separated by at least two spaces and no line ends in padding.
func WriteTable(w io.Writer, headers []string, rows [][]string) error {
	return writeAligned(w, append([][]string{headers}, rows...))
}

// WriteRows prints rows without a header, aligned like WriteTable.
func WriteRows(w io.Writer, rows [][]string) error { return writeAligned(w, rows) }

func writeAligned(w io.Writer, rows [][]string) error {
	var widths []int
	for _, r := range rows {
		for i, c := range r {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], displayWidth(c))
		}
	}
	var b strings.Builder
	for _, r := range rows {
		last := len(r) - 1 // the last non-empty cell: nothing is padded after it
		for last > 0 && r[last] == "" {
			last--
		}
		for i, c := range r[:last+1] {
			b.WriteString(c)
			if i < last {
				b.WriteString(strings.Repeat(" ", widths[i]-displayWidth(c)+2))
			}
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Generic renders a JSON result in the generic table form of the spec:
//
//   - a string, number or boolean: the bare value, one line;
//   - null or {}: an empty line;
//   - an object: one row per member, keys sorted: "<key>  <value>";
//   - an array of objects: a header row of the union of keys, sorted, then
//     one row per element (a missing key is an empty cell);
//   - any other array: one element per line (an empty array prints nothing);
//   - a nested object or array in a cell, and an explicit null in a cell:
//     compact JSON.
func Generic(w io.Writer, raw json.RawMessage) error {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return Protocol(err, "unreadable reply: result is not valid JSON: %v", err)
	}
	switch t := v.(type) {
	case nil:
		_, err := fmt.Fprintln(w)
		return err
	case map[string]any:
		if len(t) == 0 {
			_, err := fmt.Fprintln(w)
			return err
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rows := make([][]string, 0, len(keys))
		for _, k := range keys {
			rows = append(rows, []string{k, cell(t[k])})
		}
		return WriteRows(w, rows)
	case []any:
		if headers, rows, ok := recordRows(t); ok {
			return WriteTable(w, headers, rows)
		}
		for _, e := range t {
			if _, err := fmt.Fprintln(w, cell(e)); err != nil {
				return err
			}
		}
		return nil
	case string:
		_, err := fmt.Fprintln(w, t) // a lone string is the result itself, bare as sent
		return err
	default:
		_, err := fmt.Fprintln(w, cell(v))
		return err
	}
}

// recordRows turns an array of objects into columns and rows; ok is false
// when the array is empty or holds anything but objects.
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

// cell renders one value: strings bare, numbers as written, booleans as
// true/false, everything else (null, nested objects and arrays) as compact
// JSON.
func cell(v any) string {
	switch t := v.(type) {
	case string:
		// A control character would break the row: show such a string as
		// its JSON form.
		if strings.IndexFunc(t, unicode.IsControl) >= 0 {
			return compactJSON(t)
		}
		return t
	case json.Number:
		return t.String()
	default:
		return compactJSON(t)
	}
}

func compactJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

// displayWidth is the number of terminal columns s occupies: combining and
// format marks take none, East Asian wide and fullwidth characters and emoji
// take two. The package stays standard-library only, so this is a compact
// range check rather than a full Unicode width table.
func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		case isWide(r):
			n += 2
		default:
			n++
		}
	}
	return n
}

func isWide(r rune) bool {
	return r >= 0x1100 && (r <= 0x115F || // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF && r != 0x303F || // CJK radicals .. Yi
		r >= 0xAC00 && r <= 0xD7A3 || // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF || // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F || // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60 || r >= 0xFFE0 && r <= 0xFFE6 || // fullwidth forms
		r >= 0x1F300 && r <= 0x1F64F || r >= 0x1F900 && r <= 0x1F9FF || // emoji
		r >= 0x20000 && r <= 0x3FFFD) // CJK extensions
}
