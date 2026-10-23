package guide

import (
	"embed"
	"io/fs"
)

//go:embed husk-plugin.json model.go keys.go render.go run.go embed.go cmd/*/*.go
var source embed.FS

// Files contains the plugin's source and manifest for scaffold generation.
// It deliberately excludes tests and the separately built guide executable.
var Files fs.FS = source
