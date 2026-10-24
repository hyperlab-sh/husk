package manifest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

const converterDeadline = 5 * time.Second

// ExecOptions supplies deterministic time and execution seams. Nil fields use
// the system clock and a one-shot OS process. Configure before registration.
type ExecOptions struct {
	Clock protocol.Clock
	Run   func(context.Context, string, []string, []string, io.Writer, io.Writer) error
}

// ExecConverter adapts an executable's detect, convert and discover commands.
// It is safe for concurrent conversions after construction.
type ExecConverter struct {
	path string
	opts ExecOptions
}

// NewExecConverter resolves an absolute executable or a bare PATH name.
func NewExecConverter(path string, opts ExecOptions) (*ExecConverter, error) {
	if !filepath.IsAbs(path) && strings.ContainsAny(path, `/\\`) {
		return nil, fmt.Errorf("converter_unavailable: %q must be absolute or on PATH", path)
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return nil, fmt.Errorf("converter_unavailable: %q: %w", path, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("converter_unavailable: %q: %w", path, err)
	}
	return &ExecConverter{path: resolved, opts: opts}, nil
}

// Path returns the resolved absolute executable path used for registration identity.
func (c *ExecConverter) Path() string { return c.path }

// Format identifies the external converter without teaching the host a format.
func (c *ExecConverter) Format() string { return "exec:" + filepath.Base(c.path) }

// Detect captures failures as a claimed source so Registry reports them rather
// than silently treating a broken converter as a decline.
func (c *ExecConverter) Detect(dir string) (Source, bool) {
	_, _, err := c.run("detect", dir)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return Source{}, false
	}
	return Source{Path: dir, Dir: dir, err: err}, true
}

// Convert feeds the unchanged output through the built-in v2 converter.
func (c *ExecConverter) Convert(src Source) (*Spec, []Warning, error) {
	if src.err != nil {
		return nil, nil, src.err
	}
	out, stderr, err := c.run("convert", src.Dir)
	if err != nil {
		return nil, nil, err
	}
	spec, warnings, err := (V2Converter{}).Convert(Source{Path: src.Dir, Dir: src.Dir, Bytes: out})
	if err != nil {
		return nil, nil, converterFailure("convert", "exit status 0", stderr, err)
	}
	// External converters must supply an absolute executable, not depend on the
	// host's PATH or on normalization of a relative command.
	var doc struct {
		Command []string `json:"command"`
	}
	raw, _ := standardize(out) // already validated by the v2 converter above
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Command) == 0 || !filepath.IsAbs(doc.Command[0]) {
		return nil, nil, converterFailure("convert", "exit status 0", stderr, errors.New("command must be absolute argv"))
	}
	scanner := bufio.NewScanner(bytes.NewReader(stderr))
	scanner.Buffer(make([]byte, 4096), maxManifestBytes)
	for scanner.Scan() {
		var p struct{ Code, Message, Field string }
		if json.Unmarshal(scanner.Bytes(), &p) == nil && p.Code != "" {
			w := Warning{Code: "converter:" + p.Code, Message: p.Message, Field: p.Field}
			warnings = append(warnings, w)
			spec.Problems = append(spec.Problems, w)
		}
	}
	return spec, warnings, nil
}

// Discover returns at most 256 absolute directories. Any malformed or excess
// output rejects this discovery only; explicitly loaded plugins are unaffected.
func (c *ExecConverter) Discover() ([]string, error) {
	out, stderr, err := c.run("discover")
	if err != nil {
		return nil, err
	}
	var dirs []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 4096), maxManifestBytes)
	for scanner.Scan() {
		dir := scanner.Text() // bufio.ScanLines already removes a trailing carriage return.
		if dir == "" {
			continue
		}
		if len(dirs) == 256 || !filepath.IsAbs(dir) {
			return nil, converterFailure("discover", "exit status 0", stderr, errors.New("expected at most 256 absolute directories"))
		}
		dirs = append(dirs, filepath.Clean(dir))
	}
	if err := scanner.Err(); err != nil {
		return nil, converterFailure("discover", "exit status 0", stderr, err)
	}
	return dirs, nil
}

type converterOutput struct {
	// Do not embed Buffer: its promoted ReadFrom lets io.Copy bypass Write.
	buffer   bytes.Buffer
	exceeded bool
}

func (b *converterOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.buffer.Len()+n > maxManifestBytes {
		b.exceeded = true
		p = p[:maxManifestBytes-b.buffer.Len()]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

func (c *ExecConverter) run(args ...string) ([]byte, []byte, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var timer protocol.Timer
	if c.opts.Clock != nil {
		timer = c.opts.Clock.AfterFunc(converterDeadline, cancel)
	} else {
		timer = time.AfterFunc(converterDeadline, cancel)
	}
	defer timer.Stop()
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HUSK_") {
			env = append(env, entry)
		}
	}
	env = append(env, "HUSK_PROTOCOL_VERSION="+protocol.Version)
	var stdout, stderr converterOutput
	run := c.opts.Run
	if run == nil {
		run = func(ctx context.Context, path string, args, env []string, stdout, stderr io.Writer) error {
			cmd := exec.CommandContext(ctx, path, args...)
			cmd.Env, cmd.Stdout, cmd.Stderr = env, stdout, stderr
			// Bound inherited output pipes if a faulty converter leaves a child behind.
			cmd.WaitDelay = 100 * time.Millisecond
			return cmd.Run()
		}
	}
	err := run(ctx, c.path, args, env, &stdout, &stderr)
	status := "exit status 0"
	if err != nil {
		status = err.Error()
	}
	if ctx.Err() != nil {
		err = errors.New("deadline exceeded after 5s")
	}
	if err == nil && (stdout.exceeded || stderr.exceeded) {
		err = errors.New("output exceeds 1 MiB")
	}
	if err != nil {
		return nil, stderr.buffer.Bytes(), converterFailure(args[0], status, stderr.buffer.Bytes(), err)
	}
	return stdout.buffer.Bytes(), stderr.buffer.Bytes(), nil
}

// ConverterError identifies an isolated executable-converter failure.
type ConverterError struct {
	cause   error
	message string
}

// Error includes the command, exit status and first stderr line.
func (e *ConverterError) Error() string { return e.message }

// Unwrap exposes the execution or document failure.
func (e *ConverterError) Unwrap() error { return e.cause }

func converterFailure(command, status string, stderr []byte, cause error) error {
	first, _, _ := strings.Cut(string(stderr), "\n")
	return &ConverterError{cause: cause, message: fmt.Sprintf("converter_failed: %s (%s), stderr %q: %v", command, status, strings.TrimSuffix(first, "\r"), cause)}
}

var _ Converter = (*ExecConverter)(nil)
