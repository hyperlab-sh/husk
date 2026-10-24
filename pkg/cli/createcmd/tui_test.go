package createcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/plugins/guide"
)

func TestTUIValidationAndTargets(t *testing.T) {
	for _, name := range []string{"", "UPPER", "a/b", "..", strings.Repeat("a", 65)} {
		cmd := command("", fakeGuide, func() (string, error) { t.Fatal("invalid name reached filesystem"); return "", nil }, writeFile)
		if err := cmd.Run(t.Context(), []string{"tui", name}, output.Options{}); output.AsError(err).Exit != 2 {
			t.Fatalf("invalid name %q: %v", name, err)
		}
	}
	for _, state := range []string{"empty", "nonempty", "file", "symlink"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "app")
			var err error
			switch state {
			case "empty", "nonempty":
				err = os.Mkdir(dir, 0700)
				if err == nil && state == "nonempty" {
					err = os.WriteFile(filepath.Join(dir, "keep"), nil, 0600)
				}
			case "file":
				err = os.WriteFile(dir, nil, 0600)
			case "symlink":
				err = os.Symlink(t.TempDir(), dir)
			}
			if err != nil {
				t.Fatal(err)
			}
			writes := 0
			_, err = invoke(t, root, []string{"tui", "app"}, output.JSON, func(p string, b []byte, m fs.FileMode) error { writes++; return writeFile(p, b, m) })
			if state == "empty" {
				if err != nil || writes == 0 {
					t.Fatalf("writes=%d error=%v", writes, err)
				}
				info, err := os.Stat(dir)
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatalf("existing target permissions: %v %v", info, err)
				}
			} else if err == nil || output.AsError(err).Code != output.CodeCreate || writes != 0 {
				t.Fatalf("writes=%d error=%v", writes, err)
			}
		})
	}
}

func TestTUIGoldenAndBundleIdentity(t *testing.T) {
	bundle, err := bundleFiles("demo", fakeGuide)
	if err != nil {
		t.Fatal(err)
	}
	files, err := tuiFiles("demo", "example.test/demo", "v1.2.3", "1.26", bundle)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", "tui")
	if *update {
		// Keep module and source snapshots as data, not a separate Go workspace.
		snapshots := append([]file(nil), files...)
		for i := range snapshots {
			if snapshots[i].name == "go.mod" || snapshots[i].name == "main.go" {
				snapshots[i].name += ".golden"
			}
		}
		if err := writeGolden(golden, snapshots); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f.name))
		if err := makeDirs(root, filepath.Dir(p)); err != nil {
			t.Fatal(err)
		}
		if err := writeFile(p, f.data, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	assertWrittenModes(t, root)
	want := map[string]string{}
	for name, content := range tree(t, golden) {
		want[strings.TrimSuffix(name, ".golden")] = content
	}
	if got := tree(t, root); !reflect.DeepEqual(got, want) {
		for name, content := range want {
			if got[name] != content {
				t.Errorf("TUI golden differs: %s", name)
			}
		}
		t.Fatalf("TUI golden tree differs: got %d entries, want %d", len(got), len(want))
	}
	for _, f := range bundle {
		raw, err := os.ReadFile(filepath.Join(root, "bundle", filepath.FromSlash(f.name)))
		if err != nil || !bytes.Equal(raw, f.data) {
			t.Fatalf("bundle file differs: %s: %v", f.name, err)
		}
	}
}

