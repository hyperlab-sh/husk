package createcmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/substrate/store"
	"github.com/hyperlab-sh/husk/plugins/guide"
)

var update = flag.Bool("update", false, "update scaffold golden tree")
var fakeGuide = fstest.MapFS{"husk-plugin.json": {Data: []byte("fake guide manifest\n")}, "src/info": {Data: []byte("fake guide source\n")}}

func invoke(t *testing.T, root string, args []string, format output.Format, write func(string, []byte, fs.FileMode) error) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	err := command("", fakeGuide, func() (string, error) { return root, nil }, write).Run(context.Background(), args, output.Options{Format: format, Stdout: &stdout})
	return stdout.String(), err
}

func TestNamesAndArgumentsBeforeFilesystem(t *testing.T) {
	for _, name := range []string{"My App", "", "UPPER", ".", "..", "-x", "a/b", "a\\b", strings.Repeat("a", 65)} {
		t.Run(name, func(t *testing.T) {
			cmd := command("", fakeGuide, func() (string, error) { t.Fatal("filesystem reached for invalid name"); return "", nil }, writeFile)
			err := cmd.Run(context.Background(), []string{"bundle", name}, output.Options{})
			e := output.AsError(err)
			want := "invalid name " + strconvQuote(name) + ": use [a-z0-9][a-z0-9._-]{0,63}"
			if e.Exit != 2 || e.Code != output.CodeUsage || e.Message != want {
				t.Fatalf("error = %#v", e)
			}
		})
	}
	for _, args := range [][]string{nil, {"bundle"}, {"tui"}, {"tui", "app", ""}, {"tui", "app", "module", "extra"}, {"bundle", "app", "extra"}} {
		cmd := command("", fakeGuide, func() (string, error) { t.Fatal("filesystem reached for invalid arguments"); return "", nil }, writeFile)
		err := cmd.Run(t.Context(), args, output.Options{})
		if output.AsError(err).Exit != 2 {
			t.Fatalf("args %q: %v", args, err)
		}
	}
	for _, name := range []string{"0", "app", "a._-9", strings.Repeat("a", 64)} {
		if _, err := invoke(t, t.TempDir(), []string{"bundle", name}, output.JSON, writeFile); err != nil {
			t.Fatalf("name %q: %v", name, err)
		}
	}
}
func strconvQuote(s string) string { raw, _ := json.Marshal(s); return string(raw) }

func TestTargetStatesWriteNothingOnRefusal(t *testing.T) {
	for _, state := range []string{"absent", "empty", "nonempty", "file", "symlink", "unreadable parent", "unreadable target"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "app")
			switch state {
			case "empty", "nonempty", "unreadable target":
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if state == "nonempty" {
					if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("keep"), 0644); err != nil {
						t.Fatal(err)
					}
				}
			case "file":
				if err := os.WriteFile(dir, []byte("keep"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(state, "unreadable") {
				if os.Geteuid() == 0 {
					t.Skip("root bypasses permissions")
				}
				p := root
				if state == "unreadable target" {
					p = dir
				}
				if err := os.Chmod(p, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(p, 0755) })
			}
			writes := 0
			_, err := invoke(t, root, []string{"bundle", "app"}, output.JSON, func(p string, b []byte, m fs.FileMode) error { writes++; return writeFile(p, b, m) })
			if state == "absent" || state == "empty" {
				if err != nil || writes == 0 {
					t.Fatalf("writes=%d err=%v", writes, err)
				}
				return
			}
			e := output.AsError(err)
			if e.Exit != 1 || e.Code != output.CodeCreate || writes != 0 {
				t.Fatalf("writes=%d error=%#v", writes, e)
			}
			if !strings.HasPrefix(state, "unreadable") && e.Message != dir+" exists and is not empty" {
				t.Fatalf("message=%s", e.Message)
			}
		})
	}
}

func TestExistingEmptyTargetKeepsPermissions(t *testing.T) {
	for _, name := range []string{"success", "write failure"} {
		t.Run(name, func(t *testing.T) {
			fail := name == "write failure"
			root := t.TempDir()
			dir := filepath.Join(root, "app")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("injected write failure")
			_, err := invoke(t, root, []string{"bundle", "app"}, output.JSON, func(p string, b []byte, mode fs.FileMode) error {
				if fail {
					return failure
				}
				return writeFile(p, b, mode)
			})
			if fail && !errors.Is(err, failure) || !fail && err != nil {
				t.Fatalf("write: %v", err)
			}
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0700 {
				t.Fatalf("existing target mode = %04o, want 0700", info.Mode().Perm())
			}
		})
	}
}

