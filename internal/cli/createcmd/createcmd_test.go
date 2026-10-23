package createcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
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
	for _, args := range [][]string{nil, {"bundle"}, {"tui", "app"}, {"bundle", "app", "extra"}} {
		_, err := invoke(t, t.TempDir(), args, output.JSON, writeFile)
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

func TestWriteFailureAtEveryFileLeavesEarlierFiles(t *testing.T) {
	files, err := bundleFiles("app", fakeGuide)
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= len(files); n++ {
		t.Run(files[n-1].name, func(t *testing.T) {
			root := t.TempDir()
			calls := 0
			failure := errors.New("injected write failure")
			_, err := invoke(t, root, []string{"bundle", "app"}, output.JSON, func(p string, b []byte, m fs.FileMode) error {
				calls++
				if calls == n {
					return failure
				}
				return writeFile(p, b, m)
			})
			e := output.AsError(err)
			if e.Exit != 1 || e.Code != output.CodeCreate || !errors.Is(err, failure) || !strings.Contains(e.Message, "write "+filepath.Join(root, "app", filepath.FromSlash(files[n-1].name))+":") {
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
		info, err := d.Info()
		if err != nil {
			return err
		}
		content := ""
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			content = string(b)
		}
		result[filepath.ToSlash(rel)] = info.Mode().String() + ":" + content
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestGoldenBundleTreeModesAndOutput(t *testing.T) {
	root := t.TempDir()
	text, err := invoke(t, root, []string{"bundle", "demo"}, output.JSON, writeFile)
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
		if strings.HasPrefix(content, "-") {
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
		for _, f := range files {
			p := filepath.Join(golden, filepath.FromSlash(f.name))
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, f.data, f.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, f.mode); err != nil {
				t.Fatal(err)
			}
		}
	}
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