func TestTUIFormatsModuleAndVersion(t *testing.T) {
	for _, tc := range []struct{ version, want string }{
		{"", ""}, {"v1.2.3", "v1.2.3"}, {"v1.2.3-rc.1", "v1.2.3-rc.1"},
		{"v0.0.0-20261023000000-abc123", "v0.0.0-20261023000000-abc123"},
		{"v0.0.0-20261023000000-abc123+dirty", ""}, {"v1.2.3+build", ""},
		{"(devel)", ""}, {"devel go1.26", ""}, {"not-a-version", ""},
		{"v1.2", ""}, {"v01.2.3", ""}, {"v1.2.3-01", ""}, {"v1.2.3-rc..1", ""},
	} {
		version := tc.version
		for _, format := range []output.Format{output.JSON, output.Table} {
			for _, module := range []string{"", "example.test/custom", "example.test/tool/v2"} {
				root := t.TempDir()
				args := []string{"tui", "app"}
				if module != "" {
					args = append(args, module)
				}
				var out bytes.Buffer
				err := command(version, fakeGuide, func() (string, error) { return root, nil }, writeFile).Run(context.Background(), args, output.Options{Format: format, Stdout: &out})
				if err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(root, "app")
				var names []string
				for name, content := range tree(t, dir) {
					if strings.HasPrefix(content, "file:") {
						names = append(names, name)
					}
				}
				sort.Strings(names)
				if format == output.JSON {
					var result struct {
						Dir   string
						Files []string
					}
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if result.Dir != dir || !reflect.DeepEqual(result.Files, names) {
						t.Fatalf("JSON output=%s", &out)
					}
				} else if out.String() != strings.Join(names, "\n")+"\n" {
					t.Fatalf("table=%s", &out)
				}
				mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
				if err != nil {
					t.Fatal(err)
				}
				if module == "" {
					module = "app"
				}
				if !strings.Contains(string(mod), "module "+strconvQuote(module)+"\n") || !strings.Contains(string(mod), "go "+goDirective(runtime.Version())+"\n") {
					t.Fatalf("module=%s", mod)
				}
				if tc.want != "" && !strings.Contains(string(mod), "require github.com/hyperlab-sh/husk "+strconvQuote(tc.want)+"\n") {
					t.Fatalf("dependency version missing: %s", mod)
				}
				readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(readme), "`go build -o app && ./app`") {
					t.Fatalf("README build command does not name the scaffold executable for module %q: %s", module, readme)
				}
				if (tc.want == "") != !strings.Contains(string(mod), "require ") || (tc.want == "") != strings.Contains(string(readme), "go get github.com/hyperlab-sh/husk@latest") {
					t.Fatalf("version handling: module=%s README=%s", mod, readme)
				}
			}
		}
	}
}

func TestTUIBuildVetAndHeadless(t *testing.T) {
	if testing.Short() {
		t.Skip("generated application build belongs to the slow lane")
	}
	root := t.TempDir()
	cmd := command("v0.0.0", guide.Files, func() (string, error) { return root, nil }, writeFile)
	if err := cmd.Run(t.Context(), []string{"tui", "app", "example.test/custom"}, output.Options{Format: output.Table, Stdout: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(repo, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(repo)
		if parent == repo {
			t.Fatal("module root not found")
		}
		repo = parent
	}
	dir := filepath.Join(root, "app")
	f, err := os.OpenFile(filepath.Join(dir, "go.mod"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\nreplace github.com/hyperlab-sh/husk => " + strconvQuote(repo) + "\n")
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("replace: %v %v", err, closeErr)
	}
	sums, err := os.ReadFile(filepath.Join(repo, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sums, 0644); err != nil {
		t.Fatal(err)
	}
	runtimeTests, err := os.ReadFile(filepath.Join("testdata", "tui-runtime-test"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main_test.go"), runtimeTests, 0644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "app")
	run := func(workdir string, args ...string) []byte {
		t.Helper()
		c := exec.CommandContext(t.Context(), args[0], args[1:]...)
		c.Dir = workdir
		c.Env = append(os.Environ(), "GOPROXY=off", "GOFLAGS=-mod=mod")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	var build []string
	for _, fragment := range strings.Split(string(readme), "`") {
		if strings.HasPrefix(fragment, "go build ") {
			command, _, _ := strings.Cut(fragment, " && ")
			build = strings.Fields(command)
			break
		}
	}
	if len(build) == 0 {
		t.Fatal("README has no build command")
	}
	// Execute the README's command, not an independently supplied output flag.
	run(dir, build...)
	run(dir, "go", "vet", "-mod=mod", ".")
	run(dir, "go", "test", "-mod=mod", ".")
	if out := run(root, binary, "--help"); !bytes.Contains(out, []byte("-bundle")) {
		t.Fatalf("help=%s", out)
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	runtimeDir, err := os.MkdirTemp("/tmp", "htui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	// Running away from the executable proves bundle lookup is not cwd-relative.
	if out := run(root, binary, "--dump-frame"); !bytes.Contains(out, []byte("Hello from app")) {
		t.Fatalf("default bundle did not draw: %s", out)
	}
	if out := run(root, binary, "--bundle", "", "--dump-frame"); bytes.Contains(out, []byte("Hello from app")) {
		t.Fatalf("explicit empty bundle still loaded plugins: %s", out)
	}
	alternate := filepath.Join(root, "alternate")
	if err := os.Rename(filepath.Join(dir, "bundle"), alternate); err != nil {
		t.Fatal(err)
	}
	if out := run(root, binary, "--dump-frame"); bytes.Contains(out, []byte("Hello from app")) {
		t.Fatalf("missing bundle did not use embedded defaults only: %s", out)
	}
	if out := run(root, binary, "--bundle", "alternate", "--dump-frame"); !bytes.Contains(out, []byte("Hello from app")) {
		t.Fatalf("explicit relative bundle did not load: %s", out)
	}
}
