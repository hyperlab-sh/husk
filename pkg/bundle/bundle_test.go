package bundle

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const validManifest = `{"manifest_version":1,"id":"Example.bundle"}`

func directory(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	if manifest != "" {
		put(t, filepath.Join(dir, "husk-bundle.json"), manifest)
	}
	return dir
}
func put(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFatalCodesAndMessages(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, code, message string
		file, missing, plugins        bool
	}{
		{name: "missing directory", missing: true, code: CodeNotFound, message: "not found"},
		{name: "directory is file", file: true, code: CodeNotFound, message: "not found"},
		{name: "missing manifest", code: CodeManifestMissing, message: "no husk-bundle.json; a directory of plugins is loaded with --plugin-dir"},
		{name: "not JSON", manifest: `{`, code: CodeManifestInvalid, message: "husk-bundle.json: manifest: must be a JSON object"},
		{name: "array", manifest: `[]`, code: CodeManifestInvalid, message: "husk-bundle.json: manifest: must be a JSON object"},
		{name: "null", manifest: `null`, code: CodeManifestInvalid, message: "husk-bundle.json: manifest: must be a JSON object"},
		{name: "version 2", manifest: `{"manifest_version":2}`, code: CodeManifestVersion, message: "manifest_version 2 is not supported by this husk (want 1)"},
		{name: "version absent", manifest: `{}`, code: CodeManifestVersion, message: "manifest_version <absent> is not supported by this husk (want 1)"},
		{name: "version string", manifest: `{"manifest_version":"1"}`, code: CodeManifestInvalid, message: "husk-bundle.json: manifest_version: must be an integer"},
		{name: "version null", manifest: `{"manifest_version":null}`, code: CodeManifestInvalid, message: "husk-bundle.json: manifest_version: must be an integer"},
		{name: "id missing", manifest: `{"manifest_version":1}`, code: CodeIDInvalid, message: `id "" is invalid: use [A-Za-z0-9][A-Za-z0-9._-]{0,63}`},
		{name: "id empty", manifest: `{"manifest_version":1,"id":""}`, code: CodeIDInvalid, message: `id "" is invalid: use [A-Za-z0-9][A-Za-z0-9._-]{0,63}`},
		{name: "id spaces", manifest: `{"manifest_version":1,"id":"My Bundle"}`, code: CodeIDInvalid, message: `id "My Bundle" is invalid: use [A-Za-z0-9][A-Za-z0-9._-]{0,63}`},
		{name: "id 65", manifest: fmt.Sprintf(`{"manifest_version":1,"id":%q}`, strings.Repeat("a", 65)), code: CodeIDInvalid, message: fmt.Sprintf(`id %q is invalid: use [A-Za-z0-9][A-Za-z0-9._-]{0,63}`, strings.Repeat("a", 65))},
		{name: "plugins file", manifest: validManifest, plugins: true, code: CodePluginsNotADirectory, message: "plugins is not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := directory(t, tc.manifest)
			if tc.file {
				dir = filepath.Join(dir, "file")
				put(t, dir, "")
			}
			if tc.missing {
				dir = filepath.Join(dir, "missing")
			}
			if tc.plugins {
				put(t, filepath.Join(dir, "plugins"), "")
			}
			b, w, err := Load(dir)
			var diagnostic *Error
			if !errors.As(err, &diagnostic) || diagnostic.Code != tc.code || err.Error() != tc.message || b != nil || len(w) != 0 {
				t.Fatalf("Load = %+v, %+v, %v; want %s: %s", b, w, err, tc.code, tc.message)
			}
		})
	}
}

func TestManifestFieldTypesAndLimits(t *testing.T) {
	for _, tc := range []struct {
		field string
		limit int
		unit  string
	}{{"id", 0, ""}, {"name", 128, "runes"}, {"description", 1024, "bytes"}, {"version", 64, "runes"}} {
		t.Run(tc.field, func(t *testing.T) {
			for _, value := range []string{`1`, `null`, `true`, `[]`, `{}`} {
				raw := fmt.Sprintf(`{"manifest_version":1,"id":"ok",%q:%s}`, tc.field, value)
				_, _, err := Load(directory(t, raw))
				want := "husk-bundle.json: " + tc.field + ": must be a string"
				if err == nil || err.Error() != want {
					t.Fatalf("%s: %v want %s", value, err, want)
				}
			}
			if tc.limit == 0 {
				return
			}
			for _, n := range []int{tc.limit, tc.limit + 1} {
				text := strings.Repeat("é", n)
				if tc.unit == "bytes" {
					text = strings.Repeat("a", n)
				}
				raw := fmt.Sprintf(`{"manifest_version":1,"id":"ok",%q:%q}`, tc.field, text)
				_, _, err := Load(directory(t, raw))
				if n == tc.limit && err != nil {
					t.Fatal(err)
				}
				if n > tc.limit && (err == nil || err.Error() != fmt.Sprintf("husk-bundle.json: %s: exceeds %d %s", tc.field, tc.limit, tc.unit)) {
					t.Fatalf("limit: %v", err)
				}
			}
		})
	}
}

