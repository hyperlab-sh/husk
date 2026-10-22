// Command genschema writes the protocol v2 JSON Schema, by default to docs/docs/wire-schema-v2.json.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyperlab-sh/husk/pkg/protocol/schema"
)

func main() {
	out := flag.String("o", "", "output file (default: repository docs/docs/wire-schema-v2.json); - for stdout")
	flag.Parse()
	data, err := schema.Generate()
	if err == nil && *out == "-" {
		_, err = os.Stdout.Write(data)
	} else if err == nil {
		if *out == "" {
			var root string
			root, err = repositoryRoot()
			if err == nil {
				*out = filepath.Join(root, "docs", "docs", "wire-schema-v2.json")
			}
		}
		if err == nil {
			err = os.WriteFile(*out, data, 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "genschema:", err)
		os.Exit(1)
	}
}

func repositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot find go.mod above working directory")
		}
		dir = parent
	}
}
