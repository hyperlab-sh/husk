package main

import (
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestMainInstallsNoSignalHandler pins the spec's rule for command mode: the
// CLI installs no signal handler, so SIGINT, SIGTERM and SIGPIPE end husk by
// the signal. main.go is the dispatcher; it must not import os/signal.
func TestMainInstallsNoSignalHandler(t *testing.T) {
	src, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range src.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path == "os/signal" {
			t.Fatal("main.go imports os/signal")
		}
	}
}
