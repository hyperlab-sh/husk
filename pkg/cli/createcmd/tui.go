package createcmd

import (
	"sort"
	"strconv"
	"strings"
)

func tuiFiles(name, module, version, goVersion string, bundle []file) ([]file, error) {
	version = dependencyVersion(version)
	files := make([]file, 0, len(bundle)+3)
	for _, f := range bundle {
		f.name = "bundle/" + f.name
		files = append(files, f)
	}
	require := ""
	get := ""
	if version != "" {
		require = "\nrequire github.com/hyperlab-sh/husk " + strconv.Quote(version) + "\n"
	} else {
		get = "First run `go get github.com/hyperlab-sh/husk@latest` to select a Husk version.\n\n"
	}
	for _, entry := range []struct{ source, dest string }{
		{"tui-mod", "go.mod"}, {"tui-main", "main.go"}, {"tui-readme", "README.md"},
	} {
		raw, err := templates.ReadFile("templates/" + entry.source)
		if err != nil {
			return nil, err
		}
		text := strings.NewReplacer("@NAME@", name, "@MODULE@", strconv.Quote(module), "@GO@", goVersion,
			"@REQUIRE@", require, "@GET@", get).Replace(string(raw))
		files = append(files, file{entry.dest, []byte(text), 0644})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}
