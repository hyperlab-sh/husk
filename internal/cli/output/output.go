// Package output is the leaf under every husk command package: the --format
// handling, the error object and warning lines, the exit codes, the generic
// table rendering and the Command type each command package produces
// (docs/docs/design/instances.md, "Output" and "Dependency directions"). It
// imports the standard library and pkg/protocol only, so the socket commands
// in internal/cli and the local commands in other packages share it without
// importing each other.
package output

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Format is the value of --format.
type Format string

// The two output formats.
const (
	JSON  Format = "json"
	Table Format = "table"
)

// ParseFormat validates a --format value.
func ParseFormat(s string) (Format, error) {
	switch f := Format(s); f {
	case JSON, Table:
		return f, nil
	}
	return "", Usage("invalid --format %q: use json or table", s)
}

// Options is what a command runs with: the resolved format and the process
// streams. Results go to Stdout, everything else to Stderr.
type Options struct {
	// Format is the resolved output format for stdout and stderr alike.
	Format Format
	// Stdout carries results and nothing else; Stderr errors, warnings and
	// usage text; Stdin is for commands that read a payload.
	Stdout, Stderr io.Writer
	Stdin          io.Reader
	// StdoutIsTTY says whether Stdout is a terminal: JSON is indented then.
	StdoutIsTTY bool
	// Getenv reads an environment variable.
	Getenv func(string) string
}

// Command is the shape every command package produces: one noun, its usage
// line and its run function. args are the positional arguments left after
// the global flags.
type Command struct {
	// Noun is the first argument that selects the command ("store").
	Noun string
	// Usage is the synopsis shown in the usage text.
	Usage string
	// Run executes the command and returns nil or an error, normally an
	// *Error; any other error is reported as a transport failure.
	Run func(ctx context.Context, args []string, o Options) error
}

// WriteJSON prints one JSON document and a newline: indented on a terminal,
// compact otherwise.
func (o Options) WriteJSON(raw []byte) error {
	var out bytes.Buffer
	var err error
	if o.StdoutIsTTY {
		err = json.Indent(&out, raw, "", "  ")
	} else {
		err = json.Compact(&out, raw)
	}
	if err != nil {
		return Protocol(err, "unreadable reply: %v", err)
	}
	out.WriteByte('\n')
	_, err = o.Stdout.Write(out.Bytes())
	return err
}

// WriteLine prints one compact JSON document and a newline, whatever the
// terminal: the line format of a stream.
func (o Options) WriteLine(raw []byte) error {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return Protocol(err, "unreadable reply: %v", err)
	}
	out.WriteByte('\n')
	_, err := o.Stdout.Write(out.Bytes())
	return err
}

// Result prints a raw JSON result in the chosen format: the document itself
// in JSON mode, the generic rendering in table mode.
func (o Options) Result(raw json.RawMessage) error {
	if o.Format == JSON {
		return o.WriteJSON(raw)
	}
	return Generic(o.Stdout, raw)
}

// Warn prints one warning line to stderr: a {"warning": {...}} object in JSON
// mode, "husk: <message>" in table mode.
func (o Options) Warn(code, message string) {
	if o.Format == JSON {
		o.line(map[string]any{"warning": map[string]any{"code": code, "message": message}})
		return
	}
	fmt.Fprintf(o.Stderr, "husk: %s\n", message)
}

// line prints v as one compact JSON line on stderr, without HTML escaping:
// a message naming "<id>" stays readable.
func (o Options) line(v any) {
	enc := json.NewEncoder(o.Stderr)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// Report prints err as the command's one error and returns its exit code.
func (o Options) Report(err error) int {
	e := AsError(err)
	if o.Format == JSON {
		body := map[string]any{"code": e.Code, "message": e.Message}
		if len(e.Data) > 0 {
			body["data"] = e.Data
		}
		o.line(map[string]any{"error": body})
		return e.Exit
	}
	var b strings.Builder
	fmt.Fprintf(&b, "husk: %s\n", e.Message)
	width := 0
	for _, c := range e.Candidates {
		width = max(width, len(c.ID))
	}
	for _, c := range e.Candidates {
		fmt.Fprintf(&b, "  %-*s  started %s\n", width, c.ID, c.StartedAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
	}
	io.WriteString(o.Stderr, b.String())
	return e.Exit
}
