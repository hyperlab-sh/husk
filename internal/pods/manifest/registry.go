package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the built-in manifest filename.
const FileName = "husk-plugin.json"

// Source is a manifest snapshot or an implicit executable/argv description.
type Source struct {
	Path, Dir string
	Bytes     []byte
	Command   []string
	err       error
}

// Warning is a nonfatal conversion diagnostic, with a zero-based entry index.
type Warning struct {
	Code, Field, Message string
	Index                int
}

// Converter claims a directory and converts only its captured input.
type Converter interface {
	Format() string
	Detect(string) (Source, bool)
	Convert(Source) (*Spec, []Warning, error)
}

// Registry loads plugin arguments through converters in registration order.
type Registry interface {
	Register(Converter)
	Load(string) (*Spec, []Warning, error)
	LoadDir(string) ([]Result, error)
}

// Result isolates one directory's conversion failure from its siblings.
type Result struct {
	Spec     *Spec
	Warnings []Warning
	Err      error
}

// Loader is the concrete ordered converter registry; configure it before use.
type Loader struct{ converters []Converter }

// NewRegistry registers the supplied converters, or v2 then v1 by default.
func NewRegistry(converters ...Converter) *Loader {
	if len(converters) == 0 {
		converters = []Converter{V2Converter{}, V1Converter{}}
	}
	r := &Loader{}
	for _, c := range converters {
		r.Register(c)
	}
	return r
}

// Register appends a converter; the first directory claim wins.
func (r *Loader) Register(c Converter) {
	if c == nil {
		panic("manifest: nil converter")
	}
	r.converters = append(r.converters, c)
}
func (r *Loader) detect(dir string) (*Spec, []Warning, error, bool) {
	for _, c := range r.converters {
		if src, ok := c.Detect(dir); ok {
			s, w, e := c.Convert(src)
			if e != nil {
				e = fmt.Errorf("manifest %s (%s): %w", src.Path, c.Format(), e)
			}
			return s, w, e, true
		}
	}
	return nil, nil, nil, false
}

// Load resolves a directory, manifest path, bare executable or quoted command.
func (r *Loader) Load(arg string) (*Spec, []Warning, error) {
	st, err := os.Stat(arg)
	if err == nil {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return nil, nil, err
		}
		if st.IsDir() {
			s, w, e, ok := r.detect(abs)
			if !ok {
				return nil, nil, fmt.Errorf("no supported manifest in %s", arg)
			}
			return s, w, e
		}
		if filepath.Base(abs) == FileName {
			s, w, e, ok := r.detect(filepath.Dir(abs))
			if !ok {
				return nil, nil, errors.New("unsupported manifest")
			}
			return s, w, e
		}
		return r.implicit(Source{Dir: filepath.Dir(abs), Command: []string{abs}})
	}
	if !os.IsNotExist(err) {
		return nil, nil, err
	}
	argv, err := splitCommand(arg)
	if err != nil {
		return nil, nil, err
	}
	if len(argv) < 2 {
		return nil, nil, fmt.Errorf("plugin path %s not found", arg)
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	return r.implicit(Source{Dir: dir, Command: argv})
}
func (r *Loader) implicit(src Source) (*Spec, []Warning, error) {
	for _, c := range r.converters {
		if c.Format() == "husk-plugin.json/v1" {
			return c.Convert(src)
		}
	}
	return nil, nil, errors.New("implicit plugins require a v1 converter")
}

// LoadDir loads immediate claimed subdirectories; a bad plugin fails only itself.
func (r *Loader) LoadDir(dir string) ([]Result, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	var out []Result
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		s, w, e, ok := r.detect(filepath.Join(abs, entry.Name()))
		if ok {
			out = append(out, Result{s, w, e})
		}
	}
	return out, nil
}
func source(dir string) (Source, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Source{}, false
	}
	path := filepath.Join(abs, FileName)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return Source{}, false
	}
	src := Source{Path: path, Dir: abs}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		src.err = err
		return src, true
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		src.err = err
		return src, true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || !filepath.IsLocal(rel) {
		src.err = errors.New("manifest escapes its plugin directory")
		return src, true
	}
	st, err := os.Stat(target)
	if err != nil {
		src.err = err
		return src, true
	}
	if !st.Mode().IsRegular() {
		src.err = errors.New("manifest is not a regular file")
		return src, true
	}
	f, err := os.Open(path)
	if err != nil {
		src.err = err
		return src, true
	}
	defer f.Close()
	// A manifest is config, bounded like Store's config documents.
	src.Bytes, err = readManifest(f)
	src.err = err
	return src, true
}

// V2Converter handles explicit manifest_version 2 files.
type V2Converter struct{}

// Format identifies the v2 file format.
func (V2Converter) Format() string { return "husk-plugin.json/v2" }

// Detect claims only an explicit numeric version 2; bad files fall to v1 errors.
func (V2Converter) Detect(dir string) (Source, bool) {
	src, ok := source(dir)
	if !ok || src.err != nil {
		return Source{}, false
	}
	var h struct {
		Version int `json:"manifest_version"`
	}
	raw, err := standardize(src.Bytes)
	return src, err == nil && json.Unmarshal(raw, &h) == nil && h.Version == 2
}

// Convert validates and normalizes an explicitly versioned manifest snapshot.
func (V2Converter) Convert(src Source) (*Spec, []Warning, error) { return convert(src, true) }

// V1Converter handles all other built-in files and implicit plugin forms.
type V1Converter struct{}

// Format identifies the legacy file format.
func (V1Converter) Format() string { return "husk-plugin.json/v1" }

// Detect claims any existing built-in manifest, including malformed/unreadable ones.
func (V1Converter) Detect(dir string) (Source, bool) { return source(dir) }

// Convert maps legacy declarations and warns about ignored language/transport.
func (V1Converter) Convert(src Source) (*Spec, []Warning, error) { return convert(src, false) }

// splitCommand preserves the old loader's quotes, escapes and empty arguments.
func splitCommand(line string) ([]string, error) {
	var parts []string
	var cur strings.Builder
	var quote rune
	inArg, escaped := false, false
	for _, r := range line {
		switch {
		case escaped:
			if quote == '"' && r != '"' && r != '\\' {
				cur.WriteRune('\\')
			}
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inArg = true
		case r == '\\':
			escaped = true
			inArg = true
		case r == ' ' || r == '\t':
			if inArg {
				parts = append(parts, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	if escaped {
		return nil, errors.New("trailing backslash")
	}
	if inArg {
		parts = append(parts, cur.String())
	}
	return parts, nil
}

var _ Registry = (*Loader)(nil)