func TestManifestSizeBoundaryAndUnknownFields(t *testing.T) {
	raw := validManifest + strings.Repeat(" ", 64*1024-len(validManifest))
	for _, extra := range []string{"", " "} {
		b, _, err := Load(directory(t, raw+extra))
		if extra == "" {
			if err != nil || b.Manifest.Name != "Example.bundle" {
				t.Fatalf("boundary: %+v %v", b, err)
			}
		} else {
			var e *Error
			if !errors.As(err, &e) || e.Code != CodeManifestInvalid || e.Message != "husk-bundle.json: manifest: exceeds 64 KiB" {
				t.Fatalf("oversize: %v", err)
			}
		}
	}
	b, _, err := Load(directory(t, `{"manifest_version":1,"id":"ok","future":{"a":[1]},"name":"","version":"1","description":"desc"}`))
	if err != nil || b.Manifest != (Manifest{ID: "ok", Version: "1", Description: "desc"}) {
		t.Fatalf("unknown field or identity: %+v %v", b, err)
	}
}

func TestBundleDirectorySymlink(t *testing.T) {
	target := directory(t, validManifest)
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	b, _, err := Load(link)
	if err != nil || b.Dir != link || b.Manifest.ID != "Example.bundle" {
		t.Fatalf("symlink: %+v %v", b, err)
	}
}

func TestUnreadableDirectoryAndManifest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	for _, what := range []string{"directory", "manifest"} {
		t.Run(what, func(t *testing.T) {
			dir := directory(t, validManifest)
			path := dir
			if what == "manifest" {
				path = filepath.Join(dir, "husk-bundle.json")
			}
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0700) })
			_, _, err := Load(dir)
			var e *Error
			if !errors.As(err, &e) || e.Code != CodeUnreadable {
				t.Fatalf("permissions: %v", err)
			}
		})
	}
}

func TestPluginWarningsAndConfigVisibility(t *testing.T) {
	for _, state := range []string{"absent", "empty", "dotfiles", "populated"} {
		t.Run(state, func(t *testing.T) {
			dir := directory(t, validManifest)
			plugins := filepath.Join(dir, "plugins")
			if state != "absent" {
				if err := os.Mkdir(plugins, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if state == "dotfiles" {
				put(t, filepath.Join(plugins, ".keep"), "")
			}
			if state == "populated" {
				if err := os.Mkdir(filepath.Join(plugins, "example"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			put(t, filepath.Join(dir, "README.md"), "readme")
			put(t, filepath.Join(dir, "config.json"), "{}")
			b, w, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if state == "populated" {
				if b.PluginsDir != plugins || len(w) != 0 {
					t.Fatalf("populated: %+v %+v", b, w)
				}
			} else {
				if b.PluginsDir != "" || len(w) != 1 || w[0] != (Warning{CodeNoPlugins, "bundle Example.bundle: no plugins"}) {
					t.Fatalf("empty: %+v %+v", b, w)
				}
			}
			for _, name := range []string{"husk-bundle.json", "plugins", "plugins/example", "README.md"} {
				f, err := b.Config.Open(name)
				if f != nil {
					f.Close()
				}
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("visible %s: %v", name, err)
				}
			}
			entries, err := fs.ReadDir(b.Config, ".")
			if err != nil || len(entries) != 1 || entries[0].Name() != "config.json" {
				t.Fatalf("listing: %+v %v", entries, err)
			}
			raw, err := fs.ReadFile(b.Config, "config.json")
			if err != nil || string(raw) != "{}" {
				t.Fatalf("config read: %s %v", raw, err)
			}
		})
	}
}

func TestImportsStandardLibraryOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		tree, err := parser.ParseFile(token.NewFileSet(), e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(tree, func(n ast.Node) bool {
			imp, ok := n.(*ast.ImportSpec)
			if !ok {
				return true
			}
			path, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") || slices.Contains([]string{"internal", "vendor"}, first) {
				t.Errorf("non-stdlib import %s", path)
			}
			return false
		})
	}
}
