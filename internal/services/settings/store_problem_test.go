package settings

import (
	"path/filepath"
	"testing"

	"github.com/hyperlab-sh/husk/internal/substrate/store"
)

func TestStoreProblemUsesActualConfigRoot(t *testing.T) {
	for _, tc := range []struct {
		name, root, file, want string
		files                  map[string]string
	}{
		{name: "instances in config root", root: "/home/u/instances/x/.config/husk", file: "/home/u/instances/x/.config/husk/catalog.json", want: "catalog.json"},
		{name: "profiles in config root", root: "/home/u/profiles/x/.config/husk", file: "/home/u/profiles/x/.config/husk/catalog.json", want: "catalog.json"},
		{name: "instance named profiles", root: "/config", file: "/config/instances/profiles/catalog.json", want: "instances/profiles/catalog.json"},
		{name: "profile file", root: "/config", file: "/config/profiles/dev/catalog.json", want: "profiles/dev/catalog.json"},
		{name: "outside root", root: "/config", file: "/home/private/instances/profiles/catalog.json", want: "catalog.json"},
		{name: "sibling prefix", root: "/config", file: "/config-other/profiles/dev/catalog.json", want: "catalog.json"},
		{name: "missing root", file: "/private/profiles/dev/catalog.json", want: "catalog.json"},
		{name: "relative file", root: "/config", file: "profiles/dev/catalog.json", want: "profiles/dev/catalog.json"},
		{name: "known layer file", root: "/config", file: "/private/catalog.json", want: "declared.json", files: map[string]string{"user": "declared.json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := StoreProblem(store.Problem{Code: "invalid_document", Layer: store.LayerUser, File: filepath.FromSlash(tc.file), Message: "parse failure", Line: 3}, tc.files, filepath.FromSlash(tc.root))
			if p.Source.File != tc.want || p.Source.Line != 3 || p.Message != tc.want+" (user): parse failure" || p.Code != "store_error" || p.Severity != "warning" {
				t.Fatalf("%+v source %+v", p, p.Source)
			}
		})
	}
}

func TestStoreProblemOmitsAbsentLayer(t *testing.T) {
	p := StoreProblem(store.Problem{File: "catalog.json", Message: "configuration could not be loaded"}, nil, "/config")
	if p.Message != "catalog.json: configuration could not be loaded" {
		t.Fatal(p)
	}
}
