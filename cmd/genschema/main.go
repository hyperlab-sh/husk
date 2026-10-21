// Command genschema writes the JSON Schema for the Husk wire protocol,
// generated from the pkg/huskipc structs. It is run by go generate.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hyperlab-sh/husk/pkg/huskipc/schema"
)

func main() {
	out := flag.String("o", "", "write the schema to this file (default: stdout)")
	flag.Parse()

	data, err := schema.Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "genschema:", err)
		os.Exit(1)
	}
	if *out == "" {
		os.Stdout.Write(data)
		return
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "genschema:", err)
		os.Exit(1)
	}
}
