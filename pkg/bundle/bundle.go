// Package bundle validates application bundles and exposes their read-only config.
// It starts no process and writes no file.
package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Diagnostic codes describe bundle loading failures and warnings.
const (
	CodeNotFound             = "bundle_not_found"
	CodeUnreadable           = "bundle_unreadable"
	CodeManifestMissing      = "bundle_manifest_missing"
	CodeManifestInvalid      = "bundle_manifest_invalid"
	CodeManifestVersion      = "bundle_manifest_version"
	CodeIDInvalid            = "bundle_id_invalid"
	CodePathRelative         = "bundle_path_relative"
	CodePluginsNotADirectory = "bundle_plugins_not_a_directory"
	CodeNoPlugins            = "bundle_no_plugins"
	CodeSettingIgnored       = "bundle_setting_ignored"
)

// Manifest is the bundle's identity; Version is informational only.
type Manifest struct{ ID, Name, Description, Version string }

// Bundle is a validated directory, its plugin source and its config filesystem.
type Bundle struct {
	Dir        string
	Manifest   Manifest
	PluginsDir string
	Config     fs.FS
}

// Warning is a nonfatal loading diagnostic.
type Warning struct{ Code, Message string }

// Error carries a stable diagnostic code and a human-readable message.
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

// Load validates dir before any host persistence or process launch takes place.
// Directory symlinks are followed. Missing or empty plugins are only a warning.
func Load(dir string) (*Bundle, []Warning, error) {
	fail := func(code, message string) (*Bundle, []Warning, error) { return nil, nil, &Error{code, message} }
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fail(CodeUnreadable, err.Error())
	}
	st, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !st.IsDir() {
		return fail(CodeNotFound, "not found")
	}
	if err != nil {
		return fail(CodeUnreadable, err.Error())
	}
	// Listing also detects unreadable config-only bundles before Store opens.
	if _, err = os.ReadDir(abs); err != nil {
		return fail(CodeUnreadable, err.Error())
	}
	f, err := os.Open(filepath.Join(abs, "husk-bundle.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return fail(CodeManifestMissing, "no husk-bundle.json; a directory of plugins is loaded with --plugin-dir")
	}
	if err != nil {
		return fail(CodeUnreadable, err.Error())
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil {
		return fail(CodeUnreadable, err.Error())
	}
	invalid := func(field, message string) (*Bundle, []Warning, error) {
		return fail(CodeManifestInvalid, "husk-bundle.json: "+field+": "+message)
	}
	if len(raw) > 64*1024 {
		return invalid("manifest", "exceeds 64 KiB")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return invalid("manifest", "must be a JSON object")
	}
	version, present := fields["manifest_version"]
	if present {
		var n int
		if bytes.Equal(bytes.TrimSpace(version), []byte("null")) || json.Unmarshal(version, &n) != nil {
			return invalid("manifest_version", "must be an integer")
		}
		if n != 1 {
			return fail(CodeManifestVersion, fmt.Sprintf("manifest_version %s is not supported by this husk (want 1)", version))
		}
	} else {
		return fail(CodeManifestVersion, "manifest_version <absent> is not supported by this husk (want 1)")
	}
	m := Manifest{}
	for _, field := range []struct {
		name  string
		dst   *string
		limit int
		runes bool
	}{
		{"id", &m.ID, 0, false}, {"name", &m.Name, 128, true}, {"description", &m.Description, 1024, false}, {"version", &m.Version, 64, true},
	} {
		value, ok := fields[field.name]
		if !ok {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, field.dst) != nil {
			return invalid(field.name, "must be a string")
		}
		size := len(*field.dst)
		unit := "bytes"
		if field.runes {
			size = utf8.RuneCountInString(*field.dst)
			unit = "runes"
		}
		if field.limit > 0 && size > field.limit {
			return invalid(field.name, fmt.Sprintf("exceeds %d %s", field.limit, unit))
		}
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(m.ID) {
		return fail(CodeIDInvalid, fmt.Sprintf("id %q is invalid: use [A-Za-z0-9][A-Za-z0-9._-]{0,63}", m.ID))
	}
	if _, ok := fields["name"]; !ok {
		m.Name = m.ID
	}
	b := &Bundle{Dir: abs, Manifest: m, Config: configFS{os.DirFS(abs)}}
	plugins := filepath.Join(abs, "plugins")
	st, err = os.Stat(plugins)
	if err == nil && !st.IsDir() {
		return fail(CodePluginsNotADirectory, "plugins is not a directory")
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail(CodeUnreadable, err.Error())
	}
	var entries []os.DirEntry
	if err == nil {
		entries, err = os.ReadDir(plugins)
		if err != nil {
			return fail(CodeUnreadable, err.Error())
		}
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			b.PluginsDir = plugins
			break
		}
	}
	if b.PluginsDir == "" {
		return b, []Warning{{CodeNoPlugins, "bundle " + m.ID + ": no plugins"}}, nil
	}
	return b, nil, nil
}

type configFS struct{ root fs.FS }

func hidden(name string) bool {
	first, _, _ := strings.Cut(name, "/")
	return first == "husk-bundle.json" || first == "plugins" || first == "README.md"
}
func (c configFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if hidden(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	f, err := c.root.Open(name)
	if err != nil {
		return nil, err
	}
	if name == "." {
		return &configDir{File: f}, nil
	}
	return f, nil
}

type configDir struct {
	fs.File
	entries []fs.DirEntry
	loaded  bool
}

func (d *configDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if !d.loaded {
		entries, err := d.File.(fs.ReadDirFile).ReadDir(-1)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !hidden(e.Name()) {
				d.entries = append(d.entries, e)
			}
		}
		d.loaded = true
	}
	if n > 0 && len(d.entries) == 0 {
		return nil, io.EOF
	}
	count := len(d.entries)
	if n > 0 {
		count = min(n, count)
	}
	out := d.entries[:count]
	d.entries = d.entries[count:]
	return out, nil
}
