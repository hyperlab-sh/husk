// Package ptyhost is the plugin-side PTY pump. The Husk host does not import it.
package ptyhost

import (
	"strings"
	"time"
)

// Options describes the initial PTY geometry and inherited environment. Nil Env
// uses the process environment. Credentials are always removed from the child.
type Options struct {
	Width, Height int
	Env           []string
	// Dir selects the child's working directory; empty inherits the caller's.
	Dir string
	// StopGrace defaults to 500 ms. After is injectable for deterministic tests.
	StopGrace time.Duration
	After     func(time.Duration) <-chan time.Time
}

// Environment sets terminal capabilities without passing plugin credentials.
func Environment(env []string) []string {
	out := make([]string, 0, len(env)+3)
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "HUSK_PLUGIN_TOKEN", "TERM", "COLORTERM", "TERM_PROGRAM":
			continue
		}
		out = append(out, entry)
	}
	return append(out, "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=husk")
}
