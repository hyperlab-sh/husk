package protocol

import "strings"

// CLIOnlyEventPattern reports whether an event name or pattern could match a
// secrets. or services. event. secrets.read and services.reloaded are
// delivered to cli watchers only, so a plugin may neither declare a
// subscription to such a pattern nor watch it.
func CLIOnlyEventPattern(pattern string) bool {
	return strings.HasPrefix(pattern, "secrets.") || strings.HasPrefix(pattern, "services.")
}
