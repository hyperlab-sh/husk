// Package createcmd writes runnable bundles and TUI applications without reading configuration or
// contacting an instance.
package createcmd

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/hyperlab-sh/husk/pkg/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/plugins/guide"
)

//go:embed templates
var templates embed.FS

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Command returns the local create noun for bundles and Go TUI applications.
func Command(version string) output.Command {
	return command(version, guide.Files, os.Getwd, writeFile)
}

// GuideCommand returns the hidden local entry point for scaffold guide launchers.
func GuideCommand() output.Command {
	return output.Command{Noun: "__guide-plugin", Usage: "husk __guide-plugin", Hidden: true,
		Run: func(_ context.Context, args []string, _ output.Options) error {
			if len(args) != 0 {
				return output.Usage("husk __guide-plugin takes no arguments")
			}
			if err := guide.Run(); err != nil {
				return output.Environment(err, "%v", err)
			}
			return nil
		},
	}
}

type file struct {
	name string
	data []byte
	mode fs.FileMode
}

func command(version string, guideFS fs.FS, getwd func() (string, error), write func(string, []byte, fs.FileMode) error) output.Command {
	const usage = "husk create bundle <name> | husk create tui <name> [<module>]"
	return output.Command{Noun: "create", Usage: usage, Run: func(_ context.Context, args []string, o output.Options) error {
		if len(args) < 2 || (args[0] != "bundle" && args[0] != "tui") || (args[0] == "bundle" && len(args) != 2) || len(args) > 3 {
			return output.Usage("usage: %s", usage)
		}
		name := args[1]
		if !nameRE.MatchString(name) {
			return output.Usage("invalid name %q: use [a-z0-9][a-z0-9._-]{0,63}", name)
		}
		module := name
		if args[0] == "tui" && len(args) == 3 {
			module = args[2]
		}
		if args[0] == "tui" && !validModulePath(module) {
			if len(args) == 2 {
				return output.Usage("invalid name %q: not a valid Go module path; pass <module> explicitly", name)
			}
			return output.Usage("invalid module path %q", module)
		}
		files, err := bundleFiles(name, guideFS)
		if err == nil && args[0] == "tui" {
			goVersion := goDirective(runtime.Version())
			if goVersion == "" {
				return output.Create(nil, "cannot determine Go language version from %q", runtime.Version())
			}
			files, err = tuiFiles(name, module, version, goVersion, files)
		}
		if err != nil {
			return output.Create(err, "%v", err)
		}
		cwd, err := getwd()
		if err != nil {
			return output.Create(err, "%v", err)
		}
		dir := filepath.Join(cwd, name)
		if err := target(dir); err != nil {
			return output.Create(err, "%v", err)
		}
		names := make([]string, 0, len(files))
		for _, f := range files {
			dest := filepath.Join(dir, filepath.FromSlash(f.name))
			if err := makeDirs(dir, filepath.Dir(dest)); err != nil {
				return output.Create(err, "write %s: %v", dest, err)
			}
			if err := write(dest, f.data, f.mode); err != nil {
				return output.Create(err, "write %s: %v", dest, err)
			}
			names = append(names, f.name)
		}
		if o.Format == output.JSON {
			raw, err := json.Marshal(struct {
				Dir   string   `json:"dir"`
				Files []string `json:"files"`
			}{dir, names})
			if err != nil {
				return err
			}
			return o.WriteJSON(raw)
		}
		_, err = fmt.Fprintln(o.Stdout, strings.Join(names, "\n"))
		return err
	}}
}

func target(dir string) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
	}
	return fmt.Errorf("%s exists and is not empty", dir)
}

// makeDirs explicitly sets permissions on newly created directories so the
// user's umask does not change the scaffold. Existing directories keep their
// permissions. Every path is beneath the validated target, never its parent.
func makeDirs(root, dir string) error {
	if dir != root {
		if err := makeDirs(root, filepath.Dir(dir)); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0755); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	return os.Chmod(dir, 0755)
}

func writeFile(name string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func bundleFiles(name string, guideFS fs.FS) ([]file, error) {
	var files []file
	for _, entry := range []struct {
		source, dest string
		mode         fs.FileMode
	}{
		{"manifest", "husk-bundle.json", 0644}, {"config", "config.json", 0644},
		{"keymap", "keymap.json", 0644}, {"readme", "README.md", 0644},
		{"hello-manifest", "plugins/hello/husk-plugin.json", 0644},
		{"hello", "plugins/hello/hello", 0755}, {"guide-launcher", "plugins/guide/guide", 0755},
	} {
		raw, err := templates.ReadFile("templates/" + entry.source)
		if err != nil {
			return nil, err
		}
		text := strings.NewReplacer("@NAME@", name, "@MAJOR@", strconv.Itoa(protocol.Major)).Replace(string(raw))
		files = append(files, file{entry.dest, []byte(text), entry.mode})
	}
	err := fs.WalkDir(guideFS, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if name == "guide" {
			return fmt.Errorf("guide files must not contain an executable named guide")
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("guide file %s is not a regular file", name)
		}
		raw, err := fs.ReadFile(guideFS, name)
		if err != nil {
			return err
		}
		files = append(files, file{path.Join("plugins/guide", name), raw, 0644})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, err
}