func TestGuideCommandArgumentsAndEnvironment(t *testing.T) {
	cmd := GuideCommand()
	if cmd.Noun != "__guide-plugin" || !cmd.Hidden {
		t.Fatalf("hidden command metadata: %+v", cmd)
	}
	for _, tc := range []struct {
		name, path, token string
		args              []string
		code              string
		exit              int
	}{
		{"missing environment", "", "", nil, output.CodeEnvironment, output.ExitEnvironment},
		{"missing token", "unused", "", nil, output.CodeEnvironment, output.ExitEnvironment},
		{"missing path", "", "token", nil, output.CodeEnvironment, output.ExitEnvironment},
		{"arguments", "", "", []string{"extra"}, output.CodeUsage, output.ExitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HUSK_SOCKET_PATH", tc.path)
			t.Setenv("HUSK_PLUGIN_TOKEN", tc.token)
			err := cmd.Run(t.Context(), tc.args, output.Options{})
			e := output.AsError(err)
			if e.Code != tc.code || e.Exit != tc.exit {
				t.Fatalf("guide failure = %#v, want %s/exit %d", e, tc.code, tc.exit)
			}
		})
	}
}

func TestWriteFailureAtEveryFileLeavesEarlierFiles(t *testing.T) {
	for _, verb := range []string{"bundle", "tui"} {
		t.Run(verb, func(t *testing.T) { testWriteFailures(t, verb) })
	}
}

func testWriteFailures(t *testing.T, verb string) {
	t.Helper()
	files, err := bundleFiles("app", fakeGuide)
	if err == nil && verb == "tui" {
		files, err = tuiFiles("app", "app", "", "1.26", files)
	}
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= len(files); n++ {
		t.Run(files[n-1].name, func(t *testing.T) {
			root := t.TempDir()
			calls := 0
			failure := errors.New("injected write failure")
			_, err := invoke(t, root, []string{verb, "app"}, output.JSON, func(p string, b []byte, m fs.FileMode) error {
				calls++
				if calls == n {
					return failure
				}
				return writeFile(p, b, m)
			})
			e := output.AsError(err)
			if e.Exit != 1 || e.Code != output.CodeCreate || !errors.Is(err, failure) || e.Message != "write "+filepath.Join(root, "app", filepath.FromSlash(files[n-1].name))+": "+failure.Error() {
				t.Fatalf("error=%#v", e)
			}
			for i, f := range files {
				_, err := os.Stat(filepath.Join(root, "app", filepath.FromSlash(f.name)))
				if (err == nil) != (i < n-1) {
					t.Fatalf("file %s after failure %d: %v", f.name, n, err)
				}
			}
		})
	}
}

func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		content := "directory:"
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			content = "file:" + string(b)
		}
		result[filepath.ToSlash(rel)] = content
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertWrittenModes(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		want := fs.FileMode(0644)
		if d.IsDir() || strings.HasSuffix(filepath.ToSlash(rel), "plugins/hello/hello") || strings.HasSuffix(filepath.ToSlash(rel), "plugins/guide/guide") {
			want = 0755
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm() != want {
			t.Errorf("written mode %s = %04o, want %04o", rel, info.Mode().Perm(), want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeGolden(golden string, files []file) error {
	if err := os.RemoveAll(golden); err != nil {
		return err
	}
	for _, f := range files {
		p := filepath.Join(golden, filepath.FromSlash(f.name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.data, f.mode); err != nil {
			return err
		}
		if err := os.Chmod(p, f.mode); err != nil {
			return err
		}
	}
	return nil
}

func TestGoldenRegenerationRemovesStaleFiles(t *testing.T) {
	golden := filepath.Join(t.TempDir(), "golden")
	if err := os.Mkdir(golden, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(golden, "stale"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	files, err := bundleFiles("demo", fakeGuide)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGolden(golden, files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(golden, "stale")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale golden retained: %v", err)
	}
	if len(tree(t, golden)) <= len(files) {
		t.Fatal("golden tree was not regenerated")
	}
}

func TestTreeComparisonIgnoresCheckoutPermissions(t *testing.T) {
	var trees []map[string]string
	for _, mode := range []fs.FileMode{0755, 0700} {
		root := t.TempDir()
		if err := os.Chmod(root, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "nested"), mode); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"plain", "executable"} {
			fileMode := mode
			if name == "plain" {
				fileMode &= 0666
			}
			p := filepath.Join(root, "nested", name)
			if err := os.WriteFile(p, []byte(name), fileMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, fileMode); err != nil {
				t.Fatal(err)
			}
		}
		trees = append(trees, tree(t, root))
	}
	if !reflect.DeepEqual(trees[0], trees[1]) {
		t.Fatalf("checkout permissions affected content comparison: %v != %v", trees[0], trees[1])
	}
}

func TestGoldenBundleTreeModesAndOutput(t *testing.T) {
	root := t.TempDir()
	mask := syscall.Umask(0077)
	text, err := invoke(t, root, []string{"bundle", "demo"}, output.JSON, writeFile)
	syscall.Umask(mask)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "demo")
	var result struct {
		Dir   string   `json:"dir"`
		Files []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name, content := range tree(t, dir) {
		if strings.HasPrefix(content, "file:") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if result.Dir != dir || !reflect.DeepEqual(result.Files, names) {
		t.Fatalf("output=%s want %v", text, names)
	}
	golden := filepath.Join("testdata", "golden", "bundle")
	if *update {
		files, err := bundleFiles("demo", fakeGuide)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeGolden(golden, files); err != nil {
			t.Fatal(err)
		}
	}
	assertWrittenModes(t, dir)
	if got, want := tree(t, dir), tree(t, golden); !reflect.DeepEqual(got, want) {
		t.Fatalf("tree differs:\ngot %#v\nwant %#v", got, want)
	}
	table, err := invoke(t, t.TempDir(), []string{"bundle", "demo"}, output.Table, writeFile)
	if err != nil || table != strings.Join(names, "\n")+"\n" {
		t.Fatalf("table=%q err=%v", table, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.ParseDoc(raw)
	if err != nil || len(doc) != 0 {
		t.Fatalf("config=%v err=%v", doc, err)
	}
	raw, err = os.ReadFile(filepath.Join(dir, "plugins", "hello", "husk-plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Protocol int `json:"protocol"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Protocol != protocol.Major {
		t.Fatalf("major=%d want %d", manifest.Protocol, protocol.Major)
	}
}

// A shell plugin must keep its protocol stdout open while draining stdin;
// closing it announces a broken channel to the process supervisor.
func TestHelloKeepsProtocolPipeOpenUntilStdinCloses(t *testing.T) {
	root := t.TempDir()
	if _, err := invoke(t, root, []string{"bundle", "demo"}, output.JSON, writeFile); err != nil {
		t.Fatal(err)
	}
	// The stand-in CLI succeeds without needing a server. The stand-in cat
	// signals entry through the preserved protocol descriptor, then drains stdin.
	for name, text := range map[string]string{
		"host": "#!/bin/sh\nexit 0\n",
		"cat":  "#!/bin/sh\nprintf 'ready\\n' >&3\nexec /bin/cat\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0755); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(root, "demo", "plugins", "hello", "hello"))
	cmd.Env = []string{"PATH=" + root, "HUSK_BIN_PATH=" + filepath.Join(root, "host")}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		_ = stdin.Close()
		_ = cmd.Wait()
		t.Fatalf("protocol stdout closed before stdin: line=%q err=%v stderr=%s", line, err, stderr.String())
	}
	if _, err := stdin.Write([]byte("host notification\n")); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("hello exit: %v stderr=%s", err, stderr.String())
	}
}

func TestRealGuideFilesCopiedByteForByte(t *testing.T) {
	if _, err := fs.Stat(guide.Files, "guide"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("embedded executable: %v", err)
	}
	files, err := bundleFiles("demo", guide.Files)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]file{}
	for _, f := range files {
		got[f.name] = f
	}
	err = fs.WalkDir(guide.Files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := fs.ReadFile(guide.Files, name)
		if err != nil {
			return err
		}
		f, ok := got["plugins/guide/"+name]
		if !ok || !bytes.Equal(raw, f.data) || f.mode != 0644 {
			t.Errorf("guide copy differs: %s", name)
		}
		delete(got, "plugins/guide/"+name)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for name := range got {
		if strings.HasPrefix(name, "plugins/guide/") {
			count++
		}
	}
	if count != 1 || string(got["plugins/guide/guide"].data) != "#!/bin/sh\nexec \"$HUSK_BIN_PATH\" __guide-plugin\n" || got["plugins/guide/guide"].mode != 0755 {
		t.Fatal("guide launcher differs")
	}
	if _, err := bundleFiles("demo", fstest.MapFS{"guide": {Data: []byte("bad")}}); err == nil {
		t.Fatal("accepted embedded guide executable")
	}
}
